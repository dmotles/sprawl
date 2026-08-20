package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Reading spawn-intent state back out of the log (QUM-1250, M1b).
//
// Both queries are keyed on SCHEMA IDS COLLECTED BY NAME rather than on a
// literal id or a name join. Two reasons, and the second is the one that rots if
// nobody writes it down:
//
//   - Event schemas are additive-only within a name, so spawn_intent@2 will
//     eventually exist and must be found by the same query. Pinning a single id
//     would make the reconciler silently blind to every newer version — it would
//     find no intents, adopt nothing, and report a clean pass.
//   - The ids are derived (uuid.NewSHA1 over "<name>@<version>"), so collecting
//     them from the embedded registry costs nothing and needs no join against
//     event_type_schemas — which matters because the app role holds only SELECT
//     there and a join would be one more thing to get wrong in a query that runs
//     on every host restart.

// LedgerEmitter adapts a *Ledger to EventEmitter.
//
// It exists because Ledger.Emit returns the log POSITION and the write-ahead
// needs the event's IDENTITY: spawn_committed has to close the intent by id.
// Minting the id here rather than adding a method to Ledger keeps the append path
// single — this goes through exactly the same Emit, so it cannot become a route
// around validation or contract maintenance.
type LedgerEmitter struct {
	Ledger *Ledger
}

var _ EventEmitter = LedgerEmitter{}

func (e LedgerEmitter) Emit(ctx context.Context, req EmitRequest) (uuid.UUID, error) {
	// A DISABLED Ledger MUST NOT report a successful append.
	//
	// This is the one place the nil-Ledger-is-a-working-Ledger convention would
	// be actively dangerous rather than convenient. Ledger.Emit on a disabled
	// ledger returns (0, nil) — records nothing, succeeds — which is exactly
	// right for telemetry and catastrophic for a write-ahead: the spawn handler
	// would take a nil error as "the intent is recorded", create a worktree, and
	// leave a resource nothing in any log can attribute. The dispatcher should
	// never be running against a disabled store; if it is, this says so.
	if !e.Ledger.Enabled() {
		return uuid.Nil, fmt.Errorf("store: refusing to report a recorded event while the event log is disabled: a write-ahead that silently records nothing is worse than none")
	}
	if req.EventID == uuid.Nil {
		req.EventID = uuid.New()
	}
	if _, err := e.Ledger.Emit(ctx, req); err != nil {
		return uuid.Nil, err
	}
	return req.EventID, nil
}

// PgIntentReader reads intent state through a pgx pool.
type PgIntentReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

var _ IntentReader = (*PgIntentReader)(nil)

// schemaIDsFor collects every version's id for one event-type name.
func schemaIDsFor(reg *Registry, name string) []uuid.UUID {
	var ids []uuid.UUID
	for _, s := range reg.All() {
		if s.Name == name {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// openIntentsSQL finds spawn intents with no close.
//
// It reads OPEN_CONTRACTS rather than doing the anti-join, which is the whole
// reason that projection exists: it is maintained inside the append transaction,
// so it can never disagree with the log even across a crash, and the alternative
// makes every host restart run a full anti-join over the events table.
//
// The host predicate is applied in SQL rather than in Go so a host with a hundred
// peers does not fetch and discard every one of their intents on every restart.
const openIntentsSQL = `
	SELECT e.id, e.payload, e.at, e.workflow_instance_id
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	   AND e.payload->>'host_affinity' = $3
	 ORDER BY e.seq`

// failedIntentsSQL finds spawn intents already closed by a spawn_failed.
//
// The join is on closes_event_id, which is the log's own record of what closed
// what — not on any derived state — so a stray is only ever reclaimed on the
// strength of an event that says in as many words that this spawn failed.
const failedIntentsSQL = `
	SELECT i.id, i.payload, i.workflow_instance_id
	  FROM events i
	  JOIN events f ON f.closes_event_id = i.id AND f.schema_id = ANY($4)
	 WHERE i.project_id = $1
	   AND i.schema_id = ANY($2)
	   AND i.payload->>'host_affinity' = $3
	 ORDER BY i.seq`

// intentPayload is the part of a spawn_intent payload the reconciler reads.
type intentPayload struct {
	AgentName    string `json:"agent_name"`
	AgentType    string `json:"agent_type"`
	Branch       string `json:"branch"`
	HostAffinity string `json:"host_affinity"`
}

func (r *PgIntentReader) OpenIntents(ctx context.Context, projectID uuid.UUID, host string) ([]OpenIntent, error) {
	if host == "" {
		return nil, fmt.Errorf("store: reading open spawn intents requires a host; an empty one would match intents with no affinity and reconcile another host's work")
	}
	rows, err := r.Pool.Query(ctx, openIntentsSQL, projectID, schemaIDsFor(r.Registry, "spawn_intent"), host)
	if err != nil {
		return nil, fmt.Errorf("store: reading open spawn intents: %w", err)
	}
	defer rows.Close()

	var out []OpenIntent
	for rows.Next() {
		var (
			in      OpenIntent
			payload []byte
		)
		if err := rows.Scan(&in.EventID, &payload, &in.At, &in.WorkflowID); err != nil {
			return nil, fmt.Errorf("store: scanning an open spawn intent: %w", err)
		}
		var p intentPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("store: spawn intent %s has an unreadable payload: %w", in.EventID, err)
		}
		in.AgentName, in.AgentType, in.Branch, in.HostAffinity = p.AgentName, p.AgentType, p.Branch, p.HostAffinity
		out = append(out, in)
	}
	return out, rows.Err()
}

func (r *PgIntentReader) FailedIntents(ctx context.Context, projectID uuid.UUID, host string) ([]FailedIntent, error) {
	if host == "" {
		return nil, fmt.Errorf("store: reading failed spawn intents requires a host; an empty one would license reclaiming another host's resources")
	}
	rows, err := r.Pool.Query(ctx, failedIntentsSQL,
		projectID, schemaIDsFor(r.Registry, "spawn_intent"), host, schemaIDsFor(r.Registry, "spawn_failed"))
	if err != nil {
		return nil, fmt.Errorf("store: reading failed spawn intents: %w", err)
	}
	defer rows.Close()

	var out []FailedIntent
	for rows.Next() {
		var (
			in      FailedIntent
			payload []byte
		)
		if err := rows.Scan(&in.EventID, &payload, &in.WorkflowID); err != nil {
			return nil, fmt.Errorf("store: scanning a failed spawn intent: %w", err)
		}
		var p intentPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, fmt.Errorf("store: spawn intent %s has an unreadable payload: %w", in.EventID, err)
		}
		in.AgentName, in.Branch, in.HostAffinity = p.AgentName, p.Branch, p.HostAffinity
		out = append(out, in)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Notification state
// ---------------------------------------------------------------------------

// openNotifiesSQL finds owner_notify contracts with no close, for one recipient.
//
// Reads open_contracts for the same reason openIntentsSQL does: the projection is
// maintained inside the append transaction, so it cannot disagree with the log,
// and the anti-join alternative would run on every turn boundary of every agent —
// which is the single hottest path in this file.
const openNotifiesSQL = `
	SELECT e.id, e.workflow_instance_id
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	   AND e.payload->>'recipient' = $3
	 ORDER BY e.seq`

// PgNotifyReader reads outstanding notifications through a pgx pool.
type PgNotifyReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

var _ NotifyReader = (*PgNotifyReader)(nil)

func (r *PgNotifyReader) OpenNotifies(ctx context.Context, projectID uuid.UUID, recipient string) ([]OpenNotify, error) {
	if recipient == "" {
		// An empty recipient would match every notification whose payload has no
		// recipient — and acking somebody else's notification is exactly the
		// failure the open/close pair exists to prevent.
		return nil, fmt.Errorf("store: reading outstanding notifications requires a recipient")
	}
	rows, err := r.Pool.Query(ctx, openNotifiesSQL, projectID, schemaIDsFor(r.Registry, "owner_notify"), recipient)
	if err != nil {
		return nil, fmt.Errorf("store: reading outstanding notifications for %q: %w", recipient, err)
	}
	defer rows.Close()

	var out []OpenNotify
	for rows.Next() {
		n := OpenNotify{Recipient: recipient}
		if err := rows.Scan(&n.EventID, &n.WorkflowID); err != nil {
			return nil, fmt.Errorf("store: scanning an outstanding notification: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
