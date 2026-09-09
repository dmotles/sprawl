// Package uiapi is the read-only HTTP surface over the shared Postgres event
// log (QUM-1349).
//
// It is a separate deployable from `hubd`, which talks to a DIFFERENT database
// (internal/hub/store, SPRAWL_HUB_DSN) with a disjoint schema and an
// authenticating posture. Nothing here reads or writes the hub database.
//
// # The write seam
//
// v1 is SELECT-only and the DB role enforces that (migration
// 00006_ro_role.sql). A later read-WRITE slice adds an answer-the-inbox
// path, and it MUST go through the shared Go appender in internal/store —
// store.Ledger / store.Appender own schema pinning, the open_contracts close
// and the notify doorbell, and raw SQL from a handler bypasses all three
// silently. This package is shaped so that lands without moving any read code:
//
//   - The SQL lives behind reader interfaces (EventReader), implemented over a
//     narrow Pool. Handlers hold interfaces, never a pool.
//   - Config carries the readers. A write handler gets a *store.Ledger added
//     alongside them; no existing field changes meaning.
//   - Pool deliberately exposes no Exec and no Begin. A write path cannot be
//     bolted on through this seam by accident — it has to be added
//     deliberately, through the appender, in a diff that says so.
package uiapi

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Pool is the narrow Postgres surface the read API needs. *pgxpool.Pool
// satisfies it.
//
// Query/QueryRow/Ping and NOTHING ELSE, on purpose — see the package comment.
// It exists so handlers and readers can be asserted without a database, and so
// the absence of a write verb is a compile-time property rather than a habit.
type Pool interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Ping(ctx context.Context) error
}
