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
	// Owner is the REQUESTER — who asked for the work and who the result is
	// notified to. It is not necessarily the reader: an agent spawned for a
	// goal reaches it by assignment, and reads someone else's name here.
	Owner    string
	OpenedAt time.Time
}

// openGoalsForAgentSQL finds every OPEN goal an agent is party to.
//
// Reads open_contracts rather than anti-joining events against itself, for the
// reason openNotifiesSQL gives: the projection is maintained inside the append
// transaction, so it cannot disagree with the log.
//
// Both identities come from the PAYLOAD, not from events.owner_agent_id. That
// column exists but is not populated in practice — notify.go:159 documents the
// same thing for the same reason — and reading it would return an empty set
// that looks exactly like "this agent has no goal".
//
// TWO predicates, because a goal has two parties and they are not the same
// agent. `owner` is the REQUESTER: who asked for the work and who the result is
// notified to when the goal closes. The agent that DOES the work is named
// later, by the spawn_requested the dispatcher appends onto the same workflow
// instance — it cannot be in goal_opened, since the name is allocated after the
// goal exists. Matching on `owner` alone therefore answers "you have no goal"
// to the one agent whose entire prompt is that goal, and report_result then
// refuses its close, so the contract can only ever be discharged by the agent
// that did not do the work.
// A rework_requested is one of the goal-shaped schemas $2 carries, not a
// separate question. It opens a contract of its own and is discharged by its own
// goal_closed, so to the agent doing the work it IS the goal — and an agent that
// could not see it would read "you have no goal" and have nothing to close. Its
// goal_type comes from the goal it FOLLOWS, because the rework payload states
// what was wrong rather than restating the task.
const openGoalsForAgentSQL = `
	SELECT e.id, e.workflow_instance_id,
	       COALESCE(e.payload->>'goal_type', f.payload->>'goal_type', ''),
	       COALESCE(e.payload->>'owner', ''), oc.opened_at
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	  LEFT JOIN events f ON f.id = e.follows_event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	   AND (e.payload->>'owner' = $3
	        OR EXISTS (SELECT 1
	                     FROM events s
	                    WHERE s.project_id = e.project_id
	                      AND s.workflow_instance_id = e.workflow_instance_id
	                      AND s.schema_id = ANY($4)
	                      AND s.payload->>'agent_name' = $3))
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
	rows, err := r.Pool.Query(ctx, openGoalsForAgentSQL, projectID,
		schemaIDsFor(r.Registry, "goal_opened", "rework_requested"), agent, schemaIDsFor(r.Registry, "spawn_requested"))
	if err != nil {
		return nil, fmt.Errorf("store: reading open goals for %q: %w", agent, err)
	}
	defer rows.Close()

	var out []AgentGoal
	for rows.Next() {
		// Owner is SCANNED, not assumed to be the caller: the assigned agent
		// reaches its goal through the second predicate, and it needs to know
		// who to report to rather than being told it is its own requester.
		var g AgentGoal
		if err := rows.Scan(&g.GoalEventID, &g.WorkflowID, &g.GoalType, &g.Owner, &g.OpenedAt); err != nil {
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

// goalReader builds a reader bound to this Ledger's pool and registry.
//
// A disabled or degraded Ledger is REFUSED here rather than answered with an
// empty set. "You own no open goals" is a legitimate answer that an agent will
// act on — it means the work is done — so a store that cannot reach Postgres
// must not be able to produce it. nil *Ledger IS the disabled state, since Open
// returns (nil, nil) when the flag is off, and Enabled is nil-safe.
func (l *Ledger) goalReader() (*PgGoalReader, error) {
	if !l.Enabled() {
		return nil, fmt.Errorf("store: the event log is disabled on this host, so it cannot answer questions about goals; enable it with `sprawl config set event_log.enabled true`")
	}
	if l.degradedErr != nil {
		return nil, fmt.Errorf("store: the event log is unreachable, so a goal read would report absence it cannot establish: %w", l.degradedErr)
	}
	return &PgGoalReader{Pool: l.pool, Registry: l.registry}, nil
}

// OpenGoalsForAgent answers "what goal am I working on?" for one agent.
//
// Lives on the Ledger rather than only on PgGoalReader because the project id
// is the one argument a caller must never choose. An MCP tool that picked its
// own would happily return another repo's goals, and nothing in the result
// would say so.
func (l *Ledger) OpenGoalsForAgent(ctx context.Context, agent string) ([]AgentGoal, error) {
	r, err := l.goalReader()
	if err != nil {
		return nil, err
	}
	return r.OpenGoalsForAgent(ctx, l.projectID, agent)
}

// EventsByWorkflowInstance answers "what has happened on my goal?".
func (l *Ledger) EventsByWorkflowInstance(ctx context.Context, workflowID uuid.UUID, afterSeq int64, limit int) ([]DispatchedEvent, error) {
	r, err := l.goalReader()
	if err != nil {
		return nil, err
	}
	return r.EventsByWorkflowInstance(ctx, l.projectID, workflowID, afterSeq, limit)
}

// GoalOutcome is the closed set of ways a goal can end.
//
// A CLOSED set rather than the free string goal_closed.json permits, because
// `outcome` is what anyone later asks the log about ("how many goals failed?").
// A field every agent spells differently is a field nobody can query, and the
// seed schema validator implements a deliberate keyword subset with no `enum`,
// so this is the only layer that can hold the line.
type GoalOutcome string

const (
	GoalSucceeded GoalOutcome = "success"
	GoalFailed    GoalOutcome = "failure"
	GoalBlocked   GoalOutcome = "blocked"
)

// ValidGoalOutcomes is the ordered set, for error messages and for callers that
// need to render the choice.
var ValidGoalOutcomes = []GoalOutcome{GoalSucceeded, GoalFailed, GoalBlocked}

func validGoalOutcome(o GoalOutcome) bool {
	for _, v := range ValidGoalOutcomes {
		if v == o {
			return true
		}
	}
	return false
}

// CloseGoalForAgent closes one of agent's own open goals.
//
// The ownership check is not a courtesy. `closes_event_id` deletes the row from
// open_contracts, and the log is monotone — a close cannot be taken back, and a
// defect found afterwards emits rework rather than a mutation. Closing somebody
// else's goal is therefore unrecoverable, so the goal is looked up THROUGH the
// caller's own open set rather than by id: an id that is not in that set is
// refused whatever the reason.
//
// The workflow instance is taken from the goal rather than from the caller. An
// agent that guessed it wrong would append a well-formed close onto an unrelated
// instance, and the replay that derives that instance's cursor would fold over
// an event that never belonged to it.
func (l *Ledger) CloseGoalForAgent(ctx context.Context, agent string, goalEventID uuid.UUID, outcome GoalOutcome, summary string) (uuid.UUID, error) {
	if !validGoalOutcome(outcome) {
		return uuid.Nil, fmt.Errorf("store: %q is not a goal outcome; use one of %v", outcome, ValidGoalOutcomes)
	}
	open, err := l.OpenGoalsForAgent(ctx, agent)
	if err != nil {
		return uuid.Nil, err
	}
	var goal *AgentGoal
	for i := range open {
		if open[i].GoalEventID == goalEventID {
			goal = &open[i]
			break
		}
	}
	if goal == nil {
		// Deliberately one message for two causes. Telling them apart needs a
		// second query, and the remedy is the same either way: re-read your own
		// goals. Naming both is honest; naming one would be a guess.
		return uuid.Nil, fmt.Errorf("store: %s is not an open goal owned by %q — it is already closed, or it belongs to another agent; re-read your own open goals", goalEventID, agent)
	}

	closeID := uuid.New()
	// The summary may be a whole file's contents (QUM-1347); see goaltext.go.
	// It is spilled BEFORE the append, and a spill failure refuses the close
	// rather than recording a result whose body was dropped — the close is
	// permanent, so a lossy one can never be corrected.
	payload := map[string]any{"outcome": string(outcome)}
	artifactID, err := l.putTextField(ctx, payload, "summary", artifactKindGoalResult, summary)
	if err != nil {
		return uuid.Nil, fmt.Errorf("store: closing goal %s: %w", goalEventID, err)
	}
	// PROVISIONAL, and the only in-code record of that (QUM-1340). One generic
	// `goal_closed` closes every goal type, rather than a per-type closer such
	// as report_research_result. It is a working default pending dmotles's
	// confirm on QUM-1252 and is kept swappable: four production sites emit or
	// match this name (here, internal/engine/research.go, internal/engine/bug.go,
	// cmd/store_dispatch.go), so changing it is a four-line edit, not a redesign.
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName: "goal_closed", TypeVersion: 1,
		EventID:            closeID,
		WorkflowInstanceID: goal.WorkflowID,
		ClosesEventID:      &goalEventID,
		ArtifactID:         artifactID,
		Payload:            payload,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: closing goal %s: %w", goalEventID, err)
	}
	return closeID, nil
}
