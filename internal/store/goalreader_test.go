package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Statement-shape tests for the goal reader, following the convention set by
// intents_test.go: the SEMANTICS are exercised against a real database by
// goalreader_integration_test.go, and what is worth pinning here is the shape,
// because each predicate below is a silent defect if it changes — a missing one
// does not error, it returns a plausible wrong set.

// The open-goal-for-agent query must be scoped THREE ways. Losing any one of
// them returns rows that look entirely reasonable:
//
//   - no project scope   -> another project's goals
//   - no schema scope    -> every open contract, not just goals
//   - no owner predicate -> somebody else's goal, handed to this agent as its own
func TestOpenGoalsForAgentSQL_Shape(t *testing.T) {
	sql := normalize(openGoalsForAgentSQL)

	for _, want := range []string{
		"from open_contracts",
		"join events",
		"e.project_id = $1",
		"e.schema_id = any($2)",
		"e.payload->>'owner' = $3",
		"order by e.seq",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the open-goals-for-agent query is missing %q:\n%s", want, sql)
		}
	}
}

// The by-instance scan is the read side of "show me this goal's history".
//
// `seq > $3` and `ORDER BY seq` carry the same weight they do in eventScanSQL:
// without the ordering the log reads back in an arbitrary order, which for a
// derived cursor is not a cosmetic problem — Replay is order-sensitive, so an
// unordered read silently computes the wrong cursor.
func TestEventsByWorkflowInstanceSQL_Shape(t *testing.T) {
	sql := normalize(eventsByInstanceSQL)

	for _, want := range []string{
		"e.project_id = $1",
		"e.workflow_instance_id = $2",
		"e.seq > $3",
		"order by e.seq",
		"limit $4",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the by-instance scan is missing %q:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "e.seq >= $3") {
		t.Error("the by-instance scan uses >=, so the cursor's own event is re-read on every pass")
	}
}

// Both readers select the SAME column list as eventScanSQL where they return a
// DispatchedEvent, because they share one scan helper. A column added to one
// and not the other is a scan-arity mismatch at runtime, not at compile time —
// which is exactly the kind of defect that reaches Postgres before it reaches a
// reader.
func TestEventsByWorkflowInstanceSQL_SelectsTheSameColumnsAsTheCatchUpScan(t *testing.T) {
	cols := func(sql string) string {
		lower := normalize(sql)
		start := strings.Index(lower, "select ")
		end := strings.Index(lower, " from ")
		if start < 0 || end < 0 {
			t.Fatalf("could not find a SELECT ... FROM in:\n%s", sql)
		}
		return lower[start+len("select ") : end]
	}
	got, want := cols(eventsByInstanceSQL), cols(eventScanSQL)
	if got != want {
		t.Errorf("the by-instance scan selects different columns than the catch-up scan, so they cannot share a scan helper:\n by-instance: %s\n   catch-up: %s", got, want)
	}
}

// The Ledger-level gate. Each of these three states would otherwise answer
// "you own no open goals" — a legitimate result that means the work is done —
// so an agent could not tell a finished goal from a store that never looked.
//
// Each case is checked against BOTH methods, because the gate is per-method and
// a reader wired into one and not the other is exactly the shape review misses.
func TestLedgerGoalReads_RefuseWhenTheStoreCannotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ledger *Ledger
		want   string
	}{
		{"nil ledger is the disabled store", nil, "disabled"},
		{"explicitly disabled", &Ledger{}, "disabled"},
		{"degraded", &Ledger{enabled: true, degradedErr: errors.New("dial tcp: refused")}, "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := tc.ledger.OpenGoalsForAgent(ctx, "finn"); err == nil {
				t.Error("OpenGoalsForAgent answered from a store that cannot answer")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error should say %q so the caller can tell this from an empty result; got: %v", tc.want, err)
			}
			if _, err := tc.ledger.EventsByWorkflowInstance(ctx, uuid.New(), 0, 10); err == nil {
				t.Error("EventsByWorkflowInstance answered from a store that cannot answer")
			}
		})
	}
}

// The paired control: an ENABLED, non-degraded Ledger gets PAST the gate. Its
// query then fails on the nil pool, which is the point — the failure is a
// database call, not a refusal, so the gate above is not refusing everything.
func TestLedgerGoalReads_AHealthyLedgerReachesTheQuery(t *testing.T) {
	l := &Ledger{enabled: true}
	_, err := l.goalReader()
	if err != nil {
		t.Fatalf("a healthy ledger was refused by the gate: %v", err)
	}
}

// TestCloseGoalForAgent_RefusesAnOutcomeOutsideTheSet.
//
// Checked BEFORE the ledger gate is reachable and before any query, on purpose:
// the seed validator's keyword subset has no `enum`, so goal_closed.json would
// accept any string, and this is the only layer that can hold the line. An
// `outcome` every agent spells differently is a column nobody can query.
func TestCloseGoalForAgent_RefusesAnOutcomeOutsideTheSet(t *testing.T) {
	// A DISABLED ledger, so the pair below is distinguished by WHICH refusal
	// comes back: outcome validation runs before the gate, so an invalid
	// outcome says "not a goal outcome" and a valid one falls through to
	// "disabled". Nothing reaches a pool.
	l := &Ledger{}
	for _, bad := range []GoalOutcome{"", "done", "Success", "SUCCESS", "ok"} {
		_, err := l.CloseGoalForAgent(context.Background(), "finn", uuid.New(), bad, "s")
		if err == nil {
			t.Errorf("CloseGoalForAgent accepted outcome %q", bad)
			continue
		}
		if !strings.Contains(err.Error(), "not a goal outcome") {
			t.Errorf("outcome %q was refused for the wrong reason: %v", bad, err)
		}
	}

	// The control: every advertised outcome gets PAST the validation. Without
	// it, validGoalOutcome could reject everything and the loop above would
	// still pass.
	for _, good := range ValidGoalOutcomes {
		_, err := l.CloseGoalForAgent(context.Background(), "finn", uuid.New(), good, "s")
		if err == nil {
			t.Errorf("outcome %q was accepted by a DISABLED ledger", good)
			continue
		}
		if strings.Contains(err.Error(), "not a goal outcome") {
			t.Errorf("the advertised outcome %q was rejected as invalid", good)
		}
	}
}
