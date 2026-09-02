// tools_goals.go — the two READ tools an agent uses to see its own work
// (QUM-1252, M3a slice 6b).
//
//	reread_my_goal   -> "what goal am I working on?"
//	get_workflow_log -> "what has happened on it?"
//
// Both are reads. Nothing here appends, claims or acknowledges anything, which
// is why they can be answered from a degraded-but-readable store and why they
// carry no idempotency concerns. The write path (report_result) is deliberately
// a separate slice.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/store"
)

// GoalSource is the read surface these tools need, declared here rather than in
// store because the consumer defines the interface. *store.Ledger satisfies it.
//
// Enabled is part of the interface on purpose: a nil *store.Ledger is the
// DISABLED store (Open returns (nil, nil) when the flag is off), and stuffing
// one into an interface produces a non-nil interface value. Enabled is nil-safe,
// so it — not `s.goals != nil` — is the gate that actually reads the flag.
type GoalSource interface {
	Enabled() bool
	OpenGoalsForAgent(ctx context.Context, agent string) ([]store.AgentGoal, error)
	EventsByWorkflowInstance(ctx context.Context, workflowID uuid.UUID, afterSeq int64, limit int) ([]store.DispatchedEvent, error)
	// CloseGoalForAgent is the one WRITE here. It takes the agent name rather
	// than trusting a goal id alone, because the store — not the tool — is
	// where "is this yours?" has to be answered against the open set.
	CloseGoalForAgent(ctx context.Context, agent string, goalEventID uuid.UUID, outcome store.GoalOutcome, summary string) (uuid.UUID, error)
	// AskUser opens a user_question contract. It lives on this interface rather
	// than a second one so there is a single attach point and a single
	// enabled/degraded gate: a separate WithUserInbox would be one more piece of
	// wiring to forget, and forgetting it would make a working store report
	// itself switched off.
	AskUser(ctx context.Context, asker, question, questionContext string, goalEventID *uuid.UUID) (uuid.UUID, error)
	// AskQuestions / AnswerQuestions are the agent-to-agent pair, here for the
	// same single-attach-point reason as AskUser. Both take the caller's name
	// rather than trusting an id alone: "is this addressed to you?" is answered
	// in the store against the open set, never by whichever tool remembered.
	AskQuestions(ctx context.Context, asker, recipient string, questions []string, goalEventID, followUpOf *uuid.UUID) (uuid.UUID, error)
	AnswerQuestions(ctx context.Context, answerer string, askEventID uuid.UUID, answers []string) (uuid.UUID, error)
}

// workflowLogDefaultLimit / workflowLogMaxLimit bound one page of history.
// A goal's log is unbounded and this is answered inside an agent's turn, so
// "the whole thing" is not an option the caller gets to ask for.
const (
	workflowLogDefaultLimit = 100
	workflowLogMaxLimit     = 500
)

// WithGoals attaches the event-log read surface. nil clears it, which leaves
// both goal tools reporting the store as switched off. Returns the receiver for
// chaining, matching WithConfig and WithCallLog.
func (s *Server) WithGoals(g GoalSource) *Server {
	s.goals = g
	return s
}

// goalSource returns the read surface, or the error to show the agent.
//
// The error text names the config key because the agent reading it cannot see
// the host's configuration, and "no goal found" versus "this host does not
// record goals" are two answers it would otherwise be unable to tell apart.
func (s *Server) goalSource() (GoalSource, error) {
	if s.goals == nil || !s.goals.Enabled() {
		return nil, fmt.Errorf("the event log is not enabled on this host, so there are no recorded goals to read; this is a configuration state (`event_log.enabled`), not an empty result")
	}
	return s.goals, nil
}

// rereadMyGoalResult is the tool's payload. `count` is redundant with the array
// and is there anyway: it is the field an agent can restate, and a model that
// reports "I have 1 goal" against a 2-element array is a visible contradiction
// rather than a silent misread.
type rereadMyGoalResult struct {
	Agent string           `json:"agent"`
	Count int              `json:"count"`
	Goals []rereadGoalItem `json:"goals"`
	Note  string           `json:"note,omitempty"`
}

type rereadGoalItem struct {
	GoalEventID string `json:"goal_event_id"`
	WorkflowID  string `json:"workflow_instance_id"`
	GoalType    string `json:"goal_type"`
	OpenedAt    string `json:"opened_at"`
}

func (s *Server) toolRereadMyGoal(ctx context.Context) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	if caller == "" {
		// Not defensive padding: the identity is injected by the backend when
		// it routes a child's tool call, and it is empty for a call arriving
		// off that path. Guessing an owner here would hand somebody's goal to
		// whoever asked.
		return "", fmt.Errorf("reread_my_goal cannot tell which agent is calling, so it will not guess an owner")
	}

	goals, err := src.OpenGoalsForAgent(ctx, caller)
	if err != nil {
		return "", fmt.Errorf("reading your open goals: %w", err)
	}

	out := rereadMyGoalResult{Agent: caller, Count: len(goals), Goals: []rereadGoalItem{}}
	for _, g := range goals {
		out.Goals = append(out.Goals, rereadGoalItem{
			GoalEventID: g.GoalEventID.String(),
			WorkflowID:  g.WorkflowID.String(),
			GoalType:    g.GoalType,
			OpenedAt:    g.OpenedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	switch {
	case len(goals) == 0:
		out.Note = "you own no open goals; if you were told to work one, it has already been closed"
	case len(goals) > 1:
		// The store returns all of them rather than picking, deliberately. The
		// ambiguity surfaces here, loudly, instead of being tie-broken in SQL.
		out.Note = "you own more than one open goal; say which you are acting on before you act"
	}
	return marshalToolResult(out)
}

type workflowLogResult struct {
	WorkflowID string            `json:"workflow_instance_id"`
	Count      int               `json:"count"`
	NextSeq    int64             `json:"next_seq"`
	Truncated  bool              `json:"truncated"`
	Events     []workflowLogItem `json:"events"`
}

type workflowLogItem struct {
	Seq     int64           `json:"seq"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	At      string          `json:"at"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (s *Server) toolGetWorkflowLog(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	var p struct {
		WorkflowID string `json:"workflow_instance_id"`
		AfterSeq   int64  `json:"after_seq"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	wf, err := uuid.Parse(p.WorkflowID)
	if err != nil {
		return "", fmt.Errorf("workflow_instance_id %q is not a uuid; it is the workflow_instance_id reread_my_goal returned: %w", p.WorkflowID, err)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = workflowLogDefaultLimit
	}
	if limit > workflowLogMaxLimit {
		limit = workflowLogMaxLimit
	}

	events, err := src.EventsByWorkflowInstance(ctx, wf, p.AfterSeq, limit)
	if err != nil {
		return "", fmt.Errorf("reading the log for workflow instance %s: %w", wf, err)
	}

	out := workflowLogResult{
		WorkflowID: wf.String(),
		Count:      len(events),
		NextSeq:    p.AfterSeq,
		// A full page means there may be more. Reported rather than inferred by
		// the caller, because an agent that stops reading at a page boundary
		// concludes the goal has no further history — the one wrong answer this
		// tool can give that looks exactly like a right one.
		Truncated: len(events) == limit,
		Events:    []workflowLogItem{},
	}
	for _, ev := range events {
		out.Events = append(out.Events, workflowLogItem{
			Seq:     ev.Seq,
			ID:      ev.ID.String(),
			Type:    fmt.Sprintf("%s@%d", ev.SchemaName, ev.SchemaVersion),
			At:      ev.At.UTC().Format("2006-01-02T15:04:05Z"),
			Payload: ev.Payload,
		})
		out.NextSeq = ev.Seq
	}
	return marshalToolResult(out)
}

func marshalToolResult(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("rendering the result: %w", err)
	}
	return string(b), nil
}
