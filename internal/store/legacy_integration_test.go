//go:build store_pg

package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The legacy lifecycle pair against a real Postgres (QUM-1252, slice 10).
//
// What only a real database establishes: that agent_retired's closes_event_id
// actually removes the agent_spawned row from open_contracts (the schema's
// `closes` name is prose until the appender enforces it), that the by-name
// lookup finds the right agent when several are open, and — the point of the
// whole slice — that a prose-spawned agent shows up in `sprawl goals`'s listing
// next to an engine goal rather than beside it in a second view nobody reads.

func newLegacyEnv(t *testing.T) *operabilityEnv {
	t.Helper()
	return newOperabilityEnv(t)
}

// openSpawnOf reads the open agent_spawned contract for one agent through the
// reader itself rather than through a hand-written query, so the SQL under test
// is the SQL exercised.
func openSpawnOf(t *testing.T, e *operabilityEnv, name string) *OpenLegacySpawn {
	t.Helper()
	r := &PgLegacyReader{Pool: e.pool, Registry: e.registry}
	got, err := r.OpenLegacySpawn(context.Background(), e.projectID, name)
	if err != nil {
		t.Fatalf("looking up %q's open spawn: %v", name, err)
	}
	return got
}

// TestLegacyPg_SpawnOpensAContractAndRetireClosesIt.
//
// The second agent is the positive control for the by-name lookup: with one
// open spawn in the fixture, an implementation that ignored the name and closed
// whatever it found first is indistinguishable from this one.
func TestLegacyPg_SpawnOpensAContractAndRetireClosesIt(t *testing.T) {
	e := newLegacyEnv(t)
	ctx := context.Background()

	if _, err := e.ledger.RecordLegacySpawn(ctx, LegacyAgent{
		AgentName: "finn", AgentType: "engineer", Family: "engineering",
		Parent: "weave", Branch: "dmotles/thing",
	}); err != nil {
		t.Fatalf("recording finn's spawn: %v", err)
	}
	if _, err := e.ledger.RecordLegacySpawn(ctx, LegacyAgent{
		AgentName: "ratz", AgentType: "qa", Subagent: true,
	}); err != nil {
		t.Fatalf("recording ratz's spawn: %v", err)
	}

	open := openSpawnOf(t, e, "ratz")
	if open == nil {
		t.Fatal("ratz has no open spawn contract right after being recorded")
	}
	if open.AgentType != "qa" {
		t.Errorf("the lookup returned agent_type %q for ratz, want qa — it matched the wrong agent", open.AgentType)
	}

	spawnWF := open.WorkflowID
	closeID, err := e.ledger.RecordLegacyRetire(ctx, "ratz", "retired", true)
	if err != nil {
		t.Fatalf("retiring ratz: %v", err)
	}
	if closeID == uuid.Nil {
		t.Fatal("retiring an agent with an open contract returned uuid.Nil, which means 'nothing to close'")
	}
	// The close must land on the SPAWN's instance, not a fresh one. Dropping the
	// contract from open_contracts is closes_event_id's job and happens either
	// way, so nothing above can see this: a close on a minted instance leaves
	// `sprawl workflows <spawn-id>` showing an agent that never retired.
	log, err := e.ledger.EventsByWorkflowInstance(ctx, spawnWF, 0, 50)
	if err != nil {
		t.Fatalf("reading ratz's instance: %v", err)
	}
	var sawClose bool
	for _, ev := range log {
		if ev.ID == closeID {
			sawClose = true
		}
	}
	if !sawClose {
		t.Errorf("the agent_retired event is not on instance %s; the spawn's log will never show the retire", spawnWF)
	}

	gone := openSpawnOf(t, e, "ratz")
	if gone != nil {
		t.Errorf("ratz's contract %s is still open after agent_retired closed it", gone.EventID)
	}
	// The negative half: closing one contract must not close the other. A
	// closes_event_id pointed at the wrong row would satisfy every assertion
	// above and silently retire finn too.
	still := openSpawnOf(t, e, "finn")
	if still == nil {
		t.Error("finn's contract was closed by ratz's retire")
	}
}

// TestLegacyPg_RetiringAnUnrecordedAgentIsNotAnError. An agent spawned before
// this slice landed, or while the log was off, has nothing to close — and a
// retire that failed on it would make the log's own gaps into the operator's
// problem.
func TestLegacyPg_RetiringAnUnrecordedAgentIsNotAnError(t *testing.T) {
	e := newLegacyEnv(t)

	id, err := e.ledger.RecordLegacyRetire(context.Background(), "nobody", "retired", false)
	if err != nil {
		t.Fatalf("retiring an agent with no spawn record errored: %v", err)
	}
	if id != uuid.Nil {
		t.Errorf("an agent with no open contract produced close event %s; nothing should have been appended", id)
	}
}

// TestLegacyPg_ProseSpawnsAppearInTheGoalListingBesideEngineGoals.
//
// This is the acceptance criterion for the slice, and the engine goal is the
// control in both directions: a listing that read only agent_spawned would drop
// it, and one that read only goal_opened would drop the legacy agent — the state
// this slice exists to end.
func TestLegacyPg_ProseSpawnsAppearInTheGoalListingBesideEngineGoals(t *testing.T) {
	e := newLegacyEnv(t)
	ctx := context.Background()

	e.openGoalFor(t, "finn", "research", uuid.New())
	if _, err := e.ledger.RecordLegacySpawn(ctx, LegacyAgent{AgentName: "ratz", AgentType: "qa"}); err != nil {
		t.Fatalf("recording ratz's spawn: %v", err)
	}

	got, err := e.ledger.AllOpenGoals(ctx)
	if err != nil {
		t.Fatalf("AllOpenGoals: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d outstanding item(s), want 2 (one engine goal, one prose spawn): %+v", len(got), got)
	}
	// Oldest first, and the goal was appended first.
	engine, legacy := got[0], got[1]
	if engine.Owner != "finn" || engine.GoalType != "research" || engine.Legacy {
		t.Errorf("the engine goal came back owner=%q type=%q legacy=%v, want finn/research/false",
			engine.Owner, engine.GoalType, engine.Legacy)
	}
	if legacy.Owner != "ratz" {
		t.Errorf("the prose spawn came back owner=%q, want ratz — agent_name is what stands in for an owner", legacy.Owner)
	}
	if legacy.GoalType != "qa" {
		t.Errorf("the prose spawn came back type=%q, want qa — agent_type is what stands in for a goal type", legacy.GoalType)
	}
	if !legacy.Legacy {
		t.Error("the prose spawn is not marked legacy, so `sprawl goals` will present it as engine-driven work")
	}
	if legacy.OpenedAt.IsZero() {
		t.Error("the prose spawn has a zero opened_at, so its age prints as decades")
	}

	// And it leaves the listing when the agent retires — the half that makes the
	// listing shrink rather than only grow.
	if _, err := e.ledger.RecordLegacyRetire(ctx, "ratz", "retired", false); err != nil {
		t.Fatalf("retiring ratz: %v", err)
	}
	after, err := e.ledger.AllOpenGoals(ctx)
	if err != nil {
		t.Fatalf("AllOpenGoals after retire: %v", err)
	}
	if len(after) != 1 || after[0].Owner != "finn" {
		t.Fatalf("after the retire the listing is %+v, want just finn's engine goal", after)
	}
}
