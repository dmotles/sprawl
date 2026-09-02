package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Unit tests for OpenGoal — the write side of a goal (QUM-1252, M3a slice 10b).
//
// The semantics that need a real database (does the row land in open_contracts,
// does the sweeper see it) are goalopen_integration_test.go's job. What is worth
// pinning hermetically is the set of refusals, because every one of them fails
// toward a plausible success: an unmigrated goal type that opens a contract
// nothing drives, or a disabled store that hands back an id for an event that
// was never written.

func newOpenGoalLedger(t *testing.T) (*Ledger, *recordingPool) {
	t.Helper()
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	return &Ledger{
		enabled:   true,
		registry:  reg,
		projectID: uuid.New(),
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
	}, pool
}

// TestOpenGoal_AppendsAGoalOpenedEvent is the happy path, and it pins that the
// two ids come back DISTINCT and non-nil.
//
// The caller (create_goal, and after it the dispatcher) needs both: the goal
// event id is the contract that gets closed, the workflow instance id is what
// every later event on this goal is filed under. Returning the same uuid for
// both, or a zero one, produces a log that still validates and a goal whose
// history cannot be assembled.
func TestOpenGoal_AppendsAGoalOpenedEvent(t *testing.T) {
	l, pool := newOpenGoalLedger(t)

	g, err := l.OpenGoal(context.Background(), GoalResearch, "find out how the cursor is derived", "weave")
	if err != nil {
		t.Fatalf("OpenGoal: %v", err)
	}
	if g.GoalEventID == uuid.Nil {
		t.Error("OpenGoal returned a nil goal event id; that id is the contract report_result must close")
	}
	if g.WorkflowID == uuid.Nil {
		t.Error("OpenGoal returned a nil workflow instance id; every later event on this goal is filed under it")
	}
	if g.GoalEventID == g.WorkflowID {
		t.Error("the goal event id and the workflow instance id are the same uuid; they are different things and conflating them makes get_workflow_log answer with the wrong instance")
	}
	if g.GoalType != GoalResearch {
		t.Errorf("GoalType = %q, want %q", g.GoalType, GoalResearch)
	}
	if indexOf(pool.log(), "insert_event") < 0 {
		t.Errorf("OpenGoal returned success without inserting an event: %v", pool.log())
	}
}

// TestOpenGoal_RefusesAnUnmigratedGoalType is the routing table, enforced in
// code rather than left to the root card's prose.
//
// The prose says RESEARCH and BUG_INVESTIGATION go through create_goal and
// everything else goes through the legacy `spawn` path. Prose alone cannot hold
// that line: a model that opens a `change` goal gets a valid, well-formed,
// permanently-open contract that no workflow definition drives and no sweeper
// can finish, and the failure surfaces hours later as work that simply never
// happened. The refusal has to be here.
func TestOpenGoal_RefusesAnUnmigratedGoalType(t *testing.T) {
	l, pool := newOpenGoalLedger(t)

	_, err := l.OpenGoal(context.Background(), GoalType("change"), "make the thing faster", "weave")
	if err == nil {
		t.Fatal("OpenGoal accepted an unmigrated goal type; that opens a contract nothing drives and nothing can close")
	}
	if !strings.Contains(err.Error(), "spawn") {
		t.Errorf("the refusal must name the path that DOES work for this type, or the caller has been told no with nowhere to go; got: %v", err)
	}
	if !strings.Contains(err.Error(), "change") {
		t.Errorf("the refusal should name the type that was refused; got: %v", err)
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a refused goal type touched the database: %v", calls)
	}

	// Positive control: the two migrated types are accepted, so this test is
	// pinning the boundary rather than a blanket refusal.
	for _, gt := range MigratedGoalTypes {
		if _, err := l.OpenGoal(context.Background(), gt, "text", "weave"); err != nil {
			t.Errorf("control: %q is a migrated goal type and must be accepted: %v", gt, err)
		}
	}
}

// TestOpenGoal_RefusesADisabledStore is the one that guards a silent success.
//
// Ledger.Emit is nil/disabled-safe by design: with the flag off it records
// nothing and returns (0, nil). Every telemetry emitter wants that. OpenGoal
// must NOT inherit it — it mints its own ids before emitting, so without an
// explicit gate it would return a perfectly plausible goal id for an event that
// was never written, and weave would go on to tell someone the goal exists.
func TestOpenGoal_RefusesADisabledStore(t *testing.T) {
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	l := &Ledger{
		enabled:   false,
		registry:  reg,
		projectID: uuid.New(),
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
	}

	g, err := l.OpenGoal(context.Background(), GoalResearch, "find the thing", "weave")
	if err == nil {
		t.Fatalf("OpenGoal on a disabled store returned success with goal id %s; nothing was written and the caller has been handed an id that refers to no event", g.GoalEventID)
	}
	if !strings.Contains(err.Error(), "event_log.enabled") {
		t.Errorf("the refusal must name the config key, because the agent reading it cannot see the host's configuration; got: %v", err)
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a disabled store touched the database: %v", calls)
	}
}

// TestOpenGoal_FailsLoudlyOnADegradedStore pins that OpenGoal does not acquire a
// spill path by accident.
//
// goal_opened is deliberately not spillable (seeds/goal_opened.json): a goal
// recorded only in a local file is invisible to every other host and to the
// sweeper, so it reads as work nobody is doing. This asserts OpenGoal surfaces
// that refusal rather than swallowing it into a success.
func TestOpenGoal_FailsLoudlyOnADegradedStore(t *testing.T) {
	spill := &capturingSpiller{}
	l, _ := newDegradedLedger(t, spill)

	if _, err := l.OpenGoal(context.Background(), GoalResearch, "find the thing", "weave"); !errors.Is(err, ErrDegraded) {
		t.Fatalf("got err=%v, want ErrDegraded — a goal that exists only in a local spill file is work nobody can see", err)
	}
	if spill.count() != 0 {
		t.Errorf("the goal was spilled (%d record(s)); goal_opened is not spillable precisely so this cannot happen", spill.count())
	}
}

// TestOpenGoal_RequiresTextAndOwner.
//
// `text` is required by the seed. `owner` is optional there and required HERE,
// which is a deliberate narrowing on the same grounds as GoalOutcome's closed
// set: the owner is who OWNER_NOTIFY is addressed to when the goal closes, and
// an ownerless goal falls through to the dispatcher's host-level FallbackOwner —
// so the result of somebody's goal gets announced to whoever that host happens
// to name. Refusing is the only answer that cannot silently misdeliver.
func TestOpenGoal_RequiresTextAndOwner(t *testing.T) {
	l, pool := newOpenGoalLedger(t)

	if _, err := l.OpenGoal(context.Background(), GoalResearch, "", "weave"); err == nil {
		t.Error("OpenGoal accepted an empty text; the spawned agent's whole task is that string")
	}
	if _, err := l.OpenGoal(context.Background(), GoalResearch, "find the thing", ""); err == nil {
		t.Error("OpenGoal accepted an empty owner; the close notification would go to the host's fallback owner instead of the requester")
	}
	if calls := pool.log(); len(calls) != 0 {
		t.Errorf("a refused goal touched the database: %v", calls)
	}

	// Positive control: with both present it goes through, so the two refusals
	// above are about the fields and not about the fixture.
	if _, err := l.OpenGoal(context.Background(), GoalResearch, "find the thing", "weave"); err != nil {
		t.Fatalf("control: a complete goal must be accepted: %v", err)
	}
}
