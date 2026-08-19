//go:build store_pg

// Postgres integration suite for the event-log store (QUM-1249, M1a).
//
// Build-tagged OFF by default: `make validate` stays Docker-free. Run it with
//
//	make test-store-pg
//	# or: go test -tags store_pg -count=1 ./internal/store/
//
// `-count=1` is not decoration. A Docker-down run t.Skip's, which Go caches as
// a passing package result; without the cache bypass that skip replays as green
// after Docker comes back and the suite reports coverage it never measured.
//
// Docker-down is a Go-level t.Skip here (a skipped Go test exits 0), so the
// "never exit 0 when Docker/PG is unavailable" obligation is discharged by the
// shell row that wraps this suite: scripts/e2e-tests/store-pg-integration.sh.
// That row sets SPRAWL_STORE_PG_REQUIRED=1, which turns every skip below into a
// hard failure — so a container that fails to start for a reason OTHER than
// "no Docker" is reported as the setup failure it is instead of being folded
// into a silent skip.
package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// One Postgres container is shared across the whole suite; each subtest gets an
// isolated, freshly-migrated schema (far cheaper than a container per test).
// Pattern lifted from internal/hub/store/pg_test.go.
//
// The image is plain postgres:16-alpine, NOT a pgvector build: the M1a schema
// has no vector columns (event_embeddings is M4), so requiring the extension
// would add an image pull and a failure mode for nothing.
var (
	pgOnce     sync.Once
	pgBaseDSN  string
	pgSkip     string
	pgSchemaNo atomic.Int64
)

func startPG() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("sprawl"),
		tcpostgres.WithUsername("sprawl"),
		tcpostgres.WithPassword("sprawl"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		pgSkip = "postgres testcontainer did not start: " + err.Error()
		return
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		pgSkip = "postgres connection string: " + err.Error()
		return
	}
	pgBaseDSN = dsn
	// Container is intentionally left running; process exit (and Ryuk) reaps it.
}

// skipOrFatal implements the SPRAWL_STORE_PG_REQUIRED contract: when the caller
// has declared that Postgres MUST be available, an unavailable container is a
// setup failure reported in the failure class, not a skip. A skip that can
// happen for any reason is indistinguishable from a skip that means "no Docker".
func skipOrFatal(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("SPRAWL_STORE_PG_REQUIRED") == "1" {
		t.Fatalf("SPRAWL_STORE_PG_REQUIRED=1 but Postgres is unavailable — this is a SETUP FAILURE, not a skip: %s", reason)
	}
	t.Skip(reason)
}

// newTestSchema provisions an isolated, migrated schema on the shared container
// and returns its DSN plus a pool bound to it.
func newTestSchema(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	pgOnce.Do(startPG)
	if pgSkip != "" {
		skipOrFatal(t, pgSkip)
	}

	schema := fmt.Sprintf("t_%d", pgSchemaNo.Add(1))
	ctx := context.Background()

	admin, err := pgxpool.New(ctx, pgBaseDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	admin.Close()

	// pgx treats unknown DSN keywords as server runtime parameters, so
	// appending search_path pins every connection (pool + goose's stdlib
	// handle) to the isolated schema.
	dsn := pgBaseDSN + "&search_path=" + schema

	if err := Migrate(ctx, dsn); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return dsn, pool
}

// pgCode extracts the SQLSTATE from a pgx error, or "" if there is none.
//
// Every assertion about a database refusal keys on this rather than on the
// error text. Error text is lc_messages-dependent and, worse, is satisfiable by
// anything that chooses to print the same words — a BEFORE UPDATE trigger
// raising 'permission denied: append-only' would pass a substring predicate
// while proving nothing about grants.
func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// SQLSTATEs asserted on below.
const (
	sqlStateInsufficientPrivilege = "42501"
	sqlStateUniqueViolation       = "23505"
	sqlStateForeignKeyViolation   = "23503"
	sqlStateNotNullViolation      = "23502"
)

// m1aTables is the exact set of tables Appendix A prescribes for M1a. The
// migrate test asserts SET EQUALITY against it, not mere containment: a list of
// anticipated out-of-scope names would miss an unanticipated one, and scope
// creep is by definition what nobody anticipated.
var m1aTables = []string{
	"agent_cards",
	"agent_sessions",
	"artifacts",
	"event_claims",
	"event_type_schemas",
	"events",
	"open_contracts",
	"projects",
	"workflow_defs",
	"workflow_instances",
}

// gooseVersionTable is goose's own bookkeeping table, excluded from the
// set-equality comparison because it is not ours.
const gooseVersionTable = "goose_db_version"

// baseTableNames returns the BASE TABLES (relkind 'r') in the current schema.
//
// It deliberately does NOT use information_schema.tables unfiltered, which also
// lists views: `open_contracts` as a VIEW over the Appendix A anti-join is the
// obvious shortcut a future migration might take, and it would satisfy a
// presence check while destroying the "maintained in the appender txn"
// property the whole projection exists for.
func baseTableNames(t *testing.T, pool *pgxpool.Pool) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT c.relname FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = current_schema() AND c.relkind = 'r'`)
	if err != nil {
		t.Fatalf("query pg_class: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return got
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMigrate_CreatesExactlyTheM1ATables asserts set equality between the
// migrated schema's base tables and Appendix A's M1a set.
//
// Set equality rather than presence-plus-a-denylist: it subsumes "no
// event_embeddings / entities / facts / fact_provenance" (M4/M6 scope creep)
// AND catches a table nobody thought to deny. The missing-tables leg is
// t.Fatalf rather than t.Errorf on purpose — it makes the extra-tables leg
// STRUCTURALLY unreachable on a dead instrument, instead of leaving a reader to
// notice that an adjacent assertion also failed.
func TestMigrate_CreatesExactlyTheM1ATables(t *testing.T) {
	_, pool := newTestSchema(t)
	got := baseTableNames(t, pool)
	delete(got, gooseVersionTable)

	want := map[string]bool{}
	for _, n := range m1aTables {
		want[n] = true
	}

	var missing []string
	for n := range want {
		if !got[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("migration did not create %d of the %d Appendix A M1a base tables: %v (got: %v)",
			len(missing), len(m1aTables), missing, sortedKeys(got))
	}

	var extra []string
	for n := range got {
		if !want[n] {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Errorf("migration created %d table(s) outside M1a scope: %v — event_embeddings/entities/facts/fact_provenance belong to M4/M6, and anything else is unplanned scope creep",
			len(extra), extra)
	}
}

// columnFingerprint renders every column of every base table as a stable,
// comparable string: table, column, type, nullability.
func columnFingerprint(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT table_name, column_name, data_type, is_nullable
		 FROM information_schema.columns
		 WHERE table_schema = current_schema()
		 ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("query information_schema.columns: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var tbl, col, typ, nullable string
		if err := rows.Scan(&tbl, &col, &typ, &nullable); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintf(&b, "%s.%s %s null=%s\n", tbl, col, typ, nullable)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if b.Len() == 0 {
		t.Fatal("column fingerprint is empty — the instrument saw no columns at all, so any comparison against it is vacuous")
	}
	return b.String()
}

// TestMigrate_IdempotentSequential pins that re-running migrations against an
// already-migrated schema changes nothing.
//
// It compares a full column fingerprint, not a table COUNT: a count is equally
// satisfied by a re-run that dropped one table and created another, or that
// altered a column's type, which is precisely the class of damage an
// accidentally-rerunnable migration does. Scope note: this asserts the
// SEQUENTIAL property only. Two processes racing goose Up on one schema is a
// separate hazard (goose takes no advisory lock) and is NOT asserted here.
func TestMigrate_IdempotentSequential(t *testing.T) {
	dsn, pool := newTestSchema(t)
	before := columnFingerprint(t, pool)
	for i := 0; i < 2; i++ {
		if err := Migrate(context.Background(), dsn); err != nil {
			t.Fatalf("Migrate re-run %d: %v", i+1, err)
		}
	}
	if after := columnFingerprint(t, pool); before != after {
		t.Errorf("re-running migrations changed the schema.\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestMigrate_TwoSchemasOnOneCluster asserts that migrating a SECOND schema on
// the same cluster succeeds.
//
// This is an explicit assertion rather than an emergent property because of a
// scope mismatch that will otherwise surface as confusing flakiness: goose's
// bookkeeping is per-schema, but CREATE ROLE is CLUSTER-scoped. A migration
// that creates the app role non-idempotently migrates the first schema fine and
// then fails for every subsequent one — which reads as "the second test is
// flaky", not as "the migration is not re-runnable on a shared cluster".
func TestMigrate_TwoSchemasOnOneCluster(t *testing.T) {
	_, poolA := newTestSchema(t)
	_, poolB := newTestSchema(t)
	for name, pool := range map[string]*pgxpool.Pool{"first": poolA, "second": poolB} {
		got := baseTableNames(t, pool)
		if !got["events"] {
			t.Errorf("%s schema on the shared cluster has no events table — the migration is not re-runnable across schemas", name)
		}
	}
}
