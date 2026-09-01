package engine

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

func mustBugDef(t *testing.T) Definition {
	t.Helper()
	d, err := BugInvestigationDefinition(researchTestRegistry(t), uuid.New())
	if err != nil {
		t.Fatalf("BugInvestigationDefinition: %v", err)
	}
	return d
}

func TestBugInvestigationDefinition_IsValid(t *testing.T) {
	if err := mustBugDef(t).Validate(); err != nil {
		t.Errorf("the BUG_INVESTIGATION definition does not validate: %v", err)
	}
}

// TestBugInvestigationDefinition_RefusesAZeroCardAndAMissingSchema — same two
// construction-time failures as RESEARCH, for the same reason: both produce a
// goal that hangs forever with nothing logged, and both are cheap to catch at
// startup where a definition is built once.
func TestBugInvestigationDefinition_RefusesAZeroCardAndAMissingSchema(t *testing.T) {
	if _, err := BugInvestigationDefinition(researchTestRegistry(t), uuid.Nil); err == nil {
		t.Error("accepted uuid.Nil for the investigator card, which would spawn nobody")
	}
	if _, err := BugInvestigationDefinition(&store.Registry{}, uuid.New()); err == nil {
		t.Error("built a definition from an empty registry; every trigger would be uuid.Nil")
	}
}

// TestBugInvestigationDefinition_ReworkBacktracksToTheInvestigation is the AC:
// a stubbed failure on the verification step drives the REWORK path.
//
// The paired control is the happy half — the same step, recovered from NO
// failure, is not reachable at all, so the test instead pairs the backtrack
// against a Replay that completes without one. A definition whose verify step
// escalated instead of backtracking passes every happy-path test and fails only
// here.
func TestBugInvestigationDefinition_ReworkBacktracksToTheInvestigation(t *testing.T) {
	reg := researchTestRegistry(t)
	d, err := BugInvestigationDefinition(reg, uuid.New())
	if err != nil {
		t.Fatalf("BugInvestigationDefinition: %v", err)
	}

	verify := -1
	for i, s := range d.Steps {
		if s.Outcome.OnFailure == ActionBacktrack {
			verify = i
			break
		}
	}
	if verify < 0 {
		t.Fatal("no step in BUG_INVESTIGATION backtracks, so there is no REWORK path at all")
	}

	inst := Instance{ID: uuid.New(), DefName: d.Name, DefVersion: d.Version, Cursor: verify}
	rec, err := d.Recover(inst, 1)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rec.Action != ActionBacktrack {
		t.Fatalf("a failed verification gave %s, want backtrack", rec.Action)
	}
	target := d.Steps[rec.Instance.Cursor]
	if target.AgentCard == uuid.Nil {
		t.Errorf("REWORK rewound to %q, which spawns nobody; the rework must land on a step that actually redoes the work", target.Name)
	}
	if !rec.SpawnFresh {
		t.Error("REWORK re-engaged the original investigator; a diagnosis that failed verification is not fixed by asking its author to check again")
	}
}

// TestBugInvestigationDefinition_ReplaysAHappyPathToCompletion. The negative
// control is the second half: a run whose verification never arrives must not
// report complete, which is what catches a definition missing the verify step.
func TestBugInvestigationDefinition_ReplaysAHappyPathToCompletion(t *testing.T) {
	reg := researchTestRegistry(t)
	d, err := BugInvestigationDefinition(reg, uuid.New())
	if err != nil {
		t.Fatalf("BugInvestigationDefinition: %v", err)
	}
	inst := Instance{ID: uuid.New(), DefName: d.Name, DefVersion: d.Version}

	full := make([]store.Event, 0, len(d.Steps))
	for _, s := range d.Steps {
		full = append(full, store.Event{SchemaID: s.TriggerEventType, WorkflowInstanceID: inst.ID})
	}
	got, err := d.Replay(inst, full)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !got.Done(d) {
		t.Fatalf("a complete bug investigation left the instance at cursor %d of %d", got.Cursor, len(d.Steps))
	}

	got, err = d.Replay(inst, full[:len(full)-1])
	if err != nil {
		t.Fatalf("Replay of a short log: %v", err)
	}
	if got.Done(d) {
		t.Error("an investigation missing its last event reported complete")
	}
}

// TestBugInvestigationDefinition_RetriesTheSpawnThenEscalates walks the spawn
// step's retry cap end to end, so the transition from retry to escalate is asserted on
// the real definition rather than only on the fixture.
func TestBugInvestigationDefinition_RetriesTheSpawnThenEscalates(t *testing.T) {
	d := mustBugDef(t)
	inst := Instance{ID: uuid.New(), DefName: d.Name, DefVersion: d.Version}
	retryCap := d.Steps[0].Outcome.MaxRetries
	if retryCap < 1 {
		t.Fatalf("the spawn step has MaxRetries %d; a spawn that never retries makes a transient host failure fatal to the goal", retryCap)
	}
	for f := 1; f <= retryCap; f++ {
		rec, err := d.Recover(inst, f)
		if err != nil {
			t.Fatalf("Recover(%d): %v", f, err)
		}
		if rec.Action != ActionRetry {
			t.Errorf("failure %d of %d gave %s, want retry", f, retryCap, rec.Action)
		}
	}
	rec, err := d.Recover(inst, retryCap+1)
	if err != nil {
		t.Fatalf("Recover(%d): %v", retryCap+1, err)
	}
	if rec.Action != ActionEscalate {
		t.Errorf("failure %d gave %s, want escalate once the cap is spent", retryCap+1, rec.Action)
	}
	if !strings.Contains(rec.Reason, "cap") {
		t.Errorf("the reason should distinguish a cap-exhausted escalate from a declared one; got %q", rec.Reason)
	}
}
