//go:build store_pg

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The agent-facing read surface against a real Postgres (QUM-1252, M3a).
//
// What only a real database establishes: that the owner predicate is applied by
// POSTGRES rather than by a Go filter I could have written to agree with my own
// fixture, that closing a goal really does remove it from open_contracts (the
// projection is maintained by a trigger-free append path, so "it left the set"
// is a claim about the appender, not about this query), and that the by-instance
// scan really is ordered and really is scoped.

type goalReaderEnv struct {
	*spawnEnv
	goals *PgGoalReader
}

func newGoalReaderEnv(t *testing.T) *goalReaderEnv {
	t.Helper()
	e := newSpawnEnv(t)
	return &goalReaderEnv{spawnEnv: e, goals: &PgGoalReader{Pool: e.pool, Registry: e.registry}}
}

// openGoalFor appends a goal_opened owned by owner on instance wf.
func (e *goalReaderEnv) openGoalFor(t *testing.T, owner, goalType string, wf uuid.UUID) uuid.UUID {
	t.Helper()
	id, err := e.emitter.Emit(context.Background(), EmitRequest{
		TypeName: "goal_opened", TypeVersion: 1,
		WorkflowInstanceID: wf,
		Payload:            map[string]any{"goal_type": goalType, "text": "do the thing", "owner": owner},
	})
	if err != nil {
		t.Fatalf("opening a goal for %q: %v", owner, err)
	}
	return id
}

// TestGoalReaderPg_ReturnsOnlyTheCallersOwnOpenGoals.
//
// The negative control is the second agent, and it is the whole test: an
// implementation that dropped the owner predicate returns BOTH goals and looks
// perfectly healthy to a fixture with only one agent in it. Handing an agent
// somebody else's goal is the failure this predicate exists to prevent.
func TestGoalReaderPg_ReturnsOnlyTheCallersOwnOpenGoals(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	mine := e.openGoalFor(t, "finn", "research", uuid.New())
	theirs := e.openGoalFor(t, "someone-else", "research", uuid.New())

	got, err := e.goals.OpenGoalsForAgent(ctx, e.projectID, "finn")
	if err != nil {
		t.Fatalf("OpenGoalsForAgent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d open goals for finn, want exactly 1 (the other belongs to someone-else)", len(got))
	}
	if got[0].GoalEventID != mine {
		t.Errorf("got goal %s, want %s", got[0].GoalEventID, mine)
	}
	if got[0].GoalEventID == theirs {
		t.Error("the reader returned another agent's goal")
	}
	if got[0].GoalType != "research" {
		t.Errorf("goal_type came back %q, want %q — it is read out of the payload", got[0].GoalType, "research")
	}
	if got[0].OpenedAt.IsZero() {
		t.Error("opened_at came back zero; it is what orders a multi-goal agent's list")
	}

	// Positive control for the same query, aimed the other way: the predicate
	// is not merely excluding everything.
	other, err := e.goals.OpenGoalsForAgent(ctx, e.projectID, "someone-else")
	if err != nil {
		t.Fatalf("OpenGoalsForAgent(someone-else): %v", err)
	}
	if len(other) != 1 || other[0].GoalEventID != theirs {
		t.Errorf("the other agent's own query returned %d goals; a query that matched nobody would also have passed the first half", len(other))
	}
}

// TestGoalReaderPg_AClosedGoalLeavesTheOpenSet — the open_contracts leg. A
// reader that scanned `events` directly would keep returning a finished goal
// forever, and the agent would re-read a goal it already completed.
func TestGoalReaderPg_AClosedGoalLeavesTheOpenSet(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	goal := e.openGoalFor(t, "finn", "research", uuid.New())

	// Control: it IS in the set before the close. Without this half, a reader
	// that always returned nothing would pass the assertion below.
	before, err := e.goals.OpenGoalsForAgent(ctx, e.projectID, "finn")
	if err != nil {
		t.Fatalf("OpenGoalsForAgent before the close: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("got %d open goals before the close, want 1", len(before))
	}

	if _, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "goal_closed", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		ClosesEventID:      &goal,
		Payload:            map[string]any{"outcome": "success", "summary": "done"},
	}); err != nil {
		t.Fatalf("closing the goal: %v", err)
	}

	after, err := e.goals.OpenGoalsForAgent(ctx, e.projectID, "finn")
	if err != nil {
		t.Fatalf("OpenGoalsForAgent after the close: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("got %d open goals after closing the only one, want 0", len(after))
	}
}

func TestGoalReaderPg_RefusesAnEmptyAgentName(t *testing.T) {
	e := newGoalReaderEnv(t)
	e.openGoalFor(t, "finn", "research", uuid.New())

	_, err := e.goals.OpenGoalsForAgent(context.Background(), e.projectID, "")
	if err == nil {
		t.Fatal("OpenGoalsForAgent accepted an empty agent name")
	}
	if !strings.Contains(err.Error(), "agent name") {
		t.Errorf("the error should name what is missing; got: %v", err)
	}
}

// TestGoalReaderPg_ByInstanceIsScopedAndOrdered.
//
// Two instances, interleaved in the log on purpose, so a reader missing the
// instance predicate returns the other goal's events mixed into this one's —
// which for a derived cursor is not a display bug: Replay would fold over them
// and land somewhere plausible and wrong.
func TestGoalReaderPg_ByInstanceIsScopedAndOrdered(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	mineWF, theirsWF := uuid.New(), uuid.New()
	mineGoal := e.openGoalFor(t, "finn", "research", mineWF)
	theirsGoal := e.openGoalFor(t, "someone-else", "research", theirsWF)

	// Interleave: a second event on each instance, theirs first, so a reader
	// missing the instance predicate returns them out of order rather than
	// merely returning too many.
	for _, poke := range []struct {
		wf    uuid.UUID
		goal  uuid.UUID
		owner string
	}{{theirsWF, theirsGoal, "someone-else"}, {mineWF, mineGoal, "finn"}} {
		if _, err := e.emitter.Emit(ctx, EmitRequest{
			TypeName: "goal_poke", TypeVersion: 1,
			WorkflowInstanceID: poke.wf,
			Payload: map[string]any{
				"goal_event_id": poke.goal.String(),
				"owner":         poke.owner,
				"epoch":         1,
				"reason":        "checking in",
			},
		}); err != nil {
			t.Fatalf("appending a poke: %v", err)
		}
	}

	got, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, mineWF, 0, 100)
	if err != nil {
		t.Fatalf("EventsByWorkflowInstance: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events for my instance, want 2 — %d would mean the other instance leaked in", len(got), 4)
	}
	if got[0].SchemaName != "goal_opened" || got[1].SchemaName != "goal_poke" {
		t.Errorf("got %s then %s, want goal_opened then goal_poke — the scan must be in seq order", got[0].SchemaName, got[1].SchemaName)
	}
	if got[0].Seq >= got[1].Seq {
		t.Errorf("seqs came back %d then %d, which is not ascending", got[0].Seq, got[1].Seq)
	}
	for _, ev := range got {
		if ev.WorkflowInstanceID != mineWF {
			t.Errorf("event %s belongs to instance %s, not %s", ev.ID, ev.WorkflowInstanceID, mineWF)
		}
	}

	// Paging: afterSeq excludes what was already seen. A reader using >= would
	// return the first event again forever.
	page2, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, mineWF, got[0].Seq, 100)
	if err != nil {
		t.Fatalf("EventsByWorkflowInstance(page 2): %v", err)
	}
	if len(page2) != 1 || page2[0].Seq != got[1].Seq {
		t.Errorf("paging after seq %d returned %d events, want just the second", got[0].Seq, len(page2))
	}
}

func TestGoalReaderPg_ByInstanceRefusesANonPositiveLimit(t *testing.T) {
	e := newGoalReaderEnv(t)
	wf := uuid.New()
	e.openGoalFor(t, "finn", "research", wf)

	if _, err := e.goals.EventsByWorkflowInstance(context.Background(), e.projectID, wf, 0, 0); err == nil {
		t.Error("a zero limit was accepted; LIMIT 0 returns nothing, which reads exactly like a goal with no events yet")
	}
}
