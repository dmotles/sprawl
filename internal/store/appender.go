package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The transactional appender. One append is ONE transaction, and the order of
// operations inside it is binding (Appendix B items 5, 6 and 7):
//
//	validate (OUTSIDE the txn)
//	  -> BEGIN
//	  -> pg_advisory_xact_lock(workflow_instance_id)
//	  -> INSERT INTO events ... RETURNING seq
//	  -> maintain open_contracts (insert on opens, delete on closes)
//	  -> pg_notify doorbell
//	  -> COMMIT
//
// Three of those choices are easy to get wrong in a way nothing observable
// catches, so each is stated with its reason:
//
//   - Validation is OUTSIDE the transaction because a payload that is going to
//     be rejected must not hold a lock while being rejected. Both orders write
//     identical rows, so only a call-order assertion can tell them apart.
//   - The lock is pg_advisory_xact_lock and never pg_advisory_lock. A session
//     lock outlives an abandoned transaction and wedges every future append for
//     that workflow instance until the connection dies — no timeout, no local
//     symptom.
//   - pg_notify is a DOORBELL ONLY. It is inside the transaction so it fires on
//     commit and not before, but no consumer may depend on it: correctness is
//     seq-cursor catch-up plus a poll, and every dispatcher test must pass with
//     LISTEN disabled.

// PgPool is the narrow Postgres surface the appender needs. It exists so the
// order-of-operations and degraded-mode behaviour can be asserted without a
// database; *pgxpool.Pool satisfies it.
type PgPool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Ping(ctx context.Context) error
}

// NotifyChannel is the doorbell channel name. Doorbell only — see above.
const NotifyChannel = "sprawl_events"

// Event is one append. SchemaID is PINNED BY THE CALLER and is never resolved
// from a name here: that is Appendix B item 6, and it is what makes a workflow
// instance's validation stable across a schema bump that happens mid-flight.
type Event struct {
	// ID is the event's uuid. Zero means "mint one".
	ID                 uuid.UUID
	ProjectID          uuid.UUID
	WorkflowInstanceID uuid.UUID
	SchemaID           uuid.UUID
	AgentSessionID     *uuid.UUID
	// OwnerAgentID is who to notify when this contract closes.
	OwnerAgentID *uuid.UUID
	// ClosesEventID is required for a closes-typed schema and must be nil
	// otherwise.
	ClosesEventID *uuid.UUID
	Payload       json.RawMessage
	ArtifactID    *uuid.UUID
}

// AppenderDeps follows the repo's deps-struct convention: every collaborator is
// injectable so the appender is testable without a database.
type AppenderDeps struct {
	Pool     PgPool
	Registry *Registry
	Spill    Spiller
	Now      func() time.Time
	NewUUID  func() uuid.UUID
	Logger   *slog.Logger
	// Degraded, when non-nil, means the event log was already known to be
	// unreachable when this Appender was built. Every Append then goes STRAIGHT
	// to the degraded branch without touching the pool.
	//
	// This is a state rather than a per-append discovery on purpose. A dial
	// timeout is seconds; every agent turn produces events; and the emitters run
	// on agents' own EventBus subscriber goroutines. Retrying the connection per
	// append would convert "the database is down" into "every agent is wedged",
	// which is exactly what "agents never brick on the store" rules out.
	// Recovery is by restart, which matches "no new coordination starts while
	// the DB is down".
	Degraded error
	// RemoteURL identifies the project on a spilled record. A degraded Appender
	// never read the projects row, so ProjectID is unknown; without the remote
	// URL — which IS a project's identity — a replayer has nothing to resolve
	// against and every spilled record dead-letters.
	RemoteURL string
}

// Appender writes events. Safe for concurrent use: it holds no per-append state.
type Appender struct {
	pool      PgPool
	registry  *Registry
	spill     Spiller
	degraded  error
	remoteURL string
	now       func() time.Time
	newUUID   func() uuid.UUID
	log       *slog.Logger
}

// pgPool exposes the injectable Postgres seam this Appender writes through.
//
// Used by RecordHandoff to store an artifact on the SAME seam appends go
// through, rather than reaching for the Ledger's concrete *pgxpool.Pool. That
// distinction is not cosmetic: the concrete field is nil in every hermetic
// fixture, so an artifact write that bypassed this seam silently did nothing in
// tests while working in production — which is how the handoff artifact came to
// be untested on its first attempt.
func (a *Appender) pgPool() PgPool { return a.pool }

func NewAppender(d AppenderDeps) *Appender {
	a := &Appender{
		pool:      d.Pool,
		registry:  d.Registry,
		spill:     d.Spill,
		degraded:  d.Degraded,
		remoteURL: d.RemoteURL,
		now:       d.Now,
		newUUID:   d.NewUUID,
		log:       d.Logger,
	}
	if a.now == nil {
		a.now = time.Now
	}
	if a.newUUID == nil {
		a.newUUID = uuid.New
	}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	return a
}

// Append writes ev and returns the log position the database assigned.
//
// A spilled event returns (0, nil): there is no log position, and the caller is
// deliberately NOT told about the outage. Every caller is a subscriber on an
// agent's runtime EventBus, and an error there propagates into the agent's own
// operation — "agents never brick on the store" is implemented here, not
// documented elsewhere.
func (a *Appender) Append(ctx context.Context, ev Event) (int64, error) {
	// 1. Resolve the PINNED schema. Never by name, never "latest".
	schema, ok := a.registry.ByID(ev.SchemaID)
	if !ok {
		// Deliberately not spilled: with no schema there is no way to know
		// whether this type is spillable, and guessing either way is worse than
		// telling the caller.
		return 0, fmt.Errorf("store: unknown pinned schema_id %s: the emitter is pinned to a schema this build does not carry", ev.SchemaID)
	}

	// 2. Validate BEFORE the transaction and before the lock (Appendix B item 7).
	if err := Validate(schema.JSONSchema, ev.Payload); err != nil {
		// A violation is an emitter bug, not an outage: it will be exactly as
		// invalid on replay, so spilling it would trade a visible bug for a
		// dead letter nobody reads.
		return 0, fmt.Errorf("store: %s@%d: %w", schema.Name, schema.Version, err)
	}

	// 3. Shape checks that depend on the schema's contract role.
	if schema.Closes != "" && ev.ClosesEventID == nil {
		return 0, fmt.Errorf("store: %s@%d closes %q but the append carries no closes_event_id, so the contract would stay open forever",
			schema.Name, schema.Version, schema.Closes)
	}
	if schema.Closes == "" && ev.ClosesEventID != nil {
		return 0, fmt.Errorf("store: %s@%d does not close anything but the append carries a closes_event_id",
			schema.Name, schema.Version)
	}

	if ev.ID == uuid.Nil {
		ev.ID = a.newUUID()
	}

	// Known-degraded: skip the transaction entirely (see AppenderDeps.Degraded).
	// Validation above has already run, so degraded mode is not a hole in it.
	if a.degraded != nil {
		return 0, a.degradedResult(ctx, schema, ev, a.degraded)
	}

	seq, err := a.appendTx(ctx, schema, ev)
	if err == nil {
		return seq, nil
	}

	// 4. Degraded mode. Only a transport-class failure is eligible, and only
	// for a spillable type.
	if !isTransportFailure(err) {
		return 0, err
	}
	return 0, a.degradedResult(ctx, schema, ev, err)
}

// degradedResult routes one event according to its schema's spillability.
func (a *Appender) degradedResult(ctx context.Context, schema *EventTypeSchema, ev Event, cause error) error {
	if !schema.Spillable {
		return &HintError{
			// Two %w: errors.Is must match ErrDegraded (callers branch on it)
			// AND the underlying transport error must stay in the chain for
			// diagnosis.
			Err: fmt.Errorf("%w: cannot record %s@%d while the event log is unreachable: %w",
				ErrDegraded, schema.Name, schema.Version, cause),
			Hint: "the event log is the authoritative cross-host record, so this operation cannot proceed locally — check SPRAWL_DB_DSN or ~/.config/sprawl/secrets.yaml, then run `sprawl store doctor`",
		}
	}
	return a.spillEvent(ctx, schema, ev, cause)
}

// appendTx is the single transaction.
func (a *Appender) appendTx(ctx context.Context, schema *EventTypeSchema, ev Event) (int64, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("store: begin: %w", err)
	}
	// Rollback is a no-op after a successful commit.
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialise appends per workflow instance. hashtextextended gives a stable
	// 64-bit key from the uuid; xact-scoped so it is released by COMMIT or
	// ROLLBACK and cannot be abandoned.
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, ev.WorkflowInstanceID); err != nil {
		return 0, fmt.Errorf("store: advisory lock: %w", err)
	}

	var seq int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO events (id, project_id, workflow_instance_id, schema_id,
		                     agent_session_id, owner_agent_id, closes_event_id, payload, artifact_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 RETURNING seq`,
		ev.ID, ev.ProjectID, ev.WorkflowInstanceID, ev.SchemaID,
		ev.AgentSessionID, ev.OwnerAgentID, ev.ClosesEventID, []byte(ev.Payload), ev.ArtifactID,
	).Scan(&seq); err != nil {
		return 0, fmt.Errorf("store: insert event: %w", err)
	}

	// Maintain the open_contracts projection in the SAME transaction, so the
	// projection can never disagree with the log even across a crash.
	switch {
	case schema.Opens:
		if _, err := tx.Exec(ctx,
			`INSERT INTO open_contracts (event_id, owner_agent_id, workflow_instance_id, opened_at)
			 VALUES ($1,$2,$3,now())`,
			ev.ID, ev.OwnerAgentID, ev.WorkflowInstanceID); err != nil {
			return 0, fmt.Errorf("store: open contract: %w", err)
		}
	case schema.Closes != "":
		tag, err := tx.Exec(ctx, `DELETE FROM open_contracts WHERE event_id = $1`, *ev.ClosesEventID)
		if err != nil {
			return 0, fmt.Errorf("store: close contract: %w", err)
		}
		if tag.RowsAffected() != 1 {
			// Closes are final and the log is monotone. Accepting a close that
			// matched nothing would make "outstanding work" depend on delivery
			// order, and would let a double-close pass silently.
			return 0, fmt.Errorf("%w: event %s is not an open contract (already closed, or never opened)",
				ErrNoOpenContract, ev.ClosesEventID)
		}
	}

	// Doorbell. Inside the transaction so it fires on commit; no consumer may
	// depend on it (Appendix B item 5).
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, NotifyChannel,
		fmt.Sprintf(`{"seq":%d,"project_id":%q,"workflow_instance_id":%q}`,
			seq, ev.ProjectID, ev.WorkflowInstanceID)); err != nil {
		return 0, fmt.Errorf("store: notify: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("store: commit: %w", err)
	}
	return seq, nil
}

func (a *Appender) spillEvent(ctx context.Context, schema *EventTypeSchema, ev Event, cause error) error {
	rec := SpillRecord{
		EventID:            ev.ID,
		ProjectID:          ev.ProjectID,
		WorkflowInstanceID: ev.WorkflowInstanceID,
		SchemaID:           ev.SchemaID,
		SchemaName:         schema.Name,
		SchemaVersion:      schema.Version,
		AgentSessionID:     ev.AgentSessionID,
		OwnerAgentID:       ev.OwnerAgentID,
		ClosesEventID:      ev.ClosesEventID,
		Payload:            ev.Payload,
		RemoteURL:          a.remoteURL,
		At:                 a.now().UTC(),
		Reason:             cause.Error(),
	}
	if a.spill == nil {
		// No spill configured and the database is down: say so. Returning nil
		// here would be a silent drop.
		return fmt.Errorf("store: event log unreachable and no spill configured, dropping %s@%d: %w",
			schema.Name, schema.Version, cause)
	}
	if err := a.spill.Write(ctx, rec); err != nil {
		return fmt.Errorf("store: event log unreachable and spilling %s@%d also failed: %w",
			schema.Name, schema.Version, err)
	}
	a.log.Warn("event log unreachable, spilled event",
		"schema", schema.Name, "version", schema.Version, "reason", cause)
	return nil
}

// isTransportFailure distinguishes "the database is unreachable or the
// connection broke" from "the database refused this statement".
//
// The distinction decides whether an event may spill, so getting it wrong in
// either direction is expensive: treating a constraint violation as transport
// spills a row that will fail identically on replay, and treating an outage as a
// refusal turns a recoverable blip into lost telemetry.
//
// A pgconn.PgError means the server received the statement and answered, so it
// is NEVER transport, whatever it says. Anything else that reaches here — a dial
// failure, a closed connection, a context deadline — is.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoOpenContract) {
		return false
	}
	// A pgconn.PgError means the server received the statement and answered, so
	// it is a refusal rather than a transport failure — whatever it says.
	var pgErr *pgconn.PgError
	return !errors.As(err, &pgErr)
}
