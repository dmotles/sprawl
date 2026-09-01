package engine

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

// Distinct fixed UUIDs rather than uuid.New(): "which type was it waiting for"
// is the property most of this file asserts, and a failure message naming
// 11111111 vs 22222222 says which one was wrong.
var (
	schemaSpawned  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	schemaResult   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	schemaTrigger  = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	schemaUnwanted = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	cardResearcher = uuid.MustParse("55555555-5555-5555-5555-555555555555")
)

// twoStepDef is the shape every advance test uses: wait for a spawn, then wait
// for a result.
func twoStepDef() Definition {
	return Definition{
		Name:               "research",
		Version:            1,
		TriggerEventSchema: schemaTrigger,
		Steps: []Step{
			{Name: "spawn", TriggerEventType: schemaSpawned, AgentCard: cardResearcher},
			{Name: "await-result", TriggerEventType: schemaResult},
		},
	}
}

func newInstance(d Definition) Instance {
	return Instance{ID: uuid.New(), DefName: d.Name, DefVersion: d.Version}
}

func eventOf(inst Instance, schema uuid.UUID) store.Event {
	return store.Event{SchemaID: schema, WorkflowInstanceID: inst.ID}
}

// ---------------------------------------------------------------------------
// The acceptance criterion: a wrong-typed event does not advance a step
// ---------------------------------------------------------------------------

// TestAdvance_WrongTypedEventDoesNotAdvance is the AC's negative half, and
// TestAdvance_PositiveControl_RightTypedEventDoesAdvance below is the other
// half. The pair is the whole point: "did not advance" is satisfied perfectly
// by an engine that advances on NOTHING, and such an engine passes the negative
// test forever while never completing a goal. Neither test means anything
// without the other, so they are written and read together.
func TestAdvance_WrongTypedEventDoesNotAdvance(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	got, adv, err := d.Advance(inst, eventOf(inst, schemaUnwanted))
	if err != nil {
		t.Fatalf("a wrong-typed event is an ordinary occurrence on a shared log, not an error: %v", err)
	}
	if adv.OK {
		t.Fatal("a wrong-typed event advanced the step")
	}
	if got.Cursor != inst.Cursor {
		t.Errorf("cursor moved from %d to %d on an event the step does not wait for", inst.Cursor, got.Cursor)
	}
	if adv.From != adv.To {
		t.Errorf("Advanced reports a move %d -> %d despite OK=false", adv.From, adv.To)
	}
	if !strings.Contains(adv.Reason, schemaSpawned.String()) || !strings.Contains(adv.Reason, schemaUnwanted.String()) {
		t.Errorf("Reason should name both the awaited and the received type so the mismatch is diagnosable, got: %q", adv.Reason)
	}
}

// TestAdvance_PositiveControl_RightTypedEventDoesAdvance is the direction check
// for the test above: it proves the subject CAN advance, so "did not advance"
// there is a decision about the event's type rather than a mechanism that never
// fires. Same definition, same instance, same call — only the schema id differs.
func TestAdvance_PositiveControl_RightTypedEventDoesAdvance(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	got, adv, err := d.Advance(inst, eventOf(inst, schemaSpawned))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !adv.OK {
		t.Fatalf("the awaited event type did not advance the step: %s", adv.Reason)
	}
	if got.Cursor != 1 {
		t.Errorf("cursor = %d after advancing step 0, want 1", got.Cursor)
	}
	if adv.Step.Name != "spawn" {
		t.Errorf("Advanced.Step = %q, want the step that was advanced past (%q)", adv.Step.Name, "spawn")
	}
	if adv.Reason != "" {
		t.Errorf("Reason should be empty on a successful advance, got %q", adv.Reason)
	}
}

// TestAdvance_TheNEXTStepsTriggerDoesNotAdvanceTheCurrentOne is the sharper
// version of the AC. A wrong type from another workflow entirely is easy to
// reject; the type this very workflow waits for at the NEXT step is the one an
// implementation that scans all steps, rather than reading the cursor, would
// wrongly accept — and it would skip a step while looking completely healthy.
func TestAdvance_TheNextStepsTriggerDoesNotAdvanceTheCurrentOne(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	got, adv, err := d.Advance(inst, eventOf(inst, schemaResult))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if adv.OK {
		t.Fatalf("step 1's trigger advanced the instance while it was still on step 0 — a step was skipped")
	}
	if got.Cursor != 0 {
		t.Errorf("cursor = %d, want 0", got.Cursor)
	}
}

// ---------------------------------------------------------------------------
// The other ways an event fails to advance
// ---------------------------------------------------------------------------

func TestAdvance_AnEventForAnotherInstanceDoesNotAdvance(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	other := newInstance(d)

	// Right type, wrong instance. The log is one global stream and two
	// concurrent research goals run the same definition, so this is the
	// everyday case rather than an exotic one.
	got, adv, err := d.Advance(inst, eventOf(other, schemaSpawned))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if adv.OK {
		t.Fatal("another instance's event advanced this instance")
	}
	if got.Cursor != 0 {
		t.Errorf("cursor = %d, want 0", got.Cursor)
	}
	if !strings.Contains(adv.Reason, other.ID.String()) {
		t.Errorf("Reason should name the foreign instance, got: %q", adv.Reason)
	}
}

func TestAdvance_GuardRejectionDoesNotAdvance(t *testing.T) {
	d := twoStepDef()
	d.Steps[0].Guard = func(store.Event) bool { return false }
	inst := newInstance(d)

	// A correctly-typed, correctly-addressed event: everything EXCEPT the guard
	// says advance. Without that, a guard that is never consulted would pass.
	got, adv, err := d.Advance(inst, eventOf(inst, schemaSpawned))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if adv.OK {
		t.Fatal("the guard returned false and the step advanced anyway")
	}
	if got.Cursor != 0 {
		t.Errorf("cursor = %d, want 0", got.Cursor)
	}
	if !strings.Contains(adv.Reason, "guard") {
		t.Errorf("Reason should name the guard, got: %q", adv.Reason)
	}
}

func TestAdvance_GuardIsGivenTheEventAndItsTruthAdvances(t *testing.T) {
	d := twoStepDef()
	var seen []uuid.UUID
	d.Steps[0].Guard = func(ev store.Event) bool {
		seen = append(seen, ev.SchemaID)
		return true
	}
	inst := newInstance(d)

	_, adv, err := d.Advance(inst, eventOf(inst, schemaSpawned))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if !adv.OK {
		t.Fatalf("a guard returning true blocked the advance: %s", adv.Reason)
	}
	if len(seen) != 1 || seen[0] != schemaSpawned {
		t.Errorf("guard saw %v, want exactly one call carrying the triggering event", seen)
	}
}

// TestAdvance_AGuardIsNotConsultedForAWrongTypedEvent pins the ordering. A
// guard is arbitrary caller code; running it against every event on the log
// makes it a hot path and, worse, invites guards that compensate for a type
// check the engine should have done.
func TestAdvance_AGuardIsNotConsultedForAWrongTypedEvent(t *testing.T) {
	d := twoStepDef()
	calls := 0
	d.Steps[0].Guard = func(store.Event) bool { calls++; return true }
	inst := newInstance(d)

	if _, adv, _ := d.Advance(inst, eventOf(inst, schemaUnwanted)); adv.OK {
		t.Fatal("a wrong-typed event advanced")
	}
	if calls != 0 {
		t.Errorf("the guard ran %d time(s) for an event of the wrong type", calls)
	}
	// Positive control: the same guard IS consulted when the type matches, so
	// "0 calls" above is about the type check and not about a guard that is
	// never invoked at all.
	if _, adv, _ := d.Advance(inst, eventOf(inst, schemaSpawned)); !adv.OK {
		t.Fatalf("control: the awaited type did not advance: %s", adv.Reason)
	}
	if calls != 1 {
		t.Errorf("control: the guard ran %d time(s) for the awaited type, want 1", calls)
	}
}

// ---------------------------------------------------------------------------
// Completion and the version pin
// ---------------------------------------------------------------------------

func TestAdvance_RunsEveryStepInOrderAndThenReportsDone(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	inst, adv, err := d.Advance(inst, eventOf(inst, schemaSpawned))
	if err != nil || !adv.OK {
		t.Fatalf("step 0: err=%v reason=%q", err, adv.Reason)
	}
	if inst.Done(d) {
		t.Fatal("the instance reported done with a step still outstanding")
	}
	inst, adv, err = d.Advance(inst, eventOf(inst, schemaResult))
	if err != nil || !adv.OK {
		t.Fatalf("step 1: err=%v reason=%q", err, adv.Reason)
	}
	if adv.Step.Name != "await-result" {
		t.Errorf("Advanced.Step = %q, want %q", adv.Step.Name, "await-result")
	}
	if !inst.Done(d) {
		t.Errorf("cursor = %d after both steps advanced, want the instance done", inst.Cursor)
	}
}

// TestAdvance_ACompletedInstanceDoesNotAdvanceAgain matters because closes are
// final under the goal semantics: a duplicate result event replayed by the
// dispatcher must not push a finished instance past the end of its steps.
func TestAdvance_ACompletedInstanceDoesNotAdvanceAgain(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	inst.Cursor = len(d.Steps)

	got, adv, err := d.Advance(inst, eventOf(inst, schemaResult))
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if adv.OK {
		t.Fatal("a completed instance advanced again")
	}
	if got.Cursor != len(d.Steps) {
		t.Errorf("cursor = %d, want it pinned at %d", got.Cursor, len(d.Steps))
	}
}

// TestAdvance_AMismatchedVersionPinIsAnError is the one not-advancing case that
// IS an error, and the distinction is deliberate: a wrong-typed event is normal
// traffic, whereas advancing an instance against a definition it did not start
// under means the caller resolved the wrong definition, and silently declining
// would leave that goal wedged with no diagnostic.
func TestAdvance_AMismatchedVersionPinIsAnError(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	inst.DefVersion = 2

	if _, _, err := d.Advance(inst, eventOf(inst, schemaSpawned)); err == nil {
		t.Fatal("advancing an instance pinned to v2 against the v1 definition was accepted")
	}

	// Positive control: the identical call with the pin restored succeeds, so
	// the error above is about the version and not about the fixture.
	inst.DefVersion = 1
	if _, adv, err := d.Advance(inst, eventOf(inst, schemaSpawned)); err != nil || !adv.OK {
		t.Fatalf("control: the matching pin did not advance: err=%v reason=%q", err, adv.Reason)
	}
}

func TestAdvance_AMismatchedNamePinIsAnError(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	inst.DefName = "bug-investigation"

	if _, _, err := d.Advance(inst, eventOf(inst, schemaSpawned)); err == nil {
		t.Fatal("advancing an instance pinned to another workflow was accepted")
	}
}

// ---------------------------------------------------------------------------
// Validate — every leg with a defect actually present
// ---------------------------------------------------------------------------

// TestValidate_AcceptsAWellFormedDefinition is the negative control for the
// whole table below: it proves Validate can return nil, so each rejection there
// is about the defect injected and not about a validator that refuses
// everything.
func TestValidate_AcceptsAWellFormedDefinition(t *testing.T) {
	if err := twoStepDef().Validate(); err != nil {
		t.Fatalf("a well-formed definition was rejected: %v", err)
	}
}

func TestValidate_RejectsEachDefect(t *testing.T) {
	// Each case mutates one field of an otherwise-valid definition, so the
	// rejection is attributable to that field. `want` is a substring the
	// message must carry, which also pins that the diagnostic names the defect
	// rather than saying "invalid".
	cases := []struct {
		name         string
		injectDefect func(*Definition)
		want         string
	}{
		{name: "no name", injectDefect: func(d *Definition) { d.Name = "" }, want: "no name"},
		{name: "zero version", injectDefect: func(d *Definition) { d.Version = 0 }, want: "version"},
		{name: "negative version", injectDefect: func(d *Definition) { d.Version = -1 }, want: "version"},
		{name: "no trigger schema", injectDefect: func(d *Definition) { d.TriggerEventSchema = uuid.Nil }, want: "trigger event schema"},
		{name: "no steps", injectDefect: func(d *Definition) { d.Steps = nil }, want: "no steps"},
		{name: "unnamed step", injectDefect: func(d *Definition) { d.Steps[1].Name = "" }, want: "step 1 has no name"},
		{name: "duplicate step names", injectDefect: func(d *Definition) { d.Steps[1].Name = d.Steps[0].Name }, want: "two steps named"},
		{name: "step with no trigger", injectDefect: func(d *Definition) { d.Steps[1].TriggerEventType = uuid.Nil }, want: "no trigger event type"},
		{name: "negative retries", injectDefect: func(d *Definition) { d.Steps[0].Outcome.MaxRetries = -1 }, want: "MaxRetries"},
		{name: "backtrack with no target", injectDefect: func(d *Definition) {
			d.Steps[1].Outcome = OutcomePolicy{OnFailure: ActionBacktrack}
		}, want: "names no target"},
		{name: "backtrack to a non-existent step", injectDefect: func(d *Definition) {
			d.Steps[1].Outcome = OutcomePolicy{OnFailure: ActionBacktrack, BacktrackTo: "nowhere"}
		}, want: "not a step in this workflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := twoStepDef()
			tc.injectDefect(&d)
			err := d.Validate()
			if err == nil {
				t.Fatalf("Validate accepted a definition with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// TestValidate_AcceptsABacktrackToALaterStep pins the two-pass check. A
// single-pass validator that resolves targets as it walks would reject this,
// and a retry loop that rewinds forward is a legitimate shape.
func TestValidate_AcceptsABacktrackToALaterStep(t *testing.T) {
	d := twoStepDef()
	d.Steps[0].Outcome = OutcomePolicy{OnFailure: ActionBacktrack, BacktrackTo: "await-result"}
	if err := d.Validate(); err != nil {
		t.Fatalf("a backtrack to a step declared later was rejected: %v", err)
	}
}

// TestValidate_IgnoresABacktrackTargetOnANonBacktrackingPolicy keeps the check
// pointed at what it means: BacktrackTo is meaningless unless OnFailure is
// ActionBacktrack, so a stale target on a retrying step must not fail a load.
func TestValidate_IgnoresABacktrackTargetOnANonBacktrackingPolicy(t *testing.T) {
	d := twoStepDef()
	d.Steps[0].Outcome = OutcomePolicy{OnFailure: ActionRetry, MaxRetries: 2, BacktrackTo: "nowhere"}
	if err := d.Validate(); err != nil {
		t.Fatalf("an unused BacktrackTo failed the load: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Registry — the version pin, operationally
// ---------------------------------------------------------------------------

func TestRegistry_LookupResolvesTheExactPinAndNeverAnotherVersion(t *testing.T) {
	var r Registry
	v1 := twoStepDef()
	v2 := twoStepDef()
	v2.Version = 2
	v2.Steps = v2.Steps[:1] // v2 is a genuinely different shape
	for _, d := range []Definition{v1, v2} {
		if err := r.Register(d); err != nil {
			t.Fatalf("Register %s@%d: %v", d.Name, d.Version, err)
		}
	}

	got, err := r.Lookup("research", 1)
	if err != nil {
		t.Fatalf("Lookup v1: %v", err)
	}
	if len(got.Steps) != 2 {
		t.Errorf("Lookup(research, 1) returned a %d-step definition — v1 has 2 and v2 has 1, so this resolved the wrong version", len(got.Steps))
	}

	// The pin never falls back. An instance started under v3 must fail loudly
	// rather than run v2's steps.
	if _, err := r.Lookup("research", 3); err == nil {
		t.Error("Lookup of an unregistered version fell back to another version instead of erroring")
	}
	if _, err := r.Lookup("nonexistent", 1); err == nil {
		t.Error("Lookup of an unknown workflow name succeeded")
	}
}

func TestRegistry_RegisterRefusesADuplicatePinAndValidatesFirst(t *testing.T) {
	var r Registry
	if err := r.Register(twoStepDef()); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(twoStepDef()); err == nil {
		t.Error("re-registering research@1 silently replaced the definition an in-flight instance resolves through")
	}

	bad := twoStepDef()
	bad.Name = "other"
	bad.Steps[0].TriggerEventType = uuid.Nil
	if err := r.Register(bad); err == nil {
		t.Error("Register accepted a definition that Validate rejects")
	}
	if _, err := r.Lookup("other", 1); err == nil {
		t.Error("a definition that failed validation was filed in the registry anyway")
	}
}

// TestRegistry_LookupOnAZeroRegistryDoesNotPanic pins the nil-map read. Lookup
// indexes a map of maps, and the outer read on a nil map is only safe because
// Go defines it to be — worth an assertion rather than a comment, since a
// Registry is usable before anything is registered.
func TestRegistry_LookupOnAZeroRegistryDoesNotPanic(t *testing.T) {
	var r Registry
	if _, err := r.Lookup("research", 1); err == nil {
		t.Error("an empty registry resolved a definition")
	}
}

// ---------------------------------------------------------------------------
// Replay — the cursor is DERIVED from the log, never stored
// ---------------------------------------------------------------------------

// The instance cursor is not persisted anywhere, and that is a design position
// rather than an omission: `workflow_instances` has no cursor column, because
// under the v2 plan of record the LOG IS THE STATE. A stored cursor is a second
// source of truth that can disagree with the log, and the disagreement is
// silent — an instance whose cursor says step 3 while its log shows two events
// either re-runs a step or skips one, and nothing reports which.
//
// Deriving it makes crash-resume fall out for free: there is no checkpoint to
// lose, so a process that dies mid-goal recovers by reading what already
// happened.

// TestReplay_DerivesTheCursorFromTheLog is the base case.
func TestReplay_DerivesTheCursorFromTheLog(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	got, err := d.Replay(inst, []store.Event{eventOf(inst, schemaSpawned)})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Cursor != 1 {
		t.Errorf("after one advancing event the derived cursor is %d, want 1", got.Cursor)
	}
	if got.Done(d) {
		t.Error("the instance reports Done with one of two steps advanced")
	}
}

// TestReplay_OfAnEmptyLogIsTheStartingInstance. An instance that has produced
// no events is at step 0 — and this is the case a stored cursor gets wrong
// after a crash between "row inserted" and "first event appended".
func TestReplay_OfAnEmptyLogIsTheStartingInstance(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	got, err := d.Replay(inst, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Cursor != 0 {
		t.Errorf("an instance with no events derived cursor %d, want 0", got.Cursor)
	}
}

// TestReplay_IgnoresEventsThatDoNotAdvance is the load-bearing one.
//
// The log is a single global stream and an instance's slice of it contains
// plenty that advances nothing: telemetry, a notification, an event belonging
// to another instance that a caller's query was sloppy about. A Replay that
// counted events instead of folding Advance over them would over-advance the
// cursor and skip steps, and it would pass a test that only ever fed it a
// perfect log.
func TestReplay_IgnoresEventsThatDoNotAdvance(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	other := newInstance(d)

	got, err := d.Replay(inst, []store.Event{
		eventOf(inst, schemaTrigger),  // the trigger, not a step trigger
		eventOf(other, schemaSpawned), // right type, WRONG instance
		eventOf(inst, schemaResult),   // step 2's trigger, arriving while at step 1
		eventOf(inst, schemaSpawned),  // the one that actually advances
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got.Cursor != 1 {
		t.Errorf("derived cursor %d, want 1 — Replay advanced on events that do not advance this instance's current step", got.Cursor)
	}
}

// TestReplay_IsOrderSensitive pins that Replay folds in the order given.
//
// Its NEGATIVE CONTROL is the in-order case, asserted in the same test: the
// same four events in log order complete the instance, and out of order they
// do not. Without both halves, "order matters" is satisfied by an
// implementation that never advances at all.
func TestReplay_IsOrderSensitive(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)

	inOrder := []store.Event{eventOf(inst, schemaSpawned), eventOf(inst, schemaResult)}
	got, err := d.Replay(inst, inOrder)
	if err != nil {
		t.Fatalf("Replay in order: %v", err)
	}
	if !got.Done(d) {
		t.Fatalf("the in-order log left the instance at cursor %d, want it complete — this is the control, and the assertion below means nothing without it", got.Cursor)
	}

	reversed := []store.Event{eventOf(inst, schemaResult), eventOf(inst, schemaSpawned)}
	got, err = d.Replay(inst, reversed)
	if err != nil {
		t.Fatalf("Replay reversed: %v", err)
	}
	if got.Done(d) {
		t.Error("a reversed log completed the instance, so steps are being matched out of order and step 2 could run before step 1")
	}
	if got.Cursor != 1 {
		t.Errorf("the reversed log derived cursor %d, want 1 (only the spawn matched, and only once it was reached)", got.Cursor)
	}
}

// TestReplay_IsIdempotentAcrossAPrefix is the crash-resume property stated
// directly: replaying a prefix and then the remainder must land where replaying
// the whole log lands. If it did not, a process that crashed halfway would
// resume at a different step than one that never crashed.
func TestReplay_IsIdempotentAcrossAPrefix(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	log := []store.Event{eventOf(inst, schemaSpawned), eventOf(inst, schemaResult)}

	whole, err := d.Replay(inst, log)
	if err != nil {
		t.Fatalf("Replay whole: %v", err)
	}
	part, err := d.Replay(inst, log[:1])
	if err != nil {
		t.Fatalf("Replay prefix: %v", err)
	}
	resumed, err := d.Replay(part, log[1:])
	if err != nil {
		t.Fatalf("Replay remainder: %v", err)
	}
	if resumed.Cursor != whole.Cursor {
		t.Errorf("resuming from a prefix landed at cursor %d but replaying the whole log lands at %d — a crash mid-goal would resume at the wrong step", resumed.Cursor, whole.Cursor)
	}
}

// TestReplay_PropagatesAPinMismatch. Replay folds Advance, which refuses an
// instance pinned to another definition; swallowing that would silently derive
// a cursor for the wrong workflow.
func TestReplay_PropagatesAPinMismatch(t *testing.T) {
	d := twoStepDef()
	inst := newInstance(d)
	inst.DefVersion = 2

	if _, err := d.Replay(inst, []store.Event{eventOf(inst, schemaSpawned)}); err == nil {
		t.Error("Replay derived a cursor for an instance pinned to a different definition version")
	}
}
