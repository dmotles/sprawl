package uiapi

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Workflow states. Derived, never read from a column.
const (
	// WorkflowInFlight means the instance still has at least one open contract.
	WorkflowInFlight = "in_flight"
	// WorkflowSettled means it has none left.
	WorkflowSettled = "settled"
)

// Workflow is one workflow instance, summarised.
//
// # Every field here is DERIVED from `events`
//
// `workflow_instances` exists in the schema and NOTHING WRITES IT — no INSERT,
// no UPDATE, anywhere in the tree. `events.workflow_instance_id` is a bare uuid
// with no foreign key, so an instance exists only as a grouping of events. The
// WorkflowsView's copy says an instance is in flight "while it has no
// closed_at timestamp"; there is no such timestamp to read, and a query against
// that table would return nothing on a busy system.
//
// So State is computed: in_flight iff OpenContracts > 0. This matches
// store/operability.go's openWorkflowsSQL, which is the one in-tree summary of
// the same question, and it is genuinely WEAKER than a real status column —
// the schema records no failure state, so a crashed instance and a running one
// look alike here. The view's existing caveat to that effect is correct and
// must stay.
//
// There is no StartedAt/ClosedAt/BudgetTokens/BudgetUSD, because there is
// nothing to populate them from. FirstAt/LastAt are the min/max of the
// instance's own events, which is a different and honest claim.
type Workflow struct {
	WorkflowInstanceID uuid.UUID `json:"workflow_instance_id"`
	ProjectID          uuid.UUID `json:"project_id"`
	ProjectName        string    `json:"project_name"`
	// EventCount is every event on the instance, not just the open ones: it is
	// the figure that separates "just started" from "has been going a while".
	EventCount    int       `json:"event_count"`
	OpenContracts int       `json:"open_contract_count"`
	FirstSeq      int64     `json:"first_seq"`
	LastSeq       int64     `json:"last_seq"`
	FirstAt       time.Time `json:"first_at"`
	LastAt        time.Time `json:"last_at"`
	State         string    `json:"state"`
}

// WorkflowReader summarises workflow instances.
type WorkflowReader interface {
	ListWorkflows(ctx context.Context, opts ListOptions) ([]Workflow, error)
}

// PgWorkflowReader is the Postgres implementation of WorkflowReader.
type PgWorkflowReader struct{ Pool Pool }

// Unlike openWorkflowsSQL this has no HAVING clause: `sprawl workflows` answers
// "what is outstanding?" and hides settled instances, but the Workflows VIEW is
// a browser for the whole set, and an instance vanishing from it the moment its
// last contract closed would look like data loss. The state is reported instead
// of being used as a filter.
//
// ORDER BY max(e.seq) DESC — most recently active first, and by seq rather than
// by max(at) because seq is the log's total order while `at` is a wall clock two
// appenders can disagree about.
//
// This is a full aggregate over the instance's events, and there is no index
// serving GROUP BY workflow_instance_id. That is the same cost the in-tree
// query already pays; LIMIT bounds the rows RETURNED, not the rows grouped. If
// this becomes slow the fix is a summary table or an index chosen against a
// measurement, not a speculative one added here.
const listWorkflowsSQL = `
	SELECT e.workflow_instance_id, e.project_id, COALESCE(p.remote_url, ''),
	       count(*), count(oc.event_id),
	       min(e.seq), max(e.seq), min(e.at), max(e.at)
	  FROM events e
	  LEFT JOIN open_contracts oc ON oc.event_id = e.id
	  LEFT JOIN projects p ON p.id = e.project_id
	 WHERE ($1::uuid IS NULL OR e.project_id = $1::uuid)
	 GROUP BY e.workflow_instance_id, e.project_id, p.remote_url
	 ORDER BY max(e.seq) DESC
	 LIMIT $2`

// ListWorkflows returns instance summaries, most recently active first.
func (r PgWorkflowReader) ListWorkflows(ctx context.Context, opts ListOptions) ([]Workflow, error) {
	rows, err := r.Pool.Query(ctx, listWorkflowsSQL, opts.ProjectID, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("uiapi: querying workflows: %w", err)
	}
	defer rows.Close()

	out := []Workflow{}
	for rows.Next() {
		var (
			w         Workflow
			remoteURL string
		)
		if err := rows.Scan(&w.WorkflowInstanceID, &w.ProjectID, &remoteURL,
			&w.EventCount, &w.OpenContracts, &w.FirstSeq, &w.LastSeq, &w.FirstAt, &w.LastAt); err != nil {
			return nil, fmt.Errorf("uiapi: scanning a workflow: %w", err)
		}
		w.ProjectName = ProjectName(remoteURL)
		// The LEFT JOIN is what makes these two counts different numbers:
		// EventCount is the whole instance, OpenContracts only the projection
		// rows. An inner join would make both the same and neither the one
		// asked for.
		w.State = WorkflowSettled
		if w.OpenContracts > 0 {
			w.State = WorkflowInFlight
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("uiapi: reading workflows: %w", err)
	}
	return out, nil
}
