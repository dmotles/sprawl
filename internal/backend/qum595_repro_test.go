package backend

// QUM-595 deterministic repro: a custom mock transport whose Recv blocks
// indefinitely simulates the "stdout reader wedge" surface — claude-side
// nothing arrives, host reader is stuck. With watchdog (D1) configured to a
// short timeout, this MUST surface as ErrHangTimeout within bound. Run
// SPRAWL_REPRO_PRE_FIX=1 to skip the fix and show the wedge.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestQUM595_DeterministicRepro_WedgeAndRecovery(t *testing.T) {
	preFix := os.Getenv("SPRAWL_REPRO_PRE_FIX") == "1"

	// Override the watchdog interval for fast feedback.
	overrideHangCheckInterval(t, 30*time.Millisecond)
	overrideSubscriberSendDeadline(t, 80*time.Millisecond)

	transport := newMockManagedTransport()

	cfg := SessionConfig{SessionID: "qum595-repro"}
	if !preFix {
		cfg.HangTimeout = 200 * time.Millisecond
	} else {
		cfg.HangTimeout = -1 // disable watchdog to simulate pre-fix wedge
	}

	sess := NewSession(transport, cfg)
	t.Cleanup(func() { _ = sess.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "go")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	drainStartTurnPrompt(t, transport)

	// Mimic the production wedge: feed nothing. The host reader is stuck
	// on transport.Recv waiting for claude that will never speak again
	// (pipe-buffer full in production; here we simply don't feed).

	startedAt := time.Now()
	if preFix {
		// Pre-fix: prove the wedge — within 1s we get no error and the
		// subscriber chan stays open.
		select {
		case _, ok := <-events:
			if !ok {
				t.Fatalf("PRE-FIX path: events closed unexpectedly within 1s (no fault expected)")
			}
		case <-time.After(1 * time.Second):
			t.Logf("PRE-FIX repro confirmed: reader wedged for %s, no fault surfaced (LastTurnError=%v)", time.Since(startedAt), sess.LastTurnError())
			// Test only verifies the wedge held; with D1 disabled the
			// reader will sit there forever. PASS = wedge held silently.
			return
		}
		t.Fatalf("PRE-FIX path: expected wedge silence, got event activity")
	}

	// POST-FIX path: D1 watchdog should fire and the session should fault
	// within HangTimeout + slack.
	waitFor(t, 800*time.Millisecond, "ErrHangTimeout surfaces", func() bool {
		e := sess.LastTurnError()
		return e != nil && errors.Is(e, ErrHangTimeout)
	})
	t.Logf("POST-FIX recovery confirmed: ErrHangTimeout surfaced %s after StartTurn", time.Since(startedAt))

	// Subscriber chan should be closed so StartTurn waiters unwind.
	deadline := time.After(500 * time.Millisecond)
	closed := false
	for !closed {
		select {
		case _, ok := <-events:
			if !ok {
				closed = true
			}
		case <-deadline:
			t.Fatal("events chan never closed after ErrHangTimeout")
		}
	}
	t.Log("POST-FIX: events chan closed cleanly; StartTurn waiters unwound")
}
