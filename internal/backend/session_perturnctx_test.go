package backend

import (
	"context"
	"errors"
	"testing"
	"time"
)

// QUM-618: per-turn-deadline teardown regression.
//
// The 30-min per-turn TurnTimeout (runtime layer) can fire on a healthy
// long turn. When the turn's ctx is cancelled, the backend's reader runs on
// a DETACHED readerCtx, so s.currentTurn (a non-autonomous frame created by
// StartTurn) stays PINNED forever. Every subsequent StartTurn then returns
// ErrTurnInProgress (session.go StartTurn loop), the runtime queue stops
// draining, and the session eventually faults ErrSubscriberWedged.
//
// The fix threads the per-turn ctx into the backend turnFrame so currentTurn
// clears deterministically on ctx cancel, making the next StartTurn ACCEPTED
// and ensuring the reader drops frames for an abandoned turn rather than
// blocking on its send and tripping the subscriber-wedge deadline.
//
// These tests pin the contract at the backend layer. They COMPILE today but
// FAIL against current code (the fix is not yet implemented).

// TestSession_PerTurnCtxCancel_ClearsCurrentTurn_AllowsNextStartTurn is the
// core repro. A sprawl-initiated turn is left "busy" (no result frame). A
// second StartTurn is correctly rejected with ErrTurnInProgress (precondition
// — passes today). After the first turn's ctx is cancelled, currentTurn MUST
// clear so a third StartTurn is ACCEPTED. That last assertion fails today:
// cancelling the per-turn ctx does not clear the pinned currentTurn.
func TestSession_PerTurnCtxCancel_ClearsCurrentTurn_AllowsNextStartTurn(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-perturn"})
	t.Cleanup(func() { _ = session.Close() })

	// First turn with a cancelable per-turn ctx.
	turnCtx, cancel := context.WithCancel(context.Background())

	sub1, err := session.StartTurn(turnCtx, "first")
	if err != nil {
		t.Fatalf("first StartTurn() error: %v", err)
	}
	if sub1 == nil {
		t.Fatal("first StartTurn() returned nil channel")
	}
	// Drain the user prompt frame so sendCh doesn't fill. Do NOT feed a
	// `result` frame — the turn stays busy / currentTurn stays pinned.
	drainStartTurnPrompt(t, transport)

	// Precondition (passes today): a concurrent StartTurn is rejected while
	// the first turn is in flight.
	if _, err := session.StartTurn(context.Background(), "second"); !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("second StartTurn() error = %v, want ErrTurnInProgress", err)
	}

	// Cancel the first turn's ctx. The fix must clear the pinned currentTurn
	// so subsequent StartTurns are accepted.
	cancel()

	// CORE ASSERTION: poll (bounded) for a third StartTurn to be ACCEPTED
	// (nil error). Fails today because currentTurn never clears on ctx cancel.
	//
	// The poll is bounded: today the non-autonomous StartTurn branch returns
	// ErrTurnInProgress immediately (no blocking), so each iteration is cheap
	// and the loop simply exhausts its 2s deadline → no risk of hanging.
	accepted := false
	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ch, err := session.StartTurn(context.Background(), "third")
		if err == nil {
			accepted = true
			_ = ch
			break
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	if !accepted {
		t.Fatalf("third StartTurn() never accepted after per-turn ctx cancel; last error = %v (want nil; currentTurn must clear on ctx cancel)", lastErr)
	}

	// The accepted turn must have actually emitted its user prompt frame.
	drainStartTurnPrompt(t, transport)
}

// TestSession_PerTurnCtxCancel_BusyStreamDoesNotFaultSubscriberWedged pins
// that an abandoned turn (per-turn ctx cancelled, nobody draining the
// subscriber) must NOT terminally fault the session with ErrSubscriberWedged.
//
// Today: after the per-turn ctx is cancelled the reader keeps trying to send
// frames into the undrained subscriber; once subscriberSendDeadline elapses
// it faults ErrSubscriberWedged. After the fix the reader's send-select drops
// frames on the turn frame's ctx.Done() without faulting.
func TestSession_PerTurnCtxCancel_BusyStreamDoesNotFaultSubscriberWedged(t *testing.T) {
	overrideSubscriberSendDeadline(t, 50*time.Millisecond)

	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-perturn-wedge"})
	t.Cleanup(func() { _ = session.Close() })

	turnCtx, cancel := context.WithCancel(context.Background())

	if _, err := session.StartTurn(turnCtx, "busy"); err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Cancel the per-turn ctx: the turn is abandoned. Nobody drains the
	// subscriber from here on.
	cancel()

	// Feed several frames into the (now abandoned) turn's subscriber path.
	// We overflow the 100-buffer subscriber so the reader's send blocks and,
	// on current code, trips the 50ms subscriber-send deadline.
	go func() {
		for i := 0; i < 150; i++ {
			transport.feedMessage(t, assistantFrame(i))
		}
	}()

	// Bounded poll for the terminal-fault state instead of racing a fixed
	// wall-clock sleep against the 50ms subscriberSendDeadline. A fixed sleep
	// could let a loaded box miss the wedge window and report a false GREEN
	// against unfixed code. Here we give the reader a deterministic window:
	// poll up to ~2s in 20ms steps and fail the moment the session terminally
	// faults with ErrSubscriberWedged. Today the unfixed reader keeps blocking
	// on the undrained subscriber and trips the deadline → this loop detects
	// the fault → RED. After the fix the reader drops frames on the turn
	// frame's ctx.Done() and never faults, so the loop runs out its window
	// and the post-loop contract assertions hold → GREEN. The loop is bounded
	// by the deadline and cannot hang.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if session.IsTerminallyFaulted() && errors.Is(session.LastTurnError(), ErrSubscriberWedged) {
			t.Fatalf("session terminally faulted with ErrSubscriberWedged on abandoned-turn busy stream; want no fault (per-turn ctx cancel must drop frames, not wedge); LastTurnError() = %v", session.LastTurnError())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// True-contract assertions: the session must NOT have terminally faulted
	// with ErrSubscriberWedged.
	if session.IsTerminallyFaulted() {
		t.Fatalf("session terminally faulted on abandoned-turn busy stream; want no fault (per-turn ctx cancel must drop frames, not wedge)")
	}
	if err := session.LastTurnError(); errors.Is(err, ErrSubscriberWedged) {
		t.Fatalf("LastTurnError() = %v, want not ErrSubscriberWedged (reader must drop frames for an abandoned turn)", err)
	}
}
