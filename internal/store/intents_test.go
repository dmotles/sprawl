package store

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Statement-shape tests for the intent and notification readers.
//
// The sibling _test.go CLAUDE.md requires of every new file under internal/. The
// SEMANTICS are exercised against a real database by spawn_integration_test.go
// and notify_integration_test.go — a fake pgx.Rows would assert only that I can
// write a fake. What is worth pinning HERE is the shape of the queries, because
// each of these predicates is a silent defect if it changes: a missing one does
// not error, it returns a plausible wrong set.

func normalize(sql string) string {
	return strings.ToLower(strings.Join(strings.Fields(sql), " "))
}

// The open-intent query reads open_contracts, is scoped to a project, and is
// scoped to a HOST.
//
// The host predicate is the one that matters most: without it a host reconciles
// its peers' intents, and past the grace period emits spawn_failed for agents
// that are alive and well elsewhere.
func TestOpenIntentsSQL_Shape(t *testing.T) {
	sql := normalize(openIntentsSQL)
	for _, want := range []string{"from open_contracts", "project_id = $1", "schema_id = any($2)", "host_affinity' = $3"} {
		if !strings.Contains(sql, want) {
			t.Errorf("openIntentsSQL is missing %q: %s", want, sql)
		}
	}
	if !strings.Contains(sql, "order by") {
		t.Errorf("openIntentsSQL has no ORDER BY, so reconcile's log lines are in an arbitrary order: %s", sql)
	}
}

// The failed-intent query joins on the CLOSING event's type and excludes intents
// whose stray was already reclaimed.
//
// Both predicates guard the one destructive path in this layer. Joining on any
// closer at all would report every SUCCESSFUL spawn as a failed intent — which
// would have the reconciler reclaim healthy agents' worktrees. And without the
// NOT EXISTS leg, every spawn failure this host has ever had is returned on every
// restart forever, which is what makes a reused agent name collide years later.
func TestFailedIntentsSQL_Shape(t *testing.T) {
	sql := normalize(failedIntentsSQL)
	if !strings.Contains(sql, "closes_event_id = i.id and f.schema_id = any($4)") {
		t.Errorf("failedIntentsSQL does not restrict the CLOSER's type, so a spawn_committed would report as a failure and a healthy agent's worktree would be reclaimed: %s", sql)
	}
	if !strings.Contains(sql, "not exists") || !strings.Contains(sql, "any($5)") {
		t.Errorf("failedIntentsSQL does not exclude already-reclaimed intents, so it grows without bound and an ancient failure keeps matching new agents that reuse the name: %s", sql)
	}
	if !strings.Contains(sql, "host_affinity' = $3") {
		t.Errorf("failedIntentsSQL is not host-scoped, so this host could reclaim another host's resources: %s", sql)
	}
}

// The outstanding-notification query is scoped to a project AND a recipient, and
// selects the subject event.
func TestOpenNotifiesSQL_Shape(t *testing.T) {
	sql := normalize(openNotifiesSQL)
	for _, want := range []string{"from open_contracts", "project_id = $1", "schema_id = any($2)", "recipient' = $3", "subject_event_id"} {
		if !strings.Contains(sql, want) {
			t.Errorf("openNotifiesSQL is missing %q: %s", want, sql)
		}
	}
}

// Both readers REFUSE an empty scope rather than matching everything.
//
// An empty host would reconcile every host's intents; an empty recipient would
// match every notification whose payload has none — and acking somebody else's
// notification is exactly what the open/close pair exists to prevent.
func TestIntentAndNotifyReaders_RefuseAnEmptyScope(t *testing.T) {
	reg := testRegistry(t)
	intents := &PgIntentReader{Registry: reg}
	notifies := &PgNotifyReader{Registry: reg}

	if _, err := intents.OpenIntents(t.Context(), uuid.New(), ""); err == nil {
		t.Error("OpenIntents accepted an empty host")
	}
	if _, err := intents.FailedIntents(t.Context(), uuid.New(), ""); err == nil {
		t.Error("FailedIntents accepted an empty host; that would license reclaiming another host's resources")
	}
	if _, err := notifies.OpenNotifies(t.Context(), uuid.New(), ""); err == nil {
		t.Error("OpenNotifies accepted an empty recipient")
	}
}

// LedgerEmitter refuses to report a recorded event against a DISABLED ledger.
//
// Ledger.Emit on a disabled ledger returns (0, nil) — records nothing, succeeds —
// which is right for telemetry and catastrophic for a write-ahead: the caller
// would take nil as "the intent is recorded", create a worktree, and leave a
// resource nothing in any log can attribute.
func TestLedgerEmitter_RefusesADisabledLedger(t *testing.T) {
	_, err := LedgerEmitter{Ledger: nil}.Emit(t.Context(), EmitRequest{TypeName: "run_started", TypeVersion: 1})
	if err == nil {
		t.Fatal("LedgerEmitter reported a recorded event against a disabled ledger")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("the refusal does not say the log is disabled: %v", err)
	}
}

// schemaIDsFor collects EVERY version of a name.
//
// Schemas are additive-only within a name, so spawn_intent@2 will exist. A reader
// pinned to one id would be silently blind to the newer version — finding no
// intents, adopting nothing, and reporting a clean pass.
func TestSchemaIDsFor_CollectsByNameNotByASingleID(t *testing.T) {
	reg := testRegistry(t)
	got := schemaIDsFor(reg, "spawn_intent")
	if len(got) != 1 {
		t.Fatalf("schemaIDsFor(spawn_intent) = %d ids, want 1 for this build", len(got))
	}
	if got[0] != SeedID("spawn_intent", 1) {
		t.Errorf("schemaIDsFor returned %s, want the derived id for spawn_intent@1", got[0])
	}
	if len(schemaIDsFor(reg, "no_such_type")) != 0 {
		t.Error("schemaIDsFor invented ids for a name this build does not carry")
	}
	// The activity set is BOTH turn-boundary types: run_started alone calls an
	// agent mid-first-turn dead, turn_finished alone calls an agent that started
	// but has not finished a turn dead — the long-quiet-turn case.
	if len(activitySchemaIDs(reg)) != 2 {
		t.Errorf("activitySchemaIDs = %d ids, want both run_started and turn_finished", len(activitySchemaIDs(reg)))
	}
}
