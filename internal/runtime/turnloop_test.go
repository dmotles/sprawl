package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// mockSession is a test double for SessionHandle.
type mockSession struct {
	mu             sync.Mutex
	starts         []string
	onStart        func(call int) (<-chan *protocol.Message, error)
	interrupt      func(context.Context) error
	interruptCalls int
}

func (m *mockSession) StartTurn(_ context.Context, prompt string, _ ...backend.TurnSpec) (<-chan *protocol.Message, error) {
	m.mu.Lock()
	m.starts = append(m.starts, prompt)
	call := len(m.starts) - 1
	cb := m.onStart
	m.mu.Unlock()
	if cb != nil {
		return cb(call)
	}
	// Default: return a channel that closes immediately (no events).
	ch := make(chan *protocol.Message)
	close(ch)
	return ch, nil
}

func (m *mockSession) Interrupt(ctx context.Context) error {
	m.mu.Lock()
	m.interruptCalls++
	cb := m.interrupt
	m.mu.Unlock()
	if cb != nil {
		return cb(ctx)
	}
	return nil
}

func (m *mockSession) startCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.starts)
}

func (m *mockSession) startedPrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.starts))
	copy(out, m.starts)
	return out
}

func (m *mockSession) interruptCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.interruptCalls
}

// makeAssistant builds a well-formed assistant protocol.Message.
func makeAssistant(text string) *protocol.Message {
	raw, _ := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": text,
		},
	})
	return &protocol.Message{
		Type: "assistant",
		Raw:  raw,
	}
}

// makeResult builds a well-formed result protocol.Message.
func makeResult() *protocol.Message {
	res := protocol.ResultMessage{
		Type:    "result",
		Subtype: "success",
	}
	raw, _ := json.Marshal(res)
	return &protocol.Message{
		Type:    "result",
		Subtype: "success",
		Raw:     raw,
	}
}

// recvEvent pops one event from the bus subscriber or fails after d.
func recvEvent(t *testing.T, ch <-chan RuntimeEvent, d time.Duration) RuntimeEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("event channel closed unexpectedly")
		}
		return ev
	case <-time.After(d):
		t.Fatalf("timed out waiting for event after %s", d)
	}
	return RuntimeEvent{}
}

// waitFor finds the first event matching pred within d, returning collected events.
// Returns the matching event and the slice of all events seen up to and including it.
func waitFor(t *testing.T, ch <-chan RuntimeEvent, d time.Duration, pred func(RuntimeEvent) bool) (RuntimeEvent, []RuntimeEvent) {
	t.Helper()
	deadline := time.After(d)
	var seen []RuntimeEvent
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("event channel closed before predicate matched (seen=%d)", len(seen))
			}
			seen = append(seen, ev)
			if pred(ev) {
				return ev, seen
			}
		case <-deadline:
			t.Fatalf("predicate not satisfied within %s (seen=%d events)", d, len(seen))
		}
	}
}

// TestTurnLoop_OnQueueItemDelivered_FiredAfterSuccess verifies that the
// TurnLoop invokes its OnQueueItemDelivered callback once per drained
// QueueItem after a successful turn (StartTurn returned no error). The
// callback must receive each item with its EntryIDs intact and in queue
// order. See QUM-441.
func TestTurnLoop_OnQueueItemDelivered_FiredAfterSuccess(t *testing.T) {
	mock := &mockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			ch <- makeResult()
			close(ch)
			return ch, nil
		},
	}

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassInterrupt, Prompt: "p1", EntryIDs: []string{"a", "b"}})
	q.Enqueue(QueueItem{Class: ClassAsync, Prompt: "p2", EntryIDs: []string{"c"}})

	var (
		cbMu  sync.Mutex
		calls []QueueItem
	)
	loop := NewTurnLoop(TurnLoopConfig{
		Session:  mock,
		Queue:    q,
		EventBus: bus,
		OnQueueItemDelivered: func(item QueueItem) {
			cbMu.Lock()
			calls = append(calls, item)
			cbMu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventQueueDrained
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	cbMu.Lock()
	got := append([]QueueItem(nil), calls...)
	cbMu.Unlock()

	if len(got) != 2 {
		t.Fatalf("OnQueueItemDelivered call count = %d, want 2 (got=%+v)", len(got), got)
	}
	// Items drain together; class priority puts interrupt before async.
	if len(got[0].EntryIDs) != 2 || got[0].EntryIDs[0] != "a" || got[0].EntryIDs[1] != "b" {
		t.Errorf("call[0].EntryIDs = %v, want [a b]", got[0].EntryIDs)
	}
	if len(got[1].EntryIDs) != 1 || got[1].EntryIDs[0] != "c" {
		t.Errorf("call[1].EntryIDs = %v, want [c]", got[1].EntryIDs)
	}
}

// TestTurnLoop_OnQueueItemDelivered_NotFiredOnStartTurnError verifies that
// the callback is NOT invoked when StartTurn returns an error: a failed
// turn means the items were not actually delivered to the model, so the
// caller must not record them as delivered. See QUM-441.
func TestTurnLoop_OnQueueItemDelivered_NotFiredOnStartTurnError(t *testing.T) {
	mock := &mockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return nil, errors.New("boom")
		},
	}

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "do work", EntryIDs: []string{"x"}})

	var (
		cbMu  sync.Mutex
		calls []QueueItem
	)
	loop := NewTurnLoop(TurnLoopConfig{
		Session:  mock,
		Queue:    q,
		EventBus: bus,
		OnQueueItemDelivered: func(item QueueItem) {
			cbMu.Lock()
			calls = append(calls, item)
			cbMu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventTurnFailed
	})
	// Wait for QueueDrained too (the loop publishes it after a failed turn).
	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventQueueDrained
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	cbMu.Lock()
	defer cbMu.Unlock()
	if len(calls) != 0 {
		t.Errorf("OnQueueItemDelivered invoked %d time(s) on failed turn, want 0; calls=%+v", len(calls), calls)
	}
}

// TestTurnLoop_OnQueueItemDelivered_SkipsItemsWithNoEntryIDs verifies that
// items with empty EntryIDs are NOT reported via the callback (they have
// no persistent storage to clean up). Only items with EntryIDs trigger
// the callback. See QUM-441.
func TestTurnLoop_OnQueueItemDelivered_SkipsItemsWithNoEntryIDs(t *testing.T) {
	mock := &mockSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			ch := make(chan *protocol.Message, 1)
			ch <- makeResult()
			close(ch)
			return ch, nil
		},
	}

	bus := NewEventBus()
	sub, unsub := bus.Subscribe(64)
	defer unsub()

	q := NewMessageQueue()
	q.Enqueue(QueueItem{Class: ClassUser, Prompt: "user-input", EntryIDs: nil})
	q.Enqueue(QueueItem{Class: ClassAsync, Prompt: "from peer", EntryIDs: []string{"id1"}})

	var (
		cbMu  sync.Mutex
		calls []QueueItem
	)
	loop := NewTurnLoop(TurnLoopConfig{
		Session:  mock,
		Queue:    q,
		EventBus: bus,
		OnQueueItemDelivered: func(item QueueItem) {
			cbMu.Lock()
			calls = append(calls, item)
			cbMu.Unlock()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loop.Run(ctx) }()

	_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
		return ev.Type == EventQueueDrained
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	cbMu.Lock()
	got := append([]QueueItem(nil), calls...)
	cbMu.Unlock()

	if len(got) != 1 {
		t.Fatalf("OnQueueItemDelivered call count = %d, want 1 (only the item with EntryIDs); got=%+v", len(got), got)
	}
	if len(got[0].EntryIDs) != 1 || got[0].EntryIDs[0] != "id1" {
		t.Errorf("call[0].EntryIDs = %v, want [id1]", got[0].EntryIDs)
	}
}

func TestTurnLoop(t *testing.T) {
	t.Run("SingleTurnLifecycle", func(t *testing.T) {
		assistant := makeAssistant("hello")
		result := makeResult()

		mock := &mockSession{
			onStart: func(_ int) (<-chan *protocol.Message, error) {
				ch := make(chan *protocol.Message, 2)
				ch <- assistant
				ch <- result
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

		// Wait until we observe EventQueueDrained, then cancel and consume EventStopped.
		_, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventQueueDrained
		})
		cancel()
		_, seen2 := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventStopped
		})
		all := make([]RuntimeEvent, 0, len(seen)+len(seen2))
		all = append(all, seen...)
		all = append(all, seen2...)

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}

		// Verify event sequence: TurnStarted -> ProtocolMessage(assistant) ->
		// ProtocolMessage(result) -> TurnCompleted -> QueueDrained -> Stopped
		wantTypes := []RuntimeEventType{
			EventTurnStarted,
			EventProtocolMessage,
			EventProtocolMessage,
			EventTurnCompleted,
			EventQueueDrained,
			EventStopped,
		}
		if len(all) < len(wantTypes) {
			t.Fatalf("got %d events, want at least %d: %+v", len(all), len(wantTypes), all)
		}
		for i, wt := range wantTypes {
			if all[i].Type != wt {
				t.Errorf("event[%d].Type = %v, want %v (all=%+v)", i, all[i].Type, wt, all)
			}
		}
		if all[0].Prompt != "do work" {
			t.Errorf("EventTurnStarted.Prompt = %q, want %q", all[0].Prompt, "do work")
		}
		if all[1].Message != assistant {
			t.Errorf("EventProtocolMessage[0] did not carry assistant message")
		}
		if all[2].Message != result {
			t.Errorf("EventProtocolMessage[1] did not carry result message")
		}
		if all[3].Result == nil {
			t.Errorf("EventTurnCompleted.Result is nil")
		}

		if got := mock.startedPrompts(); len(got) != 1 || got[0] != "do work" {
			t.Errorf("mock.starts = %v, want [\"do work\"]", got)
		}
		if got := mock.interruptCallCount(); got != 0 {
			t.Errorf("mock.interruptCallCount = %d, want 0 (no interrupt on clean turn)", got)
		}
	})

	t.Run("StartTurnError", func(t *testing.T) {
		mock := &mockSession{
			onStart: func(_ int) (<-chan *protocol.Message, error) {
				return nil, errors.New("boom")
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

		// Expect TurnStarted -> TurnFailed -> QueueDrained.
		_, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventQueueDrained
		})
		if len(seen) < 3 {
			t.Fatalf("expected at least 3 events before QueueDrained, got %d: %+v", len(seen), seen)
		}
		if seen[0].Type != EventTurnStarted {
			t.Errorf("seen[0].Type = %v, want EventTurnStarted (seen=%+v)", seen[0].Type, seen)
		}
		if seen[1].Type != EventTurnFailed {
			t.Errorf("seen[1].Type = %v, want EventTurnFailed (seen=%+v)", seen[1].Type, seen)
		}
		if seen[1].Error == nil || seen[1].Error.Error() != "boom" {
			t.Errorf("EventTurnFailed.Error = %v, want \"boom\"", seen[1].Error)
		}
		// Loop should now be blocked on Signal(). Cancel and expect EventStopped + context.Canceled.
		cancel()
		_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventStopped
		})
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}

		if got := mock.interruptCallCount(); got != 0 {
			t.Errorf("mock.interruptCallCount = %d, want 0 (failed turn is not an interrupt)", got)
		}
	})

	t.Run("QueueDrainBetweenTurns", func(t *testing.T) {
		mock := &mockSession{
			onStart: func(_ int) (<-chan *protocol.Message, error) {
				ch := make(chan *protocol.Message, 1)
				ch <- makeResult()
				close(ch)
				return ch, nil
			},
		}

		bus := NewEventBus()
		sub, unsub := bus.Subscribe(64)
		defer unsub()

		q := NewMessageQueue()
		q.Enqueue(QueueItem{Class: ClassUser, Prompt: "A"})

		loop := NewTurnLoop(TurnLoopConfig{
			Session:  mock,
			Queue:    q,
			EventBus: bus,
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()

		// Wait for first turn's QueueDrained.
		_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventQueueDrained
		})

		// Loop should be blocked on Signal() now. Enqueue B.
		q.Enqueue(QueueItem{Class: ClassUser, Prompt: "B"})

		// Expect a second EventTurnStarted with B's prompt.
		ev, _ := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventTurnStarted
		})
		if ev.Prompt != "B" {
			t.Errorf("second EventTurnStarted.Prompt = %q, want %q", ev.Prompt, "B")
		}

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}

		prompts := mock.startedPrompts()
		if len(prompts) != 2 {
			t.Fatalf("StartTurn calls = %d, want 2 (got %v)", len(prompts), prompts)
		}
		if prompts[0] != "A" || prompts[1] != "B" {
			t.Errorf("StartTurn prompts = %v, want [A B]", prompts)
		}
	})

	t.Run("CompositePrompt", func(t *testing.T) {
		mock := &mockSession{
			onStart: func(_ int) (<-chan *protocol.Message, error) {
				ch := make(chan *protocol.Message, 1)
				ch <- makeResult()
				close(ch)
				return ch, nil
			},
		}

		bus := NewEventBus()
		sub, unsub := bus.Subscribe(64)
		defer unsub()

		q := NewMessageQueue()
		q.Enqueue(QueueItem{Class: ClassAsync, Prompt: "hello"})
		q.Enqueue(QueueItem{Class: ClassInterrupt, Prompt: "urgent"})
		q.Enqueue(QueueItem{Class: ClassTask, Prompt: "do thing"})

		loop := NewTurnLoop(TurnLoopConfig{
			Session:  mock,
			Queue:    q,
			EventBus: bus,
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()

		_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventQueueDrained
		})

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}

		prompts := mock.startedPrompts()
		if len(prompts) != 1 {
			t.Fatalf("expected 1 composite turn, got %d (%v)", len(prompts), prompts)
		}
		composite := prompts[0]

		// Header
		if !strings.Contains(composite, "[inbox] You have 3 pending item(s):") {
			t.Errorf("composite missing header; got:\n%s", composite)
		}
		// All three classed lines
		if !strings.Contains(composite, "[interrupt] urgent") {
			t.Errorf("composite missing [interrupt] urgent line; got:\n%s", composite)
		}
		if !strings.Contains(composite, "[task] do thing") {
			t.Errorf("composite missing [task] do thing line; got:\n%s", composite)
		}
		if !strings.Contains(composite, "[async] hello") {
			t.Errorf("composite missing [async] hello line; got:\n%s", composite)
		}
		// Trailing instruction
		if !strings.Contains(composite, "Continue your current work") {
			t.Errorf("composite missing trailing instruction; got:\n%s", composite)
		}
		// Priority order: interrupt before task before async.
		intIdx := strings.Index(composite, "[interrupt]")
		taskIdx := strings.Index(composite, "[task]")
		asyncIdx := strings.Index(composite, "[async]")
		if intIdx < 0 || taskIdx < 0 || asyncIdx < 0 {
			t.Fatalf("missing class markers; idx=%d/%d/%d in:\n%s", intIdx, taskIdx, asyncIdx, composite)
		}
		if intIdx >= taskIdx || taskIdx >= asyncIdx {
			t.Errorf("class order wrong: interrupt=%d task=%d async=%d (composite=\n%s)", intIdx, taskIdx, asyncIdx, composite)
		}
	})

	t.Run("InterruptMidTurn", func(t *testing.T) {
		// Channel the test controls.
		ch := make(chan *protocol.Message, 4)

		mock := &mockSession{
			onStart: func(_ int) (<-chan *protocol.Message, error) {
				return ch, nil
			},
		}

		// Emit one assistant message right away; the loop will block waiting for more.
		ch <- makeAssistant("working...")

		bus := NewEventBus()
		sub, unsub := bus.Subscribe(64)
		defer unsub()

		q := NewMessageQueue()
		q.Enqueue(QueueItem{Class: ClassUser, Prompt: "long job"})

		loop := NewTurnLoop(TurnLoopConfig{
			Session:  mock,
			Queue:    q,
			EventBus: bus,
		})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()

		// Wait for EventTurnStarted.
		_, _ = waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventTurnStarted
		})

		// Trigger interrupt.
		if err := loop.Interrupt(context.Background()); err != nil {
			t.Fatalf("Interrupt returned error: %v", err)
		}

		// Verify Session.Interrupt was invoked.
		// Allow a brief moment for the call to propagate.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && mock.interruptCallCount() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		if got := mock.interruptCallCount(); got != 1 {
			t.Errorf("Session.Interrupt call count = %d, want 1", got)
		}

		// Simulate backend draining after interrupt: send result, then close.
		ch <- makeResult()
		close(ch)

		// Expect EventInterrupted, NOT EventTurnCompleted.
		ev, seen := waitFor(t, sub, 2*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventInterrupted || ev.Type == EventTurnCompleted
		})
		if ev.Type != EventInterrupted {
			t.Errorf("got %v, want EventInterrupted (seen=%+v)", ev.Type, seen)
		}

		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	})

	t.Run("ContextCancelShutdown", func(t *testing.T) {
		mock := &mockSession{}
		bus := NewEventBus()
		sub, unsub := bus.Subscribe(64)
		defer unsub()

		q := NewMessageQueue()

		loop := NewTurnLoop(TurnLoopConfig{
			Session:  mock,
			Queue:    q,
			EventBus: bus,
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()

		// Allow loop to enter its block.
		time.Sleep(50 * time.Millisecond)
		cancel()

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return within 2s of cancel")
		}

		// EventStopped must have been published.
		_, _ = waitFor(t, sub, 1*time.Second, func(ev RuntimeEvent) bool {
			return ev.Type == EventStopped
		})

		if got := mock.startCount(); got != 0 {
			t.Errorf("StartTurn called %d times on empty queue, want 0", got)
		}
	})

	t.Run("InitialPromptForChildren", func(t *testing.T) {
		mock := &mockSession{
			onStart: func(_ int) (<-chan *protocol.Message, error) {
				ch := make(chan *protocol.Message, 1)
				ch <- makeResult()
				close(ch)
				return ch, nil
			},
		}

		bus := NewEventBus()
		sub, unsub := bus.Subscribe(64)
		defer unsub()

		q := NewMessageQueue()

		loop := NewTurnLoop(TurnLoopConfig{
			Session:       mock,
			Queue:         q,
			EventBus:      bus,
			InitialPrompt: "boot",
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- loop.Run(ctx) }()

		// First event should be EventTurnStarted with prompt "boot".
		ev := recvEvent(t, sub, 2*time.Second)
		if ev.Type != EventTurnStarted {
			t.Fatalf("first event Type = %v, want EventTurnStarted", ev.Type)
		}
		if ev.Prompt != "boot" {
			t.Errorf("first EventTurnStarted.Prompt = %q, want %q", ev.Prompt, "boot")
		}

		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not return after cancel")
		}

		prompts := mock.startedPrompts()
		if len(prompts) < 1 || prompts[0] != "boot" {
			t.Errorf("first StartTurn prompt = %v, want [boot ...]", prompts)
		}
		// The initial prompt must be sent verbatim (no composite header prefix).
		if strings.Contains(prompts[0], "[inbox] You have") {
			t.Errorf("initial prompt should be verbatim, got composite: %q", prompts[0])
		}
	})
}
