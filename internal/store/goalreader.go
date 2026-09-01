// goalreader.go — the read surface an agent's own tools need (QUM-1252, M3a).
//
// `cmd/store.go` states the position these readers are built to satisfy:
// "Agents get narrow tools and never raw SQL". That cuts both ways — it rules
// out a general query surface, and it obliges the store to grow a NARROW,
// typed reader for each question an agent is allowed to ask. There are exactly
// two of those questions here:
//
//	"what goal am I working on?"     -> OpenGoalsForAgent
//	"what has happened on it?"       -> EventsByWorkflowInstance
//
// Both are reads with no side effect and no cursor of their own, which is why
// they live apart from the dispatcher's reader: nothing here claims, advances,
// or acknowledges anything.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AgentGoal is one open goal owned by one agent.
type AgentGoal struct {
	// GoalEventID is the goal_opened event — the CONTRACT id, which is what a
	// close must reference. Not the workflow instance: several goals can share
	// an instance, and closing the wrong one is unrecoverable.
	GoalEventID uuid.UUID
	WorkflowID  uuid.UUID
	GoalType    string
	Owner       string
	OpenedAt    time.Time
}

// openGoalsForAgentSQL finds every OPEN goal owned by one agent.
//
// Reads open_contracts rather than anti-joining events against itself, for the
// reason openNotifiesSQL gives: the projection is maintained inside the append
// transaction, so it cannot disagree with the log.
//
// Ownership comes from the PAYLOAD, not from events.owner_agent_id. That column
// exists but is not populated in practice — notify.go:159 documents the same
// thing for the same reason — and reading it would return an empty set that
// looks exactly like "this agent has no goal".
const openGoalsForAgentSQL = `
	SELECT e.id, e.workflow_instance_id, COALESCE(e.payload->>'goal_type', ''), oc.opened_at
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	   AND e.payload->>'owner' = $3
	 ORDER BY e.seq`

// PgGoalReader answers an agent's questions about its own work.
type PgGoalReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

// OpenGoalsForAgent returns every open goal owned by agent, oldest first.
//
// ALL of them, deliberately, rather than "the" goal. An agent normally owns one
// and the caller can say so, but the store cannot: picking the oldest here
// would bake a silent tie-break into the data layer, and an agent handed the
// wrong goal of two would have no way to notice. Ambiguity is the caller's to
// resolve, loudly.
//
// An empty agent name is refused rather than matched: `payload->>'owner'` is
// NULL for events that carry no owner, and an empty string would be a
// surprising match for a caller that simply lost track of its own identity.
func (r *PgGoalReader) OpenGoalsForAgent(ctx context.Context, projectID uuid.UUID, agent string) ([]AgentGoal, error) {
	if agent == "" {
		return nil, fmt.Errorf("store: reading an agent's open goals requires an agent name")
	}
	rows, err := r.Pool.Query(ctx, openGoalsForAgentSQL, projectID, schemaIDsFor(r.Registry, "goal_opened"), agent)
	if err != nil {
		return nil, fmt.Errorf("store: reading open goals for %q: %w", agent, err)
	}
	defer rows.Close()

	var out []AgentGoal
	for rows.Next() {
		g := AgentGoal{Owner: agent}
		if err := rows.Scan(&g.GoalEventID, &g.WorkflowID, &g.GoalType, &g.OpenedAt); err != nil {
			return nil, fmt.Errorf("store: scanning an open goal for %q: %w", agent, err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading open goals for %q: %w", agent, err)
	}
	return out, nil
}

// eventsByInstanceSQL is one workflow instance's slice of the log.
//
// Its column list is eventScanSQL's, exactly, because both feed
// scanDispatchedEvents — a column added to one and not the other is a scan
// arity mismatch that only Postgres can detect, and only at runtime.
//
// `seq > $3` and `ORDER BY e.seq` are not cosmetic here. Replay folds Advance
// over this slice and is order-sensitive by construction, so an unordered read
// does not fail — it derives a plausible WRONG cursor, which is the whole class
// of defect deriving the cursor was meant to eliminate.
const eventsByInstanceSQL = `
	SELECT e.seq, e.id, e.project_id, e.workflow_instance_id, e.schema_id,
	       e.agent_session_id, e.owner_agent_id, e.closes_event_id, e.payload, e.at,
	       e.follows_event_id
	  FROM events e
	 WHERE e.project_id = $1 AND e.workflow_instance_id = $2 AND e.seq > $3
	 ORDER BY e.seq
	 LIMIT $4`

// EventsByWorkflowInstance returns one instance's events in log order.
//
// afterSeq and limit are the same paging contract the catch-up scan uses: pass
// 0 and a bound for the first page, then the last seq you saw. Bounded rather
// than "the whole history" because a long-running goal's log is unbounded and
// this is called from an agent's tool, in a turn, with a context deadline.
func (r *PgGoalReader) EventsByWorkflowInstance(ctx context.Context, projectID, workflowID uuid.UUID, afterSeq int64, limit int) ([]DispatchedEvent, error) {
	if limit <= 0 {
		// A zero limit reaches Postgres as LIMIT 0 and returns nothing, which
		// is indistinguishable from a goal with no events yet.
		return nil, fmt.Errorf("store: reading workflow instance %s requires a positive limit, got %d", workflowID, limit)
	}
	rows, err := r.Pool.Query(ctx, eventsByInstanceSQL, projectID, workflowID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("store: reading events for workflow instance %s: %w", workflowID, err)
	}
	return scanDispatchedEvents(rows, r.Registry)
}
