//go:build store_pg

// The committed proof that OpenPool's bounds reach the server.
//
// pool_test.go asserts the two values on the *config struct*, which is a
// necessary check and an insufficient one: it passes just as happily if the
// RuntimeParams key is misspelled (pgx forwards unknown keys as startup
// parameters and Postgres ignores what it does not recognise), or if a later
// change moves the timeout to a per-query SET that some new query forgets. Only
// a real server can answer "is a runaway query actually killed?" — so this file
// asks it.
//
// Behind `store_pg` and using the shared pgtest container, matching the
// event-log and engine suites: Docker-dependent, so out of `make validate`, and
// gated by scripts/e2e-tests/store-pg-integration.sh, which sets
// SPRAWL_STORE_PG_REQUIRED=1 so a missing container fails rather than skips.
package uiapi

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/testutil/pgtest"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sqlStateQueryCanceled is what Postgres reports when statement_timeout fires.
const sqlStateQueryCanceled = "57014"

// noMigrate is passed to pgtest.NewSchema because these tests need a live
// server, not the event-log schema: nothing here reads a sprawl table, and
// importing internal/store to migrate one would buy nothing but a dependency.
func noMigrate(context.Context, string) error { return nil }

// openBoundPool points the PG* environment at a throwaway schema and opens the
// production pool against it.
func openBoundPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, _ := pgtest.NewSchema(t, noMigrate)

	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse pgtest dsn: %v", err)
	}
	// OpenPool resolves everything from the environment by design, so the
	// environment is how a test aims it at the container.
	t.Setenv("PGHOST", cfg.Host)
	t.Setenv("PGPORT", strconv.Itoa(int(cfg.Port)))
	t.Setenv("PGUSER", cfg.User)
	t.Setenv("PGPASSWORD", cfg.Password)
	t.Setenv("PGDATABASE", cfg.Database)
	t.Setenv("PGSSLMODE", "disable")

	pool, err := OpenPool(context.Background())
	if err != nil {
		t.Fatalf("OpenPool against the pgtest container: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestOpenPool_StatementTimeoutKillsARunawayQuery(t *testing.T) {
	pool := openBoundPool(t)
	ctx := context.Background()

	// First the server's own view of the setting, which localises a failure:
	// a wrong value here means the parameter did not arrive, whereas a wrong
	// value below means it arrived and did not bite.
	var shown string
	if err := pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&shown); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if shown != StatementTimeout {
		t.Errorf("server reports statement_timeout=%q, want %q — the startup parameter did not reach Postgres", shown, StatementTimeout)
	}

	// Then the property that matters. pg_sleep is the cheapest stand-in for the
	// real hazard (an unindexed GROUP BY over the whole events table), and it
	// must be comfortably longer than the bound so a slow host cannot make this
	// pass for the wrong reason.
	start := time.Now()
	_, err := pool.Exec(ctx, "SELECT pg_sleep(30)")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("SELECT pg_sleep(30) COMPLETED after %s — an unauthenticated request can pin a backend on the cluster the runtime's event log depends on", elapsed)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != sqlStateQueryCanceled {
		t.Fatalf("pg_sleep(30) failed after %s with %v — want SQLSTATE %s (statement timeout). Another error means the query died of something other than the bound under test", elapsed, err, sqlStateQueryCanceled)
	}
	// A cancellation that took a minute is not the bound we documented.
	if elapsed > 25*time.Second {
		t.Errorf("statement was cancelled, but only after %s — the effective bound is not %s", elapsed, StatementTimeout)
	}
}

// The negative control for the test above: a query that finishes inside the
// bound must be left alone. A timeout of zero would satisfy "runaway queries are
// killed" while making the API useless.
func TestOpenPool_StatementTimeoutLeavesAFastQueryAlone(t *testing.T) {
	pool := openBoundPool(t)
	var one int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&one); err != nil {
		t.Fatalf("SELECT 1 under a %s timeout: %v", StatementTimeout, err)
	}
	if one != 1 {
		t.Fatalf("SELECT 1 = %d", one)
	}
}

func TestOpenPool_MaxConnsBoundsTheBackendsOneAPICanHold(t *testing.T) {
	pool := openBoundPool(t)

	for i := 1; i <= int(MaxConns); i++ {
		conn, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatalf("acquiring connection %d of the permitted %d: %v", i, MaxConns, err)
		}
		defer conn.Release()
	}

	// One past the cap must block rather than open another backend. A short
	// deadline distinguishes "blocked" from "granted"; it is not a latency
	// assertion, so it does not need to be generous.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err == nil {
		conn.Release()
		t.Fatalf("acquired connection %d despite MaxConns=%d — enough open browser tabs could starve the runtime's connection budget", MaxConns+1, MaxConns)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection %d failed with %v, want a wait that exceeds the deadline — a different error means the cap is enforced by something other than queueing", MaxConns+1, err)
	}
}
