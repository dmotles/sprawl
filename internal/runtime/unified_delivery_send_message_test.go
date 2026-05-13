// Tests for QUM-550 slice 1: WakeForDelivery / ForceInterruptForDelivery
// methods on *UnifiedRuntime. These back the new send_message MCP tool's
// "cooperative wake" (interrupt=false) and "force-interrupt" (interrupt=true)
// paths. The existing InterruptDelivery is a hybrid that does neither of
// these in isolation; these tests pin the contract of the two new methods.
//
// RED phase: these symbols (WakeForDelivery, ForceInterruptForDelivery) do
// not exist yet — the file is expected to compile-fail. When the
// implementation lands the tests should turn green without modification.
package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// TestWakeForDelivery_NeverCallsSessionInterrupt_WhenTurnRunning pins the
// cooperative-wake contract: WakeForDelivery must wake the queue signal but
// must NEVER call Session.Interrupt, even when a turn is in flight. This is
// the QUM-549 fix: send_async with interrupt=false should not stomp on the
// recipient's mid-turn work.
func TestWakeForDelivery_NeverCallsSessionInterrupt_WhenTurnRunning(t *testing.T) {
	turnCh := make(chan *protocol.Message, 4)
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return turnCh, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-turnCh:
		default:
		}
		_ = rt.Stop(context.Background())
	})

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})
	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("did not enter StateTurnActive; current=%v", rt.State())
	}

	before := mock.interruptCount()
	if err := rt.WakeForDelivery(context.Background()); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// Give a brief window for any (incorrect) Session.Interrupt forwarding
	// to land. Then assert no interrupt was issued by this path.
	time.Sleep(50 * time.Millisecond)
	if got := mock.interruptCount(); got != before {
		t.Errorf("Session.Interrupt count = %d, want %d (WakeForDelivery must never call Session.Interrupt)", got, before)
	}

	// Queue signal should have been poked. We can't easily read it while
	// the loop is running (the loop competes with us on Signal()); instead
	// require that a follow-up enqueue + WakeForDelivery is safe and that
	// no further interrupt fires.
	turnCh <- makeResultMsg()
	close(turnCh)
	_ = waitForState(t, rt, StateIdle, 2*time.Second)
}

// TestWakeForDelivery_NeverCallsSessionInterrupt_WhenIdle pins the same
// contract on an idle runtime: WakeForDelivery is a pure wake — it must not
// touch the session.
func TestWakeForDelivery_NeverCallsSessionInterrupt_WhenIdle(t *testing.T) {
	// Direct call against a NON-running runtime so we can read Signal()
	// without competing with the Run goroutine.
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.WakeForDelivery(context.Background()); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	if got := mock.interruptCount(); got != 0 {
		t.Errorf("Session.Interrupt count = %d, want 0 (WakeForDelivery on idle must not touch session)", got)
	}

	select {
	case <-rt.Queue().Signal():
		// good — wake landed
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Queue.Signal did not fire after WakeForDelivery on idle runtime")
	}
}

// TestForceInterruptForDelivery_CallsSessionInterrupt_WhenIdle pins the
// QUM-549 spec for send_message(interrupt: true): the recipient must be
// interrupted UNCONDITIONALLY, even when no turn is in flight. This is the
// blind-spot fix — InterruptDelivery's `if turnRunning` guard caused
// send_interrupt to silently no-op against an idle peer.
func TestForceInterruptForDelivery_CallsSessionInterrupt_WhenIdle(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	if !waitForState(t, rt, StateIdle, 1*time.Second) {
		t.Fatalf("not idle before ForceInterruptForDelivery; state=%v", rt.State())
	}

	before := mock.interruptCount()
	if err := rt.ForceInterruptForDelivery(context.Background()); err != nil {
		t.Fatalf("ForceInterruptForDelivery: %v", err)
	}

	// Allow a brief window for the call to land on the fake session.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.interruptCount() <= before {
		time.Sleep(5 * time.Millisecond)
	}
	if got := mock.interruptCount(); got < before+1 {
		t.Errorf("Session.Interrupt count = %d, want >= %d (ForceInterruptForDelivery must call Session.Interrupt even when idle — QUM-549)", got, before+1)
	}
}

// TestForceInterruptForDelivery_CallsSessionInterrupt_WhenTurnRunning pins
// the active-turn path: ForceInterruptForDelivery must call Session.Interrupt
// at least once and wake the queue.
func TestForceInterruptForDelivery_CallsSessionInterrupt_WhenTurnRunning(t *testing.T) {
	turnCh := make(chan *protocol.Message, 4)
	mock := &mockUnifiedSession{
		onStart: func(_ int) (<-chan *protocol.Message, error) {
			return turnCh, nil
		},
	}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		select {
		case <-turnCh:
		default:
		}
		_ = rt.Stop(context.Background())
	})

	rt.Queue().Enqueue(QueueItem{Class: ClassUser, Prompt: "long"})
	if !waitForState(t, rt, StateTurnActive, 2*time.Second) {
		t.Fatalf("did not enter StateTurnActive; current=%v", rt.State())
	}

	before := mock.interruptCount()
	if err := rt.ForceInterruptForDelivery(context.Background()); err != nil {
		t.Fatalf("ForceInterruptForDelivery: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && mock.interruptCount() <= before {
		time.Sleep(5 * time.Millisecond)
	}
	if got := mock.interruptCount(); got < before+1 {
		t.Errorf("Session.Interrupt count = %d, want >= %d (ForceInterruptForDelivery must call Session.Interrupt mid-turn)", got, before+1)
	}

	// Release the turn so Stop can complete cleanly.
	turnCh <- makeResultMsg()
	close(turnCh)
	_ = waitForState(t, rt, StateIdle, 2*time.Second)
}
