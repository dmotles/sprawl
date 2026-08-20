//go:build store_pg

package store

import (
	"context"
	"fmt"
	"net/url"
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

// TestOpen_OnAMigratedSchemaCanAppend pins that Open on an already-migrated
// database yields a Ledger that works.
//
// Renamed from TestOpen_MigratesAutomatically, which was a misleading name even
// while it passed: newTestSchema migrates before calling Open, so the test never
// observed Open migrating anything and would have stayed green after Open
// stopped doing so. It asserts what it can actually see — append works — and the
// no-migration property is pinned separately and hermetically by
// TestOpen_DoesNotMigrate_SchemaReadinessIsCheckedInstead.
func TestOpen_OnAMigratedSchemaCanAppend(t *testing.T) {
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

// TestOpen_WorksAsTheLeastPrivilegeAppRole is the regression test for the defect
// that made the store's two goals mutually exclusive.
//
// THE DEFECT: Open called Migrate on every start, with the APPLICATION DSN.
// goose reads its own goose_db_version table on every Up() even with nothing
// pending, and sprawl_app holds nothing on that table — so the deployment shape
// 00002_m1a_app_role.sql documents in its own words ("the application must
// actually CONNECT as a user that inherits this role") produced
// "permission denied for table goose_db_version" (42501). That is a PgError,
// hence a refusal, hence not survivable, hence a hard error from Open, which
// store.Process caches in a sync.Once for the whole process lifetime, hence a
// nil emitter for every agent and every lifecycle event silently DROPPED.
//
// So the two states were mutually exclusive: an over-privileged DSN made AC3
// (append-only enforced by grants) a comment, and a least-privilege DSN made the
// store record nothing at all. There was no configuration in which both held.
//
// Neither e2e row could see it: both connect as the container superuser.
//
// This test connects as an actual LOGIN role inheriting sprawl_app — not via
// SET ROLE, because the defect was in a connection-time code path and SET ROLE
// happens after connecting.
func TestOpen_WorksAsTheLeastPrivilegeAppRole(t *testing.T) {
	dsn, pool := newTestSchema(t)
	ctx := context.Background()

	// A login role that inherits the app role, which is the documented shape.
	// Cluster-scoped like the app role itself, so the name is unique per test.
	loginUser := fmt.Sprintf("appuser_%d", pgSchemaNo.Add(1))
	if _, err := pool.Exec(ctx,
		fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD 'apppw'`, loginUser)); err != nil {
		t.Fatalf("create login role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, loginUser))
	})
	if _, err := pool.Exec(ctx, fmt.Sprintf(`GRANT %s TO %s`, appRole, loginUser)); err != nil {
		t.Fatalf("grant app role: %v", err)
	}
	// The app role needs its own search_path to find the per-test schema, and
	// CREATE on the schema is deliberately NOT granted.
	var schema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatalf("read current schema: %v", err)
	}

	appDSN := rewriteDSNCredentials(t, dsn, loginUser, "apppw")

	l, err := Open(ctx, LedgerConfig{
		Enabled:    true,
		DSN:        appDSN,
		DSNSource:  EnvDSN,
		RemoteURL:  "https://example.invalid/least-privilege",
		SprawlRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Open as a least-privilege app role must succeed; this is the defect: %v", err)
	}
	if l == nil {
		t.Fatal("Open returned no Ledger for a least-privilege role")
	}
	t.Cleanup(l.Close)
	if degraded := l.DegradedError(); degraded != nil {
		t.Fatalf("Open degraded against a reachable database: %v", degraded)
	}

	// And it can actually record: the whole point is that AC3's deployment and a
	// working store are simultaneously satisfiable.
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:    "run_started",
		TypeVersion: 1,
		Payload: map[string]any{
			"agent_name": "finn", "agent_type": "engineer", "session_id": "s-lp",
		},
	}); err != nil {
		t.Fatalf("a least-privilege Ledger must be able to append: %v", err)
	}

	// THE OTHER HALF, which is what makes this deployment worth having: this same
	// connection must be unable to rewrite history.
	if err := VerifyAppendOnly(ctx, l.Pool()); err != nil {
		t.Errorf("append-only is not enforced for the least-privilege connection, so this deployment buys nothing: %v", err)
	}
}

// rewriteDSNCredentials swaps the user and password in a testcontainers DSN,
// preserving host, port, database and every query parameter (search_path in
// particular, which is how each test reaches its isolated schema).
func rewriteDSNCredentials(t *testing.T, dsn, user, password string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}
