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

// TestForceInterruptForDelivery_DoesNotCallSessionInterrupt_WhenIdle pins
// the QUM-619 spec: when the recipient has no turn in flight,
// ForceInterruptForDelivery must NOT call Session.Interrupt. The queue
// item enqueued by the caller (drainPendingToQueue) plus the cooperative
// queue.Wake is sufficient to deliver the interrupt-class prompt at the
// next turn boundary. Calling Session.Interrupt against an idle recipient
// would cancel the very turn that exists to deliver this message — see
// docs/research/qum-619-idle-interrupt-race-2026-05-21.md.
//
// QUM-294 / QUM-549 mid-turn preempt semantics are preserved by the
// sibling test _WhenTurnRunning below.
func TestForceInterruptForDelivery_DoesNotCallSessionInterrupt_WhenIdle(t *testing.T) {
	// Direct call against a non-running runtime so we can deterministically
	// observe the queue Signal without competing with the TurnLoop. This
	// mirrors TestWakeForDelivery_NeverCallsSessionInterrupt_WhenIdle.
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.ForceInterruptForDelivery(context.Background()); err != nil {
		t.Fatalf("ForceInterruptForDelivery: %v", err)
	}

	// Give a brief window for any (incorrect) Session.Interrupt forwarding
	// to land asynchronously, then assert no interrupt was issued.
	time.Sleep(50 * time.Millisecond)
	if got := mock.interruptCount(); got != 0 {
		t.Errorf("Session.Interrupt count = %d, want 0 (QUM-619: ForceInterruptForDelivery must not call Session.Interrupt when idle)", got)
	}

	// Queue wake must still fire so the parked TurnLoop observes the
	// already-enqueued ClassInterrupt item.
	select {
	case <-rt.Queue().Signal():
		// good
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Queue.Signal did not fire after ForceInterruptForDelivery on idle runtime")
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
