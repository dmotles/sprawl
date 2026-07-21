package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// One Postgres container is shared across the whole package's pg-arm subtests;
// each subtest gets an isolated, freshly-migrated schema (far cheaper than a
// container per test). The arm SKIPS cleanly (never fails) when Docker is
// unavailable, so `make test` stays green on CI without a Docker daemon.
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
		tcpostgres.WithDatabase("hub"),
		tcpostgres.WithUsername("hub"),
		tcpostgres.WithPassword("hub"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		pgSkip = "postgres testcontainer unavailable (Docker down?): " + err.Error()
		return
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		pgSkip = "postgres connection string: " + err.Error()
		return
	}
	pgBaseDSN = dsn
	// Container is intentionally left running; the test process exit (and Ryuk)
	// reaps it. Terminating here would race concurrent package subtests.
}

// newPGStoreForTest provisions an isolated schema on the shared container and
// returns a migrated pgStore bound to it. Skips when Docker is unavailable.
func newPGStoreForTest(t *testing.T) Store {
	t.Helper()
	pgOnce.Do(startPG)
	if pgSkip != "" {
		t.Skip(pgSkip)
	}

	schema := fmt.Sprintf("t_%d", pgSchemaNo.Add(1))
	ctx := context.Background()

	// Create the isolated schema via a short-lived pool on the base DSN.
	admin, err := pgxpool.New(ctx, pgBaseDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	admin.Close()

	// pgx treats unknown DSN keywords as server runtime parameters, so appending
	// search_path pins every connection (pool + goose's stdlib handle) to the
	// isolated schema.
	dsn := pgBaseDSN + "&search_path=" + schema

	st, err := NewPGStore(ctx, PGConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func init() {
	storeFactories = append(storeFactories, storeFactory{
		name: "pg",
		new:  newPGStoreForTest,
	})
}
