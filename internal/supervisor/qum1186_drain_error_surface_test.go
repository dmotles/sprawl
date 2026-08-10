// QUM-1186 lane 2 / D5: the bounded-write timeout becomes CALLER-VISIBLE.
//
// Companion to qum1072_child_drain_bounded_write_test.go, which owns the
// *boundedness* claim. This file owns the *reporting* claim and the two
// constraints that make reporting safe to add:
//
//	Constraint A — a failed frame must not abort the remaining frames.
//	  writeInjection's invariant 2 (`continue`, never `return`) predates the error
//	  return and has no compile-time enforcement. Adding an error return is exactly
//	  the change that tempts a `return err`. The existing
//	  TestQUM1072_ChildDrain_BothWritesBounded_WorstCaseIsPerWrite already pins
//	  that both frames reach the WIRE; what it cannot pin, because the drain
//	  returned nothing, is that both failures reach the CALLER. That is the gap
//	  this file closes.
//	Constraint B — the callerless drains keep their WARN.
//	  The weave redrain ticker and post-start hook have no caller to receive an
//	  error. "The caller gets it now, so the log is redundant" is true for
//	  send_message and false for them.
//
// The negative control (unwedged → nil error) is here too: without it, an
// implementation that returns an error unconditionally passes the inverted
// wedge assertion in the QUM-1072 file.
//
// MUTATION LOG — both assertions here have been watched fail against the
// implemented code, mutated and then reverted.
//
//	M8  writeInjection: `return err` on a failed frame instead of `continue`,
//	    breaking invariant 2. The natural tidy once the function returns an
//	    error at all.
//	    → FailedFrameDoesNotAbortRemainingFrames_AndJoinsErrors FAILED twice
//	      over: 'joined error "interrupt batch [id-qum1186-int]: context
//	      deadline exceeded" does not mention "id-qum1186-async"', and
//	      'attempted stdin writes = 1, want 2'. PartialFailure FAILED the same
//	      way. Both halves matter — the caller loses an error AND the second
//	      frame never reaches the wire.
//	M9  delete the slog.Warn inside writeInjection, KEEPING the error return.
//	    → WeaveCallerlessDrain_StillWarns FAILED: "WARN records for a failed
//	      weave drain write = 0, want exactly 1". Nothing else in the suite
//	      noticed, which is the point: the child path has a caller and stayed
//	      green throughout.
package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/backend"
)

// TestQUM1186_SendMessage_UnwedgedRecipient_ReturnsNilError is the NEGATIVE
// CONTROL for AC 7. Direction: this probe must stay QUIET.
//
// Same fixture as the wedged AC-7 test minus engageWriteWedge. A confirmed
// injection must report success — otherwise "not confirmed" degrades into a
// constant that carries no information and every send cries wolf.
func TestQUM1186_SendMessage_UnwedgedRecipient_ReturnsNilError(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	r, tmpDir := newFakeReal(t)

	agentState := testAgentState("alice")
	worktree := filepath.Join(tmpDir, "wt-alice")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	agentState.Worktree = worktree
	saveTestAgent(t, tmpDir, agentState)

	mock := newFakeBackendSession("sess-alice", backend.Capabilities{})
	oldStart := unifiedAdapterStartFn
	t.Cleanup(func() { unifiedAdapterStartFn = oldStart })
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return mock, nil
	}

	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState,
		newInProcessUnifiedStarter(backend.InitSpec{}, nil))
	if err := rt.Start(); err != nil {
		t.Fatalf("rt.Start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	// NO engageWriteWedge — alice's stdin is readable.
	if _, err := r.SendMessage(context.Background(), "alice", "hello", false, false); err != nil {
		t.Fatalf("SendMessage to an UNWEDGED recipient = %v, want nil — a confirmed injection must not report 'not confirmed'", err)
	}

	// Non-vacuity: prove the poke actually reached the wire, so the nil error
	// means "the write succeeded" rather than "the write never happened".
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"next", "From weave"},
	})
}

// TestQUM1186_Drain_FailedFrameDoesNotAbortRemainingFrames_AndJoinsErrors is
// Constraint A.
//
// Both classes are queued and the pipe is wedged, so BOTH frames fail. The
// assertions are that both still reached the wire (invariant 2 survives the
// error return) and that the returned error names BOTH batches — a `return err`
// on the first failure breaks both at once.
func TestQUM1186_Drain_FailedFrameDoesNotAbortRemainingFrames_AndJoinsErrors(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", "id-qum1186-async", "async body")
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-qum1186-int", ShortID: "id-qum1186-int",
		Class: agentloop.ClassInterrupt, From: "weave", Subject: "stop", Body: "urgent",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}
	mock.engageWriteWedge(t)

	// Run on a goroutine and race a watchdog: an unbounded regression would
	// otherwise hang the package until go test's 10m panic instead of failing.
	type res struct{ err error }
	done := make(chan res, 1)
	go func() { done <- res{uh.WakeForDelivery()} }()

	var got res
	select {
	case got = <-done:
	case <-time.After(qum1072TestTimeout):
		t.Fatalf("WakeForDelivery never returned within %v against a wedged pipe", qum1072TestTimeout)
	}

	if got.err == nil {
		t.Fatalf("WakeForDelivery with BOTH frames failing returned nil error, want the failures joined and surfaced")
	}

	// THE CONSTRAINT-A ASSERTION on the caller's side. `return err` after the
	// first failed frame yields an error naming only the interrupt batch.
	msg := got.err.Error()
	for _, want := range []string{"id-qum1186-int", "id-qum1186-async"} {
		if !strings.Contains(msg, want) {
			t.Errorf("joined error %q does not mention %q — a failed frame must not abort the remaining frames NOR suppress their errors (writeInjection invariant 2)", msg, want)
		}
	}

	// And on the wire side: both writes were still attempted. Asserted here as
	// well as in the QUM-1072 file because the two claims fail independently —
	// an early return breaks the wire claim, an unjoined error breaks the
	// caller claim, and a fix for one does not imply the other.
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"now", "id-qum1186-int"},
		{"next", "id-qum1186-async"},
	})
}

// TestQUM1186_WeaveCallerlessDrain_StillWarns is Constraint B, driven on the
// path that actually has no caller.
//
// It uses WeaveRuntimeHandle — the type whose drainPendingToStdin is invoked by
// the redrain ticker (weave_handle.go, the ticker body) and by
// rt.SetPostStartHook, NEITHER of which has anywhere to return an error. Driving
// the child unifiedHandle here instead would be cheaper and would pass, but the
// child HAS a caller, so it could not distinguish "the WARN survived" from "the
// error return replaced it".
//
// The assertion is that BOTH survive for the same failure: the error goes to
// send_message's caller, the WARN goes to the operator on behalf of the paths
// that have no caller. Deleting the WARN as a now-redundant duplicate is the
// plausible refactor this exists to reject.
func TestQUM1186_WeaveCallerlessDrain_StillWarns(t *testing.T) {
	logs := installCaptureSlog(t)

	h, fs := buildStartedWeaveRuntimeHandleForTest(t)

	seedAsyncEntry(t, h.sprawlRoot, "weave", "id-qum1186-warn", "async body")

	// Fail the write outright rather than wedging. weaveDrainWriteTimeout is a
	// deliberate 5s const with no test seam (drain_policy_test.go pins that it
	// must NOT follow the child's overridable duration), so a wedge here would
	// cost 5s of wall clock. Constraint B is about error-vs-WARN, not about
	// boundedness — which the QUM-1072 file already owns — so an immediate
	// failure exercises the same branch for free.
	fs.failWrites(1)

	// The ticker and post-start hook call drainPendingToStdin directly and
	// discard whatever it returns; this mirrors them exactly.
	_ = h.drainPendingToStdin()

	// Prefixed form: pol.logPrefix distinguishes the root path ("weave-runtime")
	// from the child path ("unified-runtime"). Matching the unprefixed substring
	// would go green on a record emitted by the wrong policy.
	recs := logs.recordsWithMessage("weave-runtime: drainPendingToStdin write failed")
	if len(recs) != 1 {
		t.Fatalf("WARN records for a failed weave drain write = %d, want exactly 1 — the redrain ticker and post-start hook have NO caller to receive an error return, so this WARN is their only channel.\nall logs:\n%s", len(recs), logs.String())
	}
	if !strings.Contains(recs[0], "id-qum1186-warn") {
		t.Errorf("WARN record %q does not carry the entry IDs", recs[0])
	}
}

// TestQUM1186_Drain_PartialFailure_NamesOnlyTheFailingBatch is the assertion
// that gives "joins errors" content.
//
// Every other error assertion here fails BOTH frames, which an implementation
// returning one fixed error for any failure satisfies. Here the interrupt frame
// fails and the async frame succeeds: the error must name only the failed batch,
// and the succeeded batch must still be acked out of pending/.
func TestQUM1186_Drain_PartialFailure_NamesOnlyTheFailingBatch(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", "id-qum1186-partial-async", "async body")
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-qum1186-partial-int", ShortID: "id-qum1186-partial-int",
		Class: agentloop.ClassInterrupt, From: "weave", Subject: "stop", Body: "urgent",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}

	// Fail ONLY the first write. Frames are written interrupt-first (priority
	// "now"), so this fails the interrupt batch and lets the async batch through.
	mock.failWrites(1)

	err := uh.WakeForDelivery()
	if err == nil {
		t.Fatalf("WakeForDelivery with a failing interrupt frame returned nil, want an error naming that batch")
	}
	if !strings.Contains(err.Error(), "id-qum1186-partial-int") {
		t.Errorf("error %q does not name the FAILED batch", err.Error())
	}
	if strings.Contains(err.Error(), "id-qum1186-partial-async") {
		t.Errorf("error %q names the SUCCEEDED batch — reporting a delivered batch as unconfirmed is the same false claim in the other direction", err.Error())
	}

	// Both frames reached the wire (invariant 2), and the succeeding one really
	// did succeed rather than being skipped.
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"now", "id-qum1186-partial-int"},
		{"next", "id-qum1186-partial-async"},
	})
}
