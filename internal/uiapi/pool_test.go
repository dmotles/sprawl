package uiapi

import "testing"

func TestPoolConfig_BoundsTheQueryAndTheConnectionCount(t *testing.T) {
	t.Setenv("PGHOST", "127.0.0.1")
	t.Setenv("PGDATABASE", "sprawl")
	t.Setenv("PGUSER", "sprawl_ro")

	cfg, err := poolConfig()
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}

	// Without this the three aggregate endpoints are unauthenticated,
	// unindexed full scans with no upper bound: one browser tab pins a
	// backend for as long as the scan takes, on the same cluster the sprawl
	// runtime's event log depends on.
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != StatementTimeout {
		t.Errorf("statement_timeout = %q, want %q — an unbounded scan can pin a backend on the runtime's own cluster", got, StatementTimeout)
	}

	// And without this a handful of tabs starve the pool, which starves the
	// system of record rather than merely the UI.
	if cfg.MaxConns != MaxConns {
		t.Errorf("MaxConns = %d, want %d — pgx defaults to max(4, NumCPU), which lets the read API monopolise the cluster's connection budget", cfg.MaxConns, MaxConns)
	}
}

// The bounds are only honest if they are small enough to leave the runtime room.
// A statement_timeout of an hour and a MaxConns of 500 would satisfy the test
// above while fixing nothing.
func TestPoolBounds_AreSmallEnoughToProtectTheRuntime(t *testing.T) {
	if MaxConns > 8 {
		t.Errorf("MaxConns = %d: too large to bound a read-only browser API against a shared cluster", MaxConns)
	}
	if StatementTimeout != "10s" {
		t.Errorf("StatementTimeout = %q: a bound long enough to outlast an operator's patience is not a bound", StatementTimeout)
	}
}
