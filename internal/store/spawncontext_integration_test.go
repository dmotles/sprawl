//go:build store_pg

package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// The spawn_context artifact against a real Postgres (QUM-1251, AC6).
//
// The hermetic tests drive a hand-written pool, and a hand-written pool agrees
// with whatever the statement says: a wrong column name or a scan destination
// that cannot take the value is invisible to it. What is asserted here is what
// only the database can answer — that the row lands, that the artifact is
// reachable FROM the event (the orphan case), and that a second identical launch
// deduplicates rather than forking the chain.

// TestPutSpawnContext_LandsAndIsReachableFromRunStarted is AC6's "retrievable by
// id" as a property of the schema.
//
// The JOIN is the assertion, deliberately, and not count(artifacts): `artifacts`
// has no read path but by id, so an artifact no event references is unreachable
// — a row, not a record — and counting rows cannot tell the two apart.
func TestPutSpawnContext_LandsAndIsReachableFromRunStarted(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()

	l := &Ledger{
		enabled:   true,
		pool:      f.pool,
		registry:  f.registry,
		projectID: f.projectID,
		appender:  f.appender,
	}
	// Keyed on the payload, not events.agent_session_id: the lifecycle emitter
	// records the backend session in the payload and leaves that column unset,
	// so a WHERE on the column would match nothing and this test would fail for
	// a reason that has nothing to do with the artifact.
	sessionID := "sess-integration"
	sc := SpawnContext{
		AgentName: "finn", AgentType: "engineer", SessionID: sessionID,
		Model: "opus", Effort: "high",
		CardName: "legacy-engineer", CardVersion: 1, CardContentSHA256: "e3b0c442",
		CardSource: "db", RenderedPrompt: "SPAWN-CONTEXT-INTEGRATION-PROMPT",
	}
	e := NewLifecycleEmitter(LifecycleDeps{
		Ledger: l, AgentName: sc.AgentName, AgentType: sc.AgentType,
		SessionID: sc.SessionID, SpawnContext: &sc,
	})
	e.RunStarted(ctx)

	var (
		id      uuid.UUID
		kind    string
		size    int
		content string
	)
	if err := f.pool.QueryRow(ctx,
		`SELECT a.id, a.kind, a.size_bytes, a.content
		   FROM events e JOIN artifacts a ON a.id = e.artifact_id
		  WHERE e.payload->>'session_id' = $1`, sc.SessionID).Scan(&id, &kind, &size, &content); err != nil {
		t.Fatalf("no artifact is reachable from this run's event: %v", err)
	}
	if kind != spawnContextKind {
		t.Errorf("artifact kind = %q, want %q", kind, spawnContextKind)
	}
	// size_bytes AND a content substring: an empty artifact is the plausible
	// value here, and size alone cannot tell a real body from a placeholder.
	if size <= 0 {
		t.Errorf("size_bytes = %d, so the artifact body is empty", size)
	}
	want, err := sc.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if content != string(want) {
		t.Errorf("artifact content is not the marshalled spawn context:\ngot  %q\nwant %q", content, want)
	}
}

// TestPutSpawnContext_IdenticalLaunchesShareOneArtifact.
//
// Content addressing means a resumed or re-woken run with a byte-identical
// context reuses the row, and the SECOND call must return the EXISTING id — that
// is the whole reason PutArtifact's ON CONFLICT is DO UPDATE rather than DO
// NOTHING. It also means "one artifact per run" is FALSE, so no assertion may
// count artifacts against a count of events.
func TestPutSpawnContext_IdenticalLaunchesShareOneArtifact(t *testing.T) {
	f := newAppenderFixture(t)
	ctx := context.Background()
	l := &Ledger{enabled: true, pool: f.pool, registry: f.registry, projectID: f.projectID, appender: f.appender}
	sc := SpawnContext{AgentName: "finn", AgentType: "engineer", SessionID: "s", RenderedPrompt: "P"}

	first := PutSpawnContext(ctx, l, sc)
	if first == nil {
		t.Fatalf("the first write recorded nothing")
	}
	second := PutSpawnContext(ctx, l, sc)
	if second == nil {
		t.Fatalf("the second write recorded nothing")
	}
	if *first != *second {
		t.Errorf("two identical contexts produced ids %s and %s — the chain has forked", *first, *second)
	}

	// Aim control: a DIFFERENT context must get its own row. Without it, a
	// PutSpawnContext that returned one fixed id would satisfy the above.
	other := sc
	other.RenderedPrompt = "A DIFFERENT PROMPT"
	third := PutSpawnContext(ctx, l, other)
	if third == nil {
		t.Fatalf("the differing write recorded nothing")
	}
	if *third == *first {
		t.Errorf("a different prompt reused artifact %s, so the digest is not a function of the context", *third)
	}
}
