package uiapi

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ListOptions is the filtering every list endpoint accepts.
//
// ProjectID is a POINTER because "no filter" and "this project" are different
// requests and a zero uuid is a legitimate-looking value, not an absence. nil
// means all projects, which is the v1 default (tower, QUM-1349): there is one
// operator and "what is in the log" is the whole point, so a mandatory selector
// would hide data behind a control nobody asked for.
type ListOptions struct {
	Limit     int
	ProjectID *uuid.UUID
	// WorkflowInstanceID filters to one instance, read only by /api/events.
	WorkflowInstanceID *uuid.UUID
	// BeforeSeq is the keyset pagination cursor, read only by /api/events:
	// strictly-less-than, so passing the last seq of a page yields the next one.
	BeforeSeq *int64
	// Bucket is the time-bucket width, read only by /api/usage. Empty means
	// DefaultBucket. It lives here rather than in a usage-specific options type
	// so every endpoint keeps one parser and one validation path — the drift
	// that duplication produces is the whole reason handleList is generic.
	Bucket string
}

// Goal is one outstanding piece of work.
//
// # Only OPEN goals, and why there is no `state` field
//
// The set is defined by `open_contracts`, which is maintained inside the same
// transaction that appends the event, so it cannot disagree with the log it
// summarises. Closed-goal HISTORY is deliberately not served in v1: reaching
// it means walking `closes_event_id` from the closing event back to its
// opener, and that column carries no index (only `follows_event_id` has one,
// and only partially). Serving it would be an unindexed join over the whole
// log on a browser-reachable endpoint, and weave's standing instruction on
// this issue is to measure before adding an index rather than add one
// speculatively.
//
// This is a NARROWING of the contract first posted on QUM-1349, which promised
// `state`/`closed_at`/`outcome`. The amendment is recorded there too. A field
// that would always read "open" is worse than an absent one — it implies a
// filter that does not exist.
//
// GoalType and Owner are COALESCEd across two payload spellings each because
// three different contract types land in this list: `goal_opened` (engine work,
// `goal_type`/`owner`), `agent_spawned` (a prose-spawned agent's existence,
// `agent_type`/`agent_name`) and `rework_requested` (work being redone, which
// carries no type key of its own and is labelled from its schema name). A list
// showing only the first would answer "what does the fleet still owe?" with a
// fraction of the truth while looking authoritative. See
// store/operability.go's allOpenGoalsSQL, which this mirrors.
type Goal struct {
	ID                 uuid.UUID `json:"id"`
	Seq                int64     `json:"seq"`
	OpenedAt           time.Time `json:"opened_at"`
	ProjectID          uuid.UUID `json:"project_id"`
	ProjectName        string    `json:"project_name"`
	WorkflowInstanceID uuid.UUID `json:"workflow_instance_id"`
	GoalType           string    `json:"goal_type"`
	Owner              string    `json:"owner"`
	// Legacy marks a goal standing in for a prose-spawned agent rather than one
	// the engine drives. Surfaced rather than filtered, for allOpenGoalsSQL's
	// reason: an operator reading a list that silently omits half the fleet is
	// worse off than one with no list.
	Legacy bool `json:"legacy"`
}

// goalContractTypes are the three event types that open a unit of work.
var goalContractTypes = []string{"goal_opened", "agent_spawned", "rework_requested"}

// GoalReader reads outstanding work.
type GoalReader interface {
	ListGoals(ctx context.Context, opts ListOptions) ([]Goal, error)
}

// PgGoalReader is the Postgres implementation of GoalReader.
type PgGoalReader struct{ Pool Pool }

// Types are matched by NAME through a join on event_type_schemas rather than by
// resolving schema ids from a Registry the way internal/store does. uiapi has
// no Registry — it is a read-only process that does not append and so has no
// business pinning schema versions — and matching on name covers every version
// of a type automatically, which is the behaviour a viewer wants.
//
// `legacy` is compared as jsonb rather than cast to boolean: a cast raises on
// any payload whose `legacy` is a string or a number, which would take the
// whole listing down over one malformed event from some future producer.
const listGoalsSQL = `
	SELECT e.id, e.seq, oc.opened_at, e.project_id, COALESCE(p.remote_url, ''),
	       e.workflow_instance_id,
	       COALESCE(NULLIF(e.payload->>'goal_type', ''),
	                NULLIF(e.payload->>'agent_type', ''),
	                CASE WHEN s.name = 'rework_requested' THEN 'rework' END,
	                ''),
	       COALESCE(e.payload->>'owner', e.payload->>'agent_name', ''),
	       COALESCE(e.payload->'legacy' = 'true'::jsonb, false)
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	  JOIN event_type_schemas s ON s.id = e.schema_id
	  LEFT JOIN projects p ON p.id = e.project_id
	 WHERE s.name = ANY($1)
	   AND ($2::uuid IS NULL OR e.project_id = $2::uuid)
	 ORDER BY e.seq DESC
	 LIMIT $3`

// ListGoals returns the outstanding goals, newest first.
func (r PgGoalReader) ListGoals(ctx context.Context, opts ListOptions) ([]Goal, error) {
	rows, err := r.Pool.Query(ctx, listGoalsSQL, goalContractTypes, opts.ProjectID, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("uiapi: querying goals: %w", err)
	}
	defer rows.Close()

	out := []Goal{}
	for rows.Next() {
		var (
			g         Goal
			remoteURL string
		)
		if err := rows.Scan(&g.ID, &g.Seq, &g.OpenedAt, &g.ProjectID, &remoteURL,
			&g.WorkflowInstanceID, &g.GoalType, &g.Owner, &g.Legacy); err != nil {
			return nil, fmt.Errorf("uiapi: scanning a goal: %w", err)
		}
		// The URL is reduced HERE and the raw value is dropped on the floor —
		// Goal has no field for it. See ProjectName.
		g.ProjectName = ProjectName(remoteURL)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("uiapi: reading goals: %w", err)
	}
	return out, nil
}
