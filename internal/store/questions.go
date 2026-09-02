// questions.go — ask_questions / answer_questions, one agent blocking on
// another for information (QUM-1252, M3a slice 8).
//
// A CONTRACT rather than a message, which is the whole reason this exists
// alongside internal/messages: mail that nobody reads is a silence somebody has
// to notice, while an unanswered ask_questions is a row in open_contracts that
// the sweeper's transitive-block term can already see (its owner-or-recipient
// reading is why an agent waiting on an answer counts as blocked rather than
// idle).
//
// Deliberately NOT the same type as user_question, per that seed's own note:
// the two differ in the property that matters operationally — a human may never
// be poked, and an agent must be.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AgentQuestion is one open ask_questions contract.
type AgentQuestion struct {
	// EventID is the ask_questions event — the CONTRACT id, and what an answer
	// must reference.
	EventID    uuid.UUID
	WorkflowID uuid.UUID
	Asker      string
	Recipient  string
	// Questions is positional: answers are matched to it by index, so its
	// length is part of the contract rather than a detail of presentation.
	Questions []string
	// GoalEventID and FollowUpOf are strings for UserQuestion's reason: both are
	// optional in the seed, and a malformed one written by some future producer
	// must not make an agent's whole question set unreadable.
	GoalEventID string
	FollowUpOf  string
	AskedAt     time.Time
}

// openQuestionsForAgentSQL finds every open question addressed TO one agent.
//
// The predicate is on `recipient`, not `asker`: this answers "what is somebody
// waiting on ME for", which is the only one of the two questions that has an
// action attached to it.
const openQuestionsForAgentSQL = `
	SELECT e.id, e.workflow_instance_id,
	       COALESCE(e.payload->>'asker', ''), COALESCE(e.payload->'questions', '[]'::jsonb),
	       COALESCE(e.payload->>'goal_event_id', ''), COALESCE(e.payload->>'follow_up_of', ''),
	       oc.opened_at
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	   AND e.payload->>'recipient' = $3
	 ORDER BY e.seq`

// PgQuestionReader reads open agent-to-agent questions through a pgx pool.
type PgQuestionReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

// OpenQuestionsForAgent returns every unanswered question addressed to agent,
// oldest first.
//
// An empty agent name is refused rather than matched, for the reason
// OpenGoalsForAgent refuses it: `payload->>'recipient'` is NULL on events that
// carry no recipient, and an empty string is a surprising match for a caller
// that has simply lost track of its own identity.
func (r *PgQuestionReader) OpenQuestionsForAgent(ctx context.Context, projectID uuid.UUID, agent string) ([]AgentQuestion, error) {
	if agent == "" {
		return nil, fmt.Errorf("store: reading an agent's open questions requires an agent name")
	}
	rows, err := r.Pool.Query(ctx, openQuestionsForAgentSQL, projectID, schemaIDsFor(r.Registry, "ask_questions"), agent)
	if err != nil {
		return nil, fmt.Errorf("store: reading open questions for %q: %w", agent, err)
	}
	defer rows.Close()

	var out []AgentQuestion
	for rows.Next() {
		q := AgentQuestion{Recipient: agent}
		if err := rows.Scan(&q.EventID, &q.WorkflowID, &q.Asker, &q.Questions, &q.GoalEventID, &q.FollowUpOf, &q.AskedAt); err != nil {
			return nil, fmt.Errorf("store: scanning an open question for %q: %w", agent, err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading open questions for %q: %w", agent, err)
	}
	return out, nil
}

// questionReader builds a reader bound to this Ledger, refusing a store that
// cannot answer — goalReader's reasoning exactly: "nobody is waiting on you" is
// an answer an agent acts on by moving to other work.
func (l *Ledger) questionReader() (*PgQuestionReader, error) {
	if !l.Enabled() {
		return nil, fmt.Errorf("store: the event log is disabled on this host, so there are no recorded questions; enable it with `sprawl config set event_log.enabled true`")
	}
	if l.degradedErr != nil {
		return nil, fmt.Errorf("store: the event log is unreachable, so an empty question set would be a claim it cannot support: %w", l.degradedErr)
	}
	return &PgQuestionReader{Pool: l.pool, Registry: l.registry}, nil
}

// OpenQuestionsForAgent answers "what is somebody waiting on me for?".
func (l *Ledger) OpenQuestionsForAgent(ctx context.Context, agent string) ([]AgentQuestion, error) {
	r, err := l.questionReader()
	if err != nil {
		return nil, err
	}
	return r.OpenQuestionsForAgent(ctx, l.projectID, agent)
}

// AskQuestions opens an ask_questions contract.
//
// goalEventID follows AskUser's rule: optional, and when given it must be one of
// the ASKER's own open goals, so the question is appended on that goal's
// instance and a replay of the goal sees the wait. A goal that is not the
// asker's is refused rather than dropped, because a silently reparented question
// is well-formed and invisible.
//
// followUpOf is recorded but not validated against the open set on purpose: the
// ask it names is usually already CLOSED (that is what makes this a follow-up),
// so requiring it to be open would refuse exactly the case the field exists for.
func (l *Ledger) AskQuestions(ctx context.Context, asker, recipient string, questions []string, goalEventID, followUpOf *uuid.UUID) (uuid.UUID, error) {
	if asker == "" {
		return uuid.Nil, fmt.Errorf("store: a question needs an asker; an answer has nowhere to go without one")
	}
	if recipient == "" {
		return uuid.Nil, fmt.Errorf("store: a question needs a recipient; nobody is obliged to answer a question addressed to no one")
	}
	if asker == recipient {
		// Not pedantry: the sweeper reads owner-or-recipient as "blocked", so a
		// self-addressed question makes an agent block on itself forever, and
		// every remedy the sweeper has (poke, escalate) targets the same agent.
		return uuid.Nil, fmt.Errorf("store: %q cannot ask itself a question — the contract would make it block on its own answer", asker)
	}
	if len(questions) == 0 {
		return uuid.Nil, fmt.Errorf("store: an ask needs at least one question")
	}
	for i, q := range questions {
		if q == "" {
			// Positional answers make this worse than merely useless: an empty
			// question still consumes an index, so the recipient must answer it
			// to answer any of the ones that follow.
			return uuid.Nil, fmt.Errorf("store: question %d is empty; answers are positional, so a blank question still has to be answered", i+1)
		}
	}

	var workflow uuid.UUID
	payload := map[string]any{"asker": asker, "recipient": recipient, "questions": questions}
	if goalEventID != nil {
		goals, err := l.OpenGoalsForAgent(ctx, asker)
		if err != nil {
			return uuid.Nil, err
		}
		for _, g := range goals {
			if g.GoalEventID == *goalEventID {
				workflow = g.WorkflowID
				break
			}
		}
		if workflow == uuid.Nil {
			return uuid.Nil, fmt.Errorf("store: %s is not an open goal owned by %q, so a question cannot be filed against it", *goalEventID, asker)
		}
		payload["goal_event_id"] = goalEventID.String()
	}
	if followUpOf != nil {
		payload["follow_up_of"] = followUpOf.String()
	}

	id := uuid.New()
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "ask_questions",
		TypeVersion:        1,
		EventID:            id,
		WorkflowInstanceID: workflow, // zero mints a fresh instance
		Payload:            payload,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: asking %q: %w", recipient, err)
	}
	return id, nil
}

// AnswerQuestions closes one open ask_questions.
//
// The ask is looked up THROUGH the answerer's own open set, which is what makes
// "only the recipient may answer" a property of the store rather than of
// whichever caller remembered to check. That matters more here than for goals:
// the asker will act on these answers, so a third party answering on the
// recipient's behalf is not a permissions curiosity, it is wrong information
// delivered under somebody else's name — and closes_event_id makes it final.
//
// answers is positional against the ask's questions and the lengths must match
// exactly. A short array would silently answer a prefix and leave the asker
// unable to tell which questions were addressed; a long one means the answerer
// is working from a different set of questions than the one it is closing.
func (l *Ledger) AnswerQuestions(ctx context.Context, answerer string, askEventID uuid.UUID, answers []string) (uuid.UUID, error) {
	if answerer == "" {
		return uuid.Nil, fmt.Errorf("store: an answer needs to say which agent gave it")
	}
	if len(answers) == 0 {
		return uuid.Nil, fmt.Errorf("store: an answer needs at least one answer")
	}

	open, err := l.OpenQuestionsForAgent(ctx, answerer)
	if err != nil {
		return uuid.Nil, err
	}
	var found *AgentQuestion
	for i := range open {
		if open[i].EventID == askEventID {
			found = &open[i]
			break
		}
	}
	if found == nil {
		// One message for several causes, as CloseGoalForAgent does: telling
		// "already answered" from "addressed to someone else" needs a second
		// query, and the remedy is the same either way.
		return uuid.Nil, fmt.Errorf("store: %s is not an open question addressed to %q — it is already answered, or it was asked of another agent", askEventID, answerer)
	}
	if len(answers) != len(found.Questions) {
		return uuid.Nil, fmt.Errorf("store: %s asks %d question(s) but %d answer(s) were given; answers are positional, so a mismatch cannot be matched up",
			askEventID, len(found.Questions), len(answers))
	}

	closeID := uuid.New()
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "answer_questions",
		TypeVersion:        1,
		EventID:            closeID,
		WorkflowInstanceID: found.WorkflowID,
		ClosesEventID:      &askEventID,
		Payload:            map[string]any{"answerer": answerer, "answers": answers},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: answering question %s: %w", askEventID, err)
	}
	return closeID, nil
}
