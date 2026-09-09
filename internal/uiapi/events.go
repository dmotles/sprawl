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
	// ListEvents returns the most recent events, newest first. limit is
	// assumed already validated by the caller.
	ListEvents(ctx context.Context, limit int) ([]Event, error)
}

// PgEventReader is the Postgres implementation of EventReader.
type PgEventReader struct{ Pool Pool }

// ListEvents returns the newest `limit` events in seq order.
//
// Newest-first because that is what a ledger browser opens on. seq rather than
// `at` is the ordering key: seq is the log's total order and is indexed, while
// `at` has no index at all and is a wall clock two appenders can disagree
// about.
//
// LEFT JOIN, not an inner join: an event whose schema row is missing is a
// serious problem, and an inner join would make it vanish from the ledger
// browser — the one view whose job is to show everything. It surfaces with an
// empty type instead.
func (r PgEventReader) ListEvents(ctx context.Context, limit int) ([]Event, error) {
	rows, err := r.Pool.Query(ctx,
		`SELECT e.seq, e.id, COALESCE(s.name, ''), e.at, e.project_id, e.workflow_instance_id, e.payload
		 FROM events e
		 LEFT JOIN event_type_schemas s ON s.id = e.schema_id
		 ORDER BY e.seq DESC
		 LIMIT $1`, limit)
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
