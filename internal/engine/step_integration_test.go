//go:build store_pg

// Postgres integration suite for the workflow engine's step runner (QUM-1252,
// M3a).
//
// Build-tagged OFF by default, exactly like internal/store's suite, and run by
// the same row. Run it with
//
//	make test-store-pg
//	# or: go test -tags store_pg -count=1 ./internal/engine/
//
// `-count=1` is not decoration: a Docker-down run t.Skip's, which Go caches as a
// passing package result, and that skip then replays as green after Docker comes
// back.
//
// What this file is FOR, and what step_test.go cannot do: step_test.go asserts
// the statement order against a fake pgx.Tx, so every claim it makes about what
// `tx.Begin()` MEANS is an assumption about pgx that the fake itself encodes.
// The savepoint-per-step pattern only works if a nested Begin really emits
// SAVEPOINT and a nested Rollback really leaves the outer transaction usable.
// That is a property of pgx and Postgres, and only a real database can be asked.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/testutil/pgtest"
)

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type pgFixture struct {
	pool      *pgxpool.Pool
	appender  *store.Appender
	registry  *store.Registry
	projectID uuid.UUID
	instance  uuid.UUID
}

// sideEffectDDL is the step body's target: a scratch table standing in for
// whatever a real step writes.
//
// A scratch table rather than one of the M1a tables on purpose. The property
// under test is "an arbitrary caller-owned write and the checkpoint event share
// one commit", and borrowing a real table would couple the test to that table's
// constraints and triggers without making the claim any stronger.
const sideEffectDDL = `CREATE TABLE step_side_effect (note text primary key)`

func newPGFixture(t *testing.T) *pgFixture {
	t.Helper()
	_, pool := pgtest.NewSchema(t, store.Migrate)
	ctx := context.Background()

	reg, err := store.SeedRegistry()
	if err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}
	projectID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO projects (id, remote_url, created_at) VALUES ($1, $2, now())`,
		projectID, "https://example.invalid/"+projectID.String()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := pool.Exec(ctx, sideEffectDDL); err != nil {
		t.Fatalf("create side-effect table: %v", err)
	}

	// track_commit_timestamp is what every assertion here reads. If it were off,
	// pg_xact_commit_timestamp() would return NULL for every row and the
	// "timestamps are equal" leg would pass vacuously on two NULLs. Assert the
	// instrument before trusting it.
	var trackCommitTS string
	if err := pool.QueryRow(ctx, `SHOW track_commit_timestamp`).Scan(&trackCommitTS); err != nil {
		t.Fatalf("SHOW track_commit_timestamp: %v", err)
	}
	if trackCommitTS != "on" {
		t.Fatalf("track_commit_timestamp = %q, want \"on\" — without it pg_xact_commit_timestamp() is NULL for every row and this suite's atomicity assertions compare two NULLs", trackCommitTS)
	}

	return &pgFixture{
		pool: pool, registry: reg, projectID: projectID, instance: uuid.New(),
		appender: store.NewAppender(store.AppenderDeps{Pool: pool, Registry: reg}),
	}
}

func (f *pgFixture) deps() StepDeps {
	return StepDeps{
		Begin:    func(ctx context.Context) (pgx.Tx, error) { return f.pool.Begin(ctx) },
		AppendTx: f.appender.AppendTx,
	}
}

func (f *pgFixture) schemaID(t *testing.T, name string) uuid.UUID {
	t.Helper()
	s, ok := f.registry.ByName(name, 1)
	if !ok {
		t.Fatalf("seed %s@1 missing", name)
	}
	return s.ID
}

// doneEvent and failedEvent stand in for the step-outcome pair. M3a's own step
// event kinds land in a later slice; these two are used only because they are
// seeded, plain (neither opens nor closes a contract, so no contract plumbing is
// needed) and DISTINCT — "which of the two was appended" is a property the
// failure-path tests assert.
func (f *pgFixture) doneEvent(t *testing.T) store.Event {
	t.Helper()
	return store.Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: f.instance,
		SchemaID:           f.schemaID(t, "turn_finished"),
		Payload:            json.RawMessage(`{"session_id":"s-1","input_tokens":1,"output_tokens":2}`),
	}
}

func (f *pgFixture) failedEvent(t *testing.T) store.Event {
	t.Helper()
	return store.Event{
		ProjectID:          f.projectID,
		WorkflowInstanceID: f.instance,
		SchemaID:           f.schemaID(t, "stray_reclaimed"),
		Payload:            json.RawMessage(`{"agent_name":"a-1","reason":"step failed","host":"h-1"}`),
	}
}

func writeNote(note string) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO step_side_effect (note) VALUES ($1)`, note)
		return err
	}
}

// commitTS reads the commit timestamp of the transaction that last wrote the
// single row matched by where.
//
// It reads pg_xact_commit_timestamp(xmin) and NOT xmin itself. xmin equality is
// a documented trap for this exact assertion (QUM-1252): the side-effect row
// here is written inside a SAVEPOINT, which Postgres implements as a
// subtransaction with its OWN xid, so xmin differs by one on a write that IS
// perfectly atomic. The commit timestamp does not have that problem — Postgres
// records the same timestamp for a transaction and every one of its
// subtransactions — and it fails in the safe direction too: xmin can match
// falsely for two rows rewritten by one later transaction, whereas two separate
// commits cannot share a commit timestamp.
func commitTS(t *testing.T, pool *pgxpool.Pool, where string, args ...any) string {
	t.Helper()
	var ts *string
	q := fmt.Sprintf(`SELECT pg_xact_commit_timestamp(xmin)::text FROM %s`, where)
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&ts); err != nil {
		t.Fatalf("commit timestamp for %s: %v", where, err)
	}
	if ts == nil {
		t.Fatalf("commit timestamp for %s is NULL — the probe measured nothing, so any comparison against it is vacuous", where)
	}
	return *ts
}

func (f *pgFixture) notes(t *testing.T) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT note FROM step_side_effect ORDER BY note`)
	if err != nil {
		t.Fatalf("query notes: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// The atomicity assertion
// ---------------------------------------------------------------------------

// TestRunAttempt_SideEffectAndCheckpointShareOneCommit is the binding assertion
// for the savepoint-per-step pattern: the step's side effect and the event
// recording it were ONE commit.
//
// The positive control is TestRunAttempt_PositiveControl_SeparateCommitsDiffer
// below, which performs the same two writes with the checkpoint in a SECOND
// transaction — the mistake this whole mechanism exists to prevent — and asserts
// the probe reports DIFFERENT timestamps. Without that control, "the two
// timestamps are equal" is satisfiable by a probe that returns a constant.
func TestRunAttempt_SideEffectAndCheckpointShareOneCommit(t *testing.T) {
	f := newPGFixture(t)

	out, err := RunAttempt(context.Background(), f.deps(), Attempt{
		Body: writeNote("atomic"),
		Done: f.doneEvent(t),
	})
	if err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if !out.OK {
		t.Fatalf("Outcome.OK = false, err = %v; want a successful step", out.Err)
	}

	effectTS := commitTS(t, f.pool, `step_side_effect WHERE note = $1`, "atomic")
	eventTS := commitTS(t, f.pool, `events WHERE seq = $1`, out.Seq)
	if effectTS != eventTS {
		t.Errorf("side effect and checkpoint were committed separately: side effect at %s, checkpoint event at %s — the step's write and the event recording it must be one commit, or a crash in the window leaves a step that ran with nothing in the log saying so",
			effectTS, eventTS)
	}
}

// TestRunAttempt_PositiveControl_SeparateCommitsDiffer is the positive control
// for the assertion above: the defect IS present here, so the probe MUST fire.
//
// It deliberately does NOT call RunAttempt. It performs the same two writes the
// wrong way — side effect committed, then the checkpoint appended in its own
// transaction — which is exactly the shape RunAttempt exists to rule out.
func TestRunAttempt_PositiveControl_SeparateCommitsDiffer(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := writeNote("split")(ctx, tx); err != nil {
		t.Fatalf("side effect: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit side effect: %v", err)
	}
	seq, err := f.appender.Append(ctx, f.doneEvent(t))
	if err != nil {
		t.Fatalf("Append checkpoint: %v", err)
	}

	effectTS := commitTS(t, f.pool, `step_side_effect WHERE note = $1`, "split")
	eventTS := commitTS(t, f.pool, `events WHERE seq = $1`, seq)
	if effectTS == eventTS {
		t.Fatalf("two SEPARATE commits reported the same commit timestamp (%s) — the probe cannot distinguish one commit from two, so the atomicity assertion above proves nothing",
			effectTS)
	}
}

// ---------------------------------------------------------------------------
// The savepoint's actual purpose, against a real server
// ---------------------------------------------------------------------------

// TestRunAttempt_FailedBodyRollsBackTheSideEffectAndStillRecordsFailure is the
// claim step_test.go can only assume: in Postgres any failed statement aborts
// the whole transaction, so recording a failure in the SAME commit that rolled
// the failure back is only possible because the body ran inside a savepoint.
//
// Both halves matter. The side-effect row must be gone AND the failure event
// must be durable; an implementation that aborted everything would satisfy the
// first leg alone.
func TestRunAttempt_FailedBodyRollsBackTheSideEffectAndStillRecordsFailure(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// A real Postgres error, not a Go-side sentinel: a Go error returned without
	// touching the server would leave the transaction perfectly healthy and the
	// savepoint would not be doing any work.
	body := func(ctx context.Context, tx pgx.Tx) error {
		if err := writeNote("doomed")(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO step_side_effect (note) VALUES ('doomed')`)
		if err == nil {
			return fmt.Errorf("duplicate insert unexpectedly succeeded")
		}
		return err
	}

	out, err := RunAttempt(ctx, f.deps(), Attempt{
		Body:   body,
		Done:   f.doneEvent(t),
		Failed: f.failedEvent(t),
	})
	if err != nil {
		t.Fatalf("RunAttempt returned an engine error, so the failure was never recorded: %v", err)
	}
	if out.OK {
		t.Fatal("Outcome.OK = true for a body that failed")
	}
	if out.Err == nil {
		t.Error("Outcome.Err is nil for a body that failed")
	}

	if notes := f.notes(t); len(notes) != 0 {
		t.Errorf("side-effect rows after a failed step = %v, want none — ROLLBACK TO SAVEPOINT did not undo the body's write", notes)
	}

	var schemaID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT schema_id FROM events WHERE seq = $1`, out.Seq).Scan(&schemaID); err != nil {
		t.Fatalf("read checkpoint event at seq %d: %v", out.Seq, err)
	}
	if want := f.schemaID(t, "stray_reclaimed"); schemaID != want {
		t.Errorf("checkpoint event schema_id = %s, want the Failed event's %s", schemaID, want)
	}
}

// TestRunAttempt_FailedBodyCheckpointIsOneCommitToo pins that the failure path
// keeps the atomicity property. The success path having it is not evidence: the
// failure path takes a different branch (ROLLBACK TO SAVEPOINT rather than
// RELEASE) before the append, and it is the path a crash is most likely to
// interrupt.
func TestRunAttempt_FailedBodyCheckpointIsOneCommitToo(t *testing.T) {
	f := newPGFixture(t)
	ctx := context.Background()

	// A committed marker row written by an EARLIER transaction gives the probe
	// something to compare against that definitely is not this commit.
	if _, err := f.pool.Exec(ctx, `INSERT INTO step_side_effect (note) VALUES ('marker')`); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	out, err := RunAttempt(ctx, f.deps(), Attempt{
		Body:   func(context.Context, pgx.Tx) error { return fmt.Errorf("business failure") },
		Done:   f.doneEvent(t),
		Failed: f.failedEvent(t),
	})
	if err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}

	markerTS := commitTS(t, f.pool, `step_side_effect WHERE note = $1`, "marker")
	eventTS := commitTS(t, f.pool, `events WHERE seq = $1`, out.Seq)
	if markerTS == eventTS {
		t.Errorf("the failure checkpoint shares a commit timestamp (%s) with a row committed by an earlier transaction — the probe is not distinguishing commits here", eventTS)
	}
}

// TestRunAttempt_AwaitOnlyStepCommitsItsCheckpoint covers the nil-Body path
// against a real server: no savepoint is opened, and the checkpoint still lands.
func TestRunAttempt_AwaitOnlyStepCommitsItsCheckpoint(t *testing.T) {
	f := newPGFixture(t)

	out, err := RunAttempt(context.Background(), f.deps(), Attempt{Done: f.doneEvent(t)})
	if err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if !out.OK {
		t.Fatalf("Outcome.OK = false for an await-only step: %v", out.Err)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM events WHERE seq = $1`, out.Seq).Scan(&n); err != nil {
		t.Fatalf("count checkpoint: %v", err)
	}
	if n != 1 {
		t.Errorf("checkpoint rows at seq %d = %d, want 1", out.Seq, n)
	}
}
