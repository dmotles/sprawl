//go:build store_pg

package store

import (
	"context"
	"testing"
)

// Ledger.Open against a real database: project registration and the
// first-enable repo_initialized marker.

func openTestLedger(t *testing.T, dsn, remoteURL string) *Ledger {
	t.Helper()
	l, err := Open(context.Background(), LedgerConfig{
		Enabled:    true,
		DSN:        dsn,
		DSNSource:  EnvDSN,
		RemoteURL:  remoteURL,
		GitSHA:     "0123456789abcdef0123456789abcdef01234567",
		SprawlRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if l == nil {
		t.Fatal("Open returned no Ledger for an enabled config")
	}
	t.Cleanup(l.Close)
	return l
}

// TestOpen_RegistersTheProjectAndEmitsRepoInitializedOnce pins the first-enable
// behaviour, and — the part that matters — pins that a SECOND Open does not
// re-emit.
//
// repo_initialized marks the moment a project joined the log. If every process
// start re-emitted it, the log would fill with markers and "when did this
// project start" would become unanswerable, which is the one question the event
// exists to answer.
func TestOpen_RegistersTheProjectAndEmitsRepoInitializedOnce(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()
	remote := "https://example.invalid/repo-once"

	first := openTestLedger(t, dsn, remote)
	if first.ProjectID().String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("Open did not resolve a project id")
	}

	var projects int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE remote_url = $1`, remote).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if projects != 1 {
		t.Errorf("projects rows for %s = %d, want 1", remote, projects)
	}

	countMarkers := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM events e
			 JOIN event_type_schemas s ON s.id = e.schema_id
			 WHERE s.name = 'repo_initialized' AND e.project_id = $1`, first.ProjectID()).Scan(&n); err != nil {
			t.Fatalf("count markers: %v", err)
		}
		return n
	}
	if got := countMarkers(); got != 1 {
		t.Fatalf("repo_initialized events after first Open = %d, want 1", got)
	}

	// Second Open on the same remote: same project, no new marker.
	second := openTestLedger(t, dsn, remote)
	if second.ProjectID() != first.ProjectID() {
		t.Errorf("second Open resolved project %s, want the same project %s — a project's identity is its remote URL",
			second.ProjectID(), first.ProjectID())
	}
	if got := countMarkers(); got != 1 {
		t.Errorf("repo_initialized events after a second Open = %d, want still 1 — every process start would otherwise add a marker", got)
	}
}

// TestOpen_DistinctRemotesAreDistinctProjects is the control for the test above:
// without it, an Open that always returned one hard-coded project would satisfy
// every "same project" assertion.
func TestOpen_DistinctRemotesAreDistinctProjects(t *testing.T) {
	dsn, _ := newTestSchema(t)
	a := openTestLedger(t, dsn, "https://example.invalid/repo-a")
	b := openTestLedger(t, dsn, "https://example.invalid/repo-b")
	if a.ProjectID() == b.ProjectID() {
		t.Errorf("two different remotes resolved to the same project %s — projects would share a namespace in the log", a.ProjectID())
	}
}

// TestOpen_MigratesAutomatically pins that Open leaves a usable schema, so a
// first run on a fresh database does not need a separate manual migrate step
// before anything can be recorded.
func TestOpen_MigratesAutomatically(t *testing.T) {
	dsn, pool := newTestSchema(t)
	l := openTestLedger(t, dsn, "https://example.invalid/repo-migrate")

	if _, err := l.Emit(context.Background(), EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload: map[string]any{
			"agent_name": "finn", "agent_type": "engineer", "session_id": "s-1",
		},
	}); err != nil {
		t.Fatalf("Emit against a freshly opened Ledger: %v", err)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events e JOIN event_type_schemas s ON s.id = e.schema_id
		 WHERE s.name = 'run_started' AND e.project_id = $1`, l.ProjectID()).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("run_started events = %d, want 1", n)
	}
}
