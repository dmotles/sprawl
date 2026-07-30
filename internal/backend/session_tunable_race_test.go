package backend

// QUM-972: the package-level test tunables at the top of session.go
// (subscriberSendDeadline, hangCheckInterval, inflightDrainTimeout,
// interruptSendTimeout) WERE plain `time.Duration` package vars, read from
// session-owned goroutines — the reader, the observer drain and the hang
// watchdog. Every test that overrides one writes it from the test goroutine.
// Nothing synchronised the two, so an override was a data race against any
// concurrently live session. They are now `atomicDuration` (atomic get/set).
//
// Before the fix, the race surfaced ACROSS tests:
// TestSession_Close_ReturnsWithinBound_WhenReaderRecvWedged leaks a wedged
// reader past its own return, and that reader's teardown defers read
// inflightDrainTimeout while the NEXT test writes it. That makes the observed
// failure a function of test ordering — useless as a red-phase anchor, and a
// flaky regression guard. The test below pins the same defect deterministically.
//
// WHAT THIS TEST DOES AND DOES NOT CONSTRAIN, precisely:
//
// It requires the tunable SEAM itself to be concurrency-safe — an override must
// be safe to perform at any time, with no ordering obligation on the caller.
// It deliberately rules out the cheaper-looking fix of snapshotting the package
// var into the session at construction time: the constructor also reads the
// var, so a caller with no ordering obligation still races the constructor.
// (Verified, not assumed — a sibling-goroutine writer plus a constructor-time
// read is still reported by the detector.) A snapshot would also have relocated
// rather than removed the false rationale that session.go used to carry at its
// two goroutine-entry reads — "snapshot at goroutine entry so tests ... can't
// race with us" — which was wrong because the snapshot read IS the racing
// access. QUM-972 deleted both claims.
//
// It pins two of the four tunables — subscriberSendDeadline and
// hangCheckInterval, the two whose reads happen at goroutine entry. The other
// two are not detectable in this shape and this test does not claim them:
// inflightDrainTimeout is read from the reader's teardown defers AND from Close()
// and drainInflight(), all of which are ordered against this test's write (the
// defers after Close()'s context cancel, the rest on the writer's own goroutine);
// interruptSendTimeout is read only on the Interrupt caller's own goroutine.
// Those two are covered by the whole-package `-race` gate instead — the pre-fix
// cross-test failures on inflightDrainTimeout were exactly that coverage
// working. A fix that converts only the two vars this test names would leave the
// other two racy, so do not read a green here as "all four are safe".
//
// TO RE-DEMONSTRATE THAT THIS TEST CAN FAIL (QUM-953): revert either tunable
// from its synchronised accessor back to a plain `time.Duration` package var,
// switch the corresponding line in the writer goroutine below to a direct
// assignment, and run `go test -race -count=1 -run
// TestSession_TunableOverrideWhileSessionLive ./internal/backend/`. It reports
// the pair at session.go's runReader / runHangWatchdog entry.
//
// Measured 12/12 red on the pre-fix tree (plain package vars, direct
// assignments). BOTH tunables this test claims were then re-demonstrated
// individually post-fix — each reverted to a plain var on its own, its accessor
// call sites swapped back to direct reads/writes, and the mutation confirmed
// landed via `git diff` before the run:
//
//	hangCheckInterval:
//	  WARNING: DATA RACE
//	  Read at 0x0000004409b0 by goroutine 13:
//	    ...backend.(*session).runHangWatchdog()   session.go:915
//	  Previous write at 0x0000004409b0 by goroutine 9:
//	    ...IsRaceFree.func2()  session_tunable_race_test.go:119
//	  --- FAIL: ..._IsRaceFree (0.00s)
//	      testing.go:1712: race detected during execution of test
//
//	subscriberSendDeadline:
//	  WARNING: DATA RACE
//	  Read at 0x0000004409b0 by goroutine 11:
//	    ...backend.(*session).runReader()         session.go:646
//	  Previous write at 0x0000004409b0 by goroutine 9:
//	    ...IsRaceFree.func2()  session_tunable_race_test.go:122
//
// (Line numbers are from those runs, on the respective mutated trees. Each was
// reverted and the test confirmed green again afterwards.)
//
// This test is also vacuous under a plain `go test`: it exercises the code but
// asserts nothing an uninstrumented build can observe. That is the whole reason
// `-race` has to be an enforced gate rather than a convention, and why
// scripts/test-race-gate.sh exists to guard the wiring. This test cannot protect
// itself.

import (
	"context"
	"testing"
	"time"
)

// TestSession_TunableOverrideWhileSessionLive_IsRaceFree asserts the property,
// not the mechanism: overriding a tunable while a session is live must not be a
// data race. Nothing here asserts HOW — atomics, a mutexed accessor, or moving
// the knobs onto SessionConfig would all stay green.
//
// The shape of this test is load-bearing, and two earlier drafts were silently
// vacuous, so the reasoning is spelled out. The detector compares vector clocks,
// not wall-clock overlap, and reports a pair in either order — so the test does
// not need the write and the read to interleave. It needs exactly two things:
//
//  1. The read must genuinely happen. Initialize performs an init handshake that
//     round-trips through the reader goroutine, so by the time it returns the
//     reader has provably run and executed its entry read of
//     subscriberSendDeadline. No sleep, no polling.
//
//  2. The write must be ordered against neither the session's construction nor
//     its reads. Hence a sibling goroutine forked BEFORE NewSession. Two
//     variants that look equivalent are not, and both passed under -race while
//     `go test -race ./internal/backend/` was failing:
//     - writing inline from the test goroutine: if a session goroutine has not
//     reached its read yet, Close()'s context cancel establishes
//     test -> goroutine and orders the write BEFORE the read.
//     - forking the writer but releasing it after Initialize: that same
//     handshake is an edge reader -> test, so releasing the writer afterwards
//     transitively orders the write after the reader's read.
func TestSession_TunableOverrideWhileSessionLive_IsRaceFree(t *testing.T) {
	// Save/restore on the test goroutine, registered before the writer is
	// forked and run after Close() has joined every session goroutine, so the
	// restore itself is never a racing access.
	prevSend, prevHang := subscriberSendDeadline.get(), hangCheckInterval.get()
	t.Cleanup(func() {
		subscriberSendDeadline.set(prevSend)
		hangCheckInterval.set(prevHang)
	})

	written := make(chan struct{})
	go func() {
		// The same assignments the override helpers in session_drain_test.go
		// and session_wedge_test.go perform. Written directly rather than via
		// those helpers because t.Cleanup/t.Helper belong to the test
		// goroutine; the restore above covers what they would have done.
		subscriberSendDeadline.set(50 * time.Millisecond)
		hangCheckInterval.set(time.Hour)
		close(written)
	}()

	transport := newMockManagedTransport()
	// A non-zero HangTimeout guarantees runHangWatchdog is spawned, so its read
	// of hangCheckInterval really happens. time.Hour keeps it from ticking and
	// faulting the session mid-test.
	sess := NewSession(transport, SessionConfig{
		SessionID:   "sess-tunable-race",
		HangTimeout: time.Hour,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Starts the reader, observer-drain and watchdog goroutines, and does not
	// return until the reader has serviced the init handshake.
	initSessionWithBridge(ctx, t, sess, transport, newCtxRespectingToolBridge())

	<-written

	// Close joins the reader, observer and watchdog goroutines, so every read of
	// a tunable has happened by the time it returns. Called explicitly rather
	// than via t.Cleanup so those joins complete before the restore above runs.
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
