package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrAppendOnlyNotEnforced means the connection in use can UPDATE, DELETE or
// TRUNCATE `events`.
var ErrAppendOnlyNotEnforced = errors.New("store: the connection in use can modify or delete events, so the log is not append-only")

// VerifyAppendOnly asks Postgres whether the CURRENT connection can mutate
// `events`, and reports it as an error if so.
//
// This closes the one hole the migration cannot. The GRANTs in
// 00002_m1a_app_role.sql make `events` append-only only if the application
// actually CONNECTS as a user inheriting sprawl_app. A DSN carrying owner or
// superuser credentials bypasses every REVOKE — and the integration tests would
// still pass, because they assume the role explicitly with SET ROLE. So the
// grants are asserted in tests, and the DEPLOYMENT is asserted here, at runtime,
// against the connection actually in use.
//
// Callers must treat this as a WARNING and not a fatal: refusing to run because
// a DSN is over-privileged would brick an agent over a configuration problem,
// which the degraded-mode requirement forbids. `sprawl store doctor` surfaces
// the verdict, and Ledger.Open logs it.
func VerifyAppendOnly(ctx context.Context, pool PgPool) error {
	var canUpdate, canDelete, canTruncate bool
	if err := pool.QueryRow(ctx,
		`SELECT has_table_privilege('events', 'UPDATE'),
		        has_table_privilege('events', 'DELETE'),
		        has_table_privilege('events', 'TRUNCATE')`).
		Scan(&canUpdate, &canDelete, &canTruncate); err != nil {
		return fmt.Errorf("store: checking events privileges: %w", err)
	}
	var verbs []string
	if canUpdate {
		verbs = append(verbs, "UPDATE")
	}
	if canDelete {
		verbs = append(verbs, "DELETE")
	}
	if canTruncate {
		verbs = append(verbs, "TRUNCATE")
	}
	if len(verbs) == 0 {
		return nil
	}
	return &HintError{
		Err:  fmt.Errorf("%w: this connection holds %v on events", ErrAppendOnlyNotEnforced, verbs),
		Hint: fmt.Sprintf("point the DSN at a login role that inherits %s instead of one that owns the tables (GRANT %s TO <login_user>)", appRoleName, appRoleName),
	}
}

// appRoleName is the least-privilege role a deployment's login user should
// inherit. Declared here rather than only in SQL so the runtime hint can name it.
const appRoleName = "sprawl_app"

// RebuildOpenContracts drops and rebuilds the open_contracts projection for one
// project from the log.
//
// It is the operator repair path, and it is deliberately the SAME query the
// projection is supposed to agree with — Appendix A's open-goals anti-join. That
// makes it a repair rather than a second, subtly different definition of
// "outstanding". The test that compares the maintained projection against this
// uses its own hand-written copy of the anti-join as the oracle, because an
// oracle sharing an implementation with its subject cannot disagree with it.
//
// Scoped to a project and run in one transaction, so a concurrent append either
// happens before the rebuild or after it, never half-way through.
func RebuildOpenContracts(ctx context.Context, pool PgPool, projectID uuid.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM open_contracts
		 WHERE event_id IN (SELECT id FROM events WHERE project_id = $1)`, projectID); err != nil {
		return fmt.Errorf("store: clearing projection: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO open_contracts (event_id, owner_agent_id, workflow_instance_id, opened_at)
		 SELECT g.id, g.owner_agent_id, g.workflow_instance_id, g.at
		 FROM events g
		 JOIN event_type_schemas s ON s.id = g.schema_id AND s.opens
		 LEFT JOIN events c ON c.closes_event_id = g.id
		 WHERE c.id IS NULL AND g.project_id = $1`, projectID); err != nil {
		return fmt.Errorf("store: rebuilding projection: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit rebuild: %w", err)
	}
	return nil
}

// PutArtifact stores content and returns its id, deduplicating on the sha256 of
// the content.
//
// Content-addressed rather than id-addressed because the same report, plan or
// diff is routinely referenced by several events across a workflow, and storing
// it twice would both waste space and — worse — make the artifact chain fork,
// so two events that quote the same plan would appear to quote different ones.
//
// The ON CONFLICT returns the EXISTING id rather than the freshly minted one:
// the caller needs whatever id the content already lives under, not the one it
// hoped to use.
func PutArtifact(ctx context.Context, pool PgPool, kind, content, baseGitSHA string) (uuid.UUID, error) {
	digest := sha256.Sum256([]byte(content))
	var baseSHA any
	if baseGitSHA != "" {
		baseSHA = baseGitSHA
	}
	var id uuid.UUID
	// DO UPDATE on a no-op rather than DO NOTHING: DO NOTHING returns no row,
	// so a second store of identical content would yield "no rows in result
	// set" instead of the existing id. Setting kind to its own value is the
	// standard way to make the row visible to RETURNING without changing it.
	if err := pool.QueryRow(ctx,
		`INSERT INTO artifacts (id, kind, content, base_git_sha, size_bytes, sha256)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (sha256) DO UPDATE SET kind = artifacts.kind
		 RETURNING id`,
		uuid.New(), kind, content, baseSHA, len(content), digest[:]).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("store: storing artifact: %w", err)
	}
	return id, nil
}
