//go:build store_pg

package store

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// appRole is the least-privilege role the application connects as in
// production. Append-only on `events` is enforced by ITS grants, not by
// application code — see migrations/00002_m1a_app_role.sql.
const appRole = "sprawl_app"

// asAppRole runs fn on a connection that has assumed appRole via SET ROLE.
//
// SET ROLE rather than a separate login: the role is NOLOGIN by design (a
// deployment's DBA grants it to whatever login user it provisions — QUM-1259),
// so there is no password to connect with. Under SET ROLE, current_user becomes
// appRole and both superuser and ownership bypass drop, so the table-privilege
// check is identical to the one a login user with INHERIT membership hits.
//
// What this does NOT establish, so that nobody over-reads it later:
//   - It is not a containment claim. A SET ROLE session can RESET ROLE and
//     regain owner rights. Irrelevant to AC3, fatal if cited as sandboxing.
//   - It says nothing about the production login user — whether that user is a
//     superuser, owns these tables, or holds privileges beyond appRole. That is
//     QUM-1259's deployment concern. This proves a property of the ROLE.
//   - Future tables are not covered: a later migration adding a table with no
//     grant is invisible here. The catalog assertion below is what bounds
//     today's grants; ALTER DEFAULT PRIVILEGES is not asserted at all.
//
// RESET ROLE is DEFERRED, before the Release. If fn calls t.Fatalf,
// runtime.Goexit unwinds and a non-deferred reset would be skipped — pgxpool
// does not issue DISCARD ALL on release, so the connection would re-enter the
// pool still acting as appRole and a later owner-side statement would silently
// run under-privileged.
func asAppRole(t *testing.T, pool *pgxpool.Pool, fn func(ctx context.Context, conn *pgx.Conn)) {
	t.Helper()
	ctx := context.Background()

	// The owner must be a member of the role to assume it. In production the
	// DBA does this; keeping it test-side means the migration never has to hand
	// the app role to whatever user happens to run it.
	if _, err := pool.Exec(ctx, `GRANT `+appRole+` TO CURRENT_USER`); err != nil {
		t.Fatalf("grant %s to current_user: %v", appRole, err)
	}

	c, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() {
		_, _ = c.Exec(ctx, `RESET ROLE`)
		c.Release()
	}()
	if _, err := c.Exec(ctx, `SET ROLE `+appRole); err != nil {
		t.Fatalf("SET ROLE %s: %v", appRole, err)
	}
	fn(ctx, c.Conn())
}

// assertRefusedAsPrivilege asserts that err is Postgres refusing for want of a
// privilege ON `events`, and not for any other reason.
//
// It keys on SQLSTATE 42501 plus the object name, never on the message text.
// Both halves matter. A substring match on "permission denied" is satisfied by
// a BEFORE UPDATE trigger doing RAISE EXCEPTION 'permission denied: events are
// append-only' (SQLSTATE P0001) — which would make a test named
// "EnforcedByGrants" green while no grant enforced anything — and it is also
// satisfied by a denial on a different object entirely ("permission denied for
// schema t_7"), which the LIVE control below would catch but the predicate
// should not depend on it doing so. Message text is additionally
// lc_messages-dependent, an undeclared dependency of any text assertion.
func assertRefusedAsPrivilege(t *testing.T, verb string, err error) {
	t.Helper()
	if got := pgCode(err); got != sqlStateInsufficientPrivilege {
		t.Errorf("%s on events as %s: SQLSTATE %q (err=%v), want %s (insufficient_privilege) — a refusal with any other code is not the GRANT doing the work",
			verb, appRole, got, err, sqlStateInsufficientPrivilege)
		return
	}
	if err != nil && !strings.Contains(err.Error(), "events") {
		t.Errorf("%s refused with %s but the error does not name `events` (%v) — the denial may be about a different object",
			verb, sqlStateInsufficientPrivilege, err)
	}
}

// TestEvents_AppendOnlyEnforcedByGrants is AC3: as the app role, UPDATE,
// DELETE, and TRUNCATE on `events` must be refused by Postgres itself.
//
// TRUNCATE is asserted alongside the other two because it is a SEPARATE
// privilege that defeats append-only completely. `GRANT ALL` would be caught by
// the UPDATE leg, but a hand-written `GRANT SELECT, INSERT, TRUNCATE` would not.
//
// Three positive controls, all in the same run, because a refusal on its own is
// worthless evidence — a dead connection, a mistyped table, or a role with no
// grants at all refuses everything:
//
//   - LIVE (must succeed): the app role can SELECT and INSERT. Rules out "the
//     role reaches nothing" and proves the refusals are specific to the
//     mutating verbs rather than blanket.
//   - AIM/update (must succeed): the OWNER runs the identical UPDATE and it
//     affects exactly 1 row. Rules out a malformed statement or an absent
//     target row.
//   - AIM/delete (must succeed, then rolled back): the same for DELETE, so the
//     DELETE leg does not borrow its aim from the UPDATE leg. Rolled back so it
//     does not consume the row the other assertions depend on.
func TestEvents_AppendOnlyEnforcedByGrants(t *testing.T) {
	_, pool := newTestSchema(t)
	f := seedFixture(t, pool)
	eventID, _ := insertEvent(t, pool, f)
	ctx := context.Background()

	asAppRole(t, pool, func(ctx context.Context, conn *pgx.Conn) {
		// LIVE control.
		var seen uuid.UUID
		if err := conn.QueryRow(ctx, `SELECT id FROM events WHERE id = $1`, eventID).Scan(&seen); err != nil {
			t.Fatalf("app role cannot SELECT the seeded event — the role is not wired to this schema, so no refusal below would mean anything: %v", err)
		}
		if _, err := conn.Exec(ctx,
			`INSERT INTO events (id, project_id, workflow_instance_id, schema_id, payload)
			 VALUES ($1, $2, $3, $4, '{}'::jsonb)`,
			uuid.New(), f.projectID, uuid.New(), f.schemaID); err != nil {
			t.Fatalf("app role must be able to INSERT into events — append-only means append-ONLY, not read-only: %v", err)
		}

		_, err := conn.Exec(ctx, `UPDATE events SET payload = '{"tampered":true}'::jsonb WHERE id = $1`, eventID)
		assertRefusedAsPrivilege(t, "UPDATE", err)

		_, err = conn.Exec(ctx, `DELETE FROM events WHERE id = $1`, eventID)
		assertRefusedAsPrivilege(t, "DELETE", err)

		_, err = conn.Exec(ctx, `TRUNCATE events`)
		assertRefusedAsPrivilege(t, "TRUNCATE", err)
	})

	// AIM control, UPDATE.
	tag, err := pool.Exec(ctx, `UPDATE events SET payload = '{"owner_control":true}'::jsonb WHERE id = $1`, eventID)
	if err != nil {
		t.Fatalf("owner UPDATE control failed — the statement itself is wrong, so the app-role refusal proves nothing: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Errorf("owner UPDATE control affected %d rows, want 1 — the app-role UPDATE may have been aimed at a row that does not exist", tag.RowsAffected())
	}

	// AIM control, DELETE — rolled back so the row survives.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin owner delete control: %v", err)
	}
	dtag, err := tx.Exec(ctx, `DELETE FROM events WHERE id = $1`, eventID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("owner DELETE control failed — the DELETE statement itself is wrong, so the app-role refusal proves nothing: %v", err)
	}
	if dtag.RowsAffected() != 1 {
		t.Errorf("owner DELETE control affected %d rows, want 1", dtag.RowsAffected())
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback owner delete control: %v", err)
	}
}

// TestEvents_AppRoleGrantCatalogIsExactlySelectInsert is the second, mechanically
// different instrument on the same property.
//
// The behavioural test above cannot see a COLUMN-level grant: Postgres grants
// UPDATE per-column, so `GRANT UPDATE (owner_agent_id) ON events TO sprawl_app`
// — a plausible future change, since owner_agent_id is the "who to notify"
// field somebody will eventually want to reassign — still refuses an UPDATE of
// `payload` with 42501 and leaves that test green while `events` is no longer
// append-only. Only the catalog answers "what CAN this role do".
func TestEvents_AppRoleGrantCatalogIsExactlySelectInsert(t *testing.T) {
	_, pool := newTestSchema(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx,
		`SELECT DISTINCT privilege_type FROM information_schema.role_table_grants
		 WHERE grantee = $1 AND table_schema = current_schema() AND table_name = 'events'`,
		appRole)
	if err != nil {
		t.Fatalf("query role_table_grants: %v", err)
	}
	var privs []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		privs = append(privs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(privs)

	want := "INSERT,SELECT"
	if got := strings.Join(privs, ","); got != want {
		t.Errorf("%s holds table privileges [%s] on events, want exactly [%s] — any mutating privilege defeats append-only",
			appRole, got, want)
	}

	// Column-level grants are a separate catalog. A table-level assertion alone
	// does not see them.
	var colUpdates int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.column_privileges
		 WHERE grantee = $1 AND table_schema = current_schema() AND table_name = 'events'
		   AND privilege_type IN ('UPDATE','DELETE','TRUNCATE')`,
		appRole).Scan(&colUpdates); err != nil {
		t.Fatalf("query column_privileges: %v", err)
	}
	if colUpdates != 0 {
		t.Errorf("%s holds %d column-level mutating privilege(s) on events, want 0 — a column grant defeats append-only while the behavioural test stays green",
			appRole, colUpdates)
	}
}

// TestOpenContracts_AppRoleInsertsAndDeletesInOneTransaction pins the
// deliberate asymmetry: `events` denies DELETE, `open_contracts` must allow it.
//
// The projection is maintained in the SAME transaction as the append (Appendix
// A: "insert on open, delete on close, same txn as append"), so this asserts the
// insert-then-delete round trip inside one transaction rather than two loose
// statements — that transaction is the production shape, and a privilege that
// worked statement-by-statement but not in-transaction would break every close
// event while looking fine.
//
// Without this assertion, "harden the app role to no DELETE anywhere" reads as
// a safe tightening and silently breaks the close path.
func TestOpenContracts_AppRoleInsertsAndDeletesInOneTransaction(t *testing.T) {
	_, pool := newTestSchema(t)
	f := seedFixture(t, pool)
	eventID, _ := insertEvent(t, pool, f)

	asAppRole(t, pool, func(ctx context.Context, conn *pgx.Conn) {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx,
			`INSERT INTO open_contracts (event_id, workflow_instance_id, opened_at)
			 VALUES ($1, $2, now())`, eventID, uuid.New()); err != nil {
			t.Fatalf("app role must be able to INSERT into open_contracts: %v", err)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM open_contracts WHERE event_id = $1`, eventID)
		if err != nil {
			t.Fatalf("app role must be able to DELETE from open_contracts in the same txn as the append — every close event depends on it: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Errorf("DELETE from open_contracts affected %d rows, want 1 — a DELETE matching nothing would also raise no error", tag.RowsAffected())
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	})
}
