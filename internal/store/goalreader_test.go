package store

import (
	"strings"
	"testing"
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
