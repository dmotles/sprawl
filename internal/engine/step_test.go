package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dmotles/sprawl/internal/store"
)

// What these tests pin is the CALL ORDER and the TRANSACTION BOUNDARY, neither
// of which a green integration test can distinguish from a wrong
// implementation. An engine that commits the side effect and then appends the
// checkpoint writes the same rows as one that commits them together; only a
// call-order assertion, or a crash injected into the window, can tell them
// apart. The commit-timestamp assertion in step_integration_test.go is the other
// half — it proves the two really are one commit in a real Postgres.

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// recordingTx is a pgx.Tx that records every terminal operation on a shared
// ordered log, and implements nested Begin as a savepoint the way pgx does.
//
// The embedded nil pgx.Tx is deliberate: any method the code under test reaches
// for that this double does not implement panics loudly rather than silently
// succeeding.
type recordingTx struct {
	pgx.Tx
	rec *recorder
	// label distinguishes the top-level transaction from a savepoint in the log.
	label string
	// commitErr fails this transaction's commit.
	commitErr error
	// rollbackErr fails this transaction's rollback.
	rollbackErr error
	done        bool
}

type recorder struct {
	mu    sync.Mutex
	calls []string
	// savepointBeginErr, when set, fails the nested Begin.
	savepointBeginErr error
	// savepointRollbackErr, when set, fails ROLLBACK TO SAVEPOINT.
	savepointRollbackErr error
}

func (r *recorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *recorder) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recorder) joined() string { return strings.Join(r.log(), ",") }

// Begin is a nested transaction, which pgx implements as SAVEPOINT.
func (t *recordingTx) Begin(context.Context) (pgx.Tx, error) {
	if t.rec.savepointBeginErr != nil {
		t.rec.record("savepoint:FAILED")
		return nil, t.rec.savepointBeginErr
	}
	t.rec.record("savepoint")
	return &recordingTx{
		rec:         t.rec,
		label:       "savepoint",
		rollbackErr: t.rec.savepointRollbackErr,
	}, nil
}

func (t *recordingTx) Commit(context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	if t.commitErr != nil {
		t.rec.record(t.terminal("commit") + ":FAILED")
		return t.commitErr
	}
	t.rec.record(t.terminal("commit"))
	return nil
}

func (t *recordingTx) Rollback(context.Context) error {
	if t.done {
		return nil
	}
	t.done = true
	if t.rollbackErr != nil {
		t.rec.record(t.terminal("rollback") + ":FAILED")
		return t.rollbackErr
	}
	t.rec.record(t.terminal("rollback"))
	return nil
}

// terminal names the operation for the log: on a savepoint, pgx's Commit is
// RELEASE and its Rollback is ROLLBACK TO SAVEPOINT. Naming them apart is what
// lets an assertion tell "the outer transaction was abandoned" from "the step
// body was rolled back and the transaction carried on".
func (t *recordingTx) terminal(op string) string {
	if t.label == "savepoint" {
		if op == "commit" {
			return "release_savepoint"
		}
		return "rollback_to_savepoint"
	}
	return op
}

// newTestStepDeps returns deps wired to a recorder, plus the recorder and the
// top-level transaction so a test can inject failures into either.
func newTestStepDeps(t *testing.T) (StepDeps, *recorder, *recordingTx) {
	t.Helper()
	rec := &recorder{}
	top := &recordingTx{rec: rec, label: "top"}
	deps := StepDeps{
		Begin: func(context.Context) (pgx.Tx, error) {
			rec.record("begin")
			return top, nil
		},
		AppendTx: func(_ context.Context, tx pgx.Tx, ev store.Event) (int64, error) {
			// Recording WHICH transaction the append was handed is the point:
			// an append issued on anything other than the top-level
			// transaction is not in the same commit as the body.
			which := "unknown"
			if rt, ok := tx.(*recordingTx); ok {
				which = rt.label
			}
			rec.record("append(" + ev.SchemaID.String()[:8] + ",on=" + which + ")")
			return 77, nil
		},
	}
	return deps, rec, top
}

// doneSchema and failedSchema are two distinct pinned schema ids. Distinct
// rather than reused, because "which event was appended" is the property under
// test on the failure path and identical ids make both answers look the same.
var (
	doneSchema   = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	failedSchema = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

func doneEvent() store.Event   { return store.Event{SchemaID: doneSchema} }
func failedEvent() store.Event { return store.Event{SchemaID: failedSchema} }

// writingBody returns a Body that performs a side effect by recording one
// statement, so the body's position relative to the append and the commit is
// observable.
func writingBody(rec *recorder, err error) func(context.Context, pgx.Tx) error {
	return func(_ context.Context, tx pgx.Tx) error {
		which := "unknown"
		if rt, ok := tx.(*recordingTx); ok {
			which = rt.label
		}
		rec.record("body(on=" + which + ")")
		return err
	}
}

// ---------------------------------------------------------------------------
// The transaction boundary
// ---------------------------------------------------------------------------

// TestRunAttempt_BodyAndCheckpointAreOneCommit is the central assertion: there
// is exactly ONE begin and ONE commit, the body runs before the append, and
// nothing commits in between.
//
// It asserts the whole ordered sequence rather than membership. A membership
// check ("both a body and an append happened") is equally satisfied by an
// implementation that commits the body first and appends afterwards, which is
// the exact defect this is here to exclude.
func TestRunAttempt_BodyAndCheckpointAreOneCommit(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)

	out, err := RunAttempt(context.Background(), deps, Attempt{
		Body: writingBody(rec, nil),
		Done: doneEvent(),
	})
	if err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	if !out.OK {
		t.Errorf("Outcome.OK = false for a body that returned nil")
	}
	if out.Seq != 77 {
		t.Errorf("Outcome.Seq = %d, want the seq AppendTx returned (77)", out.Seq)
	}

	want := "begin,savepoint,body(on=savepoint),release_savepoint,append(11111111,on=top),commit"
	if got := rec.joined(); got != want {
		t.Errorf("step did not run as one transaction.\ngot:  %s\nwant: %s", got, want)
	}
}

// TestRunAttempt_AwaitOnlyStepOpensNoSavepoint pins that a nil Body — the
// await-a-typed-event step, which is the common case — does not open a savepoint
// it has no use for. The step still appends and commits.
func TestRunAttempt_AwaitOnlyStepOpensNoSavepoint(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)

	if _, err := RunAttempt(context.Background(), deps, Attempt{Done: doneEvent()}); err != nil {
		t.Fatalf("RunAttempt: %v", err)
	}
	want := "begin,append(11111111,on=top),commit"
	if got := rec.joined(); got != want {
		t.Errorf("await-only step statement log wrong.\ngot:  %s\nwant: %s", got, want)
	}
}

// ---------------------------------------------------------------------------
// The failure path — the reason the savepoint exists
// ---------------------------------------------------------------------------

// TestRunAttempt_FailedBodyIsRolledBackToSavepointAndRecorded is the assertion
// that justifies the savepoint at all.
//
// Three things have to hold together, and each would be individually satisfied
// by a wrong implementation: the body's writes are rolled back
// (rollback_to_savepoint, NOT rollback), the FAILED event is the one appended
// (not Done), and the transaction still COMMITS — so the failure is durable
// rather than rolled back along with the work.
func TestRunAttempt_FailedBodyIsRolledBackToSavepointAndRecorded(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)
	bodyErr := errors.New("the agent never reported a result")

	out, err := RunAttempt(context.Background(), deps, Attempt{
		Body:   writingBody(rec, bodyErr),
		Done:   doneEvent(),
		Failed: failedEvent(),
	})
	// A recorded failure is a SUCCESSFUL run of the step machinery.
	if err != nil {
		t.Fatalf("RunAttempt returned an error for a body failure it recorded: %v", err)
	}
	if out.OK {
		t.Errorf("Outcome.OK = true for a body that returned an error")
	}
	if !errors.Is(out.Err, bodyErr) {
		t.Errorf("Outcome.Err = %v, want the body's own error", out.Err)
	}

	want := "begin,savepoint,body(on=savepoint),rollback_to_savepoint,append(22222222,on=top),commit"
	if got := rec.joined(); got != want {
		t.Errorf("failed step did not roll back to the savepoint and record the failure in the same commit.\ngot:  %s\nwant: %s", got, want)
	}
}

// TestRunAttempt_FailedBodyWithNoFailureEventAbortsEverything is the paired
// negative: with no Failed declared there is nothing to record, so the whole
// transaction must be abandoned rather than committed with the side effect
// still staged.
func TestRunAttempt_FailedBodyWithNoFailureEventAbortsEverything(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)
	bodyErr := errors.New("boom")

	out, err := RunAttempt(context.Background(), deps, Attempt{
		Body: writingBody(rec, bodyErr),
		Done: doneEvent(),
	})
	if err == nil {
		t.Fatal("expected an error when a body fails and no failure event is declared")
	}
	if !errors.Is(err, bodyErr) {
		t.Errorf("error should wrap the body's error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not recorded") {
		t.Errorf("error should say the failure is not recorded, got: %v", err)
	}
	if out.Seq != 0 {
		t.Errorf("Outcome.Seq = %d, want 0 — nothing was appended", out.Seq)
	}
	if got := rec.joined(); strings.Contains(got, "append") {
		t.Errorf("an event was appended despite no failure event being declared: %s", got)
	}
	if got := rec.joined(); !strings.HasSuffix(got, ",rollback") {
		t.Errorf("the top-level transaction was not rolled back: %s", got)
	}
}

// TestRunAttempt_SavepointFailureIsNotRecordedAsAStepFailure pins the
// distinction between a database fault and a verdict about the step. A savepoint
// that cannot be opened says nothing about whether the step would have
// succeeded, and writing a FAILED event for it would hand an OutcomePolicy
// something to retry or backtrack on that no retry can fix.
func TestRunAttempt_SavepointFailureIsNotRecordedAsAStepFailure(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)
	rec.savepointBeginErr = errors.New("connection reset")

	_, err := RunAttempt(context.Background(), deps, Attempt{
		Body:   writingBody(rec, nil),
		Done:   doneEvent(),
		Failed: failedEvent(),
	})
	if err == nil {
		t.Fatal("expected an error when the savepoint cannot be opened")
	}
	if !strings.Contains(err.Error(), "savepoint") {
		t.Errorf("error should name the savepoint, got: %v", err)
	}
	if got := rec.joined(); strings.Contains(got, "append") {
		t.Errorf("a savepoint fault was recorded in the log as a step outcome: %s", got)
	}
	if got := rec.joined(); strings.Contains(got, ",commit") {
		t.Errorf("the transaction committed after a savepoint fault: %s", got)
	}
}

// TestRunAttempt_CommitFailureIsReportedAndNothingIsClaimedDurable is the
// crash-window branch this whole type exists for, reached deliberately rather
// than left to chance.
//
// A commit that fails means the side effect AND the checkpoint both evaporated,
// which is the correct outcome — but only if the caller is told. A swallowed
// commit error would return an Outcome carrying a real Seq for an event that is
// not in the log, and the engine would then advance past a step that never ran.
func TestRunAttempt_CommitFailureIsReportedAndNothingIsClaimedDurable(t *testing.T) {
	deps, rec, top := newTestStepDeps(t)
	top.commitErr = errors.New("connection reset during commit")

	out, err := RunAttempt(context.Background(), deps, Attempt{
		Body: writingBody(rec, nil),
		Done: doneEvent(),
	})
	if err == nil {
		t.Fatal("expected an error when the commit fails")
	}
	if !strings.Contains(err.Error(), "commit") {
		t.Errorf("error should name the commit, got: %v", err)
	}
	// The zero Outcome is the load-bearing half: a non-zero Seq here would name
	// a log position that does not exist.
	if out != (Outcome{}) {
		t.Errorf("Outcome after a failed commit = %+v, want the zero value — a Seq or OK here claims a durability the database refused", out)
	}
	if got := rec.joined(); !strings.Contains(got, "commit:FAILED") {
		t.Fatalf("the commit was never attempted, so this test asserted nothing about the commit path: %s", got)
	}
}

// TestRunAttempt_RollbackToSavepointFailureWrapsBothErrors pins the double-%w in
// runBody.
//
// Both errors have to survive: the rollback error is what went wrong, and the
// body error is WHY we were rolling back at all. An operator handed only the
// former has a savepoint fault with no idea what provoked it. errors.Is against
// each is the assertion, rather than substring matching on the message, because
// the wrapping is the property and a message can print anything.
func TestRunAttempt_RollbackToSavepointFailureWrapsBothErrors(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)
	bodyErr := errors.New("the step itself failed")
	rollbackErr := errors.New("rollback to savepoint failed")
	rec.savepointRollbackErr = rollbackErr

	_, err := RunAttempt(context.Background(), deps, Attempt{
		Body:   writingBody(rec, bodyErr),
		Done:   doneEvent(),
		Failed: failedEvent(),
	})
	if err == nil {
		t.Fatal("expected an error when ROLLBACK TO SAVEPOINT fails")
	}
	if !errors.Is(err, rollbackErr) {
		t.Errorf("error does not wrap the rollback error: %v", err)
	}
	if !errors.Is(err, bodyErr) {
		t.Errorf("error does not wrap the body error, so the reason for the rollback is lost: %v", err)
	}
	// A rollback fault is infrastructure, not a verdict — same rule as a failed
	// savepoint. Nothing may be recorded.
	if got := rec.joined(); strings.Contains(got, "append") {
		t.Errorf("a rollback fault was recorded in the log as a step outcome: %s", got)
	}
	if got := rec.joined(); !strings.Contains(got, "rollback_to_savepoint:FAILED") {
		t.Fatalf("the rollback was never attempted, so this test asserted nothing about the rollback path: %s", got)
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------

// TestRunAttempt_DoneWithoutSchemaIDIsRefused pins that a step which would
// record nothing on success is refused up front rather than running its side
// effect and then failing in the append. The side effect must not happen.
func TestRunAttempt_DoneWithoutSchemaIDIsRefused(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)

	_, err := RunAttempt(context.Background(), deps, Attempt{Body: writingBody(rec, nil)})
	if err == nil {
		t.Fatal("expected an error for an Attempt whose Done has no SchemaID")
	}
	if !strings.Contains(err.Error(), "SchemaID") {
		t.Errorf("error should name SchemaID, got: %v", err)
	}
	if got := rec.joined(); got != "" {
		t.Errorf("a refused Attempt still touched the database: %s", got)
	}
}

func TestRunAttempt_NilBeginIsRefused(t *testing.T) {
	_, err := RunAttempt(context.Background(), StepDeps{
		AppendTx: func(context.Context, pgx.Tx, store.Event) (int64, error) { return 0, nil },
	}, Attempt{Done: doneEvent()})
	if err == nil || !strings.Contains(err.Error(), "Begin") {
		t.Fatalf("expected a refusal naming Begin, got: %v", err)
	}
}

func TestRunAttempt_NilAppendTxIsRefused(t *testing.T) {
	_, err := RunAttempt(context.Background(), StepDeps{
		Begin: func(context.Context) (pgx.Tx, error) { return nil, nil },
	}, Attempt{Done: doneEvent()})
	if err == nil || !strings.Contains(err.Error(), "AppendTx") {
		t.Fatalf("expected a refusal naming AppendTx, got: %v", err)
	}
}

// TestRunAttempt_AppendFailureAbandonsTheSideEffect pins that a failed
// checkpoint takes the body's side effect down with it. This is the property
// that makes the pattern worth having: a step whose outcome could not be
// recorded must not have happened.
func TestRunAttempt_AppendFailureAbandonsTheSideEffect(t *testing.T) {
	deps, rec, _ := newTestStepDeps(t)
	deps.AppendTx = func(context.Context, pgx.Tx, store.Event) (int64, error) {
		rec.record("append:FAILED")
		return 0, errors.New("schema violation")
	}

	if _, err := RunAttempt(context.Background(), deps, Attempt{
		Body: writingBody(rec, nil),
		Done: doneEvent(),
	}); err == nil {
		t.Fatal("expected an error when the checkpoint append fails")
	}
	got := rec.joined()
	if strings.Contains(got, ",commit") {
		t.Errorf("the transaction committed despite the checkpoint failing: %s", got)
	}
	if !strings.HasSuffix(got, ",rollback") {
		t.Errorf("the side effect was not abandoned: %s", got)
	}
}
