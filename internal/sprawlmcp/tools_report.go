// tools_report.go — report_result, the WRITE half of an agent's goal tools
// (QUM-1252, M3a slice 6c).
//
// Kept apart from tools_goals.go because the asymmetry is real rather than
// stylistic: a bad read wastes a turn, and a bad write is permanent. The log is
// monotone and `closes_event_id` removes the row from open_contracts, so a
// close cannot be retracted — a defect found afterwards emits rework, never a
// mutation. Everything unusual in this file follows from that.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/store"
)

type reportResultOutput struct {
	Closed       string `json:"closed_goal_event_id"`
	CloseEvent   string `json:"close_event_id"`
	Outcome      string `json:"outcome"`
	Irreversible bool   `json:"irreversible"`
	Note         string `json:"note"`
}

func (s *Server) toolReportResult(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	if caller == "" {
		return "", fmt.Errorf("report_result cannot tell which agent is calling, and it will not close a goal on behalf of an agent it cannot name")
	}

	var p struct {
		GoalEventID string `json:"goal_event_id"`
		Outcome     string `json:"outcome"`
		Summary     string `json:"summary"`
		SummaryFile string `json:"summary_file"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	goal, err := uuid.Parse(p.GoalEventID)
	if err != nil {
		return "", fmt.Errorf("goal_event_id %q is not a uuid; it is the goal_event_id reread_my_goal returned, NOT the workflow_instance_id: %w", p.GoalEventID, err)
	}
	// A summary is required, and emptiness is refused rather than defaulted.
	// The close is the last thing anyone reads about this goal; a blank summary
	// makes the outcome unauditable at exactly the moment it becomes permanent.
	// It may arrive inline or as a file the agent wrote first (QUM-1347) — and
	// on the file form the CONTENT is read here, before the append, so an
	// unreadable path refuses the call rather than closing the goal blank.
	if err := exactlyOneTextInput("summary", "summary_file", p.Summary, p.SummaryFile); err != nil {
		return "", fmt.Errorf("report_result: %w — the close is final and is the last thing anyone reads about this goal", err)
	}
	summary := p.Summary
	if p.SummaryFile != "" {
		if summary, err = s.readInputFile(ctx, "summary_file", p.SummaryFile); err != nil {
			return "", fmt.Errorf("report_result: %w", err)
		}
	}

	closeID, err := src.CloseGoalForAgent(ctx, caller, goal, store.GoalOutcome(p.Outcome), summary)
	if err != nil {
		return "", err
	}
	return marshalToolResult(reportResultOutput{
		Closed:       goal.String(),
		CloseEvent:   closeID.String(),
		Outcome:      p.Outcome,
		Irreversible: true,
		Note:         "this goal is closed and the log is monotone; a defect found now needs rework requested by its owner, not a second close",
	})
}
