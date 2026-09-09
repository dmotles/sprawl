package uiapi

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// FleetMember is one agent as the EVENT LOG remembers it.
//
// # This is advisory data, and the view must say so
//
// `agent_sessions` is a projection, not the authority on who is running: the
// authority is the process table on whichever host the agent lives on, and
// nothing in this tree writes that table either. So every figure here is
// reconstructed from `turn_finished` events, which means:
//
//   - There is NO `alive` boolean, and there deliberately is not one. The log
//     can say when an agent last finished a turn; it cannot say whether the
//     process still exists. A retired agent and a wedged one are identical
//     here, and a boolean would invent a distinction the data cannot support.
//     LastTurnAt is the honest form of the same question, and interpreting it
//     is the reader's job.
//   - There is no Host and no WorkingOn. `turn_finished` payloads carry neither,
//     and a permanently-null field is noise a consumer has to write code around.
//   - TurnCount and the token totals are LOWER BOUNDS, twice over: the type is
//     `spillable`, so under load its events are dropped by design rather than
//     queued, and `agent_name` is optional in its schema, so an unattributable
//     turn is excluded here (see listFleetSQL).
type FleetMember struct {
	AgentName   string    `json:"agent_name"`
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectName string    `json:"project_name"`
	// TurnCount and the token figures are lower bounds. See the type doc.
	TurnCount    int   `json:"turn_count"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	FirstSeq     int64 `json:"first_seq"`
	LastSeq      int64 `json:"last_seq"`
	// LastTurnAt is when the agent last finished a turn, which is the closest
	// thing to liveness this schema holds. It is not a promise the agent exists.
	LastTurnAt time.Time `json:"last_turn_at"`
}

// FleetReader summarises agents from their turn history.
type FleetReader interface {
	ListFleet(ctx context.Context, opts ListOptions) ([]FleetMember, error)
}

// PgFleetReader is the Postgres implementation of FleetReader.
type PgFleetReader struct{ Pool Pool }

// Matched by event type NAME through event_type_schemas rather than by resolving
// a schema id: the name covers every version of the type, so a v2 of
// turn_finished keeps appearing here instead of silently emptying the view. That
// is the opposite of what internal/store does, correctly — the appender must pin
// a version because it WRITES, and this only reads.
//
// Grouped by (agent_name, project_id), so an agent that has worked in two
// projects appears once per project. The alternative — one row per agent showing
// its most recent project — hides the other half of its history behind a value
// that changes without warning.
//
// Turns whose payload has no agent_name are EXCLUDED, not bucketed under an
// empty name: `agent_name` is optional in the turn_finished schema, and a row
// labelled "" is not an agent, it is the residue of the ones that could not be
// attributed. Excluding them is why TurnCount is documented as a lower bound.
//
// The token sums are guarded by jsonb_typeof rather than cast directly. A bare
// `(payload->>'input_tokens')::bigint` raises on the first payload holding a
// non-number, and the blast radius of that is the whole view rather than the one
// bad row — the same reason the goals query compares `legacy` as jsonb instead
// of casting it to boolean. The appender validates against the type's schema,
// but this reads a table it does not own the writes to.
//
// Neither `schema_id` nor `at` is indexed, and there is no index for grouping by
// a payload key, so this is a scan. LIMIT bounds the rows returned, not the rows
// grouped. No index is added on speculation: the fix, if a measurement ever asks
// for one, is a summary table rather than an index on a jsonb expression.
const listFleetSQL = `
	SELECT e.payload->>'agent_name', e.project_id, COALESCE(p.remote_url, ''),
	       count(*),
	       COALESCE(sum(CASE WHEN jsonb_typeof(e.payload->'input_tokens') = 'number'
	                        THEN (e.payload->>'input_tokens')::bigint END), 0),
	       COALESCE(sum(CASE WHEN jsonb_typeof(e.payload->'output_tokens') = 'number'
	                        THEN (e.payload->>'output_tokens')::bigint END), 0),
	       min(e.seq), max(e.seq), max(e.at)
	  FROM events e
	  JOIN event_type_schemas s ON s.id = e.schema_id
	  LEFT JOIN projects p ON p.id = e.project_id
	 WHERE s.name = 'turn_finished'
	   AND COALESCE(e.payload->>'agent_name', '') <> ''
	   AND ($1::uuid IS NULL OR e.project_id = $1::uuid)
	 GROUP BY e.payload->>'agent_name', e.project_id, p.remote_url
	 ORDER BY max(e.seq) DESC
	 LIMIT $2`

// ListFleet returns agents, most recently active first.
func (r PgFleetReader) ListFleet(ctx context.Context, opts ListOptions) ([]FleetMember, error) {
	rows, err := r.Pool.Query(ctx, listFleetSQL, opts.ProjectID, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("uiapi: querying the fleet: %w", err)
	}
	defer rows.Close()

	out := []FleetMember{}
	for rows.Next() {
		var (
			m         FleetMember
			remoteURL string
		)
		if err := rows.Scan(&m.AgentName, &m.ProjectID, &remoteURL,
			&m.TurnCount, &m.InputTokens, &m.OutputTokens, &m.FirstSeq, &m.LastSeq, &m.LastTurnAt); err != nil {
			return nil, fmt.Errorf("uiapi: scanning a fleet member: %w", err)
		}
		m.ProjectName = ProjectName(remoteURL)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("uiapi: reading the fleet: %w", err)
	}
	return out, nil
}
