// Tests for *WeaveRuntimeHandle (QUM-399 Phase 3). Mirrors the
// runtime_launcher_unified_test.go coverage for *unifiedHandle, but the
// handle is constructed externally via NewWeaveRuntimeHandle (no starter).

package supervisor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// QUM-925 Slice A: weave's system-notification delivery is event-driven and
// UNCONDITIONAL. WakeForDelivery (fired by the producer pokes in real.go on every
// child report_status / send_message) drains the inbox straight to the CLI stdin
// as a kind:system, priority-`next` frame — regardless of weave's turn state.
//
// This REPLACES TestWeaveRuntimeHandle_WakeForDelivery_DoesNotEnqueue_LeavesPendingForPeekAndDrain
// (QUM-471/817), which pinned the exact opposite: that the handle must NOT write,
// leaving pending entries on disk for the TUI's peekAndDrainCmd. That premise is
// retired — peekAndDrainCmd is deleted, because it was (a) a 2s poll, which
// cannot meet "the instant it arrives", (b) idle-gated, which is the QUM-925
// defect, and (c) a SECOND drainer racing this one over the destructive
// the status_change drain. The pin is being deliberately inverted, not lost.

// buildWeaveHandleForTest builds a started root UnifiedRuntime + WeaveRuntimeHandle
// over a fake backend session rooted at a fresh temp SPRAWL_ROOT.
func buildWeaveHandleForTest(t *testing.T) (*WeaveRuntimeHandle, *runtimepkg.UnifiedRuntime, *fakeBackendSession, string) {
	t.Helper()
	h, rt, mock, sprawlRoot := newWeaveHandleForTest(t, nil)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	return h, rt, mock, sprawlRoot
}

// newWeaveHandleForTest is buildWeaveHandleForTest without the Start, for tests
// that need to observe the post-start drain hook. onDelivered may be nil.
//
// Cleanup calls h.Stop (NOT rt.Stop): NewWeaveRuntimeHandle opens an FD on
// activity.ndjson, builds a usage recorder, and starts two EventBus subscriber
// goroutines — only the handle's Stop tears those down.
func newWeaveHandleForTest(t *testing.T, onDelivered func([]string)) (*WeaveRuntimeHandle, *runtimepkg.UnifiedRuntime, *fakeBackendSession, string) {
	t.Helper()
	sprawlRoot := t.TempDir()
	const name = "weave"

	mock := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:        name,
		SprawlRoot:  sprawlRoot,
		Session:     mock,
		IsRoot:      true,
		OnDelivered: onDelivered,
	})
	h, err := NewWeaveRuntimeHandle(rt, mock, sprawlRoot, name)
	if err != nil {
		t.Fatalf("NewWeaveRuntimeHandle: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.Stop(ctx)
	})
	return h, rt, mock, sprawlRoot
}

func seedAsyncEntry(t *testing.T, sprawlRoot, name, id, body string) {
	t.Helper()
	if _, err := agentloop.Enqueue(sprawlRoot, name, agentloop.Entry{
		ID:      id,
		ShortID: id,
		Class:   agentloop.ClassAsync,
		From:    "child",
		Subject: "status",
		Body:    body,
	}); err != nil {
		t.Fatalf("Enqueue(%s): %v", id, err)
	}
}

// markRunning drives the runtime into the wire-confirmed running phase via the
// captured frame router — the same seam echoReplay uses.
func markRunning(t *testing.T, mock *fakeBackendSession) {
	t.Helper()
	mock.mu.Lock()
	h := mock.router
	mock.mu.Unlock()
	if h == nil {
		t.Fatal("no frame router captured — runtime did not install one")
	}
	h(&protocol.Message{Type: "system", Subtype: "session_state_changed"},
		backendpkg.TurnInfo{Autonomous: true, StateChange: "running"})
}

// TestWeaveRuntimeHandle_WakeForDelivery_WritesPendingToStdin_AsSystemNext is the
// QUM-925 core assertion for an IDLE weave: one poke, one priority-`next` stdin
// write carrying the queued maildir body.
//
// QUM-1186: this also carried a status_change line and pinned the QUM-559
// prepend order (status line before queued mail). That channel is deleted, so
// both the seed and the ordering assertion went with it. The one-poke /
// one-frame / priority-next core is untouched.
func TestWeaveRuntimeHandle_WakeForDelivery_WritesPendingToStdin_AsSystemNext(t *testing.T) {
	h, rt, mock, sprawlRoot := buildWeaveHandleForTest(t)

	seedAsyncEntry(t, sprawlRoot, "weave", "id-async-1", "all green")

	if rt.State().InTurn {
		t.Fatal("setup: weave must be idle for this case")
	}
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// settledWrites (not waitForWrites) so an UNWANTED second write is visible —
	// the "coalesced into one frame" half of the AC.
	writes := mock.settledWrites(1, time.Second, 150*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want exactly 1 (QUM-925: one poke drains immediately, coalesced into ONE frame)", len(writes))
	}
	w := writes[0]
	if w.Priority != "next" {
		t.Errorf("write Priority = %q, want %q", w.Priority, "next")
	}
	if w.UUID == "" {
		t.Error("write UUID is empty — the pending zone cannot track→settle the frame without it")
	}
	body := w.Message.Content
	// The queue-flush prompt renders each entry as a messages_read pointer keyed on
	// its short ID — the body is deliberately NOT inlined, so assert on the ID.
	if !strings.Contains(body, "id-async-1") {
		t.Errorf("write body is missing the queued maildir entry pointer:\n%s", body)
	}
}

// TestWeaveRuntimeHandle_WakeForDelivery_MixedClass_OneNextFrame_InterruptFirst
// pins the mixed-class policy, which the deleted TestPeekAndDrainCmd_InterruptPriority
// used to cover for the old pipeline. That test pinned interrupt-class PREEMPTING
// async-class (interrupts drained, asyncs deferred to the next tick). Under QUM-925
// both classes are priority `next`, so there is nothing to preempt WITH: they are
// coalesced into one frame, interrupt-class body first (preserving the old
// precedence as ordering within the frame rather than as delivery scheduling).
func TestWeaveRuntimeHandle_WakeForDelivery_MixedClass_OneNextFrame_InterruptFirst(t *testing.T) {
	h, _, mock, sprawlRoot := buildWeaveHandleForTest(t)

	seedAsyncEntry(t, sprawlRoot, "weave", "id-async-1", "routine update")
	if _, err := agentloop.Enqueue(sprawlRoot, "weave", agentloop.Entry{
		ID: "id-int-1", ShortID: "si1", Class: agentloop.ClassInterrupt,
		From: "child", Subject: "urgent", Body: "stop the presses",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 150*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want exactly 1 (both classes coalesce into one `next` frame)", len(writes))
	}
	if got := writes[0].Priority; got != "next" {
		t.Errorf("write Priority = %q, want %q", got, "next")
	}
	body := writes[0].Message.Content
	intIdx, asyncIdx := strings.Index(body, "si1"), strings.Index(body, "id-async-1")
	if intIdx < 0 || asyncIdx < 0 {
		t.Fatalf("frame must carry BOTH classes (interrupt=%d async=%d):\n%s", intIdx, asyncIdx, body)
	}
	if intIdx > asyncIdx {
		t.Errorf("interrupt-class body must precede async-class body within the frame:\n%s", body)
	}
}

// QUM-1186: TestWeaveRuntimeHandle_WakeForDelivery_StatusChangeWhileInTurn_
// NotLost was removed here. Its subject was that a status_change arriving
// while weave is MID-TURN still reaches stdin — load-bearing precisely because
// DrainStatusChangeLines was destructive, so a dropped line was gone for good.
// The channel and the destructive read are both deleted; maildir entries stay
// in pending/ and are re-drained on the next poke, which the redraw and
// redeliver tests below cover.

// TestWeaveRuntimeHandle_WakeForDelivery_TracksEntryIDsAsSystemKind constrains the
// fix to the RIGHT primitive. The write must be kind:system with its maildir entry
// IDs attached, so that (a) the isReplay consumption ack commits delivery
// (markConsumed -> OnDelivered -> MarkDelivered) and (b) Ctrl+U cannot recall it.
//
// `kind` is unexported across packages, so both properties are asserted through
// behaviour. A WriteUserPrompt-based drain — the pre-QUM-925 TUI path — satisfies
// the write-happened tests above but fails this one.
func TestWeaveRuntimeHandle_WakeForDelivery_TracksEntryIDsAsSystemKind(t *testing.T) {
	var deliveredMu sync.Mutex
	var deliveredIDs []string
	// Mirror PRODUCTION's OnDelivered wiring (cmd/enter.go) so the assertion
	// covers the actual pending/ -> delivered/ maildir move, not just that a
	// callback fired. This replaces the deleted TestCommitDrainCmd_MovesEntriesToDelivered.
	// markRoot is set before rt.Start, and OnDelivered can only fire after Start,
	// so the read below happens-after the write with no synchronisation needed.
	markRoot := new(string)
	h, rt, mock, sprawlRoot := newWeaveHandleForTest(t, func(ids []string) {
		deliveredMu.Lock()
		deliveredIDs = append(deliveredIDs, ids...)
		deliveredMu.Unlock()
		for _, id := range ids {
			_ = agentloop.MarkDelivered(*markRoot, "weave", id)
		}
	})
	*markRoot = sprawlRoot
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	seedAsyncEntry(t, sprawlRoot, "weave", "id-async-1", "all green")
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1", len(writes))
	}

	// A kind:user CONTROL prompt, registered as genuinely cancellable. Without a
	// control the Recall assertion below is vacuous: fakeBackendSession returns
	// cancelled=false for unregistered uuids, and cancelPendingUser drops the text
	// of anything cancelled:false — so Recall would return "" for a kind:user
	// drain too, and the test would ratify the very bug it exists to catch.
	userUUID, err := rt.WriteUserPrompt(context.Background(), "typed by the human", "next")
	if err != nil {
		t.Fatalf("WriteUserPrompt: %v", err)
	}
	mock.setCancelResult(userUUID, true)
	mock.setCancelResult(writes[0].UUID, true) // would cancel IF ever attempted

	// Ctrl+U rehydrates the human's prompt and NOTHING else. A kind:user drain
	// would additionally rehydrate raw <system-notification> text into the input.
	text, err := rt.Recall(context.Background())
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if text != "typed by the human" {
		t.Errorf("Recall rehydrated %q, want exactly %q — the system frame must be excluded (non-recallable)", text, "typed by the human")
	}

	// The isReplay echo commits delivery via the attached entry IDs.
	mock.echoReplay(writes[0].UUID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		deliveredMu.Lock()
		n := len(deliveredIDs)
		deliveredMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	deliveredMu.Lock()
	got := append([]string(nil), deliveredIDs...)
	deliveredMu.Unlock()
	if len(got) != 1 || got[0] != "id-async-1" {
		t.Fatalf("OnDelivered entry IDs = %v, want [id-async-1] — the write did not carry its maildir entry IDs", got)
	}
	pending, err := agentloop.ListPending(sprawlRoot, "weave")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("ListPending = %d entries after the consumption ack, want 0 (entry must move pending/ -> delivered/)", len(pending))
	}
}

// TestWeaveRuntimeHandle_WakeForDelivery_InterruptClass_StaysNextNotNow pins the
// deliberate divergence from the child drain (runtime_launcher.go, which writes
// interrupt-class at priority "now"). For weave, interrupt-class inbox messages
// are written at `next` like every other system frame.
//
// Two load-bearing reasons (approved by axis on behalf of dmotles, QUM-925):
//  1. The LOCKED spec: system frames are `next` and STAY `next` through Ctrl+G.
//  2. A `now` write arms armInterruptLocked and preempts weave's in-flight turn,
//     flatly contradicting "Esc interrupts the turn but system frames remain
//     queued" and the dumb-forwarder rule against being cute about timing.
//
// Consequence, deliberate and documented: an inter-agent
// send_message(interrupt=true) targeting weave is non-preemptive. That is an
// asymmetry vs a child recipient; restoring preemption would be a follow-up
// issue, not a defect in this slice.
func TestWeaveRuntimeHandle_WakeForDelivery_InterruptClass_StaysNextNotNow(t *testing.T) {
	h, rt, mock, sprawlRoot := buildWeaveHandleForTest(t)

	if _, err := agentloop.Enqueue(sprawlRoot, "weave", agentloop.Entry{
		ID:      "id-int-1",
		ShortID: "si1",
		Class:   agentloop.ClassInterrupt,
		From:    "child",
		Subject: "urgent",
		Body:    "stop the presses",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	ch, unsub := rt.EventBus().SubscribeNamed("weave-interrupt-class-test", 32)
	defer unsub()

	// Drive it while BUSY — the only state in which a `now` write would actually
	// preempt something. Idle, the wrong priority is unobservable here.
	markRunning(t, mock)
	if !rt.State().InTurn {
		t.Fatal("setup: runtime must be in-turn for the preemption question to mean anything")
	}

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1", len(writes))
	}
	if got := writes[0].Priority; got != "next" {
		t.Errorf("interrupt-class write Priority = %q, want %q (weave system frames never preempt — QUM-925)", got, "next")
	}
	if !strings.Contains(writes[0].Message.Content, "si1") {
		t.Errorf("write body missing the interrupt-class entry pointer:\n%s", writes[0].Message.Content)
	}

	// No interrupt was armed: the runtime published no EventInterrupted.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Type == runtimepkg.EventInterrupted {
				t.Fatal("interrupt-class drain armed an interrupt — a `now` write preempted weave's turn")
			}
		case <-deadline:
			return
		}
	}
}

// TestWeaveRuntimeHandle_StartDrainsPendingInbox closes the backstop hole left by
// deleting peekAndDrainCmd (the 2s poll was also what delivered an inbox that was
// already non-empty at startup, when no producer poke will ever fire for it).
// The handle registers a post-start drain, so weave restarting with a non-empty
// pending/ and then sitting idle forever still receives it.
//
// The hook fires on rt.Start — i.e. after NewTUIAdapter has subscribed to the
// EventBus — so the frame's EventUserMessageSent reaches the TUI and renders.
// Draining inside NewWeaveRuntimeHandle instead would inject the frame into the
// model but never render it.
func TestWeaveRuntimeHandle_StartDrainsPendingInbox(t *testing.T) {
	_, rt, mock, sprawlRoot := newWeaveHandleForTest(t, nil)
	seedAsyncEntry(t, sprawlRoot, "weave", "id-startup-1", "left over from last session")

	// Nothing may be written before Start: the TUI has not subscribed yet, so a
	// construction-time drain would inject an unrenderable frame.
	if n := len(mock.writesSnapshot()); n != 0 {
		t.Fatalf("stdin writes before Start = %d, want 0 (drain must wait for Start so the frame is renderable)", n)
	}

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes after Start = %d, want 1 (a pending inbox at startup gets no producer poke — the post-start drain is its only delivery)", len(writes))
	}
	if !strings.Contains(writes[0].Message.Content, "id-startup-1") {
		t.Errorf("startup drain wrote the wrong body:\n%s", writes[0].Message.Content)
	}
}

// TestWeaveRuntimeHandle_WakeForDelivery_EmptyInbox_NoWrite guards against the
// unconditional drain turning every poke into a phantom empty turn.
func TestWeaveRuntimeHandle_WakeForDelivery_EmptyInbox_NoWrite(t *testing.T) {
	h, _, mock, _ := buildWeaveHandleForTest(t)

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(mock.writesSnapshot()); n != 0 {
		t.Errorf("stdin writes = %d, want 0 for an empty inbox (no phantom turn)", n)
	}
}

// TestWeaveRuntimeHandle_WakeForDelivery_RedrainBeforeAck_NoDuplicateWrite is the
// highest-probability new defect in this slice, and it is SEQUENTIAL, not
// concurrent. An agentloop pending entry stays in pending/ until its isReplay
// echo drives OnDelivered -> MarkDelivered. QUM-925 makes a poke fire on EVERY
// child report_status / send_message, so a second poke arriving inside that
// window re-runs ListPending, sees the same entry, and writes it again.
//
// The child path has the identical shape and the empirically-measured
// consequence is documented at runtime_launcher.go:583-598 — "an unbounded stdin
// write storm ... ~30 writes/s". Weave inherits it unless in-flight entry IDs are
// filtered.
func TestWeaveRuntimeHandle_WakeForDelivery_RedrainBeforeAck_NoDuplicateWrite(t *testing.T) {
	// Production's OnDelivered wiring (cmd/enter.go) — the ack must actually move
	// the entry out of pending/, or poke #3 legitimately re-drains it.
	markRoot := new(string)
	h, rt, mock, sprawlRoot := newWeaveHandleForTest(t, func(ids []string) {
		for _, id := range ids {
			_ = agentloop.MarkDelivered(*markRoot, "weave", id)
		}
	})
	*markRoot = sprawlRoot
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	seedAsyncEntry(t, sprawlRoot, "weave", "id-async-1", "only once please")

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #1: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes after poke #1 = %d, want 1", len(writes))
	}

	// Poke #2 BEFORE the consumption ack. The entry is still in pending/ — it must
	// NOT be written again.
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(mock.writesSnapshot()); got != 1 {
		t.Fatalf("stdin writes after an un-acked re-poke = %d, want 1 (write storm: the in-flight entry was re-injected)", got)
	}

	// After the ack the entry is consumed; a further poke still must not re-write
	// it (production also removes it from pending/ via MarkDelivered).
	mock.echoReplay(writes[0].UUID)
	time.Sleep(50 * time.Millisecond)
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #3: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	all := mock.writesSnapshot()
	body := ""
	for _, w := range all {
		body += w.Message.Content
	}
	if c := strings.Count(body, "id-async-1"); c != 1 {
		t.Errorf("entry body written %d times across %d stdin writes, want exactly 1", c, len(all))
	}
}

// TestWeaveRuntimeHandle_ConcurrentWakeForDelivery_NoDuplicateWrite: two children
// reporting at the same instant produce two concurrent WakeForDelivery calls on
// different MCP handler goroutines. The agentloop maildir peek can then read
// the same envelope twice — the notification would appear twice in weave's
// context and twice in the transcript. Serialised by drainMu.
//
// QUM-1186: this used to drive BOTH channels, the destructive status_change
// drain and the peek-then-ack agentloop maildir. Only the maildir survives —
// and it was always the channel that could duplicate, so the test keeps its
// teeth. The mutation control below was re-run after this change.
//
// Mutation control (recorded, and re-verified after the QUM-1186 reseed):
// deleting the drainMu lock/unlock makes this go red on the FIRST iteration,
// printing id-async-* more than once. So drainMu is demonstrably load-bearing,
// not defensive. The deterministic (non-timing-dependent) sibling guard is
// TestWeaveRuntimeHandle_WakeForDelivery_RedrainBeforeAck_NoDuplicateWrite above.
func TestWeaveRuntimeHandle_ConcurrentWakeForDelivery_NoDuplicateWrite(t *testing.T) {
	h, _, mock, sprawlRoot := buildWeaveHandleForTest(t)

	const n = 8
	for i := 0; i < n; i++ {
		seedAsyncEntry(t, sprawlRoot, "weave", fmt.Sprintf("id-async-%d", i), fmt.Sprintf("queued-%d", i))
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.WakeForDelivery()
		}()
	}
	wg.Wait()
	// Settle: a drain may complete asynchronously after WakeForDelivery returns.
	time.Sleep(200 * time.Millisecond)

	var all strings.Builder
	for _, w := range mock.writesSnapshot() {
		if w.Priority != "next" {
			t.Errorf("write Priority = %q, want %q", w.Priority, "next")
		}
		all.WriteString(w.Message.Content)
	}
	body := all.String()
	for i := 0; i < n; i++ {
		if got := strings.Count(body, fmt.Sprintf("id-async-%d", i)); got != 1 {
			t.Errorf("maildir entry id-async-%d appears %d times across stdin writes, want exactly 1", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// QUM-547: bounded-teardown regression guards for WeaveRuntimeHandle.Stop
// ---------------------------------------------------------------------------
//
// stopActivity (the join on the activity-subscriber goroutine) and
// activityFile.Close() are both potentially-unbounded blocking calls on Stop.
// If either wedges (e.g. observer parked in OnMessage writing to a stuck NFS
// activityFile, or close() hanging on a stuck FD), Stop must bound the wait,
// log, and proceed — not hang forever (which would deadlock weave.lock during
// the QUM-329 handoff cycle).

func TestWeaveRuntimeHandle_Stop_BoundsWedgedStopActivity(t *testing.T) {
	h, _ := buildStartedWeaveRuntimeHandleForTest(t)

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	h.stopActivity = func() {
		<-block
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.Stop(context.Background()) }()

	bound := 3 * stopActivityTimeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned err = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > bound {
			t.Errorf("Stop returned in %v, want <= %v (stopActivity wedge must be bounded)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("Stop wedged > %v on wedged stopActivity (QUM-547: join is unbounded)", bound)
	}
}

func TestWeaveRuntimeHandle_Stop_BoundsWedgedActivityClose(t *testing.T) {
	h, _ := buildStartedWeaveRuntimeHandleForTest(t)

	block := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block)
		}
	})
	h.activityClose = func() error {
		<-block
		return nil
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.Stop(context.Background()) }()

	bound := 3 * activityCloseTimeout
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Stop returned err = %v, want nil", err)
		}
		if elapsed := time.Since(start); elapsed > bound {
			t.Errorf("Stop returned in %v, want <= %v (activityClose wedge must be bounded)", elapsed, bound)
		}
	case <-time.After(bound):
		t.Fatalf("Stop wedged > %v on wedged activityClose (QUM-547: close is unbounded)", bound)
	}
}

// TestWeaveRuntimeHandle_WakeForDelivery_ConsumedButNotYetDelivered_NoDuplicateWrite
// pins the window one layer beneath RedrainBeforeAck, found in code review.
//
// markConsumed flips the outstanding entry to stateConsumed under outMu, RELEASES
// the lock, and only then calls OnDelivered -> agentloop.MarkDelivered (a rename on
// a shared filesystem). Inside that window the entry is simultaneously:
//   - absent from a statePending-only in-flight filter, and
//   - still returned by agentloop.ListPending (the rename has not happened).
//
// So a poke arriving there re-drains and writes the SAME notification to weave's
// stdin twice. Pokes now fire on every child report_status / send_message and
// bursts are correlated (a fleet finishing a phase), so the window is reachable.
//
// The test holds OnDelivered open to make the window deterministic rather than
// timing-dependent — which is exactly what RedrainBeforeAck's post-echo sleep
// steps over.
func TestWeaveRuntimeHandle_WakeForDelivery_ConsumedButNotYetDelivered_NoDuplicateWrite(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	markRoot := new(string)
	h, rt, mock, sprawlRoot := newWeaveHandleForTest(t, func(ids []string) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release // simulate the shared-FS rename latency
		for _, id := range ids {
			_ = agentloop.MarkDelivered(*markRoot, "weave", id)
		}
	})
	*markRoot = sprawlRoot
	t.Cleanup(func() { close(release) })
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	seedAsyncEntry(t, sprawlRoot, "weave", "id-async-1", "exactly once")
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #1: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes after poke #1 = %d, want 1", len(writes))
	}

	// The CLI acks. markConsumed flips the state, then parks in OnDelivered — the
	// entry is now consumed in memory but still on disk in pending/.
	go mock.echoReplay(writes[0].UUID)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("OnDelivered never fired — the isReplay ack did not reach markConsumed")
	}
	if pending, err := agentloop.ListPending(sprawlRoot, "weave"); err != nil || len(pending) != 1 {
		t.Fatalf("setup: ListPending = %d entries (err=%v), want 1 — the window requires the entry still on disk", len(pending), err)
	}

	// Poke inside the window. It must NOT re-write the notification.
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	all := mock.writesSnapshot()
	body := ""
	for _, w := range all {
		body += w.Message.Content
	}
	if c := strings.Count(body, "id-async-1"); c != 1 {
		t.Errorf("entry written %d times across %d stdin writes, want exactly 1 — a poke inside the consumed-but-not-yet-delivered window duplicated the notification", c, len(all))
	}
}

// TestWeaveRuntimeHandle_StrandedSystemEntry_SuppressedThenRedeliveredOnRestart is
// the QUM-1028 demonstration, run against the drain path rather than inferred from
// a code read (two prior code reads of this mechanism disagreed and both were
// wrong in part).
//
// It establishes four things in order, the third being the load-bearing one:
//  1. a kind:system entry whose isReplay echo never arrives strands as statePending;
//  2. its entry IDs are in InFlightSystemEntryIDs();
//  3. a SUBSEQUENT DRAIN ACTUALLY SKIPS IT — the wedge is real, not inferred;
//  4. and the correction to the "permanently undeliverable" reading: the
//     outstanding map is in-memory, so a restart clears the marker, the post-start
//     drain re-emits the entry, and MarkDelivered never ran — nothing was lost.
//
// So the honest scope of the wedge is "suppressed for the remainder of THIS weave
// session", not "never deliverable again". Slice A trades main's silent
// loss-at-send (commitDrainCmd marked delivered when the frame was written, never
// mind whether the CLI consumed it) for a visible, restart-recoverable delay.
func TestWeaveRuntimeHandle_StrandedSystemEntry_SuppressedThenRedeliveredOnRestart(t *testing.T) {
	sprawlRoot := t.TempDir()
	const name = "weave"

	var delivered []string
	var deliveredMu sync.Mutex
	onDelivered := func(ids []string) {
		deliveredMu.Lock()
		delivered = append(delivered, ids...)
		deliveredMu.Unlock()
		for _, id := range ids {
			_ = agentloop.MarkDelivered(sprawlRoot, name, id)
		}
	}
	newSession := func() (*WeaveRuntimeHandle, *runtimepkg.UnifiedRuntime, *fakeBackendSession) {
		mock := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
		rt := runtimepkg.New(runtimepkg.RuntimeConfig{
			Name: name, SprawlRoot: sprawlRoot, Session: mock,
			IsRoot: true, OnDelivered: onDelivered,
		})
		h, err := NewWeaveRuntimeHandle(rt, mock, sprawlRoot, name)
		if err != nil {
			t.Fatalf("NewWeaveRuntimeHandle: %v", err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = h.Stop(ctx)
		})
		return h, rt, mock
	}

	// --- Session 1: strand the entry (drain writes it; no isReplay echo ever). ---
	h1, rt1, mock1 := newSession()
	if err := rt1.Start(context.Background()); err != nil {
		t.Fatalf("session 1 Start: %v", err)
	}
	seedAsyncEntry(t, sprawlRoot, name, "id-stranded", "never acked")
	if err := h1.WakeForDelivery(); err != nil {
		t.Fatalf("session 1 WakeForDelivery: %v", err)
	}
	w1 := mock1.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(w1) != 1 {
		t.Fatalf("[1] session 1 stdin writes = %d, want 1", len(w1))
	}

	// (2) the marker is present.
	inFlight := rt1.InFlightSystemEntryIDs()
	if _, ok := inFlight["id-stranded"]; !ok {
		t.Fatalf("[2] InFlightSystemEntryIDs() = %v, want it to contain id-stranded", inFlight)
	}

	// (3) THE LOAD-BEARING ONE: a subsequent drain really does skip it. Not
	// inferred — asserted against a second poke, with the entry still on disk.
	if pending, _ := agentloop.ListPending(sprawlRoot, name); len(pending) != 1 {
		t.Fatalf("[3] setup: ListPending = %d, want 1 (entry must still be on disk for the skip to mean anything)", len(pending))
	}
	if err := h1.WakeForDelivery(); err != nil {
		t.Fatalf("session 1 WakeForDelivery #2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := len(mock1.writesSnapshot()); got != 1 {
		t.Fatalf("[3] session 1 stdin writes after a second poke = %d, want 1 — the wedge does NOT reproduce; the entry was re-injected", got)
	}

	// --- Session 2: restart. The in-memory marker is gone with the old runtime. ---
	_, rt2, mock2 := newSession()
	if got := rt2.InFlightSystemEntryIDs(); len(got) != 0 {
		t.Fatalf("[4] fresh runtime InFlightSystemEntryIDs() = %v, want empty", got)
	}
	if err := rt2.Start(context.Background()); err != nil {
		t.Fatalf("session 2 Start: %v", err)
	}

	// (4) the post-start drain re-emits it, and it was never marked delivered.
	w2 := mock2.settledWrites(1, time.Second, 150*time.Millisecond)
	if len(w2) != 1 {
		t.Fatalf("[4] session 2 stdin writes = %d, want 1 (post-start drain must re-emit the stranded entry)", len(w2))
	}
	if !strings.Contains(w2[0].Message.Content, "id-stranded") {
		t.Errorf("[4] session 2 re-emitted the wrong entry:\n%s", w2[0].Message.Content)
	}
	deliveredMu.Lock()
	got := append([]string(nil), delivered...)
	deliveredMu.Unlock()
	if len(got) != 0 {
		t.Errorf("[4] OnDelivered fired with %v — a never-acked entry must NOT be marked delivered (that is main's silent-loss failure)", got)
	}
}

// TestWeaveRuntimeHandle_NormalAck_LeavesNothingInFlight is the assertion that makes
// "the wedge needs an ABNORMAL trigger" an observation rather than an inference — the
// specific question flagged as a potential Slice A blocker (QUM-1028).
//
// On the normal path the CLI echoes isReplay, markConsumed fires OnDelivered ->
// MarkDelivered, and the entry leaves pending/ entirely. So there is nothing left for
// the in-flight filter to suppress and no wedge: reaching the wedge requires the echo
// to never arrive (CLI refusal, mid-turn session death, a dropped frame).
func TestWeaveRuntimeHandle_NormalAck_LeavesNothingInFlight(t *testing.T) {
	markRoot := new(string)
	h, rt, mock, sprawlRoot := newWeaveHandleForTest(t, func(ids []string) {
		for _, id := range ids {
			_ = agentloop.MarkDelivered(*markRoot, "weave", id)
		}
	})
	*markRoot = sprawlRoot
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	seedAsyncEntry(t, sprawlRoot, "weave", "id-normal", "ordinary ping")
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1", len(writes))
	}
	// Control: mid-flight the entry IS suppressed, so the assertion after the ack is
	// measuring the ack and not a filter that never engaged.
	if _, ok := rt.InFlightSystemEntryIDs()["id-normal"]; !ok {
		t.Fatal("setup: entry not in the in-flight set before the ack — nothing to observe")
	}

	mock.echoReplay(writes[0].UUID)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pending, _ := agentloop.ListPending(sprawlRoot, "weave"); len(pending) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending, _ := agentloop.ListPending(sprawlRoot, "weave"); len(pending) != 0 {
		t.Fatalf("ListPending = %d after a normal ack, want 0 — the normal path must fully retire the entry", len(pending))
	}
	// The entry is off disk, so no future drain can see it regardless of the filter.
	// THIS is why the wedge needs an abnormal trigger.
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := len(mock.writesSnapshot()); got != 1 {
		t.Errorf("stdin writes after a post-ack poke = %d, want 1", got)
	}
}

// TestWeaveRuntimeHandle_ConsumedStateStaysSuppressed documents the in-flight filter
// in its second direction, and corrects an assumption that was made about it: a
// stranded entry is NOT un-wedged by flipping its outstanding state to consumed.
//
// This matters for QUM-1000's settleNeverAcked sweep, which settles a never-acked
// entry by flipping state + publishing EventUserMessageConsumed WITHOUT calling
// OnDelivered. Under this filter that sweep does not cause a re-emission — and that
// is the correct outcome, not a limitation:
//
//   - stateConsumed on the NORMAL path means the CLI echoed the frame, so its content
//     is already in the conversation. Re-emitting would duplicate it.
//   - The filter cannot distinguish a genuine echo from a sweep's synthetic flip, so
//     treating consumed as "still suppressed" is the safe reading of an ambiguous
//     state. The sweep's job is to un-strand the RENDER; redelivery of the durable
//     entry is a separate concern, handled by the post-start drain after a restart
//     (see TestWeaveRuntimeHandle_StrandedSystemEntry_SuppressedThenRedeliveredOnRestart),
//     which is safe precisely because MarkDelivered never ran.
func TestWeaveRuntimeHandle_ConsumedStateStaysSuppressed(t *testing.T) {
	// No OnDelivered: mimic a sweep that flips state without marking delivered, so
	// the entry stays on disk in pending/ and only the filter can suppress it.
	h, rt, mock, sprawlRoot := buildWeaveHandleForTest(t)

	seedAsyncEntry(t, sprawlRoot, "weave", "id-swept", "swept not delivered")
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, time.Second, 100*time.Millisecond)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1", len(writes))
	}

	// The ack flips the outstanding entry to consumed. OnDelivered is nil here, so
	// nothing marks it delivered — the entry remains in pending/, exactly the state a
	// settleNeverAcked sweep leaves behind.
	mock.echoReplay(writes[0].UUID)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := rt.InFlightSystemEntryIDs()["id-swept"]; ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending, _ := agentloop.ListPending(sprawlRoot, "weave"); len(pending) != 1 {
		t.Fatalf("setup: ListPending = %d, want 1 (the sweep shape requires the entry still on disk)", len(pending))
	}
	if _, ok := rt.InFlightSystemEntryIDs()["id-swept"]; !ok {
		t.Fatal("a consumed entry left the in-flight set — a poke would now re-write content already in the conversation")
	}

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	all := mock.writesSnapshot()
	body := ""
	for _, w := range all {
		body += w.Message.Content
	}
	if c := strings.Count(body, "id-swept"); c != 1 {
		t.Errorf("entry written %d times, want exactly 1 — a consumed-but-not-delivered entry must stay suppressed within the session", c)
	}
}

// TestWeaveRuntimeHandle_InboxRedrain_DeliversUnpokedEntry closes the last
// delivery hole left by deleting the TUI's 2s poll. The event-driven poke path
// covers everything that goes through Real.SendMessage, but
// NOT an entry that appears in pending/ with no in-process producer:
//
//   - an out-of-process writer putting an envelope in the maildir directly (the
//     deleted poll's own comment named this as a case it existed to cover), and
//   - any poke dropped by Real's startedRuntime liveness gate (weave Starting /
//     Pausing / Faulted at the moment the entry landed).
//
// Those entries used to be picked up within 2s. Without a backstop they sit in
// pending/ until unrelated traffic pokes weave or the process restarts, i.e.
// possibly forever on an idle fleet.
//
// The redrain ticker is safe to add now in a way the old poll was not: it calls the
// SAME single drainer, so drainMu serialises it against pokes and the in-flight
// entry-ID filter stops it re-injecting anything already written. It is also NOT
// idle-gated — that gating was the original QUM-925 defect.
func TestWeaveRuntimeHandle_InboxRedrain_DeliversUnpokedEntry(t *testing.T) {
	withShortInboxRedrainInterval(t, 60*time.Millisecond)

	_, rt, mock, sprawlRoot := newWeaveHandleForTest(t, nil)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	// Nothing pending at Start, so the post-start drain writes nothing. This is
	// what makes the assertion below attributable to the redrain ticker and not to it.
	if n := len(mock.settledWrites(0, 0, 150*time.Millisecond)); n != 0 {
		t.Fatalf("setup: stdin writes = %d immediately after Start, want 0", n)
	}

	// An entry appears with NO poke — h.WakeForDelivery is never called.
	seedAsyncEntry(t, sprawlRoot, "weave", "id-unpoked", "nobody poked for me")

	writes := mock.waitForWrites(1, 3*time.Second)
	if len(writes) != 1 {
		t.Fatalf("stdin writes = %d, want 1 — an entry with no producer poke was never delivered (the deleted 2s poll was its only path)", len(writes))
	}
	if got := writes[0].Priority; got != "next" {
		t.Errorf("redrain write Priority = %q, want %q", got, "next")
	}
	if !strings.Contains(writes[0].Message.Content, "id-unpoked") {
		t.Errorf("redrain ticker wrote the wrong body:\n%s", writes[0].Message.Content)
	}

	// And the ticker does not re-inject it on subsequent ticks: it shares the
	// in-flight filter with the poke path.
	time.Sleep(400 * time.Millisecond)
	all := mock.writesSnapshot()
	body := ""
	for _, w := range all {
		body += w.Message.Content
	}
	if c := strings.Count(body, "id-unpoked"); c != 1 {
		t.Errorf("entry written %d times across %d writes, want exactly 1 — the redrain ticker is re-injecting an in-flight entry every tick", c, len(all))
	}
}

// TestWeaveRuntimeHandle_InboxRedrain_StopsWithHandle guards against the redrain
// goroutine outliving the handle (it holds drainMu and writes to a torn-down
// session). Stop must join it.
func TestWeaveRuntimeHandle_InboxRedrain_StopsWithHandle(t *testing.T) {
	withShortInboxRedrainInterval(t, 30*time.Millisecond)

	sprawlRoot := t.TempDir()
	mock := newFakeBackendSession("sess-weave", backendpkg.Capabilities{})
	rt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name: "weave", SprawlRoot: sprawlRoot, Session: mock, IsRoot: true,
	})
	h, err := NewWeaveRuntimeHandle(rt, mock, sprawlRoot, "weave")
	if err != nil {
		t.Fatalf("NewWeaveRuntimeHandle: %v", err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop, an entry appearing must NOT be written — the ticker is gone.
	before := len(mock.writesSnapshot())
	seedAsyncEntry(t, sprawlRoot, "weave", "id-after-stop", "too late")
	time.Sleep(300 * time.Millisecond)
	if got := len(mock.writesSnapshot()); got != before {
		t.Errorf("stdin writes went %d -> %d after Stop — the redrain goroutine outlived the handle", before, got)
	}
}

// withShortInboxRedrainInterval shortens the inbox-redrain ticker interval for the
// duration of a test (restored via t.Cleanup) so redrain tests neither wait 5s nor
// depend on the production cadence.
func withShortInboxRedrainInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := weaveInboxRedrainInterval.get()
	weaveInboxRedrainInterval.set(d)
	t.Cleanup(func() { weaveInboxRedrainInterval.set(prev) })
}

// TestWeaveRuntimeHandle_EmptyDrain_WritesNothing pins that a drain with nothing
// pending and no status lines is silent. It is the negative control the other
// drain tests in this file lean on: without it, an assertion that a drain emits
// exactly one frame could be satisfied by a drain that always emits one.
func TestWeaveRuntimeHandle_EmptyDrain_WritesNothing(t *testing.T) {
	h, _, mock, _ := newWeaveHandleForTest(t, nil)

	h.drainPendingToStdin()

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.writes) != 0 {
		t.Errorf("drain wrote %d stdin frames with nothing pending, want 0", len(mock.writes))
	}
}
