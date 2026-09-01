// tools_askuser.go — ask_user, the durable question to the human
// (QUM-1252, M3a slice 7a).
//
// Distinct from ask_user_question in server.go, and the distinction is the whole
// point of the tool existing: that one blocks the caller's turn on a TUI modal
// and its state dies with the session, while this one appends a contract the
// human can answer later from `sprawl inbox`, on another host, after a restart.
// An agent picks by whether it can continue without the answer.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

type askUserOutput struct {
	QuestionEventID string `json:"question_event_id"`
	Asker           string `json:"asker"`
	GoalEventID     string `json:"goal_event_id,omitempty"`
	Note            string `json:"note"`
}

// askUserAsker resolves the name to record as the asker.
//
// The eligibility gate treats an empty caller identity as the root weave
// session, which is also the most likely caller of this tool — so an empty
// caller cannot simply be refused the way report_result refuses it. But the seed
// REQUIRES asker, and inventing a literal here would put a name in the log that
// no agent record backs. So the root's real name is read from the supervisor,
// and a store with no root record is refused rather than guessed at.
func (s *Server) askUserAsker(ctx context.Context, caller string) (string, error) {
	if caller != "" {
		return caller, nil
	}
	agents, err := s.sup.Status(ctx)
	if err != nil {
		return "", fmt.Errorf("ask_user: looking up the root agent's name: %w", err)
	}
	for _, a := range agents {
		if a.Type == "root" {
			return a.Name, nil
		}
	}
	return "", fmt.Errorf("ask_user cannot tell which agent is asking: the call carries no identity and no root agent is registered, and a question the human cannot attribute is one they cannot answer")
}

func (s *Server) toolAskUser(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	// The same gate ask_user_question uses, deliberately reused rather than
	// re-derived: both tools interrupt the human, and two gates that were meant
	// to be identical are two gates that eventually differ.
	if err := s.askUserQuestionEligibility(ctx, caller); err != nil {
		return "", err
	}
	asker, err := s.askUserAsker(ctx, caller)
	if err != nil {
		return "", err
	}

	var p struct {
		Question    string `json:"question"`
		Context     string `json:"context"`
		GoalEventID string `json:"goal_event_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if p.Question == "" {
		return "", fmt.Errorf("ask_user needs a question; it is what a person will read out of context, hours from now")
	}
	var goal *uuid.UUID
	if p.GoalEventID != "" {
		g, err := uuid.Parse(p.GoalEventID)
		if err != nil {
			return "", fmt.Errorf("goal_event_id %q is not a uuid; it is the goal_event_id reread_my_goal returned: %w", p.GoalEventID, err)
		}
		goal = &g
	}

	id, err := src.AskUser(ctx, asker, p.Question, p.Context, goal)
	if err != nil {
		return "", err
	}
	return marshalToolResult(askUserOutput{
		QuestionEventID: id.String(),
		Asker:           asker,
		GoalEventID:     p.GoalEventID,
		Note:            "this question is now waiting in the human's inbox; nothing will poke them, so do not block on it — report what you can and say what you are waiting for",
	})
}
