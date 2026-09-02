// tools_questions.go — ask_questions / answer_questions, one agent blocking on
// another for information (QUM-1252, M3a slice 8).
//
// Both are WRITES, so they live apart from tools_goals.go for tools_report.go's
// reason: an ask opens a contract the sweeper will act on, and an answer closes
// one irreversibly. Neither is restricted the way ask_user is — every agent may
// ask its peers and must be able to answer them — but both refuse an unnamed
// caller outright, because `asker` and `answerer` are what make the reply
// deliverable and the answer attributable.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

type askQuestionsOutput struct {
	AskEventID string `json:"ask_event_id"`
	Asker      string `json:"asker"`
	Recipient  string `json:"recipient"`
	Count      int    `json:"question_count"`
	Note       string `json:"note"`
}

type answerQuestionsOutput struct {
	AskEventID   string `json:"answered_ask_event_id"`
	CloseEvent   string `json:"close_event_id"`
	Answerer     string `json:"answerer"`
	Count        int    `json:"answer_count"`
	Irreversible bool   `json:"irreversible"`
	Note         string `json:"note"`
}

// optionalUUIDArg parses an optional uuid-valued argument. Absent is nil;
// present-but-malformed is an error rather than nil, because both of these
// fields silently change where the contract lands or which thread it joins, and
// a dropped one reports success while doing the wrong thing.
func optionalUUIDArg(name, raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s %q is not a uuid: %w", name, raw, err)
	}
	return &id, nil
}

func (s *Server) toolAskQuestions(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	if caller == "" {
		return "", fmt.Errorf("ask_questions cannot tell which agent is asking, and an answer addressed to nobody has nowhere to go")
	}

	var p struct {
		Recipient   string   `json:"recipient"`
		Questions   []string `json:"questions"`
		GoalEventID string   `json:"goal_event_id"`
		FollowUpOf  string   `json:"follow_up_of"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	goal, err := optionalUUIDArg("goal_event_id", p.GoalEventID)
	if err != nil {
		return "", err
	}
	followUp, err := optionalUUIDArg("follow_up_of", p.FollowUpOf)
	if err != nil {
		return "", err
	}

	// Everything else — empty recipient, no questions, a self-addressed ask —
	// is the store's to refuse, deliberately not re-derived here: two checks
	// that were meant to be identical are two checks that eventually differ,
	// and the store's is the one that runs for every caller.
	id, err := src.AskQuestions(ctx, caller, p.Recipient, p.Questions, goal, followUp)
	if err != nil {
		return "", err
	}
	return marshalToolResult(askQuestionsOutput{
		AskEventID: id.String(),
		Asker:      caller,
		Recipient:  p.Recipient,
		Count:      len(p.Questions),
		Note:       "this is an open contract, not a message: it stays outstanding until " + p.Recipient + " answers it. Answers come back positionally, in the order you asked. Do not block on it — say what you are waiting for and do what you can meanwhile.",
	})
}

func (s *Server) toolAnswerQuestions(ctx context.Context, args json.RawMessage) (string, error) {
	src, err := s.goalSource()
	if err != nil {
		return "", err
	}
	caller := backendpkg.CallerIdentity(ctx)
	if caller == "" {
		return "", fmt.Errorf("answer_questions cannot tell which agent is answering, and it will not close a contract on behalf of an agent it cannot name")
	}

	var p struct {
		AskEventID string   `json:"ask_event_id"`
		Answers    []string `json:"answers"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	ask, err := uuid.Parse(p.AskEventID)
	if err != nil {
		return "", fmt.Errorf("ask_event_id %q is not a uuid; it is the ask_event_id of the ask_questions event you are answering: %w", p.AskEventID, err)
	}

	// Arity, ownership and emptiness are the store's, against the open set.
	closeID, err := src.AnswerQuestions(ctx, caller, ask, p.Answers)
	if err != nil {
		return "", err
	}
	return marshalToolResult(answerQuestionsOutput{
		AskEventID:   ask.String(),
		CloseEvent:   closeID.String(),
		Answerer:     caller,
		Count:        len(p.Answers),
		Irreversible: true,
		Note:         "the question is closed and the log is monotone; a correction is a NEW ask/answer pair carrying follow_up_of, never an edit",
	})
}
