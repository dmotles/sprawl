// tools_creategoal.go — create_goal, the tool that starts engine-driven work
// (QUM-1252, M3a slice 10b).
//
// create_goal appends `goal_opened` and returns. It does not spawn anybody: the
// dispatcher picks the event up and drives the spawn from it. See
// internal/store/goalopen.go for why the log comes first.
//
// The consequence lands HERE, in the note this tool returns, because this is the
// only place a caller learns what success means. "The goal is recorded" and "an
// agent is working on it" are different claims, and weave will repeat whichever
// one this tool implies.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/store"
)

// createGoalRestrictedError is the structured refusal an ineligible caller gets.
//
// A separate constant from askUserQuestionRestrictedError, sharing its shape and
// its wording for the part that is genuinely the same, because the two tools are
// restricted for different reasons and a caller that detects this
// programmatically should be able to tell them apart. The eligibility DECISION
// is shared (callerIsManagerOrRoot); only the message is not.
const createGoalRestrictedError = `{"error":"create_goal is restricted to weave and managers; ask your parent to open a goal instead"}`

type createGoalOutput struct {
	GoalEventID        string `json:"goal_event_id"`
	WorkflowInstanceID string `json:"workflow_instance_id"`
	GoalType           string `json:"goal_type"`
	Owner              string `json:"owner"`
	Note               string `json:"note"`
}

func (s *Server) toolCreateGoal(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	eligible, err := s.callerIsManagerOrRoot(ctx, caller)
	if err != nil {
		return "", fmt.Errorf("create_goal: looking up caller %q: %w", caller, err)
	}
	if !eligible {
		return "", errors.New(createGoalRestrictedError)
	}
	// The owner is resolved BEFORE the arguments are parsed for the same reason
	// every other refusal here precedes the append: nothing in this function may
	// reach the store until every reason to refuse has been checked.
	owner, err := s.callerOrRootName(ctx, caller, "create_goal")
	if err != nil {
		return "", err
	}

	var p struct {
		GoalType string `json:"goal_type"`
		Text     string `json:"text"`
		TextFile string `json:"text_file"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.GoalType == "" {
		return "", fmt.Errorf("create_goal needs a goal_type; the engine-driven types are %v, and everything else goes through `spawn`", store.MigratedGoalTypes)
	}
	// The brief may arrive inline or as a file the caller wrote first
	// (QUM-1347). On the file form the CONTENT is read here, before the append:
	// the log has to answer for this goal long after the worktree is gone, and
	// an unreadable path must refuse rather than open an empty goal.
	if err := exactlyOneTextInput("text", "text_file", p.Text, p.TextFile); err != nil {
		return "", fmt.Errorf("create_goal needs text: %w — it is the entire task the spawned agent will be given, and it is all they will get", err)
	}
	text := p.Text
	if p.TextFile != "" {
		if text, err = s.readInputFile(ctx, "text_file", p.TextFile); err != nil {
			return "", fmt.Errorf("create_goal: %w", err)
		}
	}

	g, err := src.OpenGoal(ctx, store.GoalType(p.GoalType), text, owner)
	if err != nil {
		return "", err
	}

	return marshalToolResult(createGoalOutput{
		GoalEventID:        g.GoalEventID.String(),
		WorkflowInstanceID: g.WorkflowID.String(),
		GoalType:           string(g.GoalType),
		Owner:              owner,
		Note: "the goal is recorded; the dispatcher spawns the agent from this event, so nobody is working it yet — " +
			"do not report it as underway, and do not block waiting for it. You will be notified when it closes.",
	})
}
