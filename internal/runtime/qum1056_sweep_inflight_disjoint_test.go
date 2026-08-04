package runtime

// QUM-1056: settleNeverAcked's domain and InFlightSystemEntryIDs's domain must
// stay disjoint, and that disjointness must be ASSERTED rather than described.
//
// THE INVARIANT. Two predicates in this file read the same rt.outstanding map and
// disagree about what stateConsumed means:
//
//   - settleNeverAcked (unified.go) settles a never-acked entry by flipping it to
//     stateConsumed and publishing EventUserMessageConsumed, DELIBERATELY without
//     calling OnDelivered — so as not to durably record an inbox message as
//     delivered that the CLI never consumed.
//   - InFlightSystemEntryIDs (unified.go) treats stateConsumed as STILL IN FLIGHT
//     (it excludes only stateCancelled), and an entry in that set is suppressed
//     from redelivery.
//
// Composed, a swept entry would be stateConsumed-having-never-been-delivered:
// suppressed by the filter, never redelivered, for the life of the process. That
// cannot happen today for exactly one reason — the sweep is kindUser-only and the
// filter is kindSystem-only. THE DISJOINTNESS IS THE WHOLE SAFETY ARGUMENT, and
// before this file NOTHING IN ./internal/runtime constrained it — measured, not
// asserted: under mutation (a) below, the whole package passes except this file.
// The prose recording it lives in unified.go's SCOPE paragraph on
// InFlightSystemEntryIDs and weave_handle.go's NAMING block. Note the scoped claim:
// "no in-package guard", NOT "no guard anywhere" — internal/supervisor has two live
// ones (see ON THE NEIGHBOURING TESTS).
//
// WHY PROSE WAS NOT ENOUGH. QUM-1028 records this pair hiding a semantic conflict
// behind a CLEAN TEXTUAL MERGE twice: once at the predicate level (kindUser vs
// kindSystem sweep coverage) and once at the invariant level (stateConsumed
// meaning "delivered"). Both times each change was correct on its own base and the
// merge succeeded silently. Textual independence has twice been no evidence of
// semantic independence here.
//
// ON THE NEIGHBOURING TESTS — and a correction, recorded because the mistake is
// the same class this file is about. An earlier draft of this comment (and of
// QUM-1056) called internal/supervisor/weave_handle_test.go's
// TestWeaveRuntimeHandle_ConsumedStateStaysSuppressed "a recorded false green". THAT
// WAS WRONG, and it was wrong in the specific way this file warns about: it was an
// ANALYTIC claim about what a test would survive, asserted without running the
// mutation. Run it, and under mutation (a) TWO supervisor tests fail —
// ConsumedStateStaysSuppressed and WakeForDelivery_ConsumedButNotYetDelivered_
// NoDuplicateWrite. Its assertions are live and it kills the same mutation leg 2
// here kills.
//
// Its actual defect is narrower and is a MISLABEL, not a vacuous assertion: one
// provenance sentence in its doc comment (weave_handle_test.go:918) calls the state
// it constructs "exactly the state a settleNeverAcked sweep leaves behind". The
// sweep is kindUser-only and cannot produce that state, so the test does not speak
// for the sweep — it speaks for the filter, which it genuinely guards.
//
// Read leg 2's comment with that correction in hand: leg 2 is STRUCTURALLY THE SAME
// CONSTRUCTION as that test (echoReplay -> markConsumed -> consumed kind:system ->
// assert still in the set). The difference is not the code, it is what each claims
// about itself. Leg 2's honest value is package-local coverage of a predicate whose
// only other guards live in another package.
//
// WHICH MUTATION EACH LEG KILLS — see the recorded controls at the bottom of this
// comment; a leg whose failure nobody has watched is a claim, not a check.
//
//	(b) widen settleNeverAcked to kindSystem   -> leg 1, system_oldest arm ONLY
//	(a) narrow InFlightSystemEntryIDs to
//	    `e.state != statePending`              -> leg 2 ONLY
//	sweep never runs (vacuity)                 -> leg 1 guard, both arms
//
// (b)'s kill is SHARED, not unique: TestQUM1000_SystemMessageNeverSwept_
// NoOnDelivered also fails under it. This file's unique contribution is the
// COMPOSED consequence — it is the only place asserting what the FILTER then does
// with a swept entry.
//
// RECORDED CONTROLS (QUM-953). Each mutation was applied, WATCHED FAIL, reverted;
// the quoted text is the output as printed, not a prediction of it. Baseline
// before and after: `go test -race -count=1 -run TestQUM1056 ./internal/runtime/`
// -> ok, 1.014s. This test was GREEN when first written — it is characterization
// of an invariant that holds today, so the controls, not a red-first run, are what
// make it a check.
//
//	(b) unified.go:1114 settleNeverAcked — drop `e.kind != kindUser` from the skip:
//	    --- FAIL: .../both_directions_in_one_turn/system_oldest
//	      the sweep did not settle the kind:user entry (state=statePending,
//	      consume publishes=0, want stateConsumed/1): ... events=[ProtocolMessage
//	      UserMessageConsumed TurnCompleted]
//	    system_oldest FAILED; user_oldest PASSED. That asymmetry was observed, not
//	    assumed, and is the reason the arms exist — under (b) the widened sweep
//	    still picks the user entry when the user entry is oldest. The stray
//	    UserMessageConsumed in the event list is the sweep settling the SYSTEM
//	    entry instead: the defect, visible in the trace.
//
//	(a) unified.go:929 InFlightSystemEntryIDs — `e.state == stateCancelled`
//	    -> `e.state != statePending`:
//	    --- FAIL: .../filter_spans_consumed
//	      InFlightSystemEntryIDs dropped a stateConsumed kind:system entry (set=[])
//	    leg 2 FAILED; leg 1 both arms PASSED. The whole package was then run under
//	    the same mutation (`go test -count=1 ./internal/runtime/`, 22.240s) and
//	    THIS TEST WAS THE ONLY FAILURE — so before this file nothing in
//	    ./internal/runtime constrained that predicate at all. That measurement is
//	    the cross-package gap QUM-1056 exists to close, and it is why leg 2 is not
//	    redundant with the supervisor-side test.
//
//	vacuity: unified.go:516 routeFrame — gate the sweep call to `if false &&`:
//	    --- FAIL: BOTH arms, on the guard (1), printing "either the sweep no longer
//	      runs, OR it ran and settled a different entry", with
//	      events=[ProtocolMessage TurnCompleted] and consume publishes 0/0.
//	    The control for the guard itself: a future change that silently stops the
//	    sweep firing cannot leave assertions (2)-(6) passing on an unswept entry.
//
// EVERY ASSERTION IN LEG 1 HAS ITS OWN WATCHED CONTROL — the three above cover the
// guard and the two headline mutations, but (2)-(6) each needed one, since an
// assertion nobody has watched fail is a claim. Two further mutations, each applied
// / observed / reverted (unified.go restored byte-identical, verified with `diff -q`
// against a pre-run copy):
//
//	kind-blind sweep — drop `e.kind != kindUser` AND flip every pending kind:system
//	entry to stateConsumed (the "widened AND not oldest-only" shape):
//	    (2) test.go:207 settleNeverAcked settled a kind:system entry
//	        (state=stateConsumed kind=kindSystem, want statePending) + the full
//	        COMPOSED OUTCOME text. Fires on the user_oldest arm, where the guard
//	        does not pre-empt it.
//	  ...and with the filter ALSO narrowed to `e.state != statePending`:
//	    (3) test.go:217 kind:system entry e-sys left the in-flight set (set=[])
//	    (6) test.go:241 the in-flight set changed across a turn terminal
//	        (before=[e-sys] after=[])
//	    (6) is the composed defect actually realized — the only run in which it is.
//
//	delivering sweep — as above, plus publish EventUserMessageConsumed and call
//	OnDelivered for the swept kind:system entries:
//	    (4) test.go:224 OnDelivered fired during a sweep terminal: [[e-sys]]
//	    (5) test.go:232 EventUserMessageConsumed published 1 time(s) for the
//	        kind:system entry, want 0
//
// Note (5) is unreachable under the plain (b) mutation because guard (1) Fatals
// first; it needs a sweep that settles the system entry WITHOUT stranding the user
// one. That is why this second mutation exists rather than being folded into (b).
//
// These tests direct-drive rt.routeFrame and mutate the package global
// submittedPhaseTimeout via newQUM1000Fixture, so they must NOT be t.Parallel().

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// inFlightSorted is InFlightSystemEntryIDs as a stable slice, so a failure prints
// a diffable set rather than a map in random order.
func inFlightSorted(rt *UnifiedRuntime) []string {
	set := rt.InFlightSystemEntryIDs()
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func TestQUM1056_SweepTargetsAndInFlightFilterAreDisjoint(t *testing.T) {
	// Leg 1 — both directions through the REAL sweep, in one turn: a kind:user
	// entry it settles, and a kind:system entry it must not.
	//
	// ORDERING IS LOAD-BEARING, which is why this is a table. settleNeverAcked is
	// OLDEST-ONLY (discriminator 3), so in the user_oldest arm a kind-widened sweep
	// still picks the user entry and the arm stays green under mutation (b). Only
	// system_oldest kills (b); user_oldest is a same-outcome control pinning that
	// the CORRECT behaviour is order-independent, and it must not be credited with
	// the kill.
	t.Run("both_directions_in_one_turn", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			systemFirst bool
		}{
			{name: "system_oldest", systemFirst: true},
			{name: "user_oldest", systemFirst: false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newQUM1000Fixture(t)
				ctx := context.Background()

				var sysUUID, userUUID string
				writeSystem := func() {
					var err error
					sysUUID, err = f.rt.WriteSystemMessage(ctx, "<system-notification>agent finn: complete", "next", []string{"e-sys"})
					if err != nil {
						t.Fatalf("WriteSystemMessage: %v", err)
					}
				}
				writeUser := func() {
					userUUID = writePendingUser(t, f.rt, f.mock, "/qum1056-never-acked", "next")
				}
				if tc.systemFirst {
					writeSystem()
					writeUser()
				} else {
					writeUser()
					writeSystem()
				}

				runningTransition(f.rt)
				before := inFlightSorted(f.rt)
				drainNow(f.ch) // discard the write/turn events; we only score the terminal

				cleanTerminal(t, f.rt)
				events := drainNow(f.ch)

				// (1) VACUITY GUARD. Everything below asserts what the filter does
				// with a SWEPT entry; if nothing was swept, "still in the in-flight
				// set" passes for free. Fatal, not Error: the rest is meaningless.
				//
				// It fires for TWO structurally different faults and must name both,
				// because it pre-empts the assertion that would have diagnosed the
				// second: (i) the sweep stopped running at all, and (ii) the sweep ran
				// but settled a DIFFERENT entry — which is what a kind-widened sweep
				// does. Under (ii) the consume publish below belongs to the system
				// uuid, so the message prints which uuid each consume carried rather
				// than leaving the reader to infer it from the event list.
				gotUser := entryState(t, f.rt, userUUID)
				if n := consumedFor(events, userUUID); gotUser.state != stateConsumed || n != 1 {
					t.Fatalf("the sweep did not settle the kind:user entry (state=%s, consume publishes for user uuid=%d, want stateConsumed/1). "+
						"TWO FAULTS PRINT HERE: either the sweep no longer runs, OR it ran and settled a different entry — "+
						"if consume publishes for the SYSTEM uuid is non-zero below, it is the latter and the real defect is "+
						"domain widening (settleNeverAcked reaching kind:system), not a dead sweep. "+
						"Everything after this point asserts the filter's view of a SWEPT entry and would be vacuous either way — "+
						"\"still in the in-flight set\" passes trivially when nothing was swept. "+
						"consume publishes for system uuid=%d; events=%v",
						stateName(gotUser.state), n, consumedFor(events, sysUUID), eventNames(events))
				}

				// (2) The sweep must not have reached the kind:system entry. Only
				// `state` is asserted: `kind` is stamped at creation and nothing in
				// the tree mutates it afterwards, so a kind check here could never
				// contribute a failure. It is still PRINTED, because a surprising
				// kind is exactly what a reader needs to see on this line.
				gotSys := entryState(t, f.rt, sysUUID)
				if gotSys.state != statePending {
					t.Errorf("settleNeverAcked settled a kind:system entry (state=%s kind=%s, want statePending). "+
						"COMPOSED OUTCOME: the sweep flips state WITHOUT calling OnDelivered, so this entry is now "+
						"stateConsumed-having-never-been-delivered; InFlightSystemEntryIDs counts stateConsumed as in-flight, "+
						"so entry \"e-sys\" is suppressed from redelivery for the life of this process while still sitting in "+
						"maildir pending/. The sweep's domain (kindUser) and the filter's domain (kindSystem) must stay disjoint.",
						stateName(gotSys.state), kindName(gotSys.kind))
				}

				// (3) ...and it is therefore still visible to the redelivery filter.
				if _, ok := f.rt.InFlightSystemEntryIDs()["e-sys"]; !ok {
					t.Errorf("kind:system entry e-sys left the in-flight set across a turn terminal (set=%v). "+
						"A poke arriving now would re-drain it and write the same notification to stdin a second time.",
						inFlightSorted(f.rt))
				}

				// (4) The sweep must never durably mark anything delivered.
				if got := f.deliveries(); len(got) != 0 {
					t.Errorf("OnDelivered fired during a sweep terminal: %v, want none. "+
						"A swept entry the CLI never consumed must stay in maildir pending/ and remain re-drainable; "+
						"marking it delivered is the data-loss class QUM-1000 sidesteps by never calling OnDelivered.", got)
				}

				// (5) No consume publish for the system entry — the TUI must not
				// brighten a notification the CLI never consumed.
				if n := consumedFor(events, sysUUID); n != 0 {
					t.Errorf("EventUserMessageConsumed published %d time(s) for the kind:system entry, want 0: "+
						"that would brighten a pending-zone row whose content never reached the conversation. events=%v",
						n, eventNames(events))
				}

				// (6) THE COMPOSED ASSERTION. The sweep and the redelivery filter
				// read the same map; a turn terminal must not move an entry across
				// the filter's boundary in either direction.
				if after := inFlightSorted(f.rt); !reflect.DeepEqual(before, after) {
					t.Errorf("the in-flight set changed across a turn terminal (before=%v after=%v). "+
						"If the sweep can move an entry INTO the filter's view, a never-delivered notification is permanently "+
						"suppressed (consumed, OnDelivered never ran); if OUT of it, the next poke duplicates content already "+
						"written to stdin. Neither is recoverable within this process.", before, after)
				}
			})
		}
	})

	// Leg 2 — the filter spans stateConsumed.
	//
	// THIS LEG DOES NOT USE settleNeverAcked AND IS NOT A STAND-IN FOR WHAT THE
	// SWEEP LEAVES BEHIND. The sweep is kindUser-only and cannot produce a consumed
	// kind:system entry, so any comment here claiming to speak for the sweep would
	// be false — which is the single mislabelled sentence in
	// TestWeaveRuntimeHandle_ConsumedStateStaysSuppressed's doc comment. That test's
	// ASSERTIONS are sound and kill mutation (a) too; only its provenance sentence
	// is wrong. Do not repeat it here.
	//
	// What this leg is: a PREMISE GUARD for leg 1. Leg 1 asserts the sweep can
	// never move an entry into the filter's view — which only MATTERS because the
	// filter counts stateConsumed as in-flight. Narrow the filter to statePending
	// and the composed hazard evaporates, leg 1 stays green, and leg 1's doc
	// comment starts describing a hazard that no longer exists. This leg fails in
	// exactly that case.
	//
	// Honest scope of its value, since it is STRUCTURALLY THE SAME CONSTRUCTION as
	// the supervisor test above: it is not catching something no other test catches
	// — it is package-local coverage of a predicate whose only other guards live in
	// internal/supervisor. Measured: under mutation (a) this is the only failure in
	// ./internal/runtime, and there are two failures in ./internal/supervisor.
	//
	// It reaches kindSystem+stateConsumed through routeFrame's Replay branch ->
	// the production markConsumed, which is the only real producer of that state
	// in the tree — and which has the OPPOSITE delivery semantics from the sweep's
	// (OnDelivered fires here; the sweep's deliberately does not). The claim made
	// is only: the filter spans stateConsumed.
	t.Run("filter_spans_consumed", func(t *testing.T) {
		f := newQUM1000Fixture(t)

		sysUUID, err := f.rt.WriteSystemMessage(context.Background(), "<system-notification>agent finn: complete", "next", []string{"e-echoed"})
		if err != nil {
			t.Fatalf("WriteSystemMessage: %v", err)
		}
		runningTransition(f.rt)
		replayEcho(t, f.rt, sysUUID)

		// VACUITY GUARD. Without the echo landing, this leg would be asserting the
		// filter's view of a still-PENDING entry — which every candidate predicate
		// includes, so it would prove nothing.
		got := entryState(t, f.rt, sysUUID)
		wantDelivered := [][]string{{"e-echoed"}}
		if got.state != stateConsumed || !reflect.DeepEqual(f.deliveries(), wantDelivered) {
			t.Fatalf("the isReplay echo did not land (state=%s, OnDelivered=%v, want stateConsumed/%v): "+
				"this leg would then be asserting the filter's view of a still-pending entry, which every "+
				"predicate includes, and would prove nothing", stateName(got.state), f.deliveries(), wantDelivered)
		}

		if _, ok := f.rt.InFlightSystemEntryIDs()["e-echoed"]; !ok {
			t.Errorf("InFlightSystemEntryIDs dropped a stateConsumed kind:system entry (set=%v). Two consequences, and the "+
				"second is why this leg lives in a QUM-1056 file: (1) it reopens QUM-925's duplicate-write window — "+
				"markConsumed flips state under outMu, RELEASES it, and only then calls OnDelivered -> MarkDelivered, so a "+
				"poke inside that window re-drains an entry still in pending/ and writes it to stdin twice; (2) it dissolves "+
				"the PREMISE of leg 1 above, which asserts the sweep can never move an entry into the filter's "+
				"view — that only matters while the filter counts stateConsumed as in-flight. Narrow it here and those legs "+
				"stay green while the hazard they describe no longer exists.", inFlightSorted(f.rt))
		}
	})
}
