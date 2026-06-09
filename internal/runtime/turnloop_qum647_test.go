package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-647: when the backend events channel closes WITHOUT a terminal `result`
// frame, the TurnLoop's drain-loop `!ok` branch previously returned silently
// without publishing any terminal event. This wedged the TUI: finalizeTurn()
// was never reached and TurnStreaming was never cleared, leaving the user
// unable to type or escape (only Ctrl+C worked).
//
// The fix: in the `!ok` branch, if no terminal event has been published yet,
// publish a synthetic terminal — EventInterrupted (Result: nil) when an
// interrupt was previously requested, otherwise EventTurnFailed wrapping a
// sentinel error.

// makeTaskNotification builds a system:task_notification protocol.Message
// (the QUM-640 wire shape) for use as a mid-turn frame in tests.
func makeTaskNotification(taskID, status string) *protocol.Message {
	raw, _ := json.Marshal(map[string]any{
		"type":    "system",
		"subtype": "task_notification",
		"task_id": taskID,
		"status":  status,
	})
	return &protocol.Message{
		Type:    "system",
		Subtype: "task_notification",
		Raw:     raw,
	}
}

// TestTurnLoop_ChannelClosedWithoutResult_NotInterrupted_PublishesTurnFailed
// pins the QUM-647 safety net for the non-interrupted close. With no prior
// Interrupt() the loop must surface EventTurnFailed with a sentinel error
// referencing the stream-closed-without-terminal-result condition, so
// downstream consumers (TUI finalizeTurn) can unblock.
func TestTurnLoop_ChannelClosedWithoutResult_NotInterrupted_PublishesTurnFailed(t *testing.T) {
	mock := &mockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			// Emit an assistant frame (proves the model engaged) then close
			// WITHOUT a terminal result. This is the QUM-647 wire shape.
			ch := make(chan *protocol.Message, 1)
			ch <- makeAssistant("partial output")
			close(ch)
			return ch, nil
		},
	}

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"e1"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  mock,
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

	ev, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnFailed ||
			ev.Type == EventTurnCompleted ||
			ev.Type == EventInterrupted
	})
	if ev.Type != EventTurnFailed {
		t.Fatalf("terminal event = %v, want EventTurnFailed (events closed without terminal result); seen=%+v", ev.Type, seen)
	}
	if ev.Error == nil {
		t.Fatalf("EventTurnFailed.Error is nil; want a sentinel error referencing the stream-closed condition")
	}
	if !strings.Contains(ev.Error.Error(), "stream closed without terminal result") {
		t.Errorf("EventTurnFailed.Error = %q, want it to contain %q", ev.Error.Error(), "stream closed without terminal result")
	}

	// The loop must continue (publish QueueDrained) so the runtime keeps
	// draining the queue rather than wedging.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventQueueDrained
	})
}

// TestTurnLoop_ChannelClosedWithoutResult_AfterInterrupt_PublishesInterrupted
// pins the QUM-647 safety net for the interrupted close — the captured
// transcript's exact shape. With a prior Interrupt() the loop must surface
// EventInterrupted (Result: nil) so the TUI's bridge adapter routes through
// the InterruptCompletedMsg finalize path.
func TestTurnLoop_ChannelClosedWithoutResult_AfterInterrupt_PublishesInterrupted(t *testing.T) {
	// Use a hand-controlled channel so we can deterministically interleave
	// Interrupt() before the close.
	events := make(chan *protocol.Message, 4)

	mock := &mockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return events, nil
		},
	}

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "kick off background task", EntryIDs: []string{"e1"}})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  mock,
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

	// Wait until the turn is in flight.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnStarted
	})

	// Feed a mid-turn task_notification (status:stopped — matches the QUM-647
	// transcript where the bash task was rejected after interrupt). Wait for
	// it to surface so the loop is inside the drain.
	events <- makeTaskNotification("b357dc0so", "stopped")
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventProtocolMessage &&
			ev.Message != nil && ev.Message.Subtype == "task_notification"
	})

	// User interrupts. The mockSession Interrupt is a no-op (the backend
	// would normally send the control_request and the SDK would emit a
	// terminal frame — but per ghost's diagnosis, it doesn't in the
	// local_bash-interrupt path).
	if err := loop.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	// Give the loop a moment to observe the interrupt signal (set
	// interrupted=true). Without this, the close below can race the
	// thisTurn arm.
	time.Sleep(50 * time.Millisecond)

	// Backend closes the channel WITHOUT a terminal result. This is the
	// QUM-647 wire signature.
	close(events)

	// The terminal event MUST be EventInterrupted (not EventTurnFailed,
	// not EventTurnCompleted). Result may be nil.
	ev, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventInterrupted ||
			ev.Type == EventTurnFailed ||
			ev.Type == EventTurnCompleted
	})
	if ev.Type != EventInterrupted {
		t.Fatalf("terminal event = %v, want EventInterrupted (events closed after Interrupt without terminal result); seen=%+v", ev.Type, seen)
	}

	// Continuation must NOT fire: an interrupted turn never auto-continues,
	// even if a task_notification arrived mid-turn.
	if got := mock.startCount(); got != 1 {
		t.Errorf("StartTurn called %d time(s), want 1 (interrupt must suppress continuation)", got)
	}

	// Loop continues normally.
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventQueueDrained
	})
}

// TestTurnLoop_ChannelClosedAfterResult_PublishesExactlyOneTerminal pins the
// regression guard: when a normal result frame DOES arrive followed by close,
// the QUM-647 safety net must NOT publish a second terminal event. The
// terminal must remain EventTurnCompleted and there must be exactly one.
func TestTurnLoop_ChannelClosedAfterResult_PublishesExactlyOneTerminal(t *testing.T) {
	mock := &mockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 2)
			ch <- makeAssistant("done")
			ch <- makeResult()
			close(ch)
			return ch, nil
		},
	}

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work"})

	loop := NewTurnLoop(TurnLoopConfig{
		Session:  mock,
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

	// Collect all events up to QueueDrained.
	_, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventQueueDrained
	})

	terminals := 0
	for _, ev := range seen {
		switch ev.Type {
		case EventTurnCompleted, EventTurnFailed, EventInterrupted:
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("got %d terminal events, want exactly 1 (seen=%+v)", terminals, seen)
	}
	// Confirm it's the expected class.
	var sawCompleted bool
	for _, ev := range seen {
		if ev.Type == EventTurnCompleted {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatalf("did not observe EventTurnCompleted; seen=%+v", seen)
	}
}

// Silence the unused-import warning if all real-backend tests use
// scriptedTransport from a sibling file; here we keep the backend import in
// case future tests need it for cross-checks.
var _ = backend.SessionConfig{}
