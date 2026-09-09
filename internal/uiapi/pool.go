package uiapi

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGEnvVars are the connection settings the read API is configured with, in the
// order a diagnostic should list them.
//
// Discrete libpq variables rather than one composite DSN (QUM-1350 D10): the
// password is injected as its own secret and rotated on its own, so nothing has
// to rebuild a URL — and no log line, flag value or error string can carry the
// password by accident, because the password is never part of a value this
// process assembles.
var PGEnvVars = []string{"PGHOST", "PGDATABASE", "PGUSER", "PGSSLMODE", "PGPASSWORD"}

// RequiredPGEnvVars must be set explicitly.
//
// PGSSLMODE and PGPASSWORD are deliberately absent: sslmode has a defensible
// libpq default, and a password is not required against a trust/peer-auth local
// database. The other three are required because libpq's defaults for them are
// silently plausible — an unset PGHOST means a unix socket, and an unset
// PGUSER/PGDATABASE means the OS user's name. A misconfigured container would
// therefore not fail; it would connect somewhere else, or fail with a socket
// path that tells the operator nothing about which variable was missing.
var RequiredPGEnvVars = []string{"PGHOST", "PGDATABASE", "PGUSER"}

// MissingPGEnv returns the required variables that getenv reports as empty.
func MissingPGEnv(getenv func(string) string) []string {
	var missing []string
	for _, name := range RequiredPGEnvVars {
		if getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

// StatementTimeout and MaxConns bound what this API can cost the database.
//
// The read API is unauthenticated by design and it reads the SAME cluster the
// sprawl runtime's event log depends on, while /api/usage, /api/fleet and
// /api/workflows are GROUP BYs over the whole events table — LIMIT bounds the
// rows returned, not the rows grouped. So an unbounded query pins a backend for
// as long as the scan takes, and pgx's default MaxConns of max(4, NumCPU) lets a
// few open browser tabs hold that many at once. The failure that matters is not
// a slow UI; it is the system of record degrading because someone left a tab
// open. Both bounds are therefore deliberately mean: a read-only dashboard that
// cannot answer within ten seconds should report an error, not queue.
const (
	StatementTimeout = "10s"
	MaxConns         = int32(4)
)

// poolConfig resolves the PG* environment into a pool configuration carrying
// those bounds.
//
// The empty connection string is not an oversight: it makes pgx resolve every
// setting from the environment, which is the whole point of the discrete-vars
// shape. Nothing here reads PGPASSWORD itself.
func poolConfig() (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig("")
	if err != nil {
		// The error is not wrapped: pgx echoes the resolved connection string
		// on a parse failure, and that string carries PGPASSWORD.
		return nil, fmt.Errorf("uiapi: the PG* connection settings could not be parsed")
	}
	// A startup RuntimeParam rather than a per-query SET: it applies to every
	// connection the pool ever opens, including ones opened after a restart of
	// the database, and no future query can forget it.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = StatementTimeout
	cfg.MaxConns = MaxConns
	return cfg, nil
}

// OpenPool connects using the ambient PG* environment and proves the connection
// works before returning.
//
// pgxpool.NewWithConfig is lazy — it acquires nothing — so without the Ping a
// completely unreachable database produces a process that boots cleanly,
// reports healthy, and fails on the first request. Boot is where a bad
// connection should be discovered.
func OpenPool(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := poolConfig()
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("uiapi: the PG* connection settings could not be parsed")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("uiapi: the read database is unreachable: %w", err)
	}
	return pool, nil
}
