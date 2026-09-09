package uiapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Limits on ?limit=. DefaultEventLimit is what an unspecified request gets;
// MaxEventLimit is the ceiling, because `events` has no index serving an
// unbounded scan and a browser must not be able to ask for the whole log.
const (
	DefaultEventLimit = 100
	MaxEventLimit     = 1000
)

// Event is one row of the ledger as the UI sees it.
//
// Type is the schema's NAME, resolved by the join, because a schema uuid is
// useless to a reader. Payload is passed through as raw JSON rather than
// re-marshalled through a map: it is capped at 8 KB by events_payload_thin_ck,
// its shape varies per event type, and round-tripping it through Go would
// reorder keys and mangle numeric precision for no gain.
type Event struct {
	Seq                int64           `json:"seq"`
	ID                 uuid.UUID       `json:"id"`
	Type               string          `json:"type"`
	At                 time.Time       `json:"at"`
	ProjectID          uuid.UUID       `json:"project_id"`
	WorkflowInstanceID uuid.UUID       `json:"workflow_instance_id"`
	Payload            json.RawMessage `json:"payload"`
}

// EventReader reads the ledger. Handlers depend on this, never on a pool — see
// the package comment's write-seam note.
type EventReader interface {
	// ListEvents returns the most recent matching events, newest first. opts is
	// assumed already validated by the caller.
	ListEvents(ctx context.Context, opts ListOptions) ([]Event, error)
}

// PgEventReader is the Postgres implementation of EventReader.
type PgEventReader struct{ Pool Pool }

// Every filter here is on an INDEXED column, and that is the whole design
// constraint. The indexed paths are (project_id, seq), (workflow_instance_id,
// seq), and seq itself; `at`, `schema_id`, `owner_agent_id` and the payload have
// no index. So there is deliberately no ?type= and no time-range filter: either
// one turns a browser-reachable endpoint into a sequential scan of the whole
// ledger, and adding an index to enable a filter nobody has measured a need for
// is the wrong order to do that in.
//
// ?before_seq= is keyset pagination rather than OFFSET. OFFSET makes page N cost
// N pages of work, and on an append-only log it also SHIFTS: rows arrive at the
// head between requests, so paging by offset shows the reader rows they have
// already seen and skips ones they have not. `seq < $3` cannot drift, because
// seq is immutable once assigned.
//
// LEFT JOIN, not an inner join: an event whose schema row is missing is a
// serious problem, and an inner join would make it vanish from the ledger
// browser — the one view whose job is to show everything. It surfaces with an
// empty type instead.
const listEventsSQL = `
	SELECT e.seq, e.id, COALESCE(s.name, ''), e.at, e.project_id, e.workflow_instance_id, e.payload
	  FROM events e
	  LEFT JOIN event_type_schemas s ON s.id = e.schema_id
	 WHERE ($1::uuid IS NULL OR e.project_id = $1::uuid)
	   AND ($2::uuid IS NULL OR e.workflow_instance_id = $2::uuid)
	   AND ($3::bigint IS NULL OR e.seq < $3::bigint)
	 ORDER BY e.seq DESC
	 LIMIT $4`

// ListEvents returns the newest matching events in seq order.
//
// Newest-first because that is what a ledger browser opens on. seq rather than
// `at` is the ordering key: seq is the log's total order and is indexed, while
// `at` has no index at all and is a wall clock two appenders can disagree
// about.
func (r PgEventReader) ListEvents(ctx context.Context, opts ListOptions) ([]Event, error) {
	rows, err := r.Pool.Query(ctx, listEventsSQL,
		opts.ProjectID, opts.WorkflowInstanceID, opts.BeforeSeq, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("uiapi: querying events: %w", err)
	}
	defer rows.Close()

	// Non-nil so an empty ledger encodes as [] rather than null: a JSON null
	// makes every consumer handle a second empty case for no reason.
	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.ID, &e.Type, &e.At, &e.ProjectID, &e.WorkflowInstanceID, &e.Payload); err != nil {
			return nil, fmt.Errorf("uiapi: scanning event: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("uiapi: reading events: %w", err)
	}
	return out, nil
}
