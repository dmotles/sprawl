// QUM-1066 regression guard for the child drain (unifiedHandle.drainPendingToStdin).
//
// WHAT THIS FILE GUARDS. The child drain filters UnifiedRuntime.InFlightSystemEntryIDs,
// so an async-class (priority `next`) maildir entry written into the write→ack window
// is injected into the CLI's stdin EXACTLY ONCE, no matter how many turn boundaries
// pass before its isReplay echo arrives — including never.
//
// PROVENANCE. QUM-1061 measured the defect these tests now guard against: the child
// drain had no such filter (only WeaveRuntimeHandle did), so every un-acked async
// entry was re-injected once per turn boundary — 2 injections after one boundary,
// 11 across ten, exactly linear with no decay, unbounded while the echo never
// arrived. QUM-1066 ported weave's filter. The assertions below were flipped from
// the measured defect (2 / boundaries+1) to the fixed behaviour (1 / 1); see M5.
//
// WHY IT IS NOT A RACE — and therefore why none of these tests need timing tolerance.
// runtime.routeFrame calls cfg.PostTurnSweep on its EndOfTurn leg, SYNCHRONOUSLY on
// the backend reader goroutine, and sweepCoordinator.wake is bound to
// unifiedHandle.WakeForDelivery (runtime_launcher.go, coord.Bind). So the re-drain
// necessarily runs before ANY later frame can be read — including the queued
// message's own isReplay echo, which the CLI cannot emit until it dequeues the
// message after the current turn ends. Pre-fix, an async entry drained while a turn
// was open was therefore re-written deterministically, on the ordinary ordering,
// with no interleaving required. Post-fix the suppression is equally deterministic.
//
// MECHANISM CHOICE, so nobody "simplifies" it later. Three candidates (QUM-1061's
// findings doc, § "Recommended framing"): the InFlightSystemEntryIDs filter, which
// is what shipped; ack-on-write (ConfirmDeliveredWithoutReplay, the `now` tier's),
// REJECTED because it marks an entry delivered before the CLI consumed it and so
// loses async inbox mail on a crash in that window; and a statePending-only filter,
// which is a TRAP — it does not fix this at all (QUM-925 found the
// consumed-but-not-yet-MarkDelivered hole). Durability was the deciding axis. Full
// rationale in the comment at the fix site in runtime_launcher.go.
//
// INSTRUMENT DISCIPLINE (/testing-practices § Negative assertions). "Exactly 1
// injection" is only a finding if the same counter can also read 2:
//
//   - every arm whose finding is an injection COUNT asserts a NON-ZERO count from
//     the same capture BEFORE its measurement (phase [I]), so "no duplicate" can
//     never be confused with "the harness captured nothing". (..._DiscardsWriteError
//     AndLosesStatusLines is the exception and needs no [I] gate: its finding is a
//     ZERO, and it carries an end-of-test positive control instead — see M4.);
//   - mutation M5 (revert the fix) makes the same counter read 2 and 11 — that is
//     the live demonstration, recorded below;
//   - ..._SecondEntryDeliveredWhileFirstInFlight distinguishes "suppressed" from
//     "delivered" WITHIN a single run, so over-suppression cannot masquerade as a
//     pass. The weave arm is now PARITY, not a discriminator: post-fix both paths
//     read 1, so it can no longer tell the two apart on its own.
//   - RECONCILIATION, not mere existence (an existence control licenses only "the
//     counter is not inert"; this counter COUNTS): the arms whose finding IS a count
//     of 1 in a single expected frame — MidTurn..., NeverAcked..., StatusLine... —
//     also assert the TOTAL number of captured stdin frames, so "1 injection" cannot
//     be produced by a capture that silently dropped frames. The remaining arms
//     assert per-ID counts only; those are robust to an extra frame by construction
//     (an extra frame can only ever RAISE the count), so a dropped frame there is
//     caught by the [I] gate rather than by a total.
//
// WHAT THIS FILE DOES *NOT* PIN, so nobody over-reads it. Two implementations pass
// every test here and are still wrong:
//
//   - a statePending-ONLY in-flight predicate (the trap above). Every scenario here
//     leaves the entry statePending or removes it from pending/ outright, so nothing
//     here exercises the markConsumed-released-outMu-but-MarkDelivered-not-yet-
//     renamed window. That window is guarded ONE LAYER DOWN, on the predicate both
//     drains share, by TestWeaveRuntimeHandle_WakeForDelivery_ConsumedButNotYet
//     Delivered_NoDuplicateWrite (weave_handle_test.go) — which is sufficient
//     precisely BECAUSE the child now calls the same UnifiedRuntime.InFlightSystem
//     EntryIDs rather than reimplementing the predicate. If a future change gives
//     the child its own predicate, that test stops covering it and this file needs
//     the mirrored window arm.
//   - a handle-local "IDs I have already written this process" set. Indistinguishable
//     here, and worse: it would never forget, so it could not be cleared by an ack.
//
// SCOPE FENCE. One child-vs-weave delta is deliberately still here: the child
// drain takes NO serialising mutex. Also still here, unrelated to that: a
// discarded write error losing status_change lines (QUM-1034 — still reproduced
// on purpose by ..._DiscardsWriteErrorAndLosesStatusLines; QUM-1072 since made
// that loss LOUD via a WARN carrying the bodies, but nothing re-queues them). The
// unbounded write context that used to be listed here was fixed by QUM-1072.
//
// The missing mutex bounds the guarantee above, so read "EXACTLY ONCE" as "exactly
// once per SEQUENTIAL drain". The filter is a read-then-write, so two concurrent
// drains — an MCP-handler-goroutine poke (Real.SendMessage / Real.ReportStatus)
// interleaving with PostTurnSweep on the reader goroutine — can both read the
// in-flight set before either writes. Every arm here drains sequentially, by
// construction: the turn-boundary re-drain is synchronous on the calling
// goroutine. So this file kills the STORM (linear, unbounded, deterministic) and
// does not claim to kill a rare concurrent duplicate.
//
// WHAT CHANGED IN QUM-1062, because this fence used to name it as the thing that
// justified that issue: the two drains ARE now unified, behind drain.go's shared
// runDrain + the drainPolicy table. The missing mutex did NOT get fixed there —
// unification is explicitly behaviour-preserving — but it stopped being an
// accident of having two functions and became a NAMED, commented policy field
// (childDrainPolicy's nil mu). So the residual is unchanged in substance and now
// has one place that owns it. Closing it is still its own issue.
//
// MUTATION LOG — every assertion here has been watched fail, with what it printed.
// Reproduce by re-applying the named mutation. M1–M4 are QUM-1061's original
// entries, taken against the PRE-fix tree; M5 onwards are QUM-1066's, taken against
// the post-fix tree.
//
// NB M1–M4 quote the test names as they were BEFORE the QUM-1066 rename; the
// current names are in parentheses. `go test -run` on a stale name matches nothing
// and EXITS 0, so reproduce with the parenthesised name.
//
//	M1  port weave's in-flight filter into unifiedHandle.drainPendingToStdin.
//	    → MidTurn...DuplicatedAtTurnBoundary FAILED: "injections … = 1 … want 2".
//	      (now ..._MidTurnAsyncEntry_NotDuplicatedAtTurnBoundary)
//	    → NeverAcked...GrowsWithoutBound  FAILED: "= 1 across 10 boundaries, want 11",
//	      and the BLAST RADIUS log line went from 1.0 to 0.0 writes per boundary.
//	      (now ..._NeverAckedAsyncEntry_WrittenOnceAcrossManyBoundaries)
//	    (M1 is the advance evidence that the fix QUM-1066 shipped works.)
//	M2  disable the filter in WeaveRuntimeHandle.drainPendingToStdin
//	    (`false && len(inFlight) > 0`).
//	    → WeaveDrain...HarnessNegativeControl FAILED: "injections … on the WEAVE
//	      path = 2 across 2 writes, want 1". Same helper, same scenario, reading 2.
//	      This is the demonstration that the counter is not hard-wired to 1.
//	      (now ..._WeaveDrain_MidTurnAsyncEntry_NoDuplicate_ParityWithChild)
//	M3  delete the ConfirmDeliveredWithoutReplay call from the interrupt branch.
//	    → InterruptClassEntry_NotDuplicated FAILED at the pending gate:
//	      "ListPending = 1, want 0". With that gate temporarily removed (M3b) the
//	      injection assertion itself FAILED: "injections of interrupt-class … = 2,
//	      want 1" — so that arm's counter is live in both directions too.
//	M4  the destructive-drain test asserts BOTH polarities of its own detector in
//	    one run: statusChangeEnvelopePresent is true at setup and false after the
//	    failed drain. Its POSITIVE ARM (a working write) asserts a non-zero write
//	    carrying the line body, so "0 writes" cannot be a harness that never
//	    delivers status lines.
//	M5  REVERT THE FIX — delete the in-flight filter block from
//	    unifiedHandle.drainPendingToStdin. This is the watched failure for every
//	    flipped assertion. All five child arms FAILED:
//	      ..._MidTurnAsyncEntry_NotDuplicatedAtTurnBoundary:
//	        "captured stdin frames = 2, want 1"
//	      ..._NeverAckedAsyncEntry_WrittenOnceAcrossManyBoundaries:
//	        "captured stdin frames = 11 across 10 boundaries, want 1"
//	      ..._SecondEntryDeliveredWhileFirstInFlight:
//	        "injections of the in-flight entry id-qum1066-first = 2, want 1"
//	      ..._StatusLineStillWrittenWhenEveryAsyncFiltered:
//	        "injections of the in-flight entry id-qum1066-statusline = 2, want 1"
//	      ..._SuppressedEntryRedeliveredAfterRestart:
//	        "injections before the restart = 2, want 1"
//	    The RECONCILIATION gates fire before the injection assertions in the first
//	    two, so they print frame counts; run without those gates (the red-phase run
//	    that preceded them) and the same arms print "injections … = 2 … want 1" and
//	    "= 11 across 10 boundaries, want 1", with BLAST RADIUS at 1.0/boundary.
//	M6  FILTER ONLY ASYNCS — move the filter after SplitByClass and apply it to the
//	    `asyncs` slice. → ALL GREEN, recorded as such. This is NOT an untested gap
//	    to "close" later: it is BEHAVIOURALLY EQUIVALENT. An interrupt entry either
//	    leaves pending/ via ConfirmDeliveredWithoutReplay on a successful `now`
//	    write, or leaves no outstanding record at all on a failed one (writeMessage
//	    deletes it), so the interrupt half of the filter can only act inside the
//	    markConsumed→MarkDelivered window — which needs a parked OnDelivered to make
//	    deterministic and is guarded one layer down on the shared predicate (see
//	    "WHAT THIS FILE DOES NOT PIN"). Filtering before the split is defence in
//	    depth and the shape QUM-1062 will unify; it is not load-bearing here.
//	M7  NARROW THE ASYNC GATE from `len(asyncs) > 0 || len(statusLines) > 0` to
//	    `len(asyncs) > 0` — i.e. drop the status line when every async is filtered.
//	    → ..._StatusLineStillWrittenWhenEveryAsyncFiltered FAILED: "stdin writes =
//	      1, want 2 — the status line was never written; a destructive drain with no
//	      write means it is permanently LOST".
//	    → ..._DiscardsWriteErrorAndLosesStatusLines also FAILED on its positive arm:
//	      "stdin writes on the working-write control arm = 0, want 1".
//	M8  SUPPRESS THE WHOLE DRAIN when anything is in flight
//	    (`if len(inFlight) > 0 { return }`).
//	    → ..._SecondEntryDeliveredWhileFirstInFlight FAILED: "injections of the NEW
//	      entry id-qum1066-second = 0 across 1 writes, want 1 — the in-flight filter
//	      is over-broad".
//	    → ..._StatusLineStillWrittenWhenEveryAsyncFiltered FAILED: "status_change
//	      envelope is still on disk".
//	M9  ACK-ON-WRITE, the REJECTED mechanism — delete the filter and call
//	    ConfirmDeliveredWithoutReplay on the successful async write. It silences the
//	    duplicate, so without durability assertions it would look like a valid fix.
//	    It is caught:
//	    → ..._MidTurnAsyncEntry_NotDuplicatedAtTurnBoundary FAILED at the window
//	      gate: "[W] ListPending = 0 (err=<nil>), want 1".
//	    → ..._NeverAckedAsyncEntry_WrittenOnceAcrossManyBoundaries FAILED:
//	      "ListPending = 0, want 1 — a never-acked entry must stay in pending/"
//	      (its injection count read 1 and BLAST RADIUS 0.0 — i.e. the duplicate
//	      assertions alone do NOT distinguish this from the shipped fix).
//	    → ..._SuppressedEntryRedeliveredAfterRestart FAILED: "ListPending after
//	      restart = 0, want 1 — the entry must have survived on disk".
//	    Plus a WARN: "mark delivered failed … not found in pending".
//	M9b DROP `ids` from the async WriteSystemMessage call, so nothing is ever
//	    outstanding and nothing is ever acked. This is the watched failure for
//	    ..._AckedEntryLeavesPendingAndIsNotRewritten, which passes pre-fix and would
//	    otherwise be unfalsifiable.
//	    → ..._AckedEntryLeavesPendingAndIsNotRewritten FAILED: "injections of
//	      id-qum1066-acked after the ack = 3, want 1".
//	    → four other arms FAILED too (the empty in-flight set re-opens the storm).
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/protocol"
)

// errQUM1061WriteFailed simulates a failed stdin write (a full/closed pipe).
var errQUM1061WriteFailed = errors.New("qum1061: simulated stdin write failure")

// statusChangeEnvelopePresent reports whether any status_change envelope is still
// on disk for recipient. Non-destructive, unlike DrainStatusChangeLines.
func statusChangeEnvelopePresent(t *testing.T, sprawlRoot, recipient string) bool {
	t.Helper()
	for _, sub := range []string{"new", "cur"} {
		entries, err := os.ReadDir(filepath.Join(messages.MessagesDir(sprawlRoot), recipient, sub))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			b, err := os.ReadFile(filepath.Join(messages.MessagesDir(sprawlRoot), recipient, sub, e.Name())) //nolint:gosec // test-local path
			if err != nil {
				continue
			}
			if strings.Contains(string(b), `"status_change"`) {
				return true
			}
		}
	}
	return false
}

// countEntryIDWrites counts how many times the maildir entry ID appears across
// the bodies of the captured stdin writes. A maildir citation embeds the entry's
// unique id exactly once per rendered entry (see inboxprompt), so this is a count
// of injections of that notification.
func countEntryIDWrites(ws []protocol.UserMessage, entryID string) int {
	n := 0
	for _, w := range ws {
		n += strings.Count(w.Message.Content, entryID)
	}
	return n
}

// frameRouterFor returns the runtime's installed frame router — the same seam
// echoReplay and markRunning use to synthesize CLI frames.
func frameRouterFor(t *testing.T, mock *fakeBackendSession) func(*protocol.Message, backend.TurnInfo) {
	t.Helper()
	mock.mu.Lock()
	h := mock.router
	mock.mu.Unlock()
	if h == nil {
		t.Fatal("no frame router captured — the runtime did not install one, so no turn boundary can be synthesized")
	}
	return h
}

// openFrameTurnViaWire opens a frame turn by routing a non-terminal assistant
// frame, so a subsequent drain lands MID-TURN. This is the production shape: the
// CLI cannot ack a priority-`next` write until it dequeues it after the current
// turn ends.
func openFrameTurnViaWire(t *testing.T, mock *fakeBackendSession) {
	t.Helper()
	frameRouterFor(t, mock)(
		&protocol.Message{Type: "assistant", Raw: []byte(`{"type":"assistant","message":{"content":[]}}`)},
		backend.TurnInfo{Autonomous: true},
	)
}

// fireTurnBoundary routes a terminal `result` frame, which is what drives
// routeFrame's EndOfTurn leg → cfg.PostTurnSweep → the re-drain.
func fireTurnBoundary(t *testing.T, mock *fakeBackendSession) {
	t.Helper()
	res := protocol.ResultMessage{Type: "result", Subtype: "success"}
	raw, _ := json.Marshal(res)
	frameRouterFor(t, mock)(
		&protocol.Message{Type: "result", Subtype: "success", Raw: raw},
		backend.TurnInfo{Autonomous: true, EndOfTurn: true},
	)
}

// TestQUM1066_ChildDrain_MidTurnAsyncEntry_NotDuplicatedAtTurnBoundary is the
// single-boundary guard for QUM-1066 AC 1: an async entry drained mid-turn is
// written once, and the turn-boundary re-drain (PostTurnSweep → WakeForDelivery)
// finds it in flight and skips it.
func TestQUM1066_ChildDrain_MidTurnAsyncEntry_NotDuplicatedAtTurnBoundary(t *testing.T) {
	const entryID = "id-qum1066-async"
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	// A turn is in flight when the notification arrives — so the CLI physically
	// cannot ack the `next`-priority write before this turn's terminal frame.
	openFrameTurnViaWire(t, mock)
	seedAsyncEntry(t, sprawlRoot, "alice", entryID, "all green")

	// The producer-side poke (Real.SendMessage / Real.ReportStatus call this
	// synchronously).
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}

	// [I] INSTRUMENT LIVE: a non-zero count from the same capture the measurement
	// below reads. Without this, "1 injection" and "the harness captured nothing"
	// are the same observation.
	writes := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(writes, entryID); got != 1 {
		t.Fatalf("[I] injections of %s after the first drain = %d across %d stdin writes, want exactly 1 — the instrument is not live; the rest of this test would be vacuous", entryID, got, len(writes))
	}

	// [W] THE WINDOW: no isReplay echo yet, so the entry is still on disk in
	// pending/ and still statePending in the outstanding map.
	if pending, err := agentloop.ListPending(sprawlRoot, "alice"); err != nil || len(pending) != 1 {
		t.Fatalf("[W] ListPending = %d (err=%v), want 1 — the write→ack window requires the entry still in pending/", len(pending), err)
	}

	// [R] THE TRIGGER, unmocked: the turn ends. routeFrame → PostTurnSweep →
	// coord.wake (= WakeForDelivery) → drainPendingToStdin, all on the reader
	// goroutine, before the echo can possibly be read.
	fireTurnBoundary(t, mock)

	// settledWrites(1, …) rather than (2, …): post-fix there is only ever one
	// write, so the 150ms settle window — not the wait — is what makes "no second
	// write" an observation rather than an early return.
	after := mock.settledWrites(1, 2*time.Second, 150*time.Millisecond)
	// RECONCILIATION: frames captured must equal frames expected, so "1 injection"
	// cannot be produced by a capture that lost the second frame.
	if len(after) != 1 {
		t.Fatalf("captured stdin frames = %d, want 1 — the injection count below is only meaningful if the capture accounts for every frame", len(after))
	}
	const wantInjections = 1
	if got := countEntryIDWrites(after, entryID); got != wantInjections {
		t.Fatalf("injections of %s after one turn boundary = %d across %d stdin writes, want %d.\n"+
			"  got 2 → the QUM-1066 in-flight filter regressed: the boundary re-drain re-injected an\n"+
			"          entry that is still awaiting its isReplay echo. Check that\n"+
			"          unifiedHandle.drainPendingToStdin still consults rt.InFlightSystemEntryIDs().",
			entryID, got, len(after), wantInjections)
	}

	// DURABILITY, the axis on which the in-flight filter was chosen over
	// ack-on-write: the write is suppressed, the entry is NOT marked delivered.
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 1 {
		t.Errorf("ListPending = %d, want 1 — suppressing the duplicate WRITE must not mark the entry delivered; it is only delivered by the isReplay echo", len(pending))
	}
}

// TestQUM1066_ChildDrain_AckedEntryLeavesPendingAndIsNotRewritten closes the loop
// the filter opens: it must suppress the duplicate WRITE without suppressing the
// ACK. Once the CLI's isReplay echo arrives, markConsumed → OnDelivered →
// MarkDelivered still runs, the entry leaves pending/, and no further drain
// re-injects it.
func TestQUM1066_ChildDrain_AckedEntryLeavesPendingAndIsNotRewritten(t *testing.T) {
	const entryID = "id-qum1066-acked"
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	openFrameTurnViaWire(t, mock)
	seedAsyncEntry(t, sprawlRoot, "alice", entryID, "will be acked")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(writes, entryID); got != 1 {
		t.Fatalf("[I] injections after the first drain = %d, want 1 — instrument not live", got)
	}

	// The CLI dequeues and echoes the write: the real consumption ack.
	mock.echoReplay(writes[0].UUID)

	fireTurnBoundary(t, mock)
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	after := mock.settledWrites(1, time.Second, 150*time.Millisecond)
	if got := countEntryIDWrites(after, entryID); got != 1 {
		t.Fatalf("injections of %s after the ack = %d, want 1", entryID, got)
	}
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 0 {
		t.Fatalf("ListPending = %d, want 0 — the isReplay echo must still drive MarkDelivered; if the filter suppressed the ack path too, entries would strand in pending/ forever", len(pending))
	}
	if delivered, _ := agentloop.ListDelivered(sprawlRoot, "alice"); len(delivered) != 1 {
		t.Fatalf("ListDelivered = %d, want 1 — the entry must land in delivered/, proving the suppression is write-side only", len(delivered))
	}
}

// restartChildHandleAtRoot starts a SECOND unifiedHandle against an EXISTING
// sprawlRoot, standing in for a process restart: same maildir on disk, a brand-new
// UnifiedRuntime with an empty `outstanding` map. It mirrors
// buildStartedUnifiedHandleForTest, which always mints its own t.TempDir() and so
// cannot express "same root, fresh runtime".
func restartChildHandleAtRoot(t *testing.T, sprawlRoot string) (*unifiedHandle, *fakeBackendSession) {
	t.Helper()
	oldStart := unifiedAdapterStartFn
	t.Cleanup(func() { unifiedAdapterStartFn = oldStart })

	fakeSession := newFakeBackendSession("sess-alice", backend.Capabilities{})
	unifiedAdapterStartFn = func(_ context.Context, _ backend.SessionSpec) (backend.Session, error) {
		return fakeSession, nil
	}
	starter := newInProcessUnifiedStarter(backend.InitSpec{}, nil)
	handle, err := starter.Start(RuntimeStartSpec{
		Name: "alice", Worktree: filepath.Join(sprawlRoot, "wt"), SprawlRoot: sprawlRoot,
		SessionID: "sess-alice", TreePath: "weave/alice",
	})
	if err != nil {
		t.Fatalf("restart Start: %v", err)
	}
	uh, ok := handle.(*unifiedHandle)
	if !ok {
		t.Fatalf("restart handle type = %T, want *unifiedHandle", handle)
	}
	return uh, fakeSession
}

// TestQUM1066_ChildDrain_SuppressedEntryRedeliveredAfterRestart is what makes the
// chosen mechanism's cost acceptable rather than merely asserted.
//
// The in-flight filter suppresses a never-acked entry for the LIFE OF THE PROCESS
// (the QUM-1028 shape — see UnifiedRuntime.InFlightSystemEntryIDs' SCOPE comment).
// That is only tolerable because `outstanding` is in-memory while the entry is
// durable: MarkDelivered never ran, so the entry is still in pending/, and a fresh
// runtime re-emits it. For weave the re-emitting hook is SetPostStartHook; children
// register none, and are covered instead by Real.RecoverAgents' explicit
// WakeForDelivery (QUM-605) — a different, less obvious mechanism, which is why it
// is tested rather than assumed. That RecoverAgents shape is exactly what this test
// models: a fresh handle on the same root, then an explicit WakeForDelivery.
//
// (The other restart leg — Real.Wake — redelivers via the FRESH handle's own
// PostTurnSweep → WakeForDelivery rather than via unifiedHandle.Wake, which is
// reached only from Real.Delegate on the SAME handle and so cannot clear a wedge.
// Same empty-outstanding-map argument, not separately covered here.)
//
// Suppression is therefore a DELAY, not loss. If this test ever fails, the QUM-1028
// note at the fix site is wrong and the mechanism choice must be revisited.
func TestQUM1066_ChildDrain_SuppressedEntryRedeliveredAfterRestart(t *testing.T) {
	const entryID = "id-qum1066-restart"
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})

	openFrameTurnViaWire(t, mock)
	seedAsyncEntry(t, sprawlRoot, "alice", entryID, "never acked, then restart")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	first := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(first, entryID); got != 1 {
		t.Fatalf("[I] injections after the first drain = %d, want 1 — instrument not live", got)
	}
	// No echo ever arrives: the entry is now suppressed on this runtime.
	fireTurnBoundary(t, mock)
	if got := countEntryIDWrites(mock.settledWrites(1, time.Second, 150*time.Millisecond), entryID); got != 1 {
		t.Fatalf("injections before the restart = %d, want 1 — the entry must be SUPPRESSED for the restart to be the interesting part", got)
	}
	_ = uh.Stop(context.Background())

	// Restart: same sprawlRoot, fresh runtime, empty outstanding map.
	uh2, mock2 := restartChildHandleAtRoot(t, sprawlRoot)
	defer func() { _ = uh2.Stop(context.Background()) }()

	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 1 {
		t.Fatalf("ListPending after restart = %d, want 1 — the entry must have survived on disk for redelivery to be possible at all", len(pending))
	}
	if err := uh2.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery after restart: %v", err)
	}
	redelivered := mock2.settledWrites(1, 2*time.Second, 150*time.Millisecond)
	if got := countEntryIDWrites(redelivered, entryID); got != 1 {
		t.Fatalf("injections of %s on the RESTARTED runtime = %d across %d frames, want 1 — suppression must be session-scoped, not permanent; if this is 0 the QUM-1028 exposure the fix accepts is message LOSS, not a delay", entryID, got, len(redelivered))
	}
}

// TestQUM1066_ChildDrain_SecondEntryDeliveredWhileFirstInFlight is the arm that
// makes the counter discriminating WITHIN one run. A filter that simply skipped
// the whole drain whenever anything is in flight (or one that suppressed by agent
// rather than by entry ID) would pass every other test here and fail this one:
// entry B, which has never been written, must still be delivered while entry A is
// awaiting its echo.
func TestQUM1066_ChildDrain_SecondEntryDeliveredWhileFirstInFlight(t *testing.T) {
	const entryA = "id-qum1066-first"
	const entryB = "id-qum1066-second"
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	openFrameTurnViaWire(t, mock)
	seedAsyncEntry(t, sprawlRoot, "alice", entryA, "first")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #1: %v", err)
	}
	first := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(first, entryA); got != 1 {
		t.Fatalf("[I] injections of A after the first drain = %d, want 1 — instrument not live", got)
	}

	// B arrives while A is still un-acked and still in pending/.
	seedAsyncEntry(t, sprawlRoot, "alice", entryB, "second")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	all := mock.settledWrites(2, 2*time.Second, 150*time.Millisecond)

	if got := countEntryIDWrites(all, entryB); got != 1 {
		t.Fatalf("injections of the NEW entry %s = %d across %d writes, want 1 — the in-flight filter is over-broad: it must suppress by entry ID, not suppress the whole drain while anything is outstanding", entryB, got, len(all))
	}
	if got := countEntryIDWrites(all, entryA); got != 1 {
		t.Fatalf("injections of the in-flight entry %s = %d, want 1 — A was re-injected alongside B", entryA, got)
	}
}

// TestQUM1066_ChildDrain_StatusLineStillWrittenWhenEveryAsyncFiltered guards the
// failure mode the fix itself introduces. Status/liveness lines are read with the
// DESTRUCTIVE inboxprompt.DrainStatusChangeLines, so a fix that early-returns once
// the post-filter pending set is empty would drop them permanently, with no retry
// and no record. The `len(asyncs) > 0 || len(statusLines) > 0` gate in the child
// drain is what keeps that frame flowing; this arm is what proves it.
//
// THE CONTRACT ASSERTED IS IMMEDIATE DELIVERY IN THE SAME FRAME, deliberately, and
// it is stricter than "not lost". A variant that placed the filter (and an
// emptiness check) ahead of DrainStatusChangeLines would defer the line to the next
// poke rather than lose it, and would fail here. That is intended: the line was
// already drained destructively before the filter runs, so "deferred" is only
// distinguishable from "lost" if some later poke is guaranteed, and none is. The
// two assertions below observe BOTH halves — the envelope is gone from disk AND the
// body reached stdin — so neither is inferred from the other.
func TestQUM1066_ChildDrain_StatusLineStillWrittenWhenEveryAsyncFiltered(t *testing.T) {
	const entryID = "id-qum1066-statusline"
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	openFrameTurnViaWire(t, mock)
	seedAsyncEntry(t, sprawlRoot, "alice", entryID, "the only async")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #1: %v", err)
	}
	first := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(first, entryID); got != 1 {
		t.Fatalf("[I] injections after the first drain = %d, want 1 — instrument not live", got)
	}

	// A status line arrives while the only async entry is in flight, so the
	// post-filter pending set is empty and statusLines is the entire payload.
	if _, err := messages.SendStatusChange(sprawlRoot, "child-of-alice", "alice", messages.StatusChangePayload{
		State: "working", Summary: "n1-marker",
	}); err != nil {
		t.Fatalf("SendStatusChange: %v", err)
	}
	if !statusChangeEnvelopePresent(t, sprawlRoot, "alice") {
		t.Fatal("setup: no status_change envelope on disk — the drain would have nothing to lose")
	}
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	all := mock.settledWrites(2, 2*time.Second, 150*time.Millisecond)

	// The drain consumed the envelope destructively: from here the ONLY copy of
	// the line is whatever reached stdin.
	if statusChangeEnvelopePresent(t, sprawlRoot, "alice") {
		t.Fatal("status_change envelope is still on disk — the drain did not read it, so the assertion below is not measuring the destructive-read hazard")
	}
	if len(all) != 2 {
		t.Fatalf("stdin writes = %d, want 2 — the status line was never written; a destructive drain with no write means it is permanently LOST", len(all))
	}
	if !strings.Contains(all[1].Message.Content, "n1-marker") {
		t.Fatalf("second write does not carry the status line body:\n%s", all[1].Message.Content)
	}
	// ...and the in-flight async was still suppressed in that same frame.
	if got := countEntryIDWrites(all, entryID); got != 1 {
		t.Fatalf("injections of the in-flight entry %s = %d, want 1 — the status-line frame re-injected it", entryID, got)
	}
}

// TestQUM1066_ChildDrain_NeverAckedAsyncEntry_WrittenOnceAcrossManyBoundaries is
// the unbounded-tail guard. It measures injections against MANY turn boundaries for
// an entry whose echo never arrives — the QUM-1028 strand shape, and the shape
// weave's filter comment calls "the unbounded stdin write storm measured on the
// child path". The single-boundary arm above can be satisfied by an accident of
// ordering; this one cannot.
func TestQUM1066_ChildDrain_NeverAckedAsyncEntry_WrittenOnceAcrossManyBoundaries(t *testing.T) {
	const entryID = "id-qum1066-strand"
	const boundaries = 10
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	seedAsyncEntry(t, sprawlRoot, "alice", entryID, "never acked")
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	first := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(first, entryID); got != 1 {
		t.Fatalf("[I] injections after the first drain = %d, want 1 — instrument not live", got)
	}

	start := time.Now()
	for i := 0; i < boundaries; i++ {
		fireTurnBoundary(t, mock)
	}
	// Each boundary's drain is synchronous on the calling goroutine, so the settle
	// window — not the wait — is what makes an extra async write visible.
	all := mock.settledWrites(1, 2*time.Second, 150*time.Millisecond)
	elapsed := time.Since(start)

	// RECONCILIATION: one frame in total, matching the one injection asserted below.
	if len(all) != 1 {
		t.Fatalf("captured stdin frames = %d across %d boundaries, want 1 — reconcile the capture before trusting the injection count", len(all), boundaries)
	}
	got := countEntryIDWrites(all, entryID)
	const want = 1
	// The QUM-1061 blast-radius metric, held at zero. Pre-fix this printed 1.0
	// writes per boundary; a non-zero reading here is the storm returning.
	t.Logf("BLAST RADIUS (held at zero by the in-flight filter): %d injections of one never-acked entry "+
		"across %d turn boundaries (= %.1f writes per boundary); %d boundaries drained in %v "+
		"(%.0f writes/s at this harness's boundary rate — pre-fix this was 1.0/boundary; cf. the ~30 writes/s "+
		"QUM-821 measured for the analogous now-class storm against real claude 2.1.173)",
		got, boundaries, float64(got-1)/float64(boundaries), boundaries, elapsed, float64(got-1)/elapsed.Seconds())

	if got != want {
		t.Fatalf("injections of %s = %d across %d boundaries, want %d (written once, then suppressed for every boundary).\n"+
			"  got %d > 1 → the storm is back: growth is 1 injection per turn boundary and is bounded only by\n"+
			"               how long the agent keeps taking turns. unifiedHandle.drainPendingToStdin must\n"+
			"               consult rt.InFlightSystemEntryIDs() before writing.",
			entryID, got, boundaries, want, got)
	}
	// DURABILITY: the entry is still in pending/ and was never marked delivered.
	// Suppressing the write must not be implemented by marking it delivered —
	// that is exactly the ack-on-write mechanism this fix rejected.
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 1 {
		t.Errorf("ListPending = %d, want 1 — a never-acked entry must stay in pending/ so a restart (empty outstanding map) redelivers it", len(pending))
	}
}

// TestQUM1066_WeaveDrain_MidTurnAsyncEntry_NoDuplicate_ParityWithChild pins that
// the weave path is UNCHANGED by QUM-1066 and now agrees with the child path.
//
// Pre-fix this was the discriminating negative control: it read 1 where the child
// arm read 2, which is how QUM-1061 established that the counter distinguished the
// two and that the delta was the filter rather than the harness. That job is over —
// post-fix both paths read 1, so this test can no longer tell them apart on its
// own. (That role now belongs to mutation M5 and to
// ..._SecondEntryDeliveredWhileFirstInFlight.) Its remaining job is PARITY: the two
// drains must agree, which is the baseline QUM-1062's unification will be measured
// against, and a weave-side regression must fail here rather than silently.
//
// (Weave has no PostTurnSweep binding of its own, so the boundary is followed by
// an explicit WakeForDelivery — the strictly MORE aggressive re-drain.)
func TestQUM1066_WeaveDrain_MidTurnAsyncEntry_NoDuplicate_ParityWithChild(t *testing.T) {
	const entryID = "id-qum1061-weave"
	h, _, mock, sprawlRoot := buildWeaveHandleForTest(t)

	openFrameTurnViaWire(t, mock)
	seedAsyncEntry(t, sprawlRoot, "weave", entryID, "all green")

	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #1: %v", err)
	}
	writes := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(writes, entryID); got != 1 {
		t.Fatalf("[I] injections after the first drain = %d across %d writes, want 1 — instrument not live on the weave arm either", got, len(writes))
	}
	if pending, _ := agentloop.ListPending(sprawlRoot, "weave"); len(pending) != 1 {
		t.Fatalf("[W] ListPending = %d, want 1 — same window as the child arm", len(pending))
	}

	// Same trigger, then an explicit re-drain on top of it.
	fireTurnBoundary(t, mock)
	if err := h.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	after := mock.settledWrites(1, time.Second, 150*time.Millisecond)
	if got := countEntryIDWrites(after, entryID); got != 1 {
		t.Fatalf("injections of %s on the WEAVE path = %d across %d writes, want 1 — the in-flight filter regressed, or the control is no longer measuring the same thing as the child arm", entryID, got, len(after))
	}
}

// TestQUM1066_ChildDrain_InterruptClassEntry_NotDuplicated pins that QUM-1066 did
// not disturb the interrupt-class tier (QUM-1066 AC 4). ConfirmDeliveredWithoutReplay
// still fires on the successful `now` write, so the entry leaves pending/ immediately
// and the re-drain finds nothing — the QUM-821 ack-on-write protection, which is
// independent of the new in-flight filter. Same drain function, same trigger,
// different class.
func TestQUM1066_ChildDrain_InterruptClassEntry_NotDuplicated(t *testing.T) {
	const entryID = "id-qum1061-interrupt"
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	openFrameTurnViaWire(t, mock)
	if _, err := agentloop.Enqueue(sprawlRoot, "alice", agentloop.Entry{
		ID: entryID, ShortID: entryID, Class: agentloop.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
	}); err != nil {
		t.Fatalf("Enqueue interrupt: %v", err)
	}
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery: %v", err)
	}
	writes := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if got := countEntryIDWrites(writes, entryID); got != 1 {
		t.Fatalf("[I] injections after the first drain = %d, want 1 — instrument not live", got)
	}
	// Ack-on-write: the entry is already out of pending/ with no echo.
	if pending, _ := agentloop.ListPending(sprawlRoot, "alice"); len(pending) != 0 {
		t.Fatalf("ListPending = %d, want 0 — ConfirmDeliveredWithoutReplay must have marked the now-write delivered", len(pending))
	}

	fireTurnBoundary(t, mock)
	after := mock.settledWrites(1, time.Second, 150*time.Millisecond)
	if got := countEntryIDWrites(after, entryID); got != 1 {
		t.Fatalf("injections of interrupt-class %s = %d, want 1 — the QUM-821 ack-on-write protection regressed", entryID, got)
	}
}

// TestQUM1061_ChildDrain_DiscardsWriteErrorAndLosesStatusLines answers QUM-1061's
// AC-4 lost_status_lines asymmetry by observation rather than by reading the source.
// It is UNAFFECTED by QUM-1066 and still reproduces the loss ON PURPOSE — the fix is
// QUM-1034's, out of scope here. Keep the name and the QUM-1061 prefix: this arm
// belongs to that issue, not to the fix this file now guards.
//
// The child drain performs the SAME destructive inboxprompt.DrainStatusChangeLines
// as the weave drain, so on a failed write the lines are gone from the maildir with
// no recovery path. That is the finding, and it still reproduces below.
//
// UPDATED BY QUM-1072, which fixed HALF of the original asymmetry. When this was
// written the child drain discarded the write error entirely (`_, _ =
// h.rt.WriteSystemMessage(...)`), so the loss had "no recovery path AND no record".
// QUM-1072 bounded the write and made it log a WARN carrying the verbatim bodies,
// matching weave — so there is now a RECORD. There is still no RECOVERY: nothing
// re-queues the lines, which is what QUM-1034 owns and why this test is unchanged.
func TestQUM1061_ChildDrain_DiscardsWriteErrorAndLosesStatusLines(t *testing.T) {
	uh, mock, sprawlRoot := buildStartedUnifiedHandleForTest(t, backend.Capabilities{})
	defer func() { _ = uh.Stop(context.Background()) }()

	if _, err := messages.SendStatusChange(sprawlRoot, "child-of-alice", "alice", messages.StatusChangePayload{
		State: "complete", Summary: "did the thing",
	}); err != nil {
		t.Fatalf("SendStatusChange: %v", err)
	}
	// The envelope is on disk before the drain.
	if !statusChangeEnvelopePresent(t, sprawlRoot, "alice") {
		t.Fatal("setup: no status_change envelope on disk — the loss cannot be demonstrated")
	}

	// Make the stdin write fail.
	mock.setWriteErr(errQUM1061WriteFailed)

	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery returned err = %v — the child drain swallows write errors, so this must stay nil (that IS the finding)", err)
	}

	// Nothing reached stdin...
	if got := len(mock.writesSnapshot()); got != 0 {
		t.Fatalf("stdin writes = %d, want 0 (the write was forced to fail)", got)
	}
	// ...and the envelope is gone from the maildir. Destructive drain, failed
	// write, no re-queue, no WARN carrying the bodies.
	if statusChangeEnvelopePresent(t, sprawlRoot, "alice") {
		t.Fatal("status_change envelope is still on disk — the destructive-drain loss does NOT reproduce; re-derive AC 4 before routing it to QUM-1034")
	}
	// And a second drain has nothing left to deliver: the line is permanently lost.
	mock.setWriteErr(nil)
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #2: %v", err)
	}
	if got := len(mock.settledWrites(0, 200*time.Millisecond, 150*time.Millisecond)); got != 0 {
		t.Fatalf("stdin writes after a retry drain = %d, want 0 — if the line came back it was not lost after all", got)
	}

	// POSITIVE ARM — this is what makes "0 writes" above a finding rather than a
	// harness that never delivers status lines at all. Same handle, same drain, a
	// working write: a fresh status_change DOES reach stdin as a rendered line. So
	// the zero above is attributable to the write failure, not to the drain.
	if _, err := messages.SendStatusChange(sprawlRoot, "child-of-alice", "alice", messages.StatusChangePayload{
		State: "working", Summary: "control arm",
	}); err != nil {
		t.Fatalf("SendStatusChange (control): %v", err)
	}
	if err := uh.WakeForDelivery(); err != nil {
		t.Fatalf("WakeForDelivery #3: %v", err)
	}
	ctl := mock.settledWrites(1, 2*time.Second, 100*time.Millisecond)
	if len(ctl) != 1 {
		t.Fatalf("stdin writes on the working-write control arm = %d, want 1 — the drain never delivers status lines, so the loss assertion above proves nothing", len(ctl))
	}
	if !strings.Contains(ctl[0].Message.Content, "control arm") {
		t.Fatalf("control-arm write does not carry the status line body:\n%s", ctl[0].Message.Content)
	}
}
