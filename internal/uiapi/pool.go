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

// OpenPool connects using the ambient PG* environment and proves the connection
// works before returning.
//
// The empty connection string is not an oversight: it makes pgx resolve every
// setting from the environment, which is the whole point of the discrete-vars
// shape. Nothing here reads PGPASSWORD itself.
//
// pgxpool.New is lazy — it parses and acquires nothing — so without the Ping a
// completely unreachable database produces a process that boots cleanly,
// reports healthy, and fails on the first request. Boot is where a bad
// connection should be discovered.
func OpenPool(ctx context.Context) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		// The error is not wrapped: pgx echoes the resolved connection string
		// on a parse failure, and that string carries PGPASSWORD.
		return nil, fmt.Errorf("uiapi: the PG* connection settings could not be parsed")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("uiapi: the read database is unreachable: %w", err)
	}
	return pool, nil
}
