package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
)

// QUM-640: mid-turn background-task-completion auto-continuation.
//
// When a `run_in_background` task completes MID-TURN (while weave is busy),
// the claude SDK delivers a `system/task_notification` frame into the active
// turn but does NOT auto-continue after the turn's `result`. Weave stalls
// until manual input.
//
// The fix (NOT exercised here — these tests are TDD red-phase) is for the
// turn loop to detect a mid-turn `task_notification`, and at clean turn-end
// auto-enqueue ONE synthetic continuation queue item so the existing
// queue→turnloop→StartTurn path fires a continuation turn with no manual
// input.
//
// These tests reuse the in-memory scriptedTransport + real backend.Session
// harness defined in turnloop_perturndeadline_test.go (same package). During
// a sprawl-initiated turn the backend routes inbound system/task_notification
// frames to the active turn's subscriber (internal/backend/session.go
// runReader), so they flow through executeTurn's drain loop and surface as
// EventProtocolMessage.
//
// Assertions are deliberately class-agnostic (observe EventTurnStarted counts
// and outbound Sends) because the continuation queue item's class
// (`ClassContinuation`) does not exist yet.

// taskNotificationFrame builds a system/task_notification wire frame for the
// given task_id, mirroring the QUM-632 wire capture.
func taskNotificationFrame(taskID string) string {
	return `{"type":"system","subtype":"task_notification","session_id":"sess-qum640","task_id":"` + taskID + `"}`
}

// taskNotificationFrameNoID builds a system/task_notification wire frame that
// omits task_id, mirroring older harness frames that predate the field.
func taskNotificationFrameNoID() string {
	return `{"type":"system","subtype":"task_notification","session_id":"sess-qum640"}`
}

// countTurnStartedWithin counts EventTurnStarted events arriving on sub over a
// bounded window. Used for negative-absence assertions ("no extra turn fired").
func countTurnStartedWithin(sub <-chan RuntimeEvent, window time.Duration) int {
	deadline := time.After(window)
	n := 0
	for {
		select {
		case ev, ok := <-sub:
			if !ok {
				return n
			}
			if ev.Type == EventTurnStarted {
				n++
			}
		case <-deadline:
			return n
		}
	}
}

// TestTurnLoop_MidTurnTaskNotification_SchedulesContinuation pins the core
// contract: a task_notification arriving MID-TURN (before the result) must,
// at clean turn-end, auto-fire exactly one continuation turn with NO manual
// Enqueue/Wake — observed as a SECOND EventTurnStarted and a SECOND outbound
// user-prompt Send.
//
// FAILS today: no continuation fires; the loop blocks on the queue Signal
// after turn 1 completes.
func TestTurnLoop_MidTurnTaskNotification_SchedulesContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive turn 1: init, then a MID-TURN task_notification (before result),
	// then assistant + result so the turn completes cleanly.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// A SECOND turn must auto-start with NO manual Enqueue/Wake.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	// And it must emit a second outbound user-prompt Send (the continuation
	// prompt). Counting Sends is class-agnostic.
	transport.drainOneSend(t, 2*time.Second, "continuation user prompt")

	// Complete the continuation turn cleanly so the loop doesn't deadlock on
	// teardown (cleanup cancels ctx).
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, scriptedResultFrame)
}

// TestTurnLoop_TwoTaskNotifications_SingleContinuation pins coalescing: TWO
// mid-turn task_notifications must result in EXACTLY ONE continuation turn,
// not two.
//
// FAILS today: no continuation fires at all.
func TestTurnLoop_TwoTaskNotifications_SingleContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive turn 1 with TWO mid-turn task_notifications before the result.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	transport.feed(t, taskNotificationFrame("b7sbsyp5w"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// Exactly ONE continuation turn must fire.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "continuation user prompt")

	// Complete the continuation turn cleanly.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, scriptedResultFrame)

	// After the single continuation turn completes, NO third turn may start
	// within a bounded window. Two notifications must coalesce to one
	// continuation, not two.
	if n := countTurnStartedWithin(sub, 500*time.Millisecond); n != 0 {
		t.Fatalf("observed %d additional EventTurnStarted after the single continuation turn; want 0 (two task_notifications must coalesce to ONE continuation)", n)
	}
}

// TestTurnLoop_NoTaskNotification_NoContinuation is the idle/normal-turn
// regression guard: a normal turn with NO task_notification must NOT trigger a
// continuation. The loop should block on the queue Signal after the turn.
//
// PASSES today (and must keep passing) — guards against over-firing.
func TestTurnLoop_NoTaskNotification_NoContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive a normal turn to clean completion with NO task_notification.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// Wait for the turn to complete so we don't race the turn-1 events.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnCompleted
	})

	// No second turn may start within a bounded window — the loop must block
	// on the queue Signal.
	if n := countTurnStartedWithin(sub, 500*time.Millisecond); n != 0 {
		t.Fatalf("observed %d additional EventTurnStarted after a normal (no task_notification) turn; want 0 (must not over-fire continuations)", n)
	}
	// And no further outbound Send may land.
	select {
	case <-transport.sendCh:
		t.Fatal("observed an unexpected outbound Send after a normal turn; want none")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTurnLoop_InterruptedTurnWithTaskNotification_NoContinuation locks the
// gating: auto-continue must fire ONLY on clean turn completion. A turn that
// received a mid-turn task_notification but was then INTERRUPTED by the user
// must NOT auto-continue.
//
// PASSES today (no continuation logic exists); this guards the implementer
// against firing on interrupted turns.
func TestTurnLoop_InterruptedTurnWithTaskNotification_NoContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Open the turn and deliver a mid-turn task_notification. Wait for it to
	// surface as EventProtocolMessage so the loop is firmly inside the drain
	// before we interrupt.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventProtocolMessage &&
			ev.Message != nil && ev.Message.Subtype == "task_notification"
	})

	// User interrupts before the result arrives.
	if err := loop.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// Give the loop a moment to observe the interrupt signal (set
	// interrupted=true) before the terminal result is delivered, so the turn
	// classifies as EventInterrupted rather than EventTurnCompleted.
	time.Sleep(50 * time.Millisecond)

	// Backend delivers the terminal result to wind the turn down.
	transport.feed(t, scriptedResultFrame)

	// The terminal event must be EventInterrupted (not EventTurnCompleted).
	ev, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventInterrupted || ev.Type == EventTurnCompleted
	})
	if ev.Type != EventInterrupted {
		t.Fatalf("terminal event = %v, want EventInterrupted (seen=%+v)", ev.Type, seen)
	}

	// No continuation turn may fire after an INTERRUPTED turn, even though a
	// task_notification arrived mid-turn.
	if n := countTurnStartedWithin(sub, 500*time.Millisecond); n != 0 {
		t.Fatalf("observed %d additional EventTurnStarted after an interrupted turn; want 0 (auto-continue must only fire on clean completion)", n)
	}
}

// TestTurnLoop_ContinuationTurnWithoutNotification_NoFurtherContinuation pins
// the no-infinite-loop guardrail: detection is PER-TURN. A continuation turn
// that itself sees NO new task_notification must NOT enqueue a further
// continuation — the loop must settle after exactly one continuation.
//
// FAILS today: no continuation logic exists, so the SECOND turn never starts
// and the wait for the 2nd EventTurnStarted times out. Post-fix: turn 2 fires
// (from turn 1's notification) but turn 3 must NOT.
func TestTurnLoop_ContinuationTurnWithoutNotification_NoFurtherContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive turn 1 with a mid-turn task_notification → it must schedule a
	// continuation.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// Continuation turn (turn 2) auto-starts: drain its prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "continuation user prompt")

	// Drive turn 2 to CLEAN completion with NO new task_notification.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// NO third turn may start — turn 2 saw no notification, so it must not
	// schedule a further continuation. This is the no-infinite-loop guard.
	if n := countTurnStartedWithin(sub, 500*time.Millisecond); n != 0 {
		t.Fatalf("observed %d additional EventTurnStarted after a continuation turn with no task_notification; want 0 (detection is per-turn; must not loop forever)", n)
	}
	// And no further outbound Send may land.
	select {
	case <-transport.sendCh:
		t.Fatal("observed an unexpected outbound Send after the continuation turn; want none")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTurnLoop_TaskNotificationWithoutTaskID_StillContinues locks in the
// QUM-807 legacy-frame fallback: a task_notification lacking a task_id is
// never deduped (older harness frames may omit the field) and must still
// schedule exactly one continuation, preserving the QUM-640 contract.
func TestTurnLoop_TaskNotificationWithoutTaskID_StillContinues(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive turn 1 with a mid-turn task_notification that omits task_id.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrameNoID())
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// A continuation turn must auto-start — the id-less frame always continues.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "continuation user prompt")

	// Complete the continuation turn cleanly so the loop doesn't deadlock on
	// teardown.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, scriptedResultFrame)
}

// TestTurnLoop_SameTaskIDReObserved_SingleContinuation is the QUM-807
// reproduction test: the SAME task_id task_notification observed across two
// consecutive turns must yield EXACTLY ONE continuation. Turn 1 sees task_id X
// and schedules a continuation; the continuation turn (turn 2) re-observes the
// SAME X (the double-fire defect) — but because X was already serviced, turn 2
// must NOT schedule a further continuation.
//
// FAILS before the fix: the per-turn `sawTaskNotification` boolean re-flips on
// the re-observed X in turn 2 and enqueues a spurious turn 3. Passes after the
// gate is made idempotent per task_id.
func TestTurnLoop_SameTaskIDReObserved_SingleContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive turn 1 with a mid-turn task_notification for task_id X → it must
	// schedule a continuation.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// Continuation turn (turn 2) auto-starts: drain its prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "continuation user prompt")

	// Drive turn 2 re-observing the SAME task_id X (the double-fire defect),
	// then clean completion.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// NO third turn may start — X was already serviced by turn 1's
	// continuation, so re-observing it must not schedule another.
	if n := countTurnStartedWithin(sub, 500*time.Millisecond); n != 0 {
		t.Fatalf("observed %d additional EventTurnStarted after re-observing the same task_id; want 0 (continuation gate must be idempotent per task_id)", n)
	}
	// And no further outbound Send may land.
	select {
	case <-transport.sendCh:
		t.Fatal("observed an unexpected outbound Send after re-observing the same task_id; want none")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestTurnLoop_NotificationDuringContinuationTurn_SchedulesFurtherContinuation
// pins boundary coalescing: a task completion arriving DURING the continuation
// turn must be folded into a FURTHER continuation, never dropped. Because
// detection is per-turn, turn 2 seeing a notification must schedule turn 3.
//
// FAILS today: no continuation logic exists, so the SECOND turn never starts
// and the wait for the 2nd EventTurnStarted times out.
func TestTurnLoop_NotificationDuringContinuationTurn_SchedulesFurtherContinuation(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-qum640"})
	t.Cleanup(func() { _ = session.Close() })

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(256)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"seq-A"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  session,
		Queue:    q,
		EventBus: bus,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})

	// Turn 1 starts: drain its user prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "turn-1 user prompt")

	// Drive turn 1 with a mid-turn task_notification → schedules continuation.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b02nztc1l"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// Continuation turn (turn 2) auto-starts: drain its prompt.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "continuation user prompt")

	// Drive turn 2 WITH a new mid-turn task_notification: the completion
	// arriving during the continuation turn must be folded into a further
	// continuation, not dropped.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, taskNotificationFrame("b7sbsyp5w"))
	transport.feed(t, scriptedAssistFrame)
	transport.feed(t, scriptedResultFrame)

	// A THIRD turn must auto-start with NO manual input — the notification
	// during the continuation turn was not dropped.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})
	transport.drainOneSend(t, 2*time.Second, "further continuation user prompt")

	// Complete turn 3 cleanly so the loop doesn't deadlock on teardown.
	transport.feed(t, scriptedInitFrame)
	transport.feed(t, scriptedResultFrame)
}
