package store

import (
	"strings"
	"testing"
)

// Statement-shape tests for the sweeper's candidate query.
//
// The sibling _test.go CLAUDE.md requires of every new file under internal/. The
// query's SEMANTICS are asserted against a real database by
// sweeper_integration_test.go; what is pinned here is the shape, because every
// term in it is DERIVED and a wrong subquery therefore produces a plausible
// candidate rather than an error.

// The candidate set is open goals, scoped to the project.
func TestOpenGoalsSQL_ReadsOpenContractsScopedToTheProject(t *testing.T) {
	sql := normalize(openGoalsSQL)
	for _, want := range []string{"from open_contracts", "project_id = $1", "g.schema_id = any($5)", "order by"} {
		if !strings.Contains(sql, want) {
			t.Errorf("openGoalsSQL is missing %q: %s", want, sql)
		}
	}
}

// THE BLOCKING TERM EXCLUDES THIS GOAL AND EXCLUDES owner_notify.
//
// The two most consequential details in the query, and both are silent when
// wrong. Without `o2.id <> g.id` every open goal is its own blocker and the
// sweeper pokes NOTHING — a total failure presenting as a quiet fleet. Without
// the schema exclusion, an unacked notification counts as its recipient being
// blocked, so the sweeper refuses to poke the very agent that has not seen its
// notification: gated by the symptom it exists to act on.
func TestOpenGoalsSQL_BlockingTermExcludesTheGoalAndNotifications(t *testing.T) {
	sql := normalize(openGoalsSQL)
	if !strings.Contains(sql, "o2.id <> t.id") {
		t.Errorf("the blocking term does not exclude the goal itself, so every goal blocks itself and nothing is ever poked: %s", sql)
	}
	if !strings.Contains(sql, "o2.schema_id <> all($6)") {
		t.Errorf("the blocking term does not exclude owner_notify, so an unacked notification stops its recipient being poked: %s", sql)
	}
}

// Liveness is max(at) over the turn-boundary types, scoped to the POKE TARGET.
//
// Another agent's turn must not count as the target's activity, or a busy fleet
// makes every idle agent look alive and nothing is ever swept. Scoping this to
// the OWNER rather than the target was the AC5 defect: the owner is the agent
// the result is reported TO, and its own constant activity kept every goal it
// had delegated permanently fresh.
func TestOpenGoalsSQL_LivenessIsMaxOverTurnBoundariesForTheTarget(t *testing.T) {
	sql := normalize(openGoalsSQL)
	if !strings.Contains(sql, "max(a.at)") || !strings.Contains(sql, "a.schema_id = any($2)") {
		t.Errorf("liveness is not max(at) over the turn-boundary types: %s", sql)
	}
	if !strings.Contains(sql, "a.payload->>'agent_name' = t.target") {
		t.Errorf("liveness is not scoped to the goal's poke target, so any agent's turn would make every target look alive: %s", sql)
	}
	// And it must NOT be COALESCE'd to now(): the sweeper reads a NULL as "the
	// target has never taken a turn" and falls back to the goal's own age.
	// Substituting now() would make every such goal permanently fresh.
	if strings.Contains(sql, "coalesce(max(a.at)") {
		t.Errorf("last-activity is COALESCE'd, which makes a never-active target's goal immortal: %s", sql)
	}
}

// The epoch and the quarantine flag are both derived per GOAL.
func TestOpenGoalsSQL_EpochAndQuarantineAreDerivedPerGoal(t *testing.T) {
	sql := normalize(openGoalsSQL)
	if !strings.Contains(sql, "count(*) from events p") || !strings.Contains(sql, "p.payload->>'goal_event_id' = t.id::text") {
		t.Errorf("the poke count is not derived per goal, so poke counts leak between goals and a busy project quarantines everything at once: %s", sql)
	}
	if !strings.Contains(sql, "exists (select 1 from events s") || !strings.Contains(sql, "s.payload->>'goal_event_id' = t.id::text") {
		t.Errorf("quarantine is not derived per goal: %s", sql)
	}
}

// There is deliberately NO election here any more.
//
// PgSweepElection existed, was described as an advisory-lock election, and was a
// NO-OP: it took pg_try_advisory_xact_lock through a plain QueryRow, i.e. in an
// implicit single-statement transaction, and an xact lock is released when that
// transaction commits — before the sweep looked at a single candidate. Every host
// won on every pass.
//
// It was deleted rather than repaired because it was never load-bearing: pokes
// and quarantines are now excluded by derived event ids plus `events.id UNIQUE`,
// so two sweepers already produce one poke. Describing an election that does not
// elect is worse than not having one, and this test stops it coming back by
// accident.
func TestSweepReader_HasNoElection(t *testing.T) {
	sql := normalize(openGoalsSQL)
	if strings.Contains(sql, "advisory") {
		t.Error("the candidate query takes an advisory lock; the sweeper's exclusion comes from derived event ids, and an xact lock taken outside a transaction is a no-op")
	}
}

// The assignee is the agent_name on the goal's NEWEST spawn_requested.
//
// Newest, not first: a re-spawn after a crash names a different agent, and the
// stale one is by definition not the agent working the goal now. The ORDER BY /
// LIMIT is what makes it newest, and dropping it silently returns an arbitrary
// row that is right most of the time.
func TestOpenGoalsSQL_AssigneeIsTheNewestSpawnRequested(t *testing.T) {
	sql := normalize(openGoalsSQL)
	for _, want := range []string{
		"s.schema_id = any($7)",
		"s.workflow_instance_id = g.workflow_instance_id",
		"order by s.seq desc limit 1",
		"coalesce(nullif(assignee, ''), owner) as target",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the assignee term is missing %q, so the sweeper cannot tell the worker from the owner: %s", want, sql)
		}
	}
}
