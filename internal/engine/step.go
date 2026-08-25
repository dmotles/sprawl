// Package engine is the workflow engine: thin, hand-rolled, and built directly
// on the M1a event log and the M1b dispatcher.
//
// It is hand-rolled by decision, not by omission. The DBOS spike on QUM-1252
// passed all three of its exit criteria and was rejected anyway, and the reason
// is the one thing this file has to get right: DBOS's backtrack rewinds ITS step
// log and leaves our Postgres side effects in place, so every backtrack target
// would have needed hand-written compensation regardless. What the spike was
// worth keeping is the mechanism below — the savepoint-per-step checkpoint — and
// that is about twenty lines rather than a dependency.
package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/dmotles/sprawl/internal/store"
)

// StepDeps is the narrow store surface one step needs. Function values rather
// than an interface, per the repo convention: these are two operations, not a
// stateful collaborator.
type StepDeps struct {
	// Begin starts the top-level transaction. Normally (*pgxpool.Pool).Begin.
	Begin func(ctx context.Context) (pgx.Tx, error)
	// AppendTx appends an event inside a transaction it does not own.
	// Normally (*store.Appender).AppendTx.
	AppendTx func(ctx context.Context, tx pgx.Tx, ev store.Event) (int64, error)
	Logger   *slog.Logger
}

// Attempt is one execution of one workflow step: a side effect, plus the event
// that records how it went.
//
// The two are declared together because they must COMMIT together. Splitting
// them across two transactions is the failure this whole type exists to prevent:
// a crash in the window leaves a step that ran with nothing in the log saying so,
// and since the log IS the state under the v2 plan of record, the engine would
// then re-run it forever.
type Attempt struct {
	// Body performs the step's side effect. It receives a SAVEPOINT-scoped
	// transaction, NOT the top-level one, so that a failure inside it can be
	// rolled back without losing the ability to record that failure.
	//
	// Nil means the step has no side effect of its own — an await-a-typed-event
	// step is the normal case — and is not an error.
	Body func(ctx context.Context, tx pgx.Tx) error
	// Done is appended when Body succeeds. Required.
	Done store.Event
	// Failed is appended when Body returns an error. A zero SchemaID means the
	// caller has declared no failure event, and a failing Body then aborts the
	// whole transaction instead: nothing is recorded and RunAttempt errors.
	//
	// That is deliberately not the default-safe choice. A step whose failure is
	// unrecordable cannot be retried, backtracked or escalated by an
	// OutcomePolicy, because the policy reads the log — so leaving Failed unset
	// is a decision to make the step's failure loud at the caller rather than
	// durable in the log.
	Failed store.Event
}

// Outcome is what RunAttempt committed.
//
// Note the split between Outcome.Err and RunAttempt's own error return: a Body
// that failed and had its failure durably recorded is a SUCCESSFUL run of the
// step machinery, and returning an error for it would make "the step failed"
// indistinguishable from "the engine could not record anything".
type Outcome struct {
	// Seq is the log position of whichever event was appended.
	Seq int64
	// OK reports whether Body succeeded, i.e. whether Done or Failed landed.
	OK bool
	// Err is Body's error when OK is false, and nil otherwise.
	Err error
}

// RunAttempt runs one attempt at one step. Body's side effects and the event
// recording the outcome are ONE commit in our Postgres, or neither happens.
//
// The shape, and why each part is there:
//
//	BEGIN                                  <- one top-level transaction
//	  SAVEPOINT                            <- so a Body failure is recoverable
//	    Body(tx)                           <- the step's side effect
//	  RELEASE / ROLLBACK TO SAVEPOINT
//	  AppendTx(Done or Failed)             <- the checkpoint, same transaction
//	COMMIT
//
// The savepoint is load-bearing rather than decorative. In Postgres any failed
// statement aborts the entire transaction, so without it a failing Body makes
// the subsequent append fail too, and the engine cannot record its own failure
// in the same commit that rolled the failure back. That is the mechanism the
// DBOS spike observed (DBOS wraps each step body in `SAVEPOINT dbos_step`) and
// the one piece of it worth stealing.
func RunAttempt(ctx context.Context, d StepDeps, at Attempt) (Outcome, error) {
	if d.Begin == nil {
		return Outcome{}, fmt.Errorf("engine: StepDeps.Begin is nil")
	}
	if d.AppendTx == nil {
		return Outcome{}, fmt.Errorf("engine: StepDeps.AppendTx is nil")
	}
	if at.Done.SchemaID == uuid.Nil {
		return Outcome{}, fmt.Errorf("engine: Attempt.Done has no SchemaID, so a successful step would record nothing")
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	tx, err := d.Begin(ctx)
	if err != nil {
		return Outcome{}, fmt.Errorf("engine: begin: %w", err)
	}
	// Rollback is a no-op after a successful commit.
	defer func() { _ = tx.Rollback(ctx) }()

	bodyErr, infraErr := runBody(ctx, tx, at.Body)
	if infraErr != nil {
		// Savepoint machinery failed, which is a database problem and not a
		// verdict on the step. Recording Failed here would hand an
		// OutcomePolicy a business failure to retry or backtrack on, for
		// something no retry of the step can fix.
		return Outcome{}, infraErr
	}

	ev := at.Done
	if bodyErr != nil {
		if at.Failed.SchemaID == uuid.Nil {
			return Outcome{}, fmt.Errorf("engine: step body failed and Attempt.Failed declares no event, so the failure is not recorded: %w", bodyErr)
		}
		ev = at.Failed
		log.Warn("workflow step failed, recording the failure", "reason", bodyErr)
	}

	seq, err := d.AppendTx(ctx, tx, ev)
	if err != nil {
		return Outcome{}, fmt.Errorf("engine: append step outcome: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Outcome{}, fmt.Errorf("engine: commit: %w", err)
	}
	return Outcome{Seq: seq, OK: bodyErr == nil, Err: bodyErr}, nil
}

// runBody executes body inside a savepoint on tx.
//
// The two error returns are separate because they mean opposite things to the
// caller: bodyErr is a verdict about the step and belongs in the log, infraErr
// is a database fault and must not be written into the log as though the step
// had decided something. Folding them together would make a savepoint failure
// indistinguishable from a step that genuinely failed.
func runBody(ctx context.Context, tx pgx.Tx, body func(context.Context, pgx.Tx) error) (bodyErr, infraErr error) {
	if body == nil {
		// An await-a-typed-event step has no side effect of its own. Skipping
		// the savepoint entirely rather than opening an empty one keeps the
		// statement log honest about what ran.
		return nil, nil
	}
	// pgx implements a nested Begin as SAVEPOINT, and the nested Rollback as
	// ROLLBACK TO SAVEPOINT — which is precisely the semantics wanted here.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: savepoint: %w", err)
	}
	if err := body(ctx, sp); err != nil {
		// ROLLBACK TO SAVEPOINT. The top-level transaction survives, which is
		// what lets the caller still append a failure event into this commit.
		if rbErr := sp.Rollback(ctx); rbErr != nil {
			// Both errors are wrapped: the step's failure is the reason we are
			// rolling back at all, and dropping it would leave an operator with
			// a savepoint error and no idea what provoked it.
			return nil, fmt.Errorf("engine: rollback to savepoint after step failure (%w): %w", err, rbErr)
		}
		return err, nil
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, fmt.Errorf("engine: release savepoint: %w", err)
	}
	return nil, nil
}
