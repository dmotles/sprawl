// QUM-1068 regression guard: markConsumed is IDEMPOTENT.
//
// WHAT THIS FILE GUARDS. A uuid that has already left statePending fires neither
// cfg.OnDelivered nor an EventUserMessageConsumed publish a second time. Exactly
// one of each, per uuid, for the life of the entry — no matter how many settle
// signals arrive or in what order.
//
// WHY IT EXISTS. Three comments in the tree asserted, as load-bearing protocol
// fact, that a priority:"now" write is never re-emitted via
// --replay-user-messages. That is false. A chunk-aware census of the wire logs
// (`query`, 766 logs / 46 agents, 2026-08-04) found 51 of 54 now-priority writes
// WERE echoed — 94% — spread across log-start dates 2026-06-12 → 2026-07-30, all
// of them <system-notification> bodies, i.e. exactly the interrupt-flush path
// ConfirmDeliveredWithoutReplay was written for. It was then reproduced against a
// live CLI, including the hard regime: a now-frame injected at the 6s mark into a
// running turn truncated that turn mid-output at 6003ms AND was still echoed back
// with isReplay:true. So the frame really does preempt, and really is echoed.
//
// (Do not cite weave's re-measurement of the same 51/54 figure as corroboration —
// it was RETRACTED. Its reader did json.loads(o['raw']) inside a bare
// try/except:continue, and the logs chunk a `raw` value across lines: 85,467 of
// 417,318 raws failed to parse, 14,983 of them `user` frames, silently. It agreed
// with the right answer from a broken instrument. `query`'s chunk-aware wirelib
// and the live reproduction are the two independent sources.)
//
// THE DEFECT that followed: ConfirmDeliveredWithoutReplay is a bare markConsumed
// call, and markConsumed gated only the STATE FLIP on statePending — OnDelivered
// and the publish were unconditional. So an echoed now-write called
// OnDelivered(entryIDs) twice (→ a second agentloop.MarkDelivered on an entry no
// longer in pending/, which fails and logs a WARN on the happy path) and published
// EventUserMessageConsumed twice for one uuid.
//
// MECHANISM (decided at the manager level before implementation, not chosen inside
// this diff — QUM-1068 comment "Decision on the flagged design choice"): Option 1,
// make markConsumed idempotent by capturing the statePending → stateConsumed
// transition INSIDE the outMu critical section and gating both side effects on it.
// Option 2 (leave the double, document it as tolerated) was rejected: it is
// harmless only by two accidents nobody asserts — child consume events are dropped
// by applyChildStreamInner, and weave reaches ConfirmDeliveredWithoutReplay only
// via Ctrl+G — and BOTH are scheduled to break (weave_handle.go names restoring
// preemptive `now` writes as a planned follow-up). ConfirmDeliveredWithoutReplay is
// NOT removed and the QUM-821 anti-storm guarantee stands: 3 of the 54 now-writes
// genuinely were not echoed, so the echo cannot be relied on either.
//
// ORDER INDEPENDENCE IS THE POINT, not just deduplication. Post-fix, whichever
// settle signal lands first transitions and fires; the loser is a true no-op. Both
// orders are asserted below, because the wire does not guarantee which arrives
// first and a fix that only handled ack-then-echo would leave the reverse race.
//
// RECONCILIATION, not an existence control. The spy is [][]string, one element per
// OnDelivered call carrying that call's ids, so "1" and "2" are distinguishable AND
// attributable. Critically, each arm asserts the count BEFORE the suppressed call
// (proving the spy is live) and again AFTER an independent second entry is
// delivered normally (proving the spy did not simply stop observing). A spy that
// went inert cannot produce the final [["e1"],["e2"]] reading.
//
// MUTATION LOG — every assertion here has been watched fail, with what it printed.
// The two side effects are gated on ONE flag, so they are mutated SEPARATELY: a
// single mutation reverting both would not show that each gate is independently
// load-bearing.
//
//	M1  revert the OnDelivered gate only (keep the publish gated). NOTE the recipe
//	    is two edits, not one: the early return must be deleted AND
//	    `entryIDs = e.entryIDs` restored to an `if e != nil` arm. Removing the gate
//	    alone leaves entryIDs empty on the second call, `len(entryIDs) > 0` false,
//	    and the test misleadingly GREEN. Exactly:
//	      replace `if !transitioned { return }` with `if e != nil { entryIDs = e.entryIDs }`
//	      and re-gate the publish with `if !transitioned { return }` above it.
//	    → AckOnWriteThenEcho FAILED: "OnDelivered calls after the echo =
//	      [[e1] [e1]], want STILL exactly [[e1]]".
//	    → EchoThenAckOnWrite FAILED with the same reading, proving the duplicate is
//	      not an artefact of one arrival order.
//	M2  revert the publish gate only (keep OnDelivered gated). Also a restructure,
//	    not a one-line revert: replace the `if !transitioned { return }` early
//	    return with `if transitioned && len(entryIDs) > 0 && rt.cfg.OnDelivered !=
//	    nil {` on the OnDelivered call, leaving the publish unguarded.
//	    → AckOnWriteThenEcho FAILED: "EventUserMessageConsumed published 1 more
//	      time(s) for <uuid> after the echo, want 0".
//	    → EchoThenAckOnWrite FAILED likewise.
//	    → AND, outside this file, qum1000_settle_never_acked_test.go FAILED in both
//	      swept arms: "EventUserMessageConsumed for P = 2, want 1". That is the
//	      QUM-1000 M4 tripwire firing in reverse, which is the cross-check that the
//	      2→1 edit there was this change and not an unrelated drift.
//	M3  gate on `e != nil` instead of on the transition — i.e. keep the flag but
//	    compute it wrongly, the most likely way to get this "right" and still be
//	    broken.
//	    → AckOnWriteThenEcho and EchoThenAckOnWrite both FAILED with
//	      "[[e1] [e1]]". This is why `transitioned` is set inside the
//	      `e.state == statePending` arm and not merely alongside it.
//
// The two arms in this file that are NOT red-first (they assert properties that
// already held) carry their own mutations, recorded in
// internal/tui/qum1068_spinner_boundary_test.go as M4 and M5.
package runtime

import (
	"context"
	"reflect"
	"testing"
)

// qum1068WriteNow writes a system message at priority "now" carrying entryID,
// which is the shape the QUM-821 interrupt-flush path produces.
func qum1068WriteNow(t *testing.T, f *qum1000Fixture, body, entryID string) string {
	t.Helper()
	uuid, err := f.rt.WriteSystemMessage(context.Background(), body, "now", []string{entryID})
	if err != nil {
		t.Fatalf("WriteSystemMessage(now): %v", err)
	}
	if uuid == "" {
		t.Fatal("WriteSystemMessage returned an empty uuid; nothing downstream can be keyed on it")
	}
	return uuid
}

// TestQUM1068_NowWrite_AckOnWriteThenEcho_DeliversOnce is the fixture the suite
// lacked, and the reason a 94%-of-the-time wire behaviour was documented as never
// happening: every existing test exercised ack-on-write OR the echo, never BOTH
// for the same uuid.
func TestQUM1068_NowWrite_AckOnWriteThenEcho_DeliversOnce(t *testing.T) {
	f := newQUM1000Fixture(t)
	runningTransition(f.rt)

	uuid := qum1068WriteNow(t, f, "<system-notification>urgent</system-notification>", "e1")

	// PATH 1 — ack-on-write (QUM-821). This is also the [I] instrument-live gate:
	// the spy must read exactly one call here, or every "still 1" below is vacuous.
	f.rt.ConfirmDeliveredWithoutReplay(uuid)
	if got := f.deliveries(); !reflect.DeepEqual(got, [][]string{{"e1"}}) {
		t.Fatalf("[I] OnDelivered calls after ack-on-write = %v, want exactly [[e1]] — the instrument is not live, so the duplicate assertion below would prove nothing", got)
	}
	if got := entryState(t, f.rt, uuid).state; got != stateConsumed {
		t.Fatalf("[I] entry state after ack-on-write = %s, want stateConsumed", stateName(got))
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 1 {
		t.Fatalf("[I] EventUserMessageConsumed after ack-on-write = %d, want 1", n)
	}

	// PATH 2 — the echo the census proved arrives anyway, 94% of the time.
	replayEcho(t, f.rt, uuid)

	if got := f.deliveries(); !reflect.DeepEqual(got, [][]string{{"e1"}}) {
		t.Fatalf("OnDelivered calls after the echo = %v, want STILL exactly [[e1]].\n"+
			"  [[e1] [e1]] → the QUM-1068 gate regressed: markConsumed fired OnDelivered for an entry\n"+
			"                that had already left statePending. Downstream that is a second\n"+
			"                agentloop.MarkDelivered on an entry no longer in pending/, which fails and\n"+
			"                logs a WARN on the happy path.", got)
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 0 {
		t.Fatalf("EventUserMessageConsumed published %d more time(s) for %s after the echo, want 0 — a duplicate consume relights the TUI spinner via the QUM-831 TurnIdle→TurnThinking reducer, with no path back to idle", n, uuid)
	}

	// RECONCILIATION: an independent entry, delivered normally, must still be
	// recorded. This is what distinguishes "1 because the gate works" from "1
	// because the spy stopped observing after the first call".
	uuid2 := qum1068WriteNow(t, f, "<system-notification>second</system-notification>", "e2")
	f.rt.ConfirmDeliveredWithoutReplay(uuid2)
	if got := f.deliveries(); !reflect.DeepEqual(got, [][]string{{"e1"}, {"e2"}}) {
		t.Fatalf("OnDelivered calls after a second independent entry = %v, want [[e1] [e2]] — the spy must still be appending; if this reads [[e1]] the suppression above was the spy dying, not the gate working", got)
	}
}

// TestQUM1068_NowWrite_EchoThenAckOnWrite_DeliversOnce is the REVERSE order. The
// wire does not guarantee which settle signal lands first — the live reproduction
// showed the echo arriving promptly enough to race a synchronous
// ConfirmDeliveredWithoutReplay on the supervisor's goroutine — so a gate that only
// handled ack-then-echo would leave the mirror-image duplicate in place.
func TestQUM1068_NowWrite_EchoThenAckOnWrite_DeliversOnce(t *testing.T) {
	f := newQUM1000Fixture(t)
	runningTransition(f.rt)

	uuid := qum1068WriteNow(t, f, "<system-notification>urgent</system-notification>", "e1")

	// PATH 2 first this time.
	replayEcho(t, f.rt, uuid)
	if got := f.deliveries(); !reflect.DeepEqual(got, [][]string{{"e1"}}) {
		t.Fatalf("[I] OnDelivered calls after the echo = %v, want exactly [[e1]] — instrument not live", got)
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 1 {
		t.Fatalf("[I] EventUserMessageConsumed after the echo = %d, want 1", n)
	}

	// PATH 1 second: the supervisor's ack-on-write lands after the echo already
	// consumed the entry. It must be a no-op, not a second delivery.
	f.rt.ConfirmDeliveredWithoutReplay(uuid)

	if got := f.deliveries(); !reflect.DeepEqual(got, [][]string{{"e1"}}) {
		t.Fatalf("OnDelivered calls after a late ack-on-write = %v, want STILL exactly [[e1]] — the gate must be order-independent; this is the mirror image of the ack-then-echo duplicate", got)
	}
	if n := consumedFor(drainNow(f.ch), uuid); n != 0 {
		t.Fatalf("EventUserMessageConsumed published %d more time(s) after a late ack-on-write, want 0", n)
	}
}

// TestQUM1068_UnknownUUID_PublishesNothing pins a behaviour delta the gate
// introduces, so it is a decision on the record rather than a side effect noticed
// later.
//
// Before the fix, markConsumed on a uuid absent from the outstanding map still
// published EventUserMessageConsumed — making ConfirmDeliveredWithoutReplay's own
// doc claim ("No-op for an unknown uuid") false. This is reachable in production:
// the outstanding map is in-memory, so after a session restart the CLI's replay
// echoes for pre-restart uuids all land as unknown and each published a phantom
// consume. Every one of those hit the QUM-831 TurnIdle→TurnThinking reducer.
// Suppressing them is part of the fix, not collateral.
func TestQUM1068_UnknownUUID_PublishesNothing(t *testing.T) {
	f := newQUM1000Fixture(t)
	runningTransition(f.rt)

	// [I] The instrument can see a real consume — otherwise "0" below is vacuous.
	known := qum1068WriteNow(t, f, "<system-notification>real</system-notification>", "e1")
	replayEcho(t, f.rt, known)
	if n := consumedFor(drainNow(f.ch), known); n != 1 {
		t.Fatalf("[I] EventUserMessageConsumed for a known uuid = %d, want 1 — instrument not live", n)
	}

	replayEcho(t, f.rt, "uuid-never-written-post-restart-echo")

	if n := consumedFor(drainNow(f.ch), "uuid-never-written-post-restart-echo"); n != 0 {
		t.Fatalf("EventUserMessageConsumed for an UNKNOWN uuid = %d, want 0 — a phantom consume for a uuid this runtime never wrote (the post-restart replay shape) relights the spinner with nothing in flight", n)
	}
	if got := f.deliveries(); !reflect.DeepEqual(got, [][]string{{"e1"}}) {
		t.Fatalf("OnDelivered calls = %v, want [[e1]] — an unknown uuid carries no entryIDs and must never reach OnDelivered", got)
	}
}
