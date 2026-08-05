// QUM-1068 AC 2, answered with assertions rather than prose: an echoed
// now-priority write does not leave the spinner lit after its turn ends.
//
// WHERE THIS TEST HAD TO LIVE, and why the obvious placement is wrong. The
// tempting shape is "deliver two UserMessageConsumedMsg for one uuid to the
// reducer, assert the spinner is dark". That test is RED both before AND after
// QUM-1068, because QUM-1068 stops the second message from being PRODUCED, not
// from being HANDLED. The reducer's UserMessageConsumedMsg case is unchanged and
// remains non-idempotent — its `if m.turnState == TurnIdle {
// setTurnState(TurnThinking) }` (QUM-831) fires on any consume, settled or not.
// Making that test pass would mean guarding the reducer, a different change than
// the one QUM-1068 authorises.
//
// HOW THE CLAIM IS ESTABLISHED. The path from a now-write to a lit spinner has
// three links, and each is asserted separately because no single test can span
// them: internal/runtime cannot import internal/tui (internal/tui imports
// internal/runtime, so it would be an import cycle), and building a real
// UnifiedRuntime here would mean duplicating internal/runtime's backend.Session
// fake (12 methods) inside this package. The composition is the argument:
//
//	L1  the runtime publishes EXACTLY ONE EventUserMessageConsumed per uuid,
//	    across both settle paths and both orders
//	      → internal/runtime/qum1068_markconsumed_idempotent_test.go
//	L2  TranslateRuntimeEvent maps consume events 1:1, preserving uuid
//	      → TestQUM1068_Translate_ConsumeIsOneToOne, below
//	L3  one consume followed by the turn's terminal renders a DARK spinner
//	      → TestQUM1068_Spinner_DarkAfterSingleConsume, below
//
// HONEST LIMIT OF L2, and the hop none of this covers. TranslateRuntimeEvent
// returns a single tea.Msg, so it CANNOT amplify one event into two — L2 can only
// detect dropping, whose symptom (a spinner that never lights) is the opposite of
// the defect. It is therefore a mapping check, not a guard against re-duplication.
// The layer where one-event-to-two-msgs is actually expressible is the pump
// adapter between L1 and L2 — internal/tuiruntime/tuiadapter.go's WaitForEvent,
// including its pendingMsg stash — and that hop is NOT asserted here. If a
// duplicate consume ever reappears at the reducer with L1 still green, the adapter
// is where to look first.
//
// The reducer's surviving non-idempotence is pinned separately as an explicit
// characterisation test, so the residual is on the record rather than implied by
// this file's silence.
//
// MUTATION LOG. Neither test here is red-first — both assert properties that
// already held before QUM-1068, because the defect they guard against lives
// upstream in the runtime (L1). Per QUM-953 they still have to demonstrate they
// CAN fail, so each was watched under a mutation of the link it measures:
//
//	M4  make TranslateRuntimeEvent return nil for EventUserMessageConsumed
//	    (i.e. break L2's 1:1 mapping by dropping instead of amplifying — the
//	    reachable direction, since the switch returns one msg per call).
//	    → Translate_ConsumeIsOneToOne FAILED: "TranslateRuntimeEvent produced 0
//	      UserMessageConsumedMsg from 2 consume events, want 2".
//	M5  comment out finalizeTurn's `m.setTurnState(TurnIdle)`, i.e. make the
//	    turn's terminal stop returning to idle.
//	    → Spinner_DarkAfterSingleConsume FAILED: `spinner still lit after the turn
//	      ended; want cleared, got "✶ running…"` — the exact user-visible symptom
//	      this AC is about, reached by a different cause.
//	    → the characterisation test also FAILED, at its precondition, which is the
//	      correct behaviour for a test whose baseline assumption no longer holds.
package tui

import (
	"testing"

	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
)

// TestQUM1068_Translate_ConsumeIsOneToOne is L2. It pins that the runtime's
// consume-event count is preserved exactly across translation, which is what lets
// L1's "exactly one publish" and L3's "one msg ⇒ dark spinner" compose into AC 2.
func TestQUM1068_Translate_ConsumeIsOneToOne(t *testing.T) {
	events := []sprawlrt.RuntimeEvent{
		{Type: sprawlrt.EventUserMessageConsumed, UUID: "u1"},
		{Type: sprawlrt.EventUserMessageConsumed, UUID: "u2"},
	}

	var consumes []UserMessageConsumedMsg
	for _, ev := range events {
		if msg, ok := TranslateRuntimeEvent(ev, nil).(UserMessageConsumedMsg); ok {
			consumes = append(consumes, msg)
		}
	}

	if len(consumes) != 2 {
		t.Fatalf("TranslateRuntimeEvent produced %d UserMessageConsumedMsg from 2 consume events, want 2 — translation must be 1:1, or the runtime's exactly-once guarantee (QUM-1068 L1) does not reach the reducer", len(consumes))
	}
	if consumes[0].UUID != "u1" || consumes[1].UUID != "u2" {
		t.Fatalf("translated uuids = %q, %q; want u1, u2 — the reducer keys ZoneSettle on this uuid", consumes[0].UUID, consumes[1].UUID)
	}

	// NEGATIVE CONTROL: a non-consume event must NOT become a consume msg, so the
	// count above is a count of consumes and not of events in general.
	cancelled := TranslateRuntimeEvent(sprawlrt.RuntimeEvent{Type: sprawlrt.EventUserMessageCancelled, UUID: "u3"}, nil)
	if _, isConsume := cancelled.(UserMessageConsumedMsg); isConsume {
		t.Fatal("a cancelled event translated to UserMessageConsumedMsg — the L2 counter is counting the wrong population")
	}
}

// TestQUM1068_Spinner_DarkAfterSingleConsume is L3, and the AC-2 assertion proper:
// it reads the RENDERED spinner, not turnState.
//
// This is the shape an echoed now-write produces post-fix — one consume for the
// uuid, then the turn's terminal. Pre-fix the runtime published a second consume
// that landed AFTER finalizeTurn, flipping TurnIdle → TurnThinking with no turn in
// flight and no route back to idle (finalizeTurn already ran; the only other exits
// are a session restart or the QUM-669 gap/resync path).
func TestQUM1068_Spinner_DarkAfterSingleConsume(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "urgent"})

	// [I] INSTRUMENT LIVE: the consume really does light the spinner, so the dark
	// reading after the terminal is the terminal's doing and not a spinner that
	// never lights in this harness. (Mirrors TestSpinner_LitOnTailConsumeBeforeContent.)
	lit := deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	if lit.sparkleRow(true) == "" {
		t.Fatal("[I] spinner is dark immediately after the consume — this harness never lights it, so the assertion below would pass vacuously")
	}

	app = deliver(t, lit, SessionResultMsg{}) // finalizeTurn → TurnIdle

	if got := app.sparkleRow(true); got != "" {
		t.Fatalf("spinner still lit after the turn ended; want cleared, got %q — the user is left watching a spinner with nothing running", got)
	}
}

// TestQUM1068_Spinner_ReducerIsStillNonIdempotent_Characterisation records what
// QUM-1068 did NOT change, so the tests above are not mistaken for a reducer-level
// guarantee.
//
// The QUM-831 re-arm is unconditional on whether the uuid was already settled. If a
// second consume for one uuid ever reaches the reducer after finalizeTurn — from
// any future producer — the spinner relights permanently. QUM-1068 removes the one
// producer that did this (an echoed now-write) and makes exactly-one-per-uuid a
// RUNTIME invariant; it does not make the reducer defensive. That is the whole
// reason L1 above has to hold.
//
// This asserts the CURRENT behaviour deliberately. If someone later guards the
// reducer, this test SHOULD fail — a decision to record, not a regression — and the
// failure message says so.
func TestQUM1068_Spinner_ReducerIsStillNonIdempotent_Characterisation(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	app = deliver(t, app, SessionResultMsg{}) // finalizeTurn → TurnIdle

	if got := app.sparkleRow(true); got != "" {
		t.Fatalf("precondition: spinner should be dark at true idle, got %q", got)
	}

	// A SECOND consume for the same, already-settled uuid, after the turn ended.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})

	if app.sparkleRow(true) == "" {
		t.Fatal("the reducer is now idempotent for a repeated consume.\n" +
			"  This test asserted the RESIDUAL that QUM-1068 deliberately left in place: the fix\n" +
			"  removed the duplicate PRODUCER (markConsumed) and did not guard the reducer.\n" +
			"  If you just added that guard, this failure is the intended outcome — delete this\n" +
			"  test and say so in the commit, because the upstream invariant is then belt-and-braces\n" +
			"  rather than the only thing standing between a stray consume and a stuck spinner.")
	}
}
