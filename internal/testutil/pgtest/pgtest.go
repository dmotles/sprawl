//go:build store_pg

package pgtest

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// One Postgres container is shared across the whole test binary; each caller of
// NewSchema gets an isolated, freshly-migrated schema (far cheaper than a
// container per test).
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

// NextID returns a suffix unique within this test binary. Callers that create
// their own cluster-scoped objects (roles, for instance, which are NOT
// schema-scoped and so are shared by every schema on the container) use it to
// avoid colliding with each other.
func NextID() int64 { return pgSchemaNo.Add(1) }

func startPG() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("sprawl"),
		tcpostgres.WithUsername("sprawl"),
		tcpostgres.WithPassword("sprawl"),
		// track_commit_timestamp is off by default and cannot be turned on at
		// runtime — it needs a server restart. It is what makes
		// pg_xact_commit_timestamp() non-NULL, and that function is the workflow
		// engine's atomicity assertion: it proves two rows were written by ONE
		// commit. The obvious alternative, comparing xmin, is a documented trap
		// (QUM-1252): a SAVEPOINT subtransaction gets its own xid, so xmin
		// differs by one on a perfectly atomic write, and it can equally match
		// falsely for two rows rewritten by one later transaction.
		testcontainers.WithCmdArgs("-c", "track_commit_timestamp=on"),
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

// SkipOrFatal implements the SPRAWL_STORE_PG_REQUIRED contract: when the caller
// has declared that Postgres MUST be available, an unavailable container is a
// setup failure reported in the failure class, not a skip. A skip that can
// happen for any reason is indistinguishable from a skip that means "no Docker".
func SkipOrFatal(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("SPRAWL_STORE_PG_REQUIRED") == "1" {
		t.Fatalf("SPRAWL_STORE_PG_REQUIRED=1 but Postgres is unavailable — this is a SETUP FAILURE, not a skip: %s", reason)
	}
	t.Skip(reason)
}

// NewSchema provisions an isolated schema on the shared container, runs migrate
// against it, and returns its DSN plus a pool bound to it.
//
// migrate is a parameter rather than a direct call to store.Migrate so that this
// package does not import internal/store — the store's own in-package test files
// call NewSchema, and that import would be a cycle.
func NewSchema(t *testing.T, migrate func(ctx context.Context, dsn string) error) (string, *pgxpool.Pool) {
	t.Helper()
	pgOnce.Do(startPG)
	if pgSkip != "" {
		SkipOrFatal(t, pgSkip)
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

	if err := migrate(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return dsn, pool
}
