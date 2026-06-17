package backend

// QUM-595: failing unit tests (TDD red phase) for the host stdout-reader
// wedge fix. These tests reference symbols that do not yet exist in
// session.go — they intentionally fail to compile until the implementer
// lands F1 (bounded subscriber send + ErrSubscriberWedged), F2
// (non-blocking observer dispatch via observerCh + ObserverDrops), and D1
// (HangTimeout watchdog + ErrHangTimeout) plus the BackendStats accessor
// surface on the Session interface.
//
// Test contracts (referenced by session.go implementation):
//   Stats{SubscriberDrops, ObserverDrops int64} (both atomic-backed)
//   ErrSubscriberWedged, ErrHangTimeout (errors.Is-able sentinels)
//   subscriberSendDeadline, hangCheckInterval (package vars, test-overridable)
//   SessionConfig.HangTimeout (zero=10min default, <0=disabled)
//   Session.BackendStats() Stats (interface method)
//   Close() blocks until observer drain completes or inflightDrainTimeout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// blockingObserver's OnMessage blocks until the test closes `release`,
// simulating a wedged sink. The non-blocking dispatch path (F2) must keep
// the reader draining stdout even while this observer is stuck.
type blockingObserver struct {
	release chan struct{}

	mu       sync.Mutex
	received []*protocol.Message
}

func newBlockingObserver() *blockingObserver {
	return &blockingObserver{release: make(chan struct{})}
}

func (o *blockingObserver) OnMessage(msg *protocol.Message) {
	<-o.release
	o.mu.Lock()
	o.received = append(o.received, msg)
	o.mu.Unlock()
}

func (o *blockingObserver) Release() { close(o.release) }

func (o *blockingObserver) Count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.received)
}

// assistantFrame returns a small assistant frame keyed by sequence number.
// The text payload is "a-<i>" so order can be reconstructed at assertion time.
func assistantFrame(i int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%d","message":{"role":"assistant","content":[{"type":"text","text":"a-%d"}]}}`, i, i)
}

const resultFrame = `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`

// drainStartTurnPrompt consumes the `user` prompt frame StartTurn sends so
// the transport.sendCh doesn't fill up.
func drainStartTurnPrompt(t *testing.T, transport *mockManagedTransport) {
	t.Helper()
	select {
	case <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for StartTurn to emit user prompt frame")
	}
}

// waitFor polls fn until it returns true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waitFor(%q) timed out after %s", label, d)
}

// overrideSubscriberSendDeadline overrides the package var and restores on
// cleanup. References the not-yet-defined package var
// `subscriberSendDeadline` so this file fails to compile until F1 lands.
func overrideSubscriberSendDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	prev := subscriberSendDeadline
	subscriberSendDeadline = d
	t.Cleanup(func() { subscriberSendDeadline = prev })
}

func overrideHangCheckInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := hangCheckInterval
	hangCheckInterval = d
	t.Cleanup(func() { hangCheckInterval = prev })
}

// controlResponseFrame is the typed shape sent over transport.sendCh in
// response to a control_request. We unmarshal sent items into this so
// assertions are structural, not stringly.
type controlResponseFrame struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string         `json:"subtype"`
		RequestID string         `json:"request_id"`
		Response  map[string]any `json:"response"`
	} `json:"response"`
}

// -----------------------------------------------------------------------------
// F1: subscriber send deadline
// -----------------------------------------------------------------------------

// TestSession_F1_SubscriberSendDeadline_UnwindsTurn exercises the bounded
// subscriber send path. With a deadline of 50ms, an unread subscriber that
// gets overflowed (>100 frames buffered) MUST fault the session with
// ErrSubscriberWedged, close the subscriber chan, bump SubscriberDrops, let
// StartTurn waiters unwind via LastTurnError, AND mark the session fatal so
// a subsequent StartTurn returns ErrSubscriberWedged fast.
func TestSession_F1_SubscriberSendDeadline_UnwindsTurn(t *testing.T) {
	overrideSubscriberSendDeadline(t, 50*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-f1"})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Intentionally do NOT read from events. Feed >100 frames to overflow
	// the 100-buffer subscriber. We feed 150 to be safe.
	go func() {
		for i := 0; i < 150; i++ {
			transport.feedMessage(t, assistantFrame(i))
		}
	}()

	// Within ~500ms the bounded deadline must trip and unwind the turn.
	waitFor(t, 1500*time.Millisecond, "LastTurnError ErrSubscriberWedged", func() bool {
		err := sess.LastTurnError()
		return err != nil && errors.Is(err, ErrSubscriberWedged)
	})

	// Subscriber chan must close so StartTurn callers reading the chan
	// observe end-of-turn rather than blocking forever.
	closed := false
	deadline := time.After(1 * time.Second)
loop:
	for {
		select {
		case _, ok := <-events:
			if !ok {
				closed = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !closed {
		t.Fatal("subscriber chan was never closed after subscriber wedge")
	}

	stats := sess.BackendStats()
	if stats.SubscriberDrops < 1 {
		t.Fatalf("BackendStats().SubscriberDrops = %d, want >= 1", stats.SubscriberDrops)
	}

	// Prove the session is actually torn down (fatal state is sticky):
	// a SECOND StartTurn must return ErrSubscriberWedged fast.
	startCtx, startCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer startCancel()
	_, err2 := sess.StartTurn(startCtx, "x")
	if err2 == nil {
		t.Fatal("second StartTurn after wedge returned nil error; want ErrSubscriberWedged")
	}
	if !errors.Is(err2, ErrSubscriberWedged) {
		t.Fatalf("second StartTurn err = %v, want errors.Is(..., ErrSubscriberWedged)", err2)
	}
}

// TestSession_F1_WedgeNoGoroutineLeak is a dedicated leak guard. It records
// baseline AFTER a GC + small settle, then asserts the post-Close goroutine
// count is within +/-5 of baseline. Polls up to 2s to allow goroutines to
// unwind without flaking on scheduler jitter.
func TestSession_F1_WedgeNoGoroutineLeak(t *testing.T) {
	overrideSubscriberSendDeadline(t, 50*time.Millisecond)

	runtime.GC()
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()
	const slack = 5

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-f1-leak"})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	go func() {
		for i := 0; i < 150; i++ {
			transport.feedMessage(t, assistantFrame(i))
		}
	}()

	waitFor(t, 1500*time.Millisecond, "LastTurnError ErrSubscriberWedged", func() bool {
		e := sess.LastTurnError()
		return e != nil && errors.Is(e, ErrSubscriberWedged)
	})

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	waitFor(t, 2*time.Second, "goroutines settle", func() bool {
		runtime.Gosched()
		n := runtime.NumGoroutine()
		delta := n - baseline
		return delta >= -slack && delta <= slack
	})
}

// TestSession_F1_InterruptDuringWedge_UnwindsCleanly asserts that calling
// Interrupt while the subscriber is wedged returns fast (within 200ms) and
// does NOT overwrite the sticky ErrSubscriberWedged fatal error.
func TestSession_F1_InterruptDuringWedge_UnwindsCleanly(t *testing.T) {
	overrideSubscriberSendDeadline(t, 50*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-f1-intr"})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Wedge subscriber: feed > buffer without reading.
	go func() {
		for i := 0; i < 150; i++ {
			transport.feedMessage(t, assistantFrame(i))
		}
	}()

	// Wait until wedge fires.
	waitFor(t, 1500*time.Millisecond, "LastTurnError ErrSubscriberWedged", func() bool {
		e := sess.LastTurnError()
		return e != nil && errors.Is(e, ErrSubscriberWedged)
	})

	// Interrupt must return fast — no hang, regardless of fatal state.
	interruptCtx, interruptCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer interruptCancel()
	done := make(chan error, 1)
	go func() { done <- sess.Interrupt(interruptCtx) }()
	select {
	case <-done:
		// ok: Interrupt returned in time. We don't care whether it
		// errored or not — we only require it not hang.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Interrupt did not return within 200ms during subscriber wedge")
	}

	// Fatal err is sticky: Interrupt must not overwrite ErrSubscriberWedged.
	if e := sess.LastTurnError(); !errors.Is(e, ErrSubscriberWedged) {
		t.Fatalf("LastTurnError after Interrupt = %v, want errors.Is(..., ErrSubscriberWedged)", e)
	}
}

// -----------------------------------------------------------------------------
// F2: non-blocking observer dispatch
// -----------------------------------------------------------------------------

// TestSession_F2_BlockingObserverDoesNotStallControlRequest is the core F2
// regression guard. A wedged Observer.OnMessage MUST NOT block the reader
// from servicing control_request frames. We overflow observerCh (>256
// frames) then feed a can_use_tool; the control_response must materialize
// on the transport sendCh within 2s with the precise frame shape:
//
//	{type:"control_response",
//	 response:{subtype:"success",
//	           request_id:"tool-1",
//	           response:{behavior:"allow", ...}}}
//
// ObserverDrops must be > 0.
func TestSession_F2_BlockingObserverDoesNotStallControlRequest(t *testing.T) {
	obs := newBlockingObserver()
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID: "sess-f2",
		Observer:  obs,
	})
	t.Cleanup(func() {
		obs.Release()
		_ = sess.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Drain events in the background so subscriber path isn't the wedge
	// under test (we're isolating F2 here).
	go func() {
		for range events { //nolint:revive // drain only
			_ = struct{}{}
		}
	}()

	// Overflow the observerCh (buffered 256) with assistant frames. Pump
	// 400 to give comfortable margin.
	for i := 0; i < 400; i++ {
		transport.feedMessage(t, assistantFrame(i))
	}

	// Now feed a control_request. The reader must service it despite the
	// observer being wedged.
	transport.feedMessage(t, `{"type":"control_request","request_id":"tool-1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}`)

	// Strictly assert the control_response shape. Drain non-matching
	// frames (StartTurn already consumed the user prompt — drainStartTurnPrompt
	// — so the next frame ought to be the response, but we tolerate other
	// frames preceding it.)
	deadline := time.After(2 * time.Second)
	var resp controlResponseFrame
	matched := false
	for !matched {
		select {
		case sent := <-transport.sendCh:
			data, err := json.Marshal(sent)
			if err != nil {
				t.Fatalf("marshal sendCh item: %v", err)
			}
			var candidate controlResponseFrame
			if err := json.Unmarshal(data, &candidate); err != nil {
				// Not a control_response shape; skip (e.g. another user prompt).
				continue
			}
			if candidate.Type != "control_response" {
				continue
			}
			if candidate.Response.RequestID != "tool-1" {
				continue
			}
			resp = candidate
			matched = true
		case <-deadline:
			t.Fatal("no control_response for tool-1 within 2s — reader is wedged on observer")
		}
	}

	if resp.Response.Subtype != "success" {
		t.Fatalf("control_response.response.subtype = %q, want %q", resp.Response.Subtype, "success")
	}
	if resp.Response.RequestID != "tool-1" {
		t.Fatalf("control_response.response.request_id = %q, want %q", resp.Response.RequestID, "tool-1")
	}
	if resp.Response.Response == nil {
		t.Fatalf("control_response.response.response is nil, want {behavior:\"allow\", ...}")
	}
	if behavior, _ := resp.Response.Response["behavior"].(string); behavior != "allow" {
		t.Fatalf("control_response.response.response.behavior = %q, want %q", behavior, "allow")
	}

	waitFor(t, 1*time.Second, "ObserverDrops > 0", func() bool {
		return sess.BackendStats().ObserverDrops > 0
	})
}

// -----------------------------------------------------------------------------
// D1: hang watchdog
// -----------------------------------------------------------------------------

// TestSession_D1_HangWatchdog_FiresWhenNoFrames asserts the watchdog
// faults the session when no frames arrive within HangTimeout.
func TestSession_D1_HangWatchdog_FiresWhenNoFrames(t *testing.T) {
	overrideHangCheckInterval(t, 20*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1a",
		HangTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	waitFor(t, 1500*time.Millisecond, "LastTurnError ErrHangTimeout", func() bool {
		err := sess.LastTurnError()
		return err != nil && errors.Is(err, ErrHangTimeout)
	})

	// Subscriber chan should close.
	closed := false
	deadline := time.After(1 * time.Second)
loop:
	for {
		select {
		case _, ok := <-events:
			if !ok {
				closed = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !closed {
		t.Fatal("subscriber chan was never closed after hang watchdog fired")
	}
}

// TestSession_D1_HangWatchdog_DoesNotFireWhenFramesArrive feeds frames at
// 100ms intervals well inside a 1s HangTimeout window (5x slack). The
// watchdog must NOT fire and LastTurnError must remain nil.
func TestSession_D1_HangWatchdog_DoesNotFireWhenFramesArrive(t *testing.T) {
	overrideHangCheckInterval(t, 50*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1b",
		HangTimeout: 1 * time.Second,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		for i := 0; i < 12; i++ {
			transport.feedMessage(t, assistantFrame(i))
			time.Sleep(100 * time.Millisecond)
		}
		transport.feedMessage(t, resultFrame)
	}()

	// Drain events until close.
	var got int
	for msg := range events {
		_ = msg
		got++
	}
	<-feederDone

	if got < 12 {
		t.Fatalf("subscriber received %d frames, want >= 12", got)
	}
	if err := sess.LastTurnError(); err != nil {
		t.Fatalf("LastTurnError = %v, want nil (watchdog should not have fired)", err)
	}
}

// TestSession_D1_HangWatchdog_IdleDoesNotFault is the QUM-599 regression
// guard. After Start (no StartTurn), the watchdog MUST NOT fault the
// session even after 2 × HangTimeout of idle time. The next StartTurn must
// succeed normally — i.e. the session is not bricked by between-turn idle.
func TestSession_D1_HangWatchdog_IdleDoesNotFault(t *testing.T) {
	overrideHangCheckInterval(t, 20*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1-idle",
		HangTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Idle for 2 × HangTimeout. No StartTurn — pure between-turn idle.
	time.Sleep(250 * time.Millisecond)

	if err := sess.LastTurnError(); err != nil {
		t.Fatalf("LastTurnError after %s of idle = %v, want nil (QUM-599: watchdog must not fault on between-turn idle)", 250*time.Millisecond, err)
	}

	// Next StartTurn must succeed normally and produce a working subscriber.
	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn after idle: %v, want nil (QUM-599: session must remain usable after idle)", err)
	}
	drainStartTurnPrompt(t, transport)

	// Sanity: feed a result frame and confirm the turn closes cleanly.
	transport.feedMessage(t, resultFrame)
	deadline := time.After(2 * time.Second)
	closed := false
	for !closed {
		select {
		case _, ok := <-events:
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatal("events chan did not close after result frame on post-idle turn")
		}
	}
	if err := sess.LastTurnError(); err != nil {
		t.Fatalf("LastTurnError after clean post-idle turn = %v, want nil", err)
	}
}

// TestSession_D1_HangWatchdog_IdleThenMidTurnSilenceStillFaults is the
// QUM-599 companion guard: gating the watchdog on turn-active state must
// NOT regress the QUM-595 mid-turn-silence detection. After a long idle
// gap, opening a StartTurn and then feeding nothing must still surface
// ErrHangTimeout within HangTimeout + slack.
func TestSession_D1_HangWatchdog_IdleThenMidTurnSilenceStillFaults(t *testing.T) {
	overrideHangCheckInterval(t, 20*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1-idle-then-stall",
		HangTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Idle far longer than HangTimeout — the gate must keep the session alive.
	time.Sleep(300 * time.Millisecond)

	if _, err := sess.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn after idle: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Now claude goes silent mid-turn — watchdog MUST fire.
	waitFor(t, 1500*time.Millisecond, "LastTurnError ErrHangTimeout (mid-turn silence)", func() bool {
		e := sess.LastTurnError()
		return e != nil && errors.Is(e, ErrHangTimeout)
	})
}

// TestSession_D1_HangWatchdog_DisabledWhenNegative ensures a negative
// HangTimeout disables the watchdog entirely.
func TestSession_D1_HangWatchdog_DisabledWhenNegative(t *testing.T) {
	overrideHangCheckInterval(t, 20*time.Millisecond)

	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1c",
		HangTimeout: -1,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Feed nothing for 300ms.
	time.Sleep(300 * time.Millisecond)

	if err := sess.LastTurnError(); err != nil {
		t.Fatalf("LastTurnError = %v, want nil (negative HangTimeout disables watchdog)", err)
	}
}

// TestSession_D1_HangWatchdog_PausedWhileControlRequestInflight is the
// QUM-635 fix guard. While an interactive control_request is outstanding
// (len(s.inflight) > 0), the frame-staleness watchdog MUST be paused: we owe
// claude a control_response, so claude is blocked on US, not hung, and no
// frames are expected. The watchdog must NOT fault ErrHangTimeout for the
// entire window the request is inflight — even past several × HangTimeout.
//
// Once the control_request resolves (inflight drains back to empty), the
// watchdog must RE-ARM and still fault a genuine mid-turn hang (turn active,
// no inflight, no frames). This proves D1 is paused, not permanently
// disabled.
//
// RED today: runHangWatchdog (session.go) does not consult s.inflight, so it
// faults ErrHangTimeout during the inflight window — step 8 below fails.
func TestSession_D1_HangWatchdog_PausedWhileControlRequestInflight(t *testing.T) {
	overrideHangCheckInterval(t, 20*time.Millisecond)

	transport := newMockManagedTransport()
	bridge := newCtxRespectingToolBridge()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1-inflight",
		HangTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, sess, transport, bridge)

	if _, err := sess.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Trigger an async MCP dispatch: registers an entry in s.inflight and
	// parks the ctx-respecting bridge handler. The turn is active and no
	// frames will arrive while the handler is parked.
	transport.feedMessage(t, mcpControlRequest)
	awaitCtxBridgeEntry(ctx, t, bridge)

	// KEY RED ASSERTION: 4 × HangTimeout of pure silence while a
	// control_request is inflight must NOT fault. Current code faults
	// ErrHangTimeout here because the watchdog ignores s.inflight.
	time.Sleep(400 * time.Millisecond)
	if err := sess.LastTurnError(); err != nil {
		t.Fatalf("LastTurnError while control_request inflight = %v, want nil (QUM-635: watchdog must pause while we owe claude a control_response)", err)
	}

	// Resolve the control_request: release the parked handler. It returns a
	// response and dispatchMCPAsync sends the control_response on sendCh,
	// then removes the entry from s.inflight (drains to empty).
	close(bridge.release)
	select {
	case <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("control_response not emitted after releasing the bridge handler")
	}

	// Watchdog must RE-ARM now that inflight is empty: turn is still active
	// (no result frame fed) and silence exceeds HangTimeout, so a genuine
	// hang MUST surface. This proves D1 was paused, not disabled.
	waitFor(t, 1500*time.Millisecond, "ErrHangTimeout after inflight resolved", func() bool {
		e := sess.LastTurnError()
		return e != nil && errors.Is(e, ErrHangTimeout)
	})
}

// TestSession_D1_HangWatchdog_ReseedsBaselineWhenInflightDrains is the
// resume-edge regression guard for QUM-635. lastFrameAt is refreshed only on
// inbound frames; while a control_request is inflight it freezes at the
// request's arrival time. If a human takes longer than HangTimeout to answer
// an ask_user_question, the baseline is stale by the time the request
// resolves. The watchdog must RE-SEED lastFrameAt when inflight drains so the
// resumed turn gets a fresh HangTimeout window — otherwise the first tick
// after the answer faults ErrHangTimeout on the stale (pre-answer) baseline,
// re-introducing the exact bug the fix exists to prevent.
//
// hangCheckInterval is set to an hour so the watchdog cannot tick during the
// test: this isolates the re-seed mechanism (lastFrameAt advancing without any
// inbound frame) from fault timing.
func TestSession_D1_HangWatchdog_ReseedsBaselineWhenInflightDrains(t *testing.T) {
	overrideHangCheckInterval(t, time.Hour)

	transport := newMockManagedTransport()
	bridge := newCtxRespectingToolBridge()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1-reseed",
		HangTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, sess, transport, bridge)

	if _, err := sess.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	transport.feedMessage(t, mcpControlRequest)
	awaitCtxBridgeEntry(ctx, t, bridge)

	s := sess.(*session)
	// Let the baseline (control_request arrival time) age past HangTimeout
	// with no inbound frames, mimicking a human taking >HangTimeout to answer.
	time.Sleep(150 * time.Millisecond)
	before := s.lastFrameAt.Load()

	// Resolve the request: dispatchMCPAsync sends the control_response, then
	// drains the inflight entry to empty.
	close(bridge.release)
	select {
	case <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("control_response not emitted after releasing the bridge handler")
	}

	// No inbound frame is fed, so only the inflight-drain re-seed can advance
	// lastFrameAt past the stale baseline.
	waitFor(t, 2*time.Second, "lastFrameAt re-seeded after inflight drains", func() bool {
		return s.lastFrameAt.Load() > before
	})
}

// TestSession_D1_HangWatchdog_ReArmsAfterControlRequestCancelled is the
// leak-safety guard for QUM-635. Pausing D1 while len(inflight) > 0 means a
// LEAKED inflight entry would permanently neuter the watchdog. dispatchMCPAsync
// deletes its entry in an unconditional defer, so a cancelled/errored request
// must still drain inflight to empty and let D1 re-arm. Here Interrupt cancels
// the in-flight (ctx-respecting) handler; we assert inflight returns to 0 and
// a subsequent silent, still-active turn faults ErrHangTimeout.
func TestSession_D1_HangWatchdog_ReArmsAfterControlRequestCancelled(t *testing.T) {
	overrideHangCheckInterval(t, 20*time.Millisecond)

	transport := newMockManagedTransport()
	bridge := newCtxRespectingToolBridge()
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-d1-cancel-rearm",
		HangTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, sess, transport, bridge)

	if _, err := sess.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	transport.feedMessage(t, mcpControlRequest)
	awaitCtxBridgeEntry(ctx, t, bridge)

	s := sess.(*session)
	// Sanity: the entry is registered (watchdog is now paused).
	s.mu.Lock()
	n := len(s.inflight)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("len(inflight) = %d after dispatch, want 1", n)
	}

	// Cancel the in-flight request via a control_cancel_request (the CLI→client
	// per-request cancel path). The ctx-respecting bridge returns ctx.Err(),
	// dispatchMCPAsync sends an error control_response, and its defer deletes
	// the entry. (QUM-827: Interrupt no longer cancels in-flight handlers, so a
	// targeted control_cancel_request — not Interrupt — is what drains one.)
	transport.feedMessage(t, `{"type":"control_cancel_request","request_id":"mcp-1"}`)
	// Drain whatever the error response queued on the wire.
	go func() {
		for {
			select {
			case <-transport.sendCh:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Leak-safety: inflight MUST drain back to empty even on the cancel path.
	waitFor(t, 2*time.Second, "inflight drains to 0 after cancel", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.inflight) == 0
	})

	// D1 must re-arm: turn still active, nothing inflight, no frames → fault.
	waitFor(t, 1500*time.Millisecond, "ErrHangTimeout after cancelled request drains", func() bool {
		e := sess.LastTurnError()
		return e != nil && errors.Is(e, ErrHangTimeout)
	})
}

// -----------------------------------------------------------------------------
// F2: observer queue flush on Close
// -----------------------------------------------------------------------------

// extractAssistantText pulls the first text content block out of an assistant
// frame's Raw payload. Returns "" on any parse failure.
func extractAssistantText(raw json.RawMessage) string {
	var outer struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return ""
	}
	for _, c := range outer.Message.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

// TestSession_F2_Close_FlushesObserverQueue pins the contract:
//
//	Close() blocks until the observer queue is fully drained or
//	inflightDrainTimeout elapses.
//
// We feed 10 assistants + 1 result, drain `events` to read everything, then
// call Close(). After Close() returns we assert (synchronously, no polling)
// that the observer received all 11 frames. We also assert the observed
// frames are in arrival order — assistant texts must be a-0..a-9 in sequence,
// followed by the result.
//
// If the implementer chooses a "best-effort" flush rather than a blocking
// flush, this test fails — that is the signal to either (a) change the
// implementation to block, or (b) edit this test comment to document the
// weaker contract and relax to a polled assertion.
func TestSession_F2_Close_FlushesObserverQueue(t *testing.T) {
	obs := &recordingObserver{}
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{
		SessionID: "sess-f2-flush",
		Observer:  obs,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	go func() {
		for i := 0; i < 10; i++ {
			transport.feedMessage(t, assistantFrame(i))
		}
		transport.feedMessage(t, resultFrame)
	}()

	for range events { //nolint:revive // drain only
		_ = struct{}{}
	}

	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Synchronous assertion — Close must have flushed.
	obs.mu.Lock()
	msgs := make([]*protocol.Message, len(obs.messages))
	copy(msgs, obs.messages)
	obs.mu.Unlock()

	if len(msgs) < 11 {
		t.Fatalf("observer received %d messages after Close, want >= 11 (10 assistants + 1 result). Close() did not flush.", len(msgs))
	}

	var assistants, results int
	var assistantTexts []string
	for _, m := range msgs {
		switch {
		case m.Type == "assistant":
			assistants++
			assistantTexts = append(assistantTexts, extractAssistantText(m.Raw))
		case m.Type == "result" || strings.HasPrefix(m.Subtype, "success"):
			if m.Type == "result" {
				results++
			}
		}
	}
	if assistants < 10 || results < 1 {
		t.Fatalf("observer saw assistants=%d results=%d; want >= 10 assistants + 1 result", assistants, results)
	}

	// Order preservation: assistant texts must be a-0, a-1, ... a-9 in order.
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("a-%d", i)
		if i >= len(assistantTexts) {
			t.Fatalf("observer missing assistant index %d", i)
		}
		if assistantTexts[i] != want {
			t.Fatalf("assistantTexts[%d] = %q, want %q (order not preserved)", i, assistantTexts[i], want)
		}
	}
}
