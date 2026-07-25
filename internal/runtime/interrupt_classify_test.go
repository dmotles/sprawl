package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-827: a user-initiated Esc-abort that lands MID-TURN must surface as a
// clean interrupt (EventInterrupted → InterruptCompletedMsg "Interrupted"),
// NOT as the interrupted turn's is_error `result` frame (which routeFrame would
// otherwise publish as EventTurnCompleted{IsError} → SessionResultMsg{IsError}
// → the empty "Session Error" γ-overlay). UnifiedRuntime.Interrupt only emitted
// the synthetic EventInterrupted on the !inTurn branch, so an in-turn interrupt
// fell through to the error path.

func resultFrame(t *testing.T, isError bool, durationMs int) *protocol.Message {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type":           "result",
		"subtype":        "success",
		"is_error":       isError,
		"duration_ms":    durationMs,
		"num_turns":      1,
		"total_cost_usd": 0.0,
		"result":         "",
	})
	if err != nil {
		t.Fatalf("marshal result frame: %v", err)
	}
	return &protocol.Message{Type: "result", Subtype: "success", Raw: raw}
}

func openTurn(t *testing.T, rt *UnifiedRuntime) {
	t.Helper()
	// QUM-903: a bare init opens the frame lifecycle but no longer sets in_turn;
	// the authoritative wire `running` signal does. Drive both so the turn is
	// genuinely in flight (InTurn=true) before the test arms its interrupt.
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "init"}, backend.TurnInfo{Autonomous: true})
	rt.routeFrame(&protocol.Message{Type: "system", Subtype: "session_state_changed"}, backend.TurnInfo{Autonomous: true, StateChange: "running"})
	deadline := time.Now().Add(2 * time.Second)
	for !rt.State().InTurn {
		if time.Now().After(deadline) {
			t.Fatal("turn never entered InTurn")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// openTurnBoundary drives the QUM-927 wire shape: a frame-level turn is open
// (init routed) but the CLI has already reported session_state_changed:idle
// after the model's end_turn while async Agent sidechains are still resolving.
// So State().InTurn is FALSE even though a terminal `result` for that turn is
// still inbound.
func openTurnBoundary(t *testing.T, rt *UnifiedRuntime) {
	t.Helper()
	openTurn(t, rt)
	rt.routeFrame(stateFrame(protocol.SessionStateIdle))
	if rt.State().InTurn {
		t.Fatal("turn-boundary setup: State().InTurn is still true after wire idle; the test would exercise the mid-turn path instead")
	}
	// The whole point of the shape: the FRAME turn is still open, so its terminal
	// `result` is still inbound even though the phase machine reads idle.
	if !frameTurnOpenFlag(rt) {
		t.Fatal("turn-boundary setup: frame turn is not open; there would be no inbound terminal to re-classify")
	}
}

// interruptPendingFlag reads the pending-interrupt flag under mu.
func interruptPendingFlag(rt *UnifiedRuntime) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.interruptPending
}

// frameTurnOpenFlag reads the frame-turn-open mirror under mu.
func frameTurnOpenFlag(rt *UnifiedRuntime) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.frameTurnOpen
}

// assertNoTerminalYet drains a window after Interrupt and before the terminal
// frame is routed, asserting NOTHING terminal has been published. Two things
// ride on this: the QUM-775 synthetic must be suppressed while a real terminal is
// inbound (QUM-927 — a spurious turn-boundary signal can unblock StopAfterTurn
// mid-turn), and it keeps the post-terminal window's `interrupted == 1` strictly
// about the re-classification.
func assertNoTerminalYet(t *testing.T, ch <-chan RuntimeEvent) {
	t.Helper()
	interrupted, completed, failed := tallyTerminalEvents(ch, 150*time.Millisecond)
	if interrupted != 0 || completed != 0 || failed != 0 {
		t.Fatalf("pre-terminal window: interrupted=%d completed=%d failed=%d, want 0/0/0 (a real terminal is inbound; no event may be published before it)", interrupted, completed, failed)
	}
}

// waitForBackendFaulted drains ch for `window`, reporting whether an
// EventBackendFaulted was published.
func waitForBackendFaulted(ch <-chan RuntimeEvent, window time.Duration) bool {
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return false
			}
			if ev.Type == EventBackendFaulted {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// QUM-927: Esc pressed at a TURN BOUNDARY — after the model's end_turn (wire
// state already `idle`) but while async Agent sidechains are still resolving and
// the frame-level turn is still open. The backend still emits an is_error
// `result` terminal for the interrupt; without arming the pending-interrupt flag
// on this path routeFrame publishes EventTurnCompleted{IsError} → the spurious
// "Session Error" quit/restart modal.
func TestInterrupt_AtTurnBoundary_TrailingIsErrorSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-esc", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-esc-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)

	// seq 1163 in the QUM-927 wire capture.
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the terminal frame must be re-classified as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 (interrupted turn must not surface as a completed/error turn → Session Error)", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the interrupted turn's result (for the Interrupted-duration UX)")
	}
}

// QUM-927: same boundary shape, but the interrupt closes the stream with no
// terminal `result` (EndOfTurn && msg==nil) — must still be a clean interrupt,
// not EventTurnFailed{stream-closed}.
func TestInterrupt_AtTurnBoundary_StreamCloseSurfacesInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-streamclose", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-streamclose-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the stream-close terminal must be re-classified as a clean interrupt)", interrupted)
	}
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}

// QUM-927: the arm must survive an intervening wire `running` state change. At a
// turn boundary the CLI commonly reports running again as it processes the
// interrupt / next sidechain frame; setPhaseLocked's idle→non-idle clear would
// otherwise clobber the arm before the terminal is routed, re-opening the bug.
func TestInterrupt_AtTurnBoundary_WireRunningDoesNotClearArm(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-running", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-running-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(stateFrame(protocol.SessionStateRunning))
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (an intervening wire `running` must not clear the boundary arm)", interrupted)
	}
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the re-classified terminal's result")
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
}

// QUM-927: with NO frame-level turn open there is no terminal inbound to
// re-classify, so Interrupt must not arm the flag at all — otherwise it sits
// armed and could mis-classify an unrelated later error.
func TestInterrupt_TrulyIdle_NoFrameTurn_DoesNotArm(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-truly-idle", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if interruptPendingFlag(rt) {
		t.Error("interruptPending armed by an interrupt with no frame turn open; want false (no terminal inbound to re-classify)")
	}
}

// QUM-927 must not regress QUM-775 item 4: with no frame turn open, no real
// terminal is inbound, so Interrupt still emits the synthetic EventInterrupted
// that unwedges a TUI turnState reducer stuck in TurnStreaming after a dropped
// terminal event. Conversely, at a boundary (a real terminal IS inbound) the
// synthetic is suppressed — a duplicate turn-boundary signal can unblock
// StopAfterTurn (QUM-866) / the pause waiter while the frame turn is still open.
// Both halves are asserted here so the pair of semantics is explicit.
func TestInterrupt_SyntheticFiresOnlyWhenNoTerminalInbound(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-synthetic-gate", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("synthetic-gate-test", 32)
	defer unsub()

	// (a) Genuinely idle, no frame turn: the QUM-775 synthetic still fires.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	interrupted, _, _, lastInterrupt := tallyTerminalEventsWithResult(ch, 250*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("idle interrupt: EventInterrupted count = %d, want 1 (QUM-775 synthetic)", interrupted)
	}
	if lastInterrupt != nil {
		t.Error("idle interrupt: synthetic EventInterrupted carried a Result; want nil (there is no terminal)")
	}

	// (b) At a boundary a real terminal is inbound, so the synthetic is suppressed
	// and only the re-classified terminal publishes.
	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if interrupted, _, _ := tallyTerminalEvents(ch, 250*time.Millisecond); interrupted != 0 {
		t.Errorf("boundary interrupt: EventInterrupted count = %d before the terminal, want 0 (synthetic must be suppressed)", interrupted)
	}
}

// QUM-927: once the boundary-armed flag is consumed by its own turn's terminal,
// a SUBSEQUENT turn's genuine is_error result must still surface as
// EventTurnCompleted{IsError} — the arm must not leak forward and swallow it.
func TestInterrupt_AtTurnBoundary_ArmDoesNotLeakToNextTurn(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-noleak", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-noleak-test", 32)
	defer unsub()

	// Turn 1: interrupted at the boundary.
	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 1 || completed != 0 {
		t.Fatalf("turn 1: interrupted=%d completed=%d, want 1/0 (the arm must have been armed AND consumed here)", interrupted, completed)
	}

	// Turn 2: a genuine error result, no interrupt. Must NOT be re-classified.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, true, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ = tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("turn 2: EventInterrupted count = %d, want 0 (boundary arm leaked and swallowed a real error)", interrupted)
	}
	if completed != 1 {
		t.Errorf("turn 2: EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-927: the boundary arm must not survive a fresh turn/resume boundary that
// arrives WITHOUT a terminal `result` for the armed turn. A `system/init` with
// the frame turn already open is exactly that case (routeFrame's clear-on-open
// is gated on !st.open, so it does not fire) — if the arm survived it, the next
// turn's genuine is_error result would be swallowed as a clean interrupt.
func TestInterrupt_AtTurnBoundary_ArmDoesNotSurviveInit(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-init", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-init-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)

	// A fresh turn/resume boundary lands with no terminal for the armed turn.
	feedInit(rt)
	if interruptPendingFlag(rt) {
		t.Error("interruptPending survived a system/init turn boundary; a stale arm can swallow the next turn's real error")
	}

	// The next turn's genuine error must surface as a real error.
	rt.routeFrame(resultFrame(t, true, 9), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (stale arm swallowed a real error across init)", interrupted)
	}
	if completed != 1 {
		t.Errorf("EventTurnCompleted count = %d, want 1", completed)
	}
}

// QUM-927: a boundary interrupt whose turn terminates CLEANLY (is_error=false)
// is still classified as an interrupt — the user did press Esc, and the
// classification deliberately does not depend on is_error (same contract as the
// QUM-827 mid-turn path). Pinned so the semantic is explicit, not accidental.
func TestInterrupt_AtTurnBoundary_CleanTerminalStillInterrupt(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-clean", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("boundary-clean-test", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	rt.routeFrame(resultFrame(t, false, 12), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (classification does not depend on is_error)", interrupted)
	}
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the re-classified terminal's result")
	}
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}

// QUM-927 AC: a genuine backend fault at a turn boundary with NO preceding
// interrupt still surfaces EventBackendFaulted AND an EventTurnFailed terminal.
func TestGenuineFault_NoPrecedingInterrupt_StillFaultsAtBoundary(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-fault", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	faultCh, unsubFault := rt.EventBus().SubscribeNamed("boundary-fault-fault", 32)
	defer unsubFault()
	turnCh, unsubTurn := rt.EventBus().SubscribeNamed("boundary-fault-turn", 32)
	defer unsubTurn()

	openTurnBoundary(t, rt)
	mock.fireTerminalErr(backend.ErrSubprocessExited)
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if !waitForBackendFaulted(faultCh, time.Second) {
		t.Error("EventBackendFaulted was not published for a genuine subprocess-exit fault")
	}
	// TWO EventTurnFailed publishes are expected, from two distinct publishers:
	// the SetTerminalErrorHandler closure (carrying the REAL fault) and the
	// orphan-teardown branch in routeFrame (carrying the generic
	// errStreamClosedNoResult, because the stream closed with no terminal
	// `result`). This is the same shape the mid-turn path has always had; the
	// QUM-927 rework widened the handler's gate to include frameTurnOpen so the
	// boundary path matches it.
	//
	// The double publish is NOT harmless, which is why QUM-967 tracks it: both
	// translate to SessionResultMsg{IsError} and the second one CLOBBERS the
	// first's text (app.go rebuilds errorDialog from msg.Result), so on the no-arm
	// fault path the user reads the generic "autonomous turn stream closed without
	// terminal result" instead of the real "claude subprocess exited unexpectedly".
	// The assertion below is what keeps the REAL fault present at all.
	//
	// ONE drain feeds every assertion below. Splitting it would silently kill the
	// misclassification guard: a second drain over the same subscription reads an
	// already-empty channel, so `interrupted` could only ever be 0 and the "a real
	// fault must not surface as a clean interrupt" check — the exact property
	// QUM-927 regressed — would pass unconditionally.
	interrupted, _, failures := tallyTerminalEventsWithErrors(turnCh, 400*time.Millisecond)
	if len(failures) != 2 {
		t.Errorf("EventTurnFailed count = %d, want 2 (the terminal-error handler AND the orphan teardown); errs=%v", len(failures), failures)
	}
	// The load-bearing assertion: one of them must name the REAL fault. Without
	// this, the widened gate could regress and the count could still be met by
	// the orphan branch alone plus any other generic failure.
	var sawRealFault bool
	for _, err := range failures {
		if errors.Is(err, backend.ErrSubprocessExited) {
			sawRealFault = true
		}
	}
	if !sawRealFault {
		t.Errorf("no EventTurnFailed carried the real fault (%v); the fault-surface gate is suppressing it at the turn boundary, so the TUI cannot name the failure; errs=%v",
			backend.ErrSubprocessExited, failures)
	}
	if interrupted != 0 {
		t.Errorf("EventInterrupted count = %d, want 0 (a genuine fault with no preceding interrupt must never be misclassified as a clean interrupt)", interrupted)
	}
}

// QUM-927 AC: even when a boundary interrupt DID precede a genuine fault, the
// session-fault surface is independent — EventBackendFaulted must still fire,
// AND a real EventTurnFailed naming the fault must still be published.
//
// The rework corrected what this test asserts. It previously demanded failed==0
// on the theory that the arm's re-label of the TEARDOWN event was the whole
// story. It isn't: EventBackendFaulted has no root-session consumer
// (runFaultSubscriber is children-only) and TranslateRuntimeEvent maps it to
// nil, so "only the turn event is re-labelled" meant the TUI surfaced NOTHING
// for a dead subprocess. Asserting failed==0 here actively codified that gap —
// a bus-level test cannot see an event the TUI never consumes. The reducer-level
// counterpart lives in internal/tui/app_boundary_fault_test.go.
func TestGenuineFault_AfterBoundaryInterrupt_StillPublishesBackendFaulted(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "weave-boundary-fault-esc", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	// Two subscriptions so draining for the fault event cannot consume the turn
	// event (their relative order is not guaranteed).
	faultCh, unsubFault := rt.EventBus().SubscribeNamed("boundary-fault-esc-fault", 32)
	defer unsubFault()
	ch, unsub := rt.EventBus().SubscribeNamed("boundary-fault-esc-turn", 32)
	defer unsub()

	openTurnBoundary(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	assertNoTerminalYet(t, ch)
	mock.fireTerminalErr(backend.ErrSubprocessExited)
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	if !waitForBackendFaulted(faultCh, time.Second) {
		t.Error("EventBackendFaulted was suppressed by the interrupt re-classification; real faults must not be swallowed")
	}
	// The arm re-labels the TEARDOWN event as a clean interrupt, but the fault
	// must ALSO surface as a real EventTurnFailed naming it — that is the event
	// the root TUI actually consumes (→ SessionResultMsg{IsError} → the "Session
	// Error" dialog with [r] restart).
	interrupted, completed, failed, _ := tallyTerminalEventsWithResult(ch, 400*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (the interrupted turn is re-labelled, exactly once)", interrupted)
	}
	if failed != 1 {
		t.Errorf("EventTurnFailed count = %d, want 1 (the genuine fault must still fail the turn so the TUI can surface it); "+
			"0 here means a dead subprocess renders as a clean \"Interrupted\"", failed)
	}
	if completed != 0 {
		t.Errorf("completed=%d, want 0", completed)
	}
}

// QUM-927 / QUM-830: a Ctrl+G send-all-now priority:"now" write at a turn
// boundary preempts the still-open turn the same way, producing the identical
// is_error terminal — it must classify as a clean interrupt too.
func TestSendAllNow_NowWriteAtTurnBoundary_SurfacesInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sendnow-boundary", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-boundary-test", 32)
	defer unsub()

	writePendingUser(t, rt, mock, "send me now", "next")
	openTurnBoundary(t, rt)
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (boundary now-write preempt must surface as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
}

// tallyTerminalEvents drains ch for `window` and counts the terminal turn
// events. Returns (interrupted, completed, failed).
func tallyTerminalEvents(ch <-chan RuntimeEvent, window time.Duration) (int, int, int) {
	interrupted, completed, failed, _ := tallyTerminalEventsWithResult(ch, window)
	return interrupted, completed, failed
}

// tallyTerminalEventsWithErrors is tallyTerminalEvents plus the actual
// EventTurnFailed errors, so a test can assert WHICH fault surfaced and not
// merely that one did — "some turn-failed event fired" is satisfied by the
// orphan branch's generic errStreamClosedNoResult, which would hide a
// regression of the real fault surface.
//
// It returns everything from ONE drain on purpose. A channel drain is
// destructive, so two sequential drains over the same subscription cannot both
// see the same events: the first consumes the window and any assertion built on
// the second is dead — it can only ever observe zero. (QUM-927 rework)
func tallyTerminalEventsWithErrors(ch <-chan RuntimeEvent, window time.Duration) (interrupted, completed int, failures []error) {
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return interrupted, completed, failures
			}
			switch ev.Type {
			case EventInterrupted:
				interrupted++
			case EventTurnCompleted:
				completed++
			case EventTurnFailed:
				failures = append(failures, ev.Error)
			}
		case <-deadline:
			return interrupted, completed, failures
		}
	}
}

// QUM-830: send-all-now (Ctrl+G) writes one priority:"now" message that
// PREEMPTS the in-flight model turn (cancel-and-replace). The preempted turn
// emits an is_error `result` terminal frame — the SAME shape an Esc-abort
// produces. Without arming the pending-interrupt flag on the now-write,
// routeFrame publishes EventTurnCompleted{IsError} → SessionResultMsg{IsError}
// → the empty "Session Error" overlay → session restart. QUM-827 fixed this for
// the bare-Esc path only; the now-write preempt is a separate entry that must
// be classified as a clean interrupt too.
func TestSendAllNow_NowWritePreemptMidTurn_SurfacesInterruptNotError(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sendnow-preempt", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-preempt-test", 32)
	defer unsub()

	// A human-typed prompt queued behind the in-flight turn.
	writePendingUser(t, rt, mock, "send me now", "next")

	// Turn in flight.
	openTurn(t, rt)

	// Ctrl+G send-all-now: cancels the pending prompt and writes one now-priority
	// message that preempts the in-flight turn.
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}

	// The preempted turn's terminal is_error result.
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed, lastInterrupt := tallyTerminalEventsWithResult(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (now-write preempt must surface as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 (preempted turn must not surface as a completed/error turn → Session Error)", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
	// The interrupt carries the terminal result so the TUI can render
	// "Interrupted (Nms)" rather than an empty/error overlay.
	if lastInterrupt == nil {
		t.Error("EventInterrupted carried a nil Result; want the preempted turn's result (for the Interrupted-duration UX)")
	}
}

// tallyTerminalEventsWithResult is tallyTerminalEvents plus the Result of the
// last EventInterrupted seen (nil if none) — used to assert the preempt path
// carries the terminal result for the "Interrupted (Nms)" UX.
func tallyTerminalEventsWithResult(ch <-chan RuntimeEvent, window time.Duration) (int, int, int, *protocol.ResultMessage) {
	var interrupted, completed, failed int
	var lastInterrupt *protocol.ResultMessage
	deadline := time.After(window)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return interrupted, completed, failed, lastInterrupt
			}
			switch ev.Type {
			case EventInterrupted:
				interrupted++
				lastInterrupt = ev.Result
			case EventTurnCompleted:
				completed++
			case EventTurnFailed:
				failed++
			}
		case <-deadline:
			return interrupted, completed, failed, lastInterrupt
		}
	}
}

// TestSendAllNow_NowWriteArm_DoesNotLeakToNextTurn guards the QUM-827 stale-flag
// invariant for the new arm site: once the preempted turn consumes the pending-
// interrupt flag, a SUBSEQUENT clean turn completion must publish
// EventTurnCompleted, not EventInterrupted.
func TestSendAllNow_NowWriteArm_DoesNotLeakToNextTurn(t *testing.T) {
	mock := &mockUnifiedSession{cancelResults: map[string]bool{}}
	rt := New(RuntimeConfig{Name: "weave-sendnow-noleak", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("sendnow-noleak-test", 32)
	defer unsub()

	// Turn 1: preempted by send-all-now.
	writePendingUser(t, rt, mock, "send me now", "next")
	openTurn(t, rt)
	if err := rt.SendAllNow(context.Background()); err != nil {
		t.Fatalf("SendAllNow: %v", err)
	}
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	if interrupted, _, _ := tallyTerminalEvents(ch, 400*time.Millisecond); interrupted != 1 {
		t.Fatalf("turn 1: EventInterrupted count = %d, want 1", interrupted)
	}

	// Turn 2: clean completion — must NOT inherit the consumed interrupt flag.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, false, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("turn 2: EventInterrupted count = %d, want 0 (now-write arm leaked)", interrupted)
	}
	if completed != 1 {
		t.Errorf("turn 2: EventTurnCompleted count = %d, want 1", completed)
	}
}

func TestUnifiedRuntime_InTurnInterruptEmitsEventInterrupted(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-interrupt", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("esc-interrupt-test", 32)
	defer unsub()

	openTurn(t, rt)

	// User Esc mid-turn.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// claude's interrupted-turn terminal result (is_error, empty text).
	rt.routeFrame(resultFrame(t, true, 42), backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (in-turn interrupt must surface as a clean interrupt)", interrupted)
	}
	if completed != 0 {
		t.Errorf("EventTurnCompleted count = %d, want 0 (interrupted turn must not surface as a completed/error turn)", completed)
	}
	if failed != 0 {
		t.Errorf("EventTurnFailed count = %d, want 0", failed)
	}
}

// TestUnifiedRuntime_InTurnInterrupt_StreamClose covers the alternate path
// where the interrupt closes the stream with no terminal `result` frame
// (EndOfTurn && msg==nil): it too must surface as EventInterrupted, not the
// EventTurnFailed{stream-closed} error.
func TestUnifiedRuntime_InTurnInterrupt_StreamClose(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-streamclose", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("esc-streamclose-test", 32)
	defer unsub()

	openTurn(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	rt.routeFrame(nil, backend.TurnInfo{Autonomous: true, EndOfTurn: true})

	interrupted, completed, failed := tallyTerminalEvents(ch, 750*time.Millisecond)
	if interrupted != 1 {
		t.Errorf("EventInterrupted count = %d, want 1 (stream-close after interrupt is still a clean interrupt)", interrupted)
	}
	if completed != 0 || failed != 0 {
		t.Errorf("completed=%d failed=%d, want 0/0", completed, failed)
	}
}

// TestUnifiedRuntime_InterruptIsQueueNonDestructive pins the locked QUM-827 /
// QUM-828 contract: Esc is a pure halt — UnifiedRuntime.Interrupt must NOT
// touch the outstanding-map queue. A queued (kind:user, state:pending) entry
// must survive the abort unchanged so the CLI consumes it on its next
// iteration.
func TestUnifiedRuntime_InterruptIsQueueNonDestructive(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-queue", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	// Queue a pending user message, then open a turn.
	uuid, err := rt.WriteUserPrompt(context.Background(), "queued while busy", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	openTurn(t, rt)

	// User Esc mid-turn.
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// The queued entry must still be present and still pending.
	out := rt.Outstanding()
	e, ok := out[uuid]
	if !ok {
		t.Fatalf("queued message %s was dropped by Interrupt; the abort must be queue-non-destructive", uuid)
	}
	if e.kind != kindUser || e.state != statePending {
		t.Errorf("queued entry kind/state = %v/%v after Interrupt, want kindUser/statePending (untouched)", e.kind, e.state)
	}
}

// TestUnifiedRuntime_InterruptFlagDoesNotLeakToNextTurn guards the stale-flag
// race: after an interrupt is consumed by one turn-end, a SUBSEQUENT normal
// turn completion must publish EventTurnCompleted, not EventInterrupted.
func TestUnifiedRuntime_InterruptFlagDoesNotLeakToNextTurn(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{Name: "agent-esc-noleak", Session: mock})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	}()

	ch, unsub := rt.EventBus().SubscribeNamed("esc-noleak-test", 32)
	defer unsub()

	// Turn 1: interrupted.
	openTurn(t, rt)
	if err := rt.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	rt.routeFrame(resultFrame(t, true, 5), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	if interrupted, _, _ := tallyTerminalEvents(ch, 400*time.Millisecond); interrupted != 1 {
		t.Fatalf("turn 1: EventInterrupted count = %d, want 1", interrupted)
	}

	// Turn 2: clean completion, NO interrupt. Must publish EventTurnCompleted.
	openTurn(t, rt)
	rt.routeFrame(resultFrame(t, false, 7), backend.TurnInfo{Autonomous: true, EndOfTurn: true})
	interrupted, completed, _ := tallyTerminalEvents(ch, 400*time.Millisecond)
	if interrupted != 0 {
		t.Errorf("turn 2: EventInterrupted count = %d, want 0 (stale interrupt flag leaked)", interrupted)
	}
	if completed != 1 {
		t.Errorf("turn 2: EventTurnCompleted count = %d, want 1", completed)
	}
}
