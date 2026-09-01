package engine

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A definition whose three steps carry one of each interesting policy, so a
// Recover test can pick a cursor rather than build a bespoke definition per
// case. Step triggers are distinct because Validate demands non-Nil, not
// because Recover reads them — Recover never looks at an event.
func recoverTestDef(t *testing.T) Definition {
	t.Helper()
	d := Definition{
		Name:               "recover-fixture",
		Version:            1,
		TriggerEventSchema: uuid.New(),
		Steps: []Step{
			{
				Name:             "first",
				TriggerEventType: uuid.New(),
				Outcome:          OutcomePolicy{OnFailure: ActionRetry, MaxRetries: 2},
			},
			{
				Name:             "second",
				TriggerEventType: uuid.New(),
				Outcome:          OutcomePolicy{OnFailure: ActionBacktrack, BacktrackTo: "first"},
				ReEngagement:     DiscardAndRedo,
			},
			{
				Name:             "third",
				TriggerEventType: uuid.New(),
				Outcome:          OutcomePolicy{OnFailure: ActionEscalate},
			},
			// "fourth" backtracks ACROSS several steps on purpose. A target one
			// step back is also what a plain cursor-- produces, so a fixture
			// that only ever backtracks by one cannot tell the two apart —
			// measured: the decrement mutation left the 1->0 case green.
			{
				Name:             "fourth",
				TriggerEventType: uuid.New(),
				Outcome:          OutcomePolicy{OnFailure: ActionBacktrack, BacktrackTo: "first"},
			},
		},
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("the recover fixture does not validate: %v", err)
	}
	return d
}

func recoverTestInstance(d Definition, cursor int) Instance {
	return Instance{ID: uuid.New(), DefName: d.Name, DefVersion: d.Version, Cursor: cursor}
}

// TestRecover_RetriesWithinTheCapAndLeavesTheCursorWhereItIs.
//
// A retry must NOT move the cursor: the step has not been passed, it is being
// run again. A Recover that advanced here would skip the failed step entirely
// and report the goal complete having never done its work.
func TestRecover_RetriesWithinTheCapAndLeavesTheCursorWhereItIs(t *testing.T) {
	d := recoverTestDef(t)
	inst := recoverTestInstance(d, 0)
	for failures := 1; failures <= 2; failures++ {
		got, err := d.Recover(inst, failures)
		if err != nil {
			t.Fatalf("Recover(failure %d): %v", failures, err)
		}
		if got.Action != ActionRetry {
			t.Errorf("failure %d of a MaxRetries=2 step gave %s, want retry", failures, got.Action)
		}
		if got.Instance.Cursor != 0 {
			t.Errorf("failure %d moved the cursor to %d; a retry re-runs the step, it does not pass it", failures, got.Instance.Cursor)
		}
	}
}

// TestRecover_EscalatesOnceTheRetryCapIsExhausted is the assertion that keeps a
// failing step from looping forever. MaxRetries=2 means the step may be re-run
// twice, so the THIRD failure is past the cap.
func TestRecover_EscalatesOnceTheRetryCapIsExhausted(t *testing.T) {
	d := recoverTestDef(t)
	got, err := d.Recover(recoverTestInstance(d, 0), 3)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got.Action != ActionEscalate {
		t.Fatalf("the 3rd failure of a MaxRetries=2 step gave %s, want escalate — a retry here is an infinite loop", got.Action)
	}
	if !strings.Contains(got.Reason, "first") {
		t.Errorf("the reason should name the exhausted step; got %q", got.Reason)
	}
}

// TestRecover_BacktracksToTheNamedStep. The cursor must rewind to the TARGET's
// index, not merely decrement.
//
// It recovers the step at index 3, whose target is index 0, precisely so a
// decrement (which would give 2) is distinguishable from a seek. The adjacent
// case cannot make that distinction, and relying on it left a real decrement
// mutation undetected here.
func TestRecover_BacktracksToTheNamedStep(t *testing.T) {
	d := recoverTestDef(t)
	got, err := d.Recover(recoverTestInstance(d, 3), 1)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got.Action != ActionBacktrack {
		t.Fatalf("got %s, want backtrack", got.Action)
	}
	if got.Instance.Cursor != 0 {
		t.Errorf("backtrack to %q left the cursor at %d, want 0 (that step's index); 2 would mean it decremented rather than sought the target", d.Steps[3].Outcome.BacktrackTo, got.Instance.Cursor)
	}
}

// TestRecover_CoversBothReEngagementPolicies.
//
// The two policies must be DISTINGUISHABLE in the verdict, or the interpreter
// cannot act on them and the field is decoration. The pairing is the whole
// test: asserting DiscardAndRedo sets SpawnFresh means nothing unless
// ReEngageOriginal is shown to leave it clear on the same code path.
func TestRecover_CoversBothReEngagementPolicies(t *testing.T) {
	d := recoverTestDef(t)

	// DiscardAndRedo — the "second" step. A fresh agent, because a wrong result
	// is rarely fixed by handing it back to whoever produced it.
	discard, err := d.Recover(recoverTestInstance(d, 1), 1)
	if err != nil {
		t.Fatalf("Recover(discard-and-redo): %v", err)
	}
	if !discard.SpawnFresh {
		t.Error("a DiscardAndRedo step recovered with SpawnFresh=false, so the interpreter would re-engage the agent whose work was just discarded")
	}
	if discard.ReEngagement != DiscardAndRedo {
		t.Errorf("verdict carries %s, want discard-and-redo", discard.ReEngagement)
	}

	// ReEngageOriginal — the "first" step (zero value). The paired control: if
	// SpawnFresh were hard-coded true, this half fails.
	reengage, err := d.Recover(recoverTestInstance(d, 0), 1)
	if err != nil {
		t.Fatalf("Recover(re-engage-original): %v", err)
	}
	if reengage.SpawnFresh {
		t.Error("a ReEngageOriginal step recovered with SpawnFresh=true, discarding context the policy exists to preserve")
	}
	if reengage.ReEngagement != ReEngageOriginal {
		t.Errorf("verdict carries %s, want re-engage-original", reengage.ReEngagement)
	}
}

// TestRecover_EscalateAndAbortLeaveTheCursorAlone. Neither ends by passing the
// step: an escalated goal is waiting on its owner and an aborted one is over,
// and a cursor bumped by either would make a later Replay resume mid-workflow.
func TestRecover_EscalateAndAbortLeaveTheCursorAlone(t *testing.T) {
	d := recoverTestDef(t)
	got, err := d.Recover(recoverTestInstance(d, 2), 1)
	if err != nil {
		t.Fatalf("Recover(escalate): %v", err)
	}
	if got.Action != ActionEscalate || got.Instance.Cursor != 2 {
		t.Errorf("escalate gave %s at cursor %d, want escalate at 2", got.Action, got.Instance.Cursor)
	}

	d.Steps[2].Outcome = OutcomePolicy{OnFailure: ActionAbort}
	got, err = d.Recover(recoverTestInstance(d, 2), 1)
	if err != nil {
		t.Fatalf("Recover(abort): %v", err)
	}
	if got.Action != ActionAbort || got.Instance.Cursor != 2 {
		t.Errorf("abort gave %s at cursor %d, want abort at 2", got.Action, got.Instance.Cursor)
	}
}

// TestRecover_RefusesAFailureCountBelowOne. Recover handles a failure that has
// already happened, so zero is not a smaller number of failures — it is a
// caller that has lost track. Under a silent zero the retry cap would be
// compared against a count that never grows, which is the infinite loop the cap
// exists to prevent.
func TestRecover_RefusesAFailureCountBelowOne(t *testing.T) {
	d := recoverTestDef(t)
	if _, err := d.Recover(recoverTestInstance(d, 0), 0); err == nil {
		t.Error("Recover accepted a failure count of 0; the cap would then never be reached")
	}
}

// TestRecover_RefusesACompleteInstance. A step that failed cannot be the step
// after the last one; reaching here means the caller matched an outcome to the
// wrong instance, and inventing a recovery would act on that mistake.
func TestRecover_RefusesACompleteInstance(t *testing.T) {
	d := recoverTestDef(t)
	if _, err := d.Recover(recoverTestInstance(d, len(d.Steps)), 1); err == nil {
		t.Error("Recover produced a verdict for a complete instance, which has no failing step")
	}
}

// TestRecover_RefusesAPinMismatch — same reasoning as Advance's: resolving a
// recovery against the wrong definition would rewind to whatever step happens
// to share the index.
func TestRecover_RefusesAPinMismatch(t *testing.T) {
	d := recoverTestDef(t)
	inst := recoverTestInstance(d, 0)
	inst.DefVersion = 2
	if _, err := d.Recover(inst, 1); err == nil {
		t.Error("Recover accepted an instance pinned to a different definition version")
	}
}
