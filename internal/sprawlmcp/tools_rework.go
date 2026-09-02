// tools_rework.go — request_rework, the owner's answer to a bad result
// (QUM-1252, M3a, AC3).
//
// Closes are final and the log is monotone, so an owner who finds a defect in a
// landed result cannot reopen the goal. This tool appends `rework_requested`,
// which opens a NEW contract linked to the rejected event, and returns. Like
// create_goal it spawns nobody: the dispatcher drives the fresh agent from the
// event. See internal/store/rework.go.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/store"
)

// requestReworkRestrictedError is the structured refusal an ineligible caller
// gets. Its own constant, not create_goal's, for the reason that one gives: the
// eligibility DECISION is shared, the message is not, and a caller detecting
// this programmatically has to be able to tell which tool refused it.
const requestReworkRestrictedError = `{"error":"request_rework is restricted to weave and managers; ask the agent that owns the goal to reject the result instead"}`

type requestReworkOutput struct {
	ReworkEventID      string `json:"rework_event_id"`
	WorkflowInstanceID string `json:"workflow_instance_id"`
	RejectedEventID    string `json:"rejected_event_id"`
	Owner              string `json:"owner"`
	Note               string `json:"note"`
}

func (s *Server) toolRequestRework(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	eligible, err := s.callerIsManagerOrRoot(ctx, caller)
	if err != nil {
		return "", fmt.Errorf("request_rework: looking up caller %q: %w", caller, err)
	}
	if !eligible {
		return "", errors.New(requestReworkRestrictedError)
	}
	owner, err := s.callerOrRootName(ctx, caller, "request_rework")
	if err != nil {
		return "", err
	}

	var p struct {
		GoalEventID string `json:"goal_event_id"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	rejected, err := uuid.Parse(p.GoalEventID)
	if err != nil {
		return "", fmt.Errorf("request_rework needs the goal_event_id of the work being rejected, as an id: %w", err)
	}
	if p.Reason == "" {
		return "", fmt.Errorf("request_rework needs a reason; it is the only thing that tells the next agent what was wrong with the last result, and it cannot see this conversation")
	}

	// DiscardAndRedo is passed rather than offered as an argument. It is the
	// only policy anything downstream implements, so a `re_engagement` parameter
	// would be a choice the caller cannot actually have.
	rw, err := src.RequestRework(ctx, owner, rejected, p.Reason, store.DiscardAndRedo)
	if err != nil {
		return "", err
	}

	return marshalToolResult(requestReworkOutput{
		ReworkEventID:      rw.ReworkEventID.String(),
		WorkflowInstanceID: rw.WorkflowID.String(),
		RejectedEventID:    rejected.String(),
		Owner:              owner,
		Note: "the rework is recorded; the dispatcher spawns a FRESH agent from this event, so nobody is redoing the work yet — " +
			"do not report it as underway, and do not block waiting for it. You will be notified when the rework closes.",
	})
}
