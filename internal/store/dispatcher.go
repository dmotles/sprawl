package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The per-host dispatcher (QUM-1250, M1b).
//
// CORRECTNESS IS A SEQ CURSOR PLUS A POLL. Nothing else. `events.seq` is a
// gapless total order, so "what have I not seen" is a single comparison, and a
// host that has been down for a week catches up by asking the same question it
// asks every two seconds. LISTEN/NOTIFY appears nowhere in that sentence, which
// is Appendix B item 5: NOTIFY is a doorbell that lowers latency, and every test
// in dispatcher_test.go runs with it disabled. A design that needed the doorbell
// would be broken by a single dropped notification, and pg_notify makes no
// delivery guarantee across a reconnect — so dropping one is normal operation.
//
// FOUR DECISIONS PER EVENT, in this order, and the order is the design:
//
//	1. Is this event mine?      host affinity, from the payload
//	2. May I act on it?         event_claims, conditional insert
//	3. Act.                     the handler
//	4. Record that I got here.  the cursor, AFTER the handler returns
//
// Step 4 coming last is what AC1 rests on. Save-then-handle and handle-then-save
// are indistinguishable on every run that does not crash; on the run that does,
// the wrong order marks work done that never happened, and — because the claim
// was also written — nothing ever comes back for it.
//
// WHAT HAPPENS WHEN A HANDLER FAILS is the one policy choice here with a real
// cost, so it is stated rather than left implicit: the pass STOPS at the failing
// event, keeps the cursor where it was, and releases the claim. That is
// head-of-line blocking — one persistently failing event stops this consumer
// dispatching anything after it. The alternative is worse in the direction that
// matters: advancing past a failure is a SILENT DROP, because the cursor then
// says this host is finished with an event nobody acted on and no claim remains
// to expire. Blocking is loud (the cursor visibly stops while the log grows) and
// recoverable (the next pass retries). Silence is neither.

// EventReader is the catch-up scan.
//
// An interface rather than a raw pool call so the loop's decisions are testable
// without a database — faking pgx.Rows is far more code than faking this, and
// the SQL itself is pinned separately (eventScanSQL) and exercised for real by
// dispatcher_integration_test.go.
type EventReader interface {
	// Read returns events for this project with seq strictly greater than
	// afterSeq, in ascending seq order, at most limit of them.
	Read(ctx context.Context, projectID uuid.UUID, afterSeq int64, limit int) ([]DispatchedEvent, error)
}

// Doorbell is the optional latency path. A nil Doorbell means LISTEN is
// disabled, which every dispatcher test uses and which must always work.
type Doorbell interface {
	// Wait blocks until an event may be available or d has elapsed, whichever
	// comes first. It is advisory in both directions: a spurious wake is
	// harmless (the scan finds nothing) and a missed wake costs latency only
	// (the poll deadline still fires).
	Wait(ctx context.Context, d time.Duration)
}

// DispatchedEvent is one event handed to a handler.
//
// It carries the resolved schema NAME and VERSION as well as the pinned id: a
// handler is registered by name, and an error message naming an opaque uuid
// sends whoever reads it nowhere.
type DispatchedEvent struct {
	Seq                int64
	ID                 uuid.UUID
	ProjectID          uuid.UUID
	WorkflowInstanceID uuid.UUID
	SchemaID           uuid.UUID
	SchemaName         string
	SchemaVersion      int
	AgentSessionID     *uuid.UUID
	OwnerAgentID       *uuid.UUID
	ClosesEventID      *uuid.UUID
	Payload            json.RawMessage
	At                 time.Time
	// HostAffinity, when non-empty, names the ONLY host that may claim this
	// event. Empty means any host may. Populated from the payload — see
	// hostAffinityOf.
	HostAffinity string
}

// Handler acts on one event. Registered by schema NAME, because a handler
// handles a KIND of event across every version of it.
type Handler interface {
	Handle(ctx context.Context, ev DispatchedEvent) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, ev DispatchedEvent) error

func (f HandlerFunc) Handle(ctx context.Context, ev DispatchedEvent) error { return f(ctx, ev) }

// eventScanSQL is the catch-up scan, and its shape is the requirement the issue
// states verbatim: "SELECT … WHERE seq > last_seen_seq ORDER BY seq".
//
// Three details in it are each a distinct silent defect if changed:
//
//   - `seq > $2`, never `>=`. With `>=` the cursor's own event is re-read every
//     pass, forever.
//   - `ORDER BY seq`. Without it Postgres may return rows in any order it finds
//     convenient, so a close can be dispatched before its opener while the cursor
//     advances past the opener unhandled.
//   - `LIMIT $3`. Without it, a first run against an established project reads
//     the entire history into memory at once.
const eventScanSQL = `
	SELECT e.seq, e.id, e.project_id, e.workflow_instance_id, e.schema_id,
	       e.agent_session_id, e.owner_agent_id, e.closes_event_id, e.payload, e.at
	  FROM events e
	 WHERE e.project_id = $1 AND e.seq > $2
	 ORDER BY e.seq
	 LIMIT $3`

// PgEventReader reads the log through a pgx pool.
type PgEventReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
	// Registry resolves each row's pinned schema_id to a name and version.
	Registry *Registry
}

var _ EventReader = (*PgEventReader)(nil)

func (r *PgEventReader) Read(ctx context.Context, projectID uuid.UUID, afterSeq int64, limit int) ([]DispatchedEvent, error) {
	rows, err := r.Pool.Query(ctx, eventScanSQL, projectID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: scanning events after seq %d: %w", afterSeq, err)
	}
	defer rows.Close()

	var out []DispatchedEvent
	for rows.Next() {
		var ev DispatchedEvent
		var payload []byte
		if err := rows.Scan(&ev.Seq, &ev.ID, &ev.ProjectID, &ev.WorkflowInstanceID, &ev.SchemaID,
			&ev.AgentSessionID, &ev.OwnerAgentID, &ev.ClosesEventID, &payload, &ev.At); err != nil {
			return nil, fmt.Errorf("store: scanning an event row: %w", err)
		}
		ev.Payload = json.RawMessage(payload)

		// An event whose schema_id this build does not carry is NOT skipped.
		//
		// Skipping would be a silent drop of exactly the events a rolling
		// upgrade produces — a newer host publishing a type an older host does
		// not know. Refusing is loud and the remedy is obvious (upgrade the
		// host, or run `sprawl store migrate`), whereas a skip would look like
		// an idle dispatcher.
		schema, ok := r.Registry.ByID(ev.SchemaID)
		if !ok {
			return nil, fmt.Errorf("store: event %s at seq %d carries schema_id %s, which this build does not know: upgrade this host, or it cannot safely dispatch anything after this event",
				ev.ID, ev.Seq, ev.SchemaID)
		}
		ev.SchemaName = schema.Name
		ev.SchemaVersion = schema.Version
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the event scan: %w", err)
	}
	return out, nil
}

// DispatcherDeps follows the repo's deps-struct convention: every collaborator
// is injectable, so the race and ordering tests are deterministic.
type DispatcherDeps struct {
	Events   EventReader
	Claims   ClaimStore
	Cursor   CursorStore
	Registry *Registry

	ProjectID uuid.UUID
	// Host identifies this dispatcher for claims and affinity. INJECTED rather
	// than read from os.Hostname() at the point of use, because AC2 runs two
	// hosts inside one process and a hostname call would make them the same
	// host — which would make the test pass for the wrong reason.
	Host string
	// Consumer is the event_claims.consumer value. Two dispatchers with the
	// same Consumer compete for events; two with different Consumers each see
	// every event.
	Consumer string

	// Handlers is keyed by schema NAME. A name with no entry is skipped and the
	// cursor advances past it — unhandled types are the common case.
	Handlers map[string]Handler

	// Doorbell is optional. nil means LISTEN disabled, which must always work.
	Doorbell Doorbell

	Now   func() time.Time
	Poll  func() time.Duration
	Lease func() time.Duration
	// Batch bounds one scan. 0 takes the default.
	Batch  int
	Logger *slog.Logger
}

// Dispatcher is one host's consumer loop.
type Dispatcher struct {
	events   EventReader
	claims   ClaimStore
	cursor   CursorStore
	registry *Registry

	projectID uuid.UUID
	host      string
	consumer  string
	handlers  map[string]Handler
	doorbell  Doorbell

	now   func() time.Time
	poll  func() time.Duration
	lease func() time.Duration
	batch int
	log   *slog.Logger
}

// Defaults. The poll interval is the 2–5s band the issue specifies; the batch is
// large enough that catch-up after an outage is not thousands of round trips and
// small enough that one scan is not an unbounded allocation.
const (
	defaultDispatchPoll  = 2 * time.Second
	defaultDispatchBatch = 256
)

// NewDispatcher validates the configuration and builds the loop.
//
// It REFUSES an incomplete configuration rather than defaulting its way past
// one, because every missing field here fails silently at run time in a way that
// looks like an idle system: an empty Host makes affinity match nothing and
// claims un-renewable, an empty Consumer merges this consumer's claims with
// another's, and a nil ProjectID scopes the scan to no project so the loop polls
// forever and dispatches nothing.
func NewDispatcher(d DispatcherDeps) (*Dispatcher, error) {
	switch {
	case d.Events == nil:
		return nil, fmt.Errorf("store: dispatcher needs an EventReader")
	case d.Claims == nil:
		return nil, fmt.Errorf("store: dispatcher needs a ClaimStore; without claims nothing makes dispatch exactly-once")
	case d.Cursor == nil:
		return nil, fmt.Errorf("store: dispatcher needs a CursorStore")
	case d.Registry == nil:
		return nil, fmt.Errorf("store: dispatcher needs a Registry")
	case d.ProjectID == uuid.Nil:
		return nil, fmt.Errorf("store: dispatcher needs a project id; scoped to no project it would poll forever and dispatch nothing, which is indistinguishable from an idle system")
	case d.Host == "":
		return nil, fmt.Errorf("store: dispatcher needs a host identity; without one, host affinity matches nothing and claims cannot be renewed or released")
	case d.Consumer == "":
		return nil, fmt.Errorf("store: dispatcher needs a consumer name; it is half the claim key, so an empty one merges this consumer's claims with another's")
	}

	dp := &Dispatcher{
		events: d.Events, claims: d.Claims, cursor: d.Cursor, registry: d.Registry,
		projectID: d.ProjectID, host: d.Host, consumer: d.Consumer,
		handlers: d.Handlers, doorbell: d.Doorbell,
		now: d.Now, poll: d.Poll, lease: d.Lease, batch: d.Batch, log: d.Logger,
	}
	if dp.handlers == nil {
		dp.handlers = map[string]Handler{}
	}
	if dp.now == nil {
		dp.now = time.Now
	}
	if dp.poll == nil {
		dp.poll = func() time.Duration { return defaultDispatchPoll }
	}
	if dp.lease == nil {
		dp.lease = func() time.Duration { return DefaultClaimLease }
	}
	if dp.batch <= 0 {
		dp.batch = defaultDispatchBatch
	}
	if dp.log == nil {
		dp.log = slog.New(slog.DiscardHandler)
	}
	return dp, nil
}

// StepResult reports what one catch-up pass did. Returned so a caller — a test,
// `sprawl store dispatch`'s logging — can tell "nothing to do" from "did work"
// without inferring it from timing.
type StepResult struct {
	Scanned int
	Handled int
	Skipped int
	// AdvancedTo is the cursor position after the pass.
	AdvancedTo int64
}

// Step runs one catch-up pass and returns when the scan is exhausted or an event
// could not be completed.
func (d *Dispatcher) Step(ctx context.Context) (StepResult, error) {
	var res StepResult

	cursor, err := d.cursor.Load(d.consumer)
	if err != nil {
		// A cursor that cannot be read is NOT treated as 0. Re-scanning from the
		// start is safe (claims absorb it) but doing so silently, on every poll,
		// would turn a corrupt cursor into a permanent full-log scan that nobody
		// is told about. FileCursorStore already distinguishes absence (0, nil)
		// from damage (an error), and this is the half that respects it.
		return res, fmt.Errorf("store: dispatcher %q on %s: %w", d.consumer, d.host, err)
	}
	res.AdvancedTo = cursor

	// persisted tracks what is actually on disk, so skipped events can share one
	// cursor write instead of taking one each.
	//
	// This split is not an optimisation for its own sake — it fixes a real
	// defect found by the unhandled-types test. A HANDLED event must persist its
	// position immediately, because that write is the boundary AC1 depends on. A
	// SKIPPED event has no side effect to be ordered against, so persisting per
	// event would cost one file write for every event of every type this
	// consumer ignores — which is most events in the log — while a pass that
	// persisted only on the handled path never advanced at all when nothing was
	// handled, and re-read the entire log on every poll, forever.
	persisted := cursor
	flush := func() error {
		if cursor == persisted {
			return nil
		}
		if err := d.cursor.Save(d.consumer, cursor); err != nil {
			return err
		}
		persisted = cursor
		return nil
	}

	for {
		batch, err := d.events.Read(ctx, d.projectID, cursor, d.batch)
		if err != nil {
			// Flush what the pass already earned. Discarding it would make an
			// outage mid-catch-up cost the whole batch's progress on the next
			// attempt — safe, thanks to claims, but needlessly slow.
			if ferr := flush(); ferr != nil {
				d.log.Warn("could not persist cursor progress after a failed scan", "error", ferr)
			}
			return res, err
		}
		if len(batch) == 0 {
			return res, flush()
		}
		for _, ev := range batch {
			res.Scanned++
			advanced, handled, err := d.one(ctx, ev)
			if handled {
				res.Handled++
			} else {
				res.Skipped++
			}
			if err != nil {
				if ferr := flush(); ferr != nil {
					d.log.Warn("could not persist cursor progress after a failed event", "error", ferr)
				}
				return res, err
			}
			if !advanced {
				return res, flush()
			}
			cursor = ev.Seq
			res.AdvancedTo = cursor
			if handled {
				// The AC1 boundary: recorded only after the side effect, and
				// before anything else is attempted.
				if err := d.cursor.Save(d.consumer, cursor); err != nil {
					return res, err
				}
				persisted = cursor
			}
		}
		if err := flush(); err != nil {
			return res, err
		}
		if len(batch) < d.batch {
			return res, nil
		}
	}
}

// one processes a single event.
//
// Returns (advanced, handled, err). `advanced` is false only when the pass must
// stop without moving past this event — which happens for a handler failure, and
// never for a skip.
func (d *Dispatcher) one(ctx context.Context, ev DispatchedEvent) (advanced, handled bool, err error) {
	// 1. Affinity. Decided BEFORE the claim, deliberately: claiming an event
	// that belongs to another host and then declining to act on it would leave
	// the owning host permanently unable to claim its own work.
	affinity, err := hostAffinityOf(ev)
	if err != nil {
		return false, false, err
	}
	if affinity != "" && affinity != d.host {
		// Advance past it. It is not this host's work, and blocking on it would
		// let one absent peer stall this host's whole scan.
		return true, false, nil
	}

	handler, wanted := d.handlers[ev.SchemaName]
	if !wanted {
		// Unhandled types are the COMMON case: the log carries every type for a
		// project and each consumer handles a few. Advance, and do NOT claim —
		// a claim on an event this consumer will never act on would make a
		// handler added later see all of history as already taken.
		return true, false, nil
	}

	// 2. The claim. Nothing irreversible has happened yet.
	won, err := d.claims.Claim(ctx, ev.ID, d.consumer, d.host, d.lease())
	if err != nil {
		// NOT read as "somebody else has it". Skipping AND advancing on a
		// transport failure would lose the event permanently: no claim row was
		// written, so no lease expires and no takeover recovers it.
		return false, false, err
	}
	if !won {
		// Somebody holds it. That somebody may be DEAD, so before skipping,
		// offer to take over an EXPIRED lease.
		//
		// Order matters and is the reason these are two calls rather than one
		// upsert: the ordinary path must never steal, so the insert is attempted
		// first and unconditionally, and the takeover is reached only after the
		// insert has proved somebody else already holds the claim. An
		// implementation that tried the takeover first, or that used ON CONFLICT
		// DO UPDATE, would make every claim a steal.
		//
		// Without this call the lease column is DECORATIVE: a crashed host's
		// claims sit in the table forever and the events beneath them are never
		// dispatched by anyone. Found by
		// TestDispatchPg_ExpiredClaimIsTakenOverByAnotherHost, which failed on
		// the first implementation for exactly that reason.
		//
		// See claims.go's header for why a takeover is safe at all: the
		// SPAWN_INTENT write-ahead makes the side effect re-checkable, so the
		// second handler reconciles instead of repeating the work.
		took, terr := d.claims.TakeoverExpired(ctx, ev.ID, d.consumer, d.host, d.lease())
		if terr != nil {
			return false, false, terr
		}
		if !took {
			// A live lease. Advance — waiting for a peer's work to finish would
			// make this host's progress hostage to that peer's liveness.
			d.log.Debug("event already claimed and its lease is live, skipping",
				"seq", ev.Seq, "event", ev.ID, "type", ev.SchemaName, "consumer", d.consumer)
			return true, false, nil
		}
		d.log.Warn("took over an expired claim; the previous holder did not finish",
			"seq", ev.Seq, "event", ev.ID, "type", ev.SchemaName, "consumer", d.consumer, "host", d.host)
	}

	// 3. Act.
	if err := handler.Handle(ctx, ev); err != nil {
		// Release so a retry does not have to wait out the whole lease. A
		// release that fails is logged and not merged into the returned error:
		// the handler failure is the fact the caller needs, and burying it under
		// a cleanup failure would misdirect whoever reads the log.
		if rerr := d.claims.Release(ctx, ev.ID, d.consumer, d.host); rerr != nil {
			d.log.Warn("could not release the claim on a failed event; a retry must now wait for the lease to expire",
				"seq", ev.Seq, "event", ev.ID, "error", rerr)
		}
		d.log.Warn("handler failed; the cursor stays put and this event will be retried",
			"seq", ev.Seq, "event", ev.ID, "type", ev.SchemaName, "error", err)
		return false, false, fmt.Errorf("store: dispatching %s@%d at seq %d: %w", ev.SchemaName, ev.SchemaVersion, ev.Seq, err)
	}

	// Step 4 — recording the position — is Step's job, not this function's, so
	// that the "persist immediately when handled, batch when skipped" rule lives
	// in one place rather than being half here and half there.
	return true, true, nil
}

// hostAffinityOf reads the affinity a worktree-bound event carries.
//
// It lives in the PAYLOAD rather than in a column, and that is a deliberate
// consequence of M1a's decision that M1b ships no migration: Appendix A gives
// `events` no host column, and the design says events CARRY affinity, which for
// a thin event means the payload. `event_claims.host` cannot serve — it records
// who DID claim, and does not exist until after the claim it would have to gate.
//
// KNOWN LIMITATION, recorded because it is weaker than the neighbouring
// guarantee and should not be read as equivalent: this is enforced in Go, not by
// the database. `events` is append-only because a privilege is missing, which is
// a refusal; affinity holds because every dispatcher checks it, which is a
// convention. No SQL constraint can express "only host X may insert this claim"
// without a host identity on the connection. So AC3's guarantee is "no sprawl
// dispatcher claims the wrong host's event", not "the database refuses it".
//
// A malformed affinity is an ERROR, not a shrug. Reading a non-string as
// "unaffined" would silently convert a broken worktree-bound event into one any
// host may claim — the exact outcome affinity exists to prevent, reached through
// a type error.
func hostAffinityOf(ev DispatchedEvent) (string, error) {
	if len(ev.Payload) == 0 {
		return "", nil
	}
	var probe struct {
		HostAffinity *json.RawMessage `json:"host_affinity"`
	}
	if err := json.Unmarshal(ev.Payload, &probe); err != nil {
		return "", fmt.Errorf("store: event %s at seq %d has an unreadable payload: %w", ev.ID, ev.Seq, err)
	}
	if probe.HostAffinity == nil {
		return "", nil
	}
	var host string
	if err := json.Unmarshal(*probe.HostAffinity, &host); err != nil {
		return "", fmt.Errorf("store: event %s at seq %d carries a non-string host_affinity (%s); refusing rather than treating it as claimable by any host",
			ev.ID, ev.Seq, string(*probe.HostAffinity))
	}
	return host, nil
}

// Run polls until ctx is cancelled.
//
// A FAILING PASS DOES NOT END THE LOOP. This process's whole purpose is to keep
// dispatching; if a transient claim error ended it, a two-second database blip
// would stop dispatch on that host until a human noticed and restarted it. The
// failure is logged at WARN and the next poll retries — which is also what makes
// the head-of-line blocking above recoverable rather than terminal.
func (d *Dispatcher) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		if _, err := d.Step(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.log.Warn("dispatch pass failed, retrying on the next poll",
				"consumer", d.consumer, "host", d.host, "error", err)
		}

		wait := d.poll()
		if d.doorbell != nil {
			// The doorbell may return early; it may also never fire. Either way
			// the poll deadline bounds the wait, which is what keeps correctness
			// independent of NOTIFY.
			d.doorbell.Wait(ctx, wait)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// eventByIDSQL resolves one event. Used to read the contract a close refers to,
// so the notification handler can find out who owns it.
const eventByIDSQL = `
	SELECT e.seq, e.id, e.project_id, e.workflow_instance_id, e.schema_id,
	       e.agent_session_id, e.owner_agent_id, e.closes_event_id, e.payload, e.at
	  FROM events e
	 WHERE e.id = $1`

var _ EventLookup = (*PgEventReader)(nil)

// ByID resolves one event by its uuid.
//
// Deliberately NOT scoped to a project: closes_event_id is a foreign key onto
// events(id), so the referenced event is already guaranteed to exist, and adding
// a project predicate would turn a guaranteed hit into a silent miss if the two
// ever disagreed — which is a data-integrity problem worth surfacing rather than
// filtering away.
func (r *PgEventReader) ByID(ctx context.Context, id uuid.UUID) (DispatchedEvent, error) {
	var (
		ev      DispatchedEvent
		payload []byte
	)
	if err := r.Pool.QueryRow(ctx, eventByIDSQL, id).Scan(
		&ev.Seq, &ev.ID, &ev.ProjectID, &ev.WorkflowInstanceID, &ev.SchemaID,
		&ev.AgentSessionID, &ev.OwnerAgentID, &ev.ClosesEventID, &payload, &ev.At,
	); err != nil {
		return DispatchedEvent{}, fmt.Errorf("store: reading event %s: %w", id, err)
	}
	ev.Payload = json.RawMessage(payload)
	schema, ok := r.Registry.ByID(ev.SchemaID)
	if !ok {
		return DispatchedEvent{}, fmt.Errorf("store: event %s carries schema_id %s, which this build does not know", id, ev.SchemaID)
	}
	ev.SchemaName, ev.SchemaVersion = schema.Name, schema.Version
	return ev, nil
}
