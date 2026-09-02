//go:build store_pg

package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The operator read surface against a real Postgres (QUM-1252, slice 9).
//
// What only a real database establishes: that these readers are scoped by
// PROJECT and by nothing else (the agent-facing reader next door is scoped by
// owner, and the two share a shape — a stray owner predicate here would return
// an empty fleet view and look like a quiet day), that a closed contract really
// leaves both listings, that `Events` and `OpenContracts` are counted over
// different sets rather than being the same number twice, and that a malformed
// `legacy` payload degrades to false instead of failing the whole listing.

type operabilityEnv struct {
	*goalReaderEnv
	ops *PgOperabilityReader
}

func newOperabilityEnv(t *testing.T) *operabilityEnv {
	t.Helper()
	e := newGoalReaderEnv(t)
	return &operabilityEnv{goalReaderEnv: e, ops: &PgOperabilityReader{Pool: e.pool, Registry: e.registry}}
}

// openGoalWithPayload appends a goal_opened carrying extra payload keys that
// openGoalFor does not model.
func (e *operabilityEnv) openGoalWithPayload(t *testing.T, wf uuid.UUID, payload map[string]any) uuid.UUID {
	t.Helper()
	id, err := e.emitter.Emit(context.Background(), EmitRequest{
		TypeName: "goal_opened", TypeVersion: 1,
		WorkflowInstanceID: wf,
		Payload:            payload,
	})
	if err != nil {
		t.Fatalf("opening a goal with payload %v: %v", payload, err)
	}
	return id
}

// TestOperabilityPg_AllOpenGoalsSpansEveryOwnerAndDropsClosedOnes.
func TestOperabilityPg_AllOpenGoalsSpansEveryOwnerAndDropsClosedOnes(t *testing.T) {
	e := newOperabilityEnv(t)
	ctx := context.Background()

	mine := e.openGoalFor(t, "finn", "research", uuid.New())
	// A second OWNER is the positive control for "project-wide": with one owner
	// in the fixture, a reader that had kept the owner predicate is indis-
	// tinguishable from this one.
	theirs := e.openGoalFor(t, "ratz", "bug_investigation", uuid.New())
	closedWF := uuid.New()
	closed := e.openGoalFor(t, "finn", "research", closedWF)

	got, err := e.ops.AllOpenGoals(ctx, e.projectID)
	if err != nil {
		t.Fatalf("AllOpenGoals: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d open goals, want 3 across two owners", len(got))
	}
	if got[0].GoalEventID != mine || got[1].GoalEventID != theirs || got[2].GoalEventID != closed {
		t.Errorf("goals are not in log order: %s, %s, %s", got[0].GoalEventID, got[1].GoalEventID, got[2].GoalEventID)
	}
	if got[1].Owner != "ratz" || got[1].GoalType != "bug_investigation" {
		t.Errorf("the second goal came back as owner=%q type=%q, want ratz/bug_investigation", got[1].Owner, got[1].GoalType)
	}
	if got[0].WorkflowID == uuid.Nil {
		t.Error("the goal carries no workflow instance, so nothing can read its log")
	}
	if got[0].OpenedAt.IsZero() {
		t.Error("opened_at is zero, so nothing can tell how long the goal has been outstanding")
	}
	if got[0].Legacy {
		t.Error("a goal with no legacy key read as legacy; every engine goal would be mislabelled")
	}

	if _, err := e.ledger.CloseGoalForAgent(ctx, "finn", closed, GoalSucceeded, "done"); err != nil {
		t.Fatalf("closing a goal: %v", err)
	}
	after, err := e.ops.AllOpenGoals(ctx, e.projectID)
	if err != nil {
		t.Fatalf("re-reading open goals: %v", err)
	}
	if len(after) != 2 || after[0].GoalEventID != mine || after[1].GoalEventID != theirs {
		t.Fatalf("after one close there are %d open goals, want %s and %s", len(after), mine, theirs)
	}
}

// TestOperabilityPg_LegacyIsSurfacedAndAMalformedOneDoesNotKillTheListing.
//
// The malformed case is the reason the SQL compares jsonb instead of casting:
// a cast raises, and one bad event written by a future producer would take the
// operator's whole view of outstanding work down with it.
func TestOperabilityPg_LegacyIsSurfacedAndAMalformedOneDoesNotKillTheListing(t *testing.T) {
	e := newOperabilityEnv(t)
	ctx := context.Background()

	legacy := e.openGoalWithPayload(t, uuid.New(), map[string]any{
		"goal_type": "legacy_spawn", "text": "prose-spawned", "owner": "finn", "legacy": true,
	})
	malformed := e.openGoalWithPayload(t, uuid.New(), map[string]any{
		"goal_type": "research", "text": "malformed legacy", "owner": "finn", "legacy": "yes",
	})
	engine := e.openGoalFor(t, "finn", "research", uuid.New())

	got, err := e.ops.AllOpenGoals(ctx, e.projectID)
	if err != nil {
		t.Fatalf("AllOpenGoals with a malformed legacy in the set: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d goals, want all 3 — a malformed legacy must not remove rows either", len(got))
	}
	byID := map[uuid.UUID]OpenGoal{}
	for _, g := range got {
		byID[g.GoalEventID] = g
	}
	if !byID[legacy].Legacy {
		t.Error("a legacy:true goal did not read as legacy, so prose-spawned work is indistinguishable from engine work")
	}
	if byID[malformed].Legacy {
		t.Error(`legacy:"yes" read as true; only a real boolean should count`)
	}
	if byID[engine].Legacy {
		t.Error("an engine goal read as legacy")
	}
}

// TestOperabilityPg_OpenWorkflowsCountsTheInstanceAndHidesFinishedOnes.
func TestOperabilityPg_OpenWorkflowsCountsTheInstanceAndHidesFinishedOnes(t *testing.T) {
	e := newOperabilityEnv(t)
	ctx := context.Background()

	busy := uuid.New()
	first := e.openGoalFor(t, "finn", "research", busy)
	e.openGoalFor(t, "ratz", "research", busy)

	finished := uuid.New()
	done := e.openGoalFor(t, "finn", "research", finished)

	got, err := e.ops.OpenWorkflows(ctx, e.projectID)
	if err != nil {
		t.Fatalf("OpenWorkflows: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d open workflows, want 2", len(got))
	}
	if got[0].WorkflowID != busy || got[1].WorkflowID != finished {
		t.Fatalf("workflows are not in start order: %s then %s", got[0].WorkflowID, got[1].WorkflowID)
	}
	if got[0].Events != 2 || got[0].OpenContracts != 2 {
		t.Errorf("the busy instance reports %d event(s) / %d open contract(s), want 2/2", got[0].Events, got[0].OpenContracts)
	}
	if got[0].StartedAt.IsZero() || got[0].LastEventAt.IsZero() {
		t.Error("the instance carries no timestamps, so nothing can tell a fresh instance from a stuck one")
	}
	// Closing the only contract on `finished` appends an event: the instance now
	// has MORE events and NO open contracts, which is exactly the pair a single
	// count over one set could not produce.
	if _, err := e.ledger.CloseGoalForAgent(ctx, "finn", done, GoalSucceeded, "done"); err != nil {
		t.Fatalf("closing a goal: %v", err)
	}
	after, err := e.ops.OpenWorkflows(ctx, e.projectID)
	if err != nil {
		t.Fatalf("re-reading open workflows: %v", err)
	}
	if len(after) != 1 || after[0].WorkflowID != busy {
		t.Fatalf("after closing its only contract the finished instance is still listed: got %d workflow(s)", len(after))
	}

	// And the surviving instance still counts events over the WHOLE instance:
	// close one of its two goals and events must go up while open goes down.
	if _, err := e.ledger.CloseGoalForAgent(ctx, "finn", first, GoalSucceeded, "done"); err != nil {
		t.Fatalf("closing a goal on the busy instance: %v", err)
	}
	final, err := e.ops.OpenWorkflows(ctx, e.projectID)
	if err != nil {
		t.Fatalf("re-reading open workflows: %v", err)
	}
	if len(final) != 1 {
		t.Fatalf("got %d open workflows, want the busy one", len(final))
	}
	if final[0].Events != 3 || final[0].OpenContracts != 1 {
		t.Errorf("after one close the busy instance reports %d event(s) / %d open contract(s), want 3/1",
			final[0].Events, final[0].OpenContracts)
	}
}
