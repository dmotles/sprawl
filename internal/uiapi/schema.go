package uiapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/dmotles/sprawl/internal/store"
)

// ErrSchemaNotMigrated means the database the read API was pointed at does not
// carry the event-log schema.
var ErrSchemaNotMigrated = errors.New("uiapi: the configured database has not been migrated")

// requiredTables is what the read API needs to serve anything at all: the
// ledger, and the type table every view resolves an event's name through.
//
// Deliberately NOT the whole M1a table set. This is a boot precondition, not a
// schema audit — store's own migrate tests own set equality — and listing all
// ten here would make the API refuse to boot against a database that is
// perfectly able to serve it.
var requiredTables = []string{"events", "event_type_schemas"}

// VerifySchema reports whether the connected database carries the tables the
// read API reads.
//
// This is the read API's REPLACEMENT for running migrations, and the reason it
// exists is that it must not run them. It connects as a SELECT-only role, so a
// migrate would fail anyway — but it would fail deep inside goose, at whichever
// DDL statement came first, with a permission error rather than a diagnosis.
// Worse, a deployment whose DSN was accidentally over-privileged WOULD succeed,
// and a browser-reachable process silently owning the schema of the shared
// event log is the one outcome most worth making impossible.
//
// So the schema is a PRECONDITION, checked at boot and reported with the
// command that fixes it. Callers must treat a failure as fatal: unlike
// store.VerifyAppendOnly's advisory warning, there is nothing useful to serve.
//
// to_regclass rather than a `SELECT 1 FROM events LIMIT 1` probe: an empty
// table and a missing one are different problems, and only one of them is this
// function's business. It resolves against search_path, so it is correct for
// the per-schema deployments the migrations support.
func VerifySchema(ctx context.Context, pool Pool) error {
	for _, table := range requiredTables {
		var present bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&present); err != nil {
			return fmt.Errorf("uiapi: checking for table %q: %w", table, err)
		}
		if !present {
			return &store.HintError{
				Err:  fmt.Errorf("%w: table %q does not exist", ErrSchemaNotMigrated, table),
				Hint: "migrate the database from an ADMIN dsn first — `SPRAWL_DB_DSN=<admin dsn> sprawl store migrate`. The read API never migrates: it connects as a login user inheriting sprawl_ro, which is SELECT-only",
			}
		}
	}
	return nil
}
