// operability.go — the OPERATOR's read surface: what work is outstanding
// across the whole project, and on which workflow instances (QUM-1252, M3a
// slice 9, backing `sprawl goals` and `sprawl workflows`).
//
// Deliberately separate from goalreader.go even though the SQL rhymes. Those
// readers answer an AGENT's questions about its own work and are scoped by
// owner; these answer a HUMAN's question about everyone's, and are scoped only
// by project. Folding them together would mean one accidental missing predicate
// away from an agent reading the whole fleet's goals through its own tool.
//
// Both are derived from the log and its open_contracts projection rather than
// from workflow_instances. That table exists, but its closed_at is written by
// whoever remembers to; open_contracts is maintained inside the append
// transaction, so it cannot disagree with the events it summarises.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// OpenGoal is one outstanding goal, whoever owns it.
type OpenGoal struct {
	GoalEventID uuid.UUID
	WorkflowID  uuid.UUID
	GoalType    string
	Owner       string
	// Legacy marks a goal that stands in for a prose-`spawn`ed agent rather than
	// one the engine drives. Surfaced rather than filtered because the point of
	// the paired legacy lifecycle events is that `sprawl goals` shows ALL
	// outstanding work — an operator reading a list that silently omits half the
	// fleet is worse off than one with no list.
	Legacy   bool
	OpenedAt time.Time
}

// allOpenGoalsSQL is every open goal in the project, oldest first.
//
// It reads THREE contract types, and that is the point rather than an
// optimisation: `goal_opened` is engine-driven work, `rework_requested` is
// engine-driven work that is being redone (QUM-1336), `agent_spawned` is a
// prose-spawned agent's existence (slice 10), and a listing that showed only the
// first would answer "what does the fleet still owe?" with a fraction of the
// truth while looking authoritative. The payloads name the same two things
// under different keys, so each is COALESCEd onto one column — `owner`/
// `agent_name`, `goal_type`/`agent_type`.
//
// A rework has NO type key of its own: its subject lives on the goal it
// follows, several hops back along follows_event_id. Rather than walk that
// chain in the listing query, the type is the literal `rework` — which is the
// word an operator needs to recognise the item anyway, and $3 (the rework
// schema ids) is what selects it. NULLIF before the COALESCE so a payload that
// carries an EMPTY goal_type still falls through to it.
//
// `legacy` is read as a jsonb equality rather than a cast to boolean. A cast
// raises on any payload whose `legacy` is a string or a number, and it would
// take the whole listing down over one malformed event written by some future
// producer — the operator's view of outstanding work is exactly the thing that
// must survive a bad row.
const allOpenGoalsSQL = `
	SELECT e.id, e.workflow_instance_id,
	       COALESCE(NULLIF(e.payload->>'goal_type', ''),
	                NULLIF(e.payload->>'agent_type', ''),
	                CASE WHEN e.schema_id = ANY($3) THEN 'rework' END,
	                ''),
	       COALESCE(e.payload->>'owner', e.payload->>'agent_name', ''),
	       COALESCE(e.payload->'legacy' = 'true'::jsonb, false),
	       oc.opened_at
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	 ORDER BY e.seq`

// WorkflowSummary is one workflow instance that still has outstanding work.
type WorkflowSummary struct {
	WorkflowID uuid.UUID
	// Events is every event on the instance, not just the open ones: it is the
	// figure that separates "just started" from "has been going a while".
	Events        int
	OpenContracts int
	StartedAt     time.Time
	LastEventAt   time.Time
}

// openWorkflowsSQL summarises the instances with at least one open contract.
//
// HAVING rather than a limit: an instance whose contracts are all closed is
// finished, and listing finished work would make the output grow with history
// instead of with what is outstanding. Such an instance is still readable by id
// through EventsByWorkflowInstance, which is what the command's hint points at.
//
// The LEFT JOIN is what makes `Events` the whole instance while `OpenContracts`
// counts only the projection rows; an inner join would make both the same
// number and neither the one asked for.
const openWorkflowsSQL = `
	SELECT e.workflow_instance_id,
	       count(*), count(oc.event_id), min(e.at), max(e.at)
	  FROM events e
	  LEFT JOIN open_contracts oc ON oc.event_id = e.id
	 WHERE e.project_id = $1
	 GROUP BY e.workflow_instance_id
	HAVING count(oc.event_id) > 0
	 ORDER BY min(e.seq)`

// PgOperabilityReader answers the operator's project-wide questions.
type PgOperabilityReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

// AllOpenGoals returns every open goal in the project, oldest first.
func (r *PgOperabilityReader) AllOpenGoals(ctx context.Context, projectID uuid.UUID) ([]OpenGoal, error) {
	rework := schemaIDsFor(r.Registry, "rework_requested")
	schemas := append(schemaIDsFor(r.Registry, "goal_opened"), schemaIDsFor(r.Registry, "agent_spawned")...)
	schemas = append(schemas, rework...)
	rows, err := r.Pool.Query(ctx, allOpenGoalsSQL, projectID, schemas, rework)
	if err != nil {
		return nil, fmt.Errorf("store: reading open goals: %w", err)
	}
	defer rows.Close()

	var out []OpenGoal
	for rows.Next() {
		var g OpenGoal
		if err := rows.Scan(&g.GoalEventID, &g.WorkflowID, &g.GoalType, &g.Owner, &g.Legacy, &g.OpenedAt); err != nil {
			return nil, fmt.Errorf("store: scanning an open goal: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading open goals: %w", err)
	}
	return out, nil
}

// OpenWorkflows returns the instances that still have outstanding contracts.
func (r *PgOperabilityReader) OpenWorkflows(ctx context.Context, projectID uuid.UUID) ([]WorkflowSummary, error) {
	rows, err := r.Pool.Query(ctx, openWorkflowsSQL, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: reading open workflows: %w", err)
	}
	defer rows.Close()

	var out []WorkflowSummary
	for rows.Next() {
		var w WorkflowSummary
		if err := rows.Scan(&w.WorkflowID, &w.Events, &w.OpenContracts, &w.StartedAt, &w.LastEventAt); err != nil {
			return nil, fmt.Errorf("store: scanning an open workflow: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading open workflows: %w", err)
	}
	return out, nil
}

// operabilityReader refuses a store that cannot answer, for goalReader's
// reason: "nothing is outstanding" is the answer an operator acts on by going
// home, so a switched-off or unreachable log must not be able to produce it.
func (l *Ledger) operabilityReader() (*PgOperabilityReader, error) {
	if !l.Enabled() {
		return nil, fmt.Errorf("store: the event log is disabled on this host, so there is no record of outstanding work; enable it with `sprawl config set event_log.enabled true`")
	}
	if l.degradedErr != nil {
		return nil, fmt.Errorf("store: the event log is unreachable, so an empty list of outstanding work would be a claim it cannot support: %w", l.degradedErr)
	}
	return &PgOperabilityReader{Pool: l.pool, Registry: l.registry}, nil
}

// AllOpenGoals answers "what is the fleet working on?".
func (l *Ledger) AllOpenGoals(ctx context.Context) ([]OpenGoal, error) {
	r, err := l.operabilityReader()
	if err != nil {
		return nil, err
	}
	return r.AllOpenGoals(ctx, l.projectID)
}

// OpenWorkflows answers "which workflow instances are still in flight?".
func (l *Ledger) OpenWorkflows(ctx context.Context) ([]WorkflowSummary, error) {
	r, err := l.operabilityReader()
	if err != nil {
		return nil, err
	}
	return r.OpenWorkflows(ctx, l.projectID)
}
