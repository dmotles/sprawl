// QUM-1072 regression guard: every stdin write made on a child's behalf is BOUNDED.
//
// WHAT THIS FILE GUARDS. No write to a child's stdin may block indefinitely when
// that child's pipe is full. THREE writes are covered, all in runtime_launcher.go:
// drainPendingToStdin's interrupt-class (`now`) and async-class (`next`) writes,
// and feedTasks' task notification (`later`). A wedged recipient makes a
// notification LATE rather than hanging the writer.
//
// QUM-1186: a feedTasks arm used to be described here — the one write the
// drain's bound could not protect, because Wake called feedTasks BEFORE
// drainPendingToStdin. feedTasks is deleted; Wake is the drain alone, and it
// keeps its own bounded-write arm below.
//
// WHY THE HARM IS CROSS-AGENT, which is what makes this severe rather than
// untidy. The goroutine that blocks is not the wedged child's — it belongs to the
// SENDER. Verified in the current tree, not inherited from the issue:
//
//	Real.SendMessage (real.go) calls
//	  `_ = runtime.WakeForDelivery()` INLINE — no goroutine — on the MCP handler
//	  goroutine currently serving some other agent's tool call;
//	→ AgentRuntime.WakeForDelivery (synchronous)
//	→ unifiedHandle.WakeForDelivery → drainPendingToStdin (synchronous)
//	→ rt.WriteSystemMessage(ctx, …) → session.WriteUserMessage(ctx, …)
//	→ backend/claude/adapter.go transport.Send, which runs the blocking WriteJSON
//	  in a goroutine and selects on ctx.Done().
//
// That last hop is the whole issue: ctx is the ONLY escape from a full pipe. With
// context.Background() there is no Done channel, so the select blocks forever and
// an unrelated agent's send_message / report_status never returns. One wedged
// child could hang the fleet.
//
// WHY A "THE WRITE RETURNED AN ERROR" TEST WOULD PROVE NOTHING. The defect is not
// that the write fails — it is that the write NEVER RETURNS. A fake that returns
// an error immediately passes just as happily against the unbounded code. So every
// assertion here is about BOUNDED TIME, measured against a fake that really does
// block (see fakeBackendSession.WriteUserMessage's wedged branch, which selects on
// ctx.Done() precisely because the real transport does).
//
// The drain is run on a goroutine and raced against a watchdog, so the negative
// control is a clean FAIL rather than a hung package waiting for go test's 10m
// panic.
//
// WHAT A TIMED-OUT BATCH LOSES — asymmetric, and the asymmetry matters for
// QUM-1034:
//   - LOST: status_change lines. inboxprompt.DrainStatusChangeLines is
//     DESTRUCTIVE — the envelopes are already off disk when the write is
//     attempted, and nothing re-queues them. That is QUM-1034's bug with a
//     deadline attached, which is why the WARN below carries the bodies: this
//     issue makes the loss LOUD; recovery stays QUM-1034's.
//   - NOT LOST: maildir entries. agentloop.ListPending is a non-destructive peek,
//     and runtime.writeMessage deletes the outstanding entry on a write error — so
//     QUM-1066's in-flight filter does not suppress the retry and the next poke
//     re-drains them.
//
// THAT LIST USED TO HAVE TWO ENTRIES ON THE LOST SIDE, and the second one going
// away is worth stating rather than silently editing. QUM-1071 deleted the
// supervisor heartbeat and with it ConsumeLivenessNudge, a consume-and-clear
// atomic whose nudge was also destroyed by a failed write. So the destructive
// surface a timeout can lose went from two independent sources to one.
//
// The conclusion below is unchanged — a timed-out write still loses data
// permanently — but it now rests on ONE source instead of two, and the deletion
// IMPROVED the property these tests measure. That combination is the dangerous
// one: a claim whose support quietly halves while its conclusion stays true looks
// completely healthy, every test stays green, and nothing prompts a re-read. It is
// the reason this paragraph exists instead of a one-word noun swap.
//
// MUTATION LOG — every assertion here has been watched fail, with what it printed.
//
// NOTE ON WHAT THE RED-PHASE RUN DID AND DID NOT PROVE. Pre-fix, all five tests
// died inside drainElapsed's watchdog — the drain never returned. So the red run
// exercised ONLY the hang. The WARN-content assertions, the durability/re-drain
// assertions, and the 2×deadline independence check never executed at all, and
// needed their own mutations (M3–M5) once the drain returned.
//
//	M1  revert BOTH writes to context.Background() (the pre-fix state).
//	    → all four wedged arms FAILED: "drainPendingToStdin never returned within
//	      5s against a wedged stdin write", and SenderMCPCallReturns FAILED with
//	      "Real.SendMessage never returned within 5s while the RECIPIENT was
//	      wedged" — the cross-agent harm, observed rather than argued.
//	M2  bound ONLY the async write, leaving the interrupt write unbounded — the
//	    natural half-fix, since the async branch is the one that looks like the
//	    "main" write.
//	    → InterruptWrite_Bounded… and BothWritesBounded… FAILED ("never returned"),
//	      while the async arm stayed GREEN. That asymmetry is the reason the two
//	      writes are separate test arms rather than one.
//	M3  drop the async WARN (discard the error again, `_ = err`).
//	    → TimedOutWrite_WarnsWithBodiesAndDeadline FAILED: "WARN records for a
//	      timed-out write = 0, want exactly 1".
//	M4  keep the WARN but drop its lost_status_bodies attr.
//	    → same test FAILED: `WARN record missing "QUM1072-LOST-LINE"`. This is the
//	      one that matters most: without the bodies the lines are gone from disk
//	      AND from the log, which is strictly worse than the pre-fix state where at
//	      least the hang was obvious.
//	M5  use ONE shared context for both writes instead of one per write.
//	    → BothWritesBounded… FAILED: "drain with both classes returned in
//	      150.568041ms, want >= 300ms (2 × 150ms) — the two writes share one
//	      deadline, so the second gets a near-expired context and fails early".
//	      That is the failure mode the per-write choice exists to avoid, and the
//	      async batch — the one carrying destructively-drained status lines — is
//	      the one that would have been sacrificed.
//	M6  revert ONLY feedTasks' write to context.Background(), leaving both drain
//	    writes bounded — i.e. exactly the state this commit was in before code
//	    review caught the sibling.
//	    → FeedTasks_BoundedAgainstWedgedStdin FAILED: "unifiedHandle.Wake never
//	      returned within 5s against a wedged stdin write", while every
//	      drainPendingToStdin arm stayed GREEN. That green-while-hung combination
//	      is the whole point of the arm: bounding the drain alone looks complete
//	      from the drain's own tests.
//
// QUM-1186 (lane 2) additions. PROVENANCE, stated because the distinction
// matters: R1 is a RED-FIRST observation (the assertion was inverted against the
// unmodified tree, which is the pre-slice state by definition — there was
// nothing to mutate). M8/M9 are true mutations, applied to the implemented code
// and then reverted.
//
//	R1  red-first, before any implementation: the inverted assertion run against
//	    the tree where Real.SendMessage still discards the poke's result.
//	    → SenderMCPCallReturns_WhileRecipientWedged FAILED: "SendMessage returned
//	      err = <nil> while the recipient was wedged and the injection never
//	      landed — want a not-confirmed error (QUM-1186 AC 7). A nil return here
//	      claims delivery that did not happen."
//	M8  make writeInjection `return err` instead of `continue` on a failed frame
//	    — the natural-looking tidy once the function returns an error at all.
//	M9  delete the slog.Warn in writeInjection, keeping the error return — the
//	    "the caller gets it now, the log is redundant" refactor.
//	    → both recorded, with what they printed, in the header of
//	      qum1186_drain_error_surface_test.go, where their assertions live.
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

// qum1072TestTimeout is how long a bounded drain is given before the test calls it
// hung. Generous relative to the deadline the test sets, so a slow CI host cannot
// produce a false failure — the signal is "returned vs never returned", not a
// tight margin.
const qum1072TestTimeout = 5 * time.Second

// withShortChildDrainTimeout overrides the child drain's write deadline for the
// duration of a test and restores it afterwards. Package-global, so these tests
// must not call t.Parallel.
func withShortChildDrainTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := childDrainWriteTimeout.get()
	childDrainWriteTimeout.set(d)
	t.Cleanup(func() { childDrainWriteTimeout.set(prev) })
}

// drainElapsed runs WakeForDelivery on its own goroutine and returns how long it
// took. It FAILS the test (rather than hanging it) if the drain never returns,
// which is exactly what the unbounded code does.
func drainElapsed(t *testing.T, uh *unifiedHandle) time.Duration {
	t.Helper()
	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_ = uh.WakeForDelivery()
		done <- time.Since(start)
	}()
	select {
	case elapsed := <-done:
		return elapsed
	case <-time.After(qum1072TestTimeout):
		t.Fatalf("QUM-1072: drainPendingToStdin never returned within %v against a wedged stdin write.\n"+
			"  The write context is unbounded (context.Background()), so transport.Send's\n"+
			"  select has no Done channel to fall back on and blocks forever. In production\n"+
			"  this goroutine belongs to the SENDER's MCP handler, so an unrelated agent's\n"+
			"  send_message / report_status never returns.", qum1072TestTimeout)
		return 0 // unreachable
	}
}

// qum1072SlackFactor sets how far past the configured deadline a bounded write may
// return, as a MULTIPLE of that deadline rather than a fixed number of seconds.
// The upper bound's only job is to prove the fix honours THE CONFIGURED knob, and
// a fixed multi-second slack defeats that: against a 150ms deadline it would let an
// implementation that ignores childDrainWriteTimeout and hardcodes its own 1s or 2s
// timeout pass cleanly. Scaling with the deadline keeps the check meaningful.
// "Never returned" is covered separately by drainElapsed's watchdog.
const qum1072SlackFactor = 4

// assertBounded checks the drain returned because the DEADLINE fired: after it,
// but not far after. The lower bound is what stops a drain that skipped the write
// entirely from passing as "bounded".
func assertBounded(t *testing.T, elapsed, deadline time.Duration, what string) {
	t.Helper()
	if upper := qum1072SlackFactor * deadline; elapsed > upper {
		t.Fatalf("%s returned after %v, want <= %v (%d× its %v deadline) — the write is not bounded by the CONFIGURED deadline; an implementation that hardcodes its own timeout instead of reading childDrainWriteTimeout looks like this", what, elapsed, upper, qum1072SlackFactor, deadline)
	}
	if elapsed < deadline {
		t.Fatalf("%s returned in %v, BEFORE its %v deadline — the write was never attempted, so this test is not measuring the bound at all", what, elapsed, deadline)
	}
}

// assertAttempted asserts the drain reached the wire with exactly the expected
// writes, identified by priority and body substring. This is the non-vacuity
// control for every bounded-time assertion here: a drain that never wrote at all
// also "returns promptly", and only this can tell the two apart.
func assertAttempted(t *testing.T, mock *fakeBackendSession, want []struct{ priority, bodyContains string }) {
	t.Helper()
	got := mock.attemptedSnapshot()
	if len(got) != len(want) {
		var lines []string
		for _, w := range got {
			lines = append(lines, w.Priority+": "+w.Message.Content)
		}
		t.Fatalf("attempted stdin writes = %d, want %d — the drain did not reach the wire as expected; got:\n%s", len(got), len(want), strings.Join(lines, "\n"))
	}
	for i, w := range want {
		if got[i].Priority != w.priority {
			t.Fatalf("attempted write %d priority = %q, want %q", i, got[i].Priority, w.priority)
		}
		if !strings.Contains(got[i].Message.Content, w.bodyContains) {
			t.Fatalf("attempted write %d does not carry %q:\n%s", i, w.bodyContains, got[i].Message.Content)
		}
	}
}

// TestQUM1072_ChildDrain_AsyncWrite_BoundedAgainstWedgedStdin is the async-class
// (`next`) arm — the ordinary inbox-notification path.
func TestQUM1072_ChildDrain_AsyncWrite_BoundedAgainstWedgedStdin(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", "id-qum1072-async", "hello")
	mock.engageWriteWedge(t) // AFTER Start: the launcher writes during startup

	assertBounded(t, drainElapsed(t, uh), deadline, "async drain")
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"next", "id-qum1072-async"},
	})

	// DURABILITY, asserted as a RETRY rather than as on-disk state. An async entry
	// stays in pending/ until its isReplay echo drives MarkDelivered, and this fake
	// never echoes — so `ListPending == 1` holds whether the write timed out or
	// succeeded, and would prove nothing. What the fix actually depends on is that
	// QUM-1066's in-flight filter does NOT suppress the retry: writeMessage deletes
	// the outstanding entry on a write error, so no in-flight marker survives.
	// Drain again and require the same entry to reach the wire a second time.
	assertBounded(t, drainElapsed(t, uh), deadline, "async re-drain")
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"next", "id-qum1072-async"},
		{"next", "id-qum1072-async"},
	})
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 1 {
		t.Errorf("ListPending after two timed-out writes = %d, want 1 — the entry must still be queued", len(pending))
	}
}

// TestQUM1072_ChildDrain_InterruptWrite_BoundedAgainstWedgedStdin is the
// interrupt-class (`now`) arm. It is a SEPARATE write in the same function, so
// bounding only one of the two leaves the other able to hang the fleet — this arm
// is what makes that visible (mutation M2).
func TestQUM1072_ChildDrain_InterruptWrite_BoundedAgainstWedgedStdin(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	logs := installCaptureSlog(t)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-qum1072-interrupt", ShortID: "id-qum1072-interrupt",
		Class: agentloop.ClassInterrupt, From: "weave", Subject: "stop", Body: "urgent",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}
	mock.engageWriteWedge(t)

	assertBounded(t, drainElapsed(t, uh), deadline, "interrupt drain")
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"now", "id-qum1072-interrupt"},
	})

	// A timed-out now-write must NOT be confirmed delivered:
	// ConfirmDeliveredWithoutReplay is gated on err == nil, so the entry stays
	// queued rather than being marked delivered on a write that never landed.
	// Unlike the async arm's ListPending check this one DOES discriminate: on a
	// successful now-write the ack fires immediately and the entry leaves pending/.
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 1 {
		t.Errorf("ListPending after a timed-out now-write = %d, want 1 — a failed write must not ack delivery", len(pending))
	}

	// The interrupt batch gets its OWN WARN, distinct from the async one: no
	// status lines ride it, so it must not claim lines were lost.
	recs := logs.recordsWithMessage("unified-runtime: drainPendingToStdin write failed")
	if len(recs) != 1 {
		t.Fatalf("WARN records for a timed-out interrupt write = %d, want exactly 1.\nall logs:\n%s", len(recs), logs.String())
	}
	if !strings.Contains(recs[0], "id-qum1072-interrupt") || !strings.Contains(recs[0], "agent=alice") {
		t.Fatalf("interrupt WARN missing the recipient or the batch entry IDs:\n%s", recs[0])
	}
	if strings.Contains(recs[0], "lost_status_bodies") {
		t.Fatalf("interrupt WARN claims lost status lines, but status lines ride the ASYNC batch only — this would send a reader hunting for data that was never in this write:\n%s", recs[0])
	}
}

// TestQUM1072_ChildDrain_BothWritesBounded_WorstCaseIsPerWrite pins the
// deliberate consequence of bounding each write independently: a batch carrying
// BOTH classes against a fully wedged pipe costs up to 2×deadline, not 1×.
//
// This is a decision, not an oversight. One shared context for both writes would
// let the interrupt write consume the entire budget and hand the async batch a
// near-expired context — converting a slow path into a guaranteed-failing one, and
// the async batch is the one carrying destructively-drained status lines.
func TestQUM1072_ChildDrain_BothWritesBounded_WorstCaseIsPerWrite(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", "id-qum1072-both-async", "async body")
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-qum1072-both-int", ShortID: "id-qum1072-both-int",
		Class: agentloop.ClassInterrupt, From: "weave", Subject: "stop", Body: "urgent",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}
	mock.engageWriteWedge(t)

	elapsed := drainElapsed(t, uh)

	// PRIMARY claim, asserted directly rather than inferred from the clock: BOTH
	// writes reached the wire, in class order, each carrying its own batch. Timing
	// alone cannot establish this — one write bounded at 2×deadline, or one write
	// plus an unrelated stall, would satisfy a duration check.
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"now", "id-qum1072-both-int"},
		{"next", "id-qum1072-both-async"},
	})

	// SECONDARY claim: the two deadlines are INDEPENDENT, so a fully wedged pipe
	// costs 2×. That is the accepted cost of per-write bounds (see the fix site).
	if elapsed < 2*deadline {
		t.Fatalf("drain with both classes returned in %v, want >= %v (2 × %v) — the two writes share one deadline, so the second gets a near-expired context and fails early instead of getting its own budget", elapsed, 2*deadline, deadline)
	}
	if upper := qum1072SlackFactor * 2 * deadline; elapsed > upper {
		t.Fatalf("drain with both classes returned after %v, want <= %v — a write is not bounded by the configured deadline", elapsed, upper)
	}
}

// TestQUM1072_ChildDrain_TimedOutWrite_WarnsWithEntryIDsAndDeadline covers AC 4.
//
// QUM-1186: this was ..._WarnsWithBodiesAndDeadline. The drain is no longer
// destructive — the status_change envelope that used to be off disk before the
// write was attempted is gone — so the WARN no longer carries verbatim line
// bodies and that assertion went with it.
//
// What SURVIVES, and is still asserted: a timed-out write emits EXACTLY ONE
// WARN, naming the recipient, the deadline that was exceeded, and the batch's
// entry IDs. Those entries stay in pending/ for the next poke, so the WARN is
// a lateness record rather than a loss record — but a timeout that logged
// nothing at all would still be invisible, which is what this pins.
func TestQUM1072_ChildDrain_TimedOutWrite_WarnsWithEntryIDsAndDeadline(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	logs := installCaptureSlog(t)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", "id-qum1072-warn", "queued body")
	mock.engageWriteWedge(t)

	assertBounded(t, drainElapsed(t, uh), deadline, "async drain")

	// Scoped to the WARN record itself. Asserting against the whole log dump would
	// be near-vacuous: the capture handler is installed before the handle is built
	// and enabled at every level, so unrelated startup/debug records already
	// mention the agent name and the entry ID.
	recs := logs.recordsWithMessage("unified-runtime: drainPendingToStdin write failed")
	if len(recs) != 1 {
		t.Fatalf("WARN records for a timed-out write = %d, want exactly 1. A write that times out with no log at all is invisible to the operator.\nall logs:\n%s", len(recs), logs.String())
	}
	warn := recs[0]

	for _, want := range []string{
		"agent=alice",     // recipient (key=value, not a bare substring)
		"id-qum1072-warn", // the batch's entry IDs
		deadline.String(), // the deadline that was exceeded
	} {
		if !strings.Contains(warn, want) {
			t.Fatalf("WARN record missing %q — AC 4 requires the recipient, the deadline and the batch entry IDs.\nrecord: %s", want, warn)
		}
	}
}

// TestQUM1072_SenderMCPCallReturns_WhileRecipientWedged is AC 3, and it is the
// only arm that exercises the harm the issue is actually about. Every other test
// here calls WakeForDelivery directly; this one goes in through Real.SendMessage,
// which is what an agent's send_message MCP tool call runs.
//
// The distinction matters because the blocked goroutine is the SENDER's. Bounding
// the write is only interesting if it unblocks that goroutine, and this is the
// assertion that says so. It also stays meaningful if someone later moves the poke
// into its own goroutine (the issue notes that would downgrade the severity to a
// leaked goroutine) — this test would then pass trivially, which is the correct
// signal rather than a false one.
//
// QUM-1186 AC 7: this test now carries TWO claims, not one. The sender must
// return BOUNDED (QUM-1072, unchanged) *and* must be TOLD the injection was not
// confirmed (new). The err-is-nil assertion was inverted for the second claim;
// see the block below and MUTATION LOG entry M7. Returning promptly with a nil
// error is no longer a pass — it is the precise shape of the defect, because it
// reports a delivery that did not happen.
func TestQUM1072_SenderMCPCallReturns_WhileRecipientWedged(t *testing.T) {
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

	// Real launcher (so drainPendingToStdin actually runs) over a fake session.
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

	// QUM-1186: an UNRELATED entry, already queued by somebody else, that this
	// drain will also try to flush. It exists so the leak assertion below has a
	// subject — without it, "the error does not contain id-qum1072-unrelated" is
	// trivially true and pins nothing.
	seedAsyncEntry(t, tmpDir, "alice", "id-qum1072-unrelated", "someone else's mail")

	mock.engageWriteWedge(t) // alice's stdin pipe is now full and unread

	// weave sends alice a message. Real.SendMessage pokes alice's runtime
	// SYNCHRONOUSLY on this goroutine — in production, the MCP handler goroutine.
	type sendResult struct {
		elapsed time.Duration
		err     error
	}
	done := make(chan sendResult, 1)
	start := time.Now()
	go func() {
		_, err := r.SendMessage(context.Background(), "alice", "hello", false, false)
		done <- sendResult{time.Since(start), err}
	}()

	select {
	case got := <-done:
		// QUM-1186 AC 7 — INVERTED, deliberately. Until this slice the assertion
		// here was `got.err != nil → Fatalf`. That was correct for QUM-1072, whose
		// claim was solely "the sender's goroutine is unblocked", and a nil error
		// was the cheapest proof the call had not bailed early.
		//
		// It is now wrong. Returning nil while the injection never landed tells
		// the caller the message was delivered, which is the silent false claim
		// QUM-1185 exists to eliminate. AC 7 makes this path TWO claims —
		// *bounded* AND *reported* — and the timing assertion below still carries
		// the first one, so inverting this does not relax QUM-1072's guarantee.
		if got.err == nil {
			t.Fatalf("SendMessage returned err = <nil> while the recipient was wedged and the injection never landed — want a not-confirmed error (QUM-1186 AC 7). A nil return here claims delivery that did not happen.")
		}
		// The error must be HONEST in both directions: the entry is already
		// durable in pending/ before the write is attempted (real.go, the Enqueue
		// above the poke) and is retried on the next poke, so this is neither a
		// delivery nor a loss.
		msg := got.err.Error()
		lower := strings.ToLower(msg)
		for _, want := range []string{"not confirmed", "queued", "next action:"} {
			if !strings.Contains(lower, want) {
				t.Errorf("error %q does not contain %q — AC 7 requires it to say delivery is NOT CONFIRMED and the message REMAINS QUEUED, with a next action", msg, want)
			}
		}
		// The honesty guard. "delivery failed" would be a loud lie replacing a
		// silent one — the message is still queued and will be retried.
		if strings.Contains(strings.ToLower(msg), "delivery failed") {
			t.Errorf("error %q claims delivery FAILED — the entry is durably queued and will be retried; that is a different false claim, not a fix", msg)
		}
		// Constraint C: the drain flushes whatever is pending, not only this
		// caller's entry, so the error must not be attributed to this message.
		for _, banned := range []string{"your message was lost", "message was not sent"} {
			if strings.Contains(strings.ToLower(msg), banned) {
				t.Errorf("error %q attributes the outcome to THIS message; the drain flushes the whole pending queue and cannot honestly single one out", msg)
			}
		}
		// Constraint C, second half — the LAYERING rule. The drain's own error
		// names every failed batch's entry IDs, which is right for the operator-
		// facing WARN and wrong here: this error is returned to whichever agent
		// called send_message, and those IDs belong to OTHER agents' queued mail.
		// So Real.SendMessage must summarise, not wrap-and-forward.
		if strings.Contains(msg, "id-qum1072-unrelated") {
			t.Errorf("error %q leaks an unrelated queued entry's ID to the sender — the drain flushes the whole queue, so its per-entry detail belongs in the WARN, not in this caller's tool result", msg)
		}
		if upper := qum1072SlackFactor * deadline; got.elapsed > upper {
			t.Fatalf("SendMessage returned after %v, want <= %v", got.elapsed, upper)
		}
	case <-time.After(qum1072TestTimeout):
		t.Fatalf("QUM-1072: Real.SendMessage never returned within %v while the RECIPIENT was wedged.\n"+
			"  The sender's MCP tool call is hung on an unrelated agent's full stdin pipe —\n"+
			"  one wedged child hangs every agent that reports to or messages it.", qum1072TestTimeout)
	}

	// THE NON-VACUITY CONTROL, and the reason this test is not just "SendMessage is
	// fast". The poke is gated on startedRuntime (Liveness == Running); if that
	// gate ever stops matching in this fixture, SendMessage returns instantly and a
	// timing-only assertion goes green while testing nothing. Requiring the write
	// to have reached the wire proves the poke actually ran.
	//
	// It also survives the future the issue anticipates: if the poke is ever moved
	// onto its own goroutine, this assertion still holds (poll for it) while a bare
	// elapsed-time check would silently become trivial.
	// Matched on the citation, not on "hello": the drain is a dumb forwarder
	// (QUM-925) and writes a <system-notification> citing the sender and a
	// messages_read id, never the message body.
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"next", "From weave"},
	})
}

// TestQUM1072_Wake_BoundedAgainstWedgedStdin covers unifiedHandle.Wake() as a
// SEPARATE entry point from WakeForDelivery.
//
// QUM-1186: this was TestQUM1072_FeedTasks_BoundedAgainstWedgedStdin. The
// bounded-write invariant survives; only the feedTasks vehicle died. Wake()
// used to call feedTasks() BEFORE drainPendingToStdin(), and the delegate poke
// arrived through Wake, so an unbounded feedTasks write hung the delegator and
// the drain's deadline was never reached. feedTasks is gone and Wake is now
// the drain alone — but Wake remains independently reachable, so it keeps its
// own arm rather than being folded into the WakeForDelivery tests. If a future
// change re-adds a write ahead of the drain in Wake, this is what catches it.
func TestQUM1072_Wake_BoundedAgainstWedgedStdin(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: "id-qum1186-wake", ShortID: "id-qum1186-wake",
		Class: agentloop.ClassAsync, From: "weave", Subject: "hi", Body: "do the wedged thing",
	}); err != nil {
		t.Fatalf("Enqueue async: %v", err)
	}
	mock.engageWriteWedge(t)

	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		_ = uh.Wake()
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		assertBounded(t, elapsed, deadline, "Wake")
	case <-time.After(qum1072TestTimeout):
		t.Fatalf("QUM-1072: unifiedHandle.Wake never returned within %v against a wedged stdin write.\n"+
			"  Wake is a distinct entry point from WakeForDelivery; an unbounded write\n"+
			"  reached through it hangs the caller's MCP call.", qum1072TestTimeout)
	}

	// THE NON-VACUITY CONTROL. Without this, a Wake that silently wrote nothing
	// would satisfy the timing bound above while proving nothing.
	assertAttempted(t, mock, []struct{ priority, bodyContains string }{
		{"next", "id-qum1186-wake"},
	})
}

// TestQUM1072_ChildDrain_UnwedgedWriteStillDelivers is the positive control that
// stops every "bounded" assertion above from being satisfied by a drain that
// simply never writes. Same handle shape, no wedge: the entry reaches stdin.
func TestQUM1072_ChildDrain_UnwedgedWriteStillDelivers(t *testing.T) {
	const deadline = 150 * time.Millisecond
	withShortChildDrainTimeout(t, deadline)

	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", "id-qum1072-control", "delivered body")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	writes := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(writes, "id-qum1072-control"); got != 1 {
		t.Fatalf("injections of the entry on an UNWEDGED drain = %d across %d writes, want 1 — the deadline broke ordinary delivery, so the bounded-time assertions elsewhere prove nothing", got, len(writes))
	}
}
