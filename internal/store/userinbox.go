// userinbox.go — the human's side of the log (QUM-1252, M3a slice 7a).
//
// A `user_question` contract addressed to the person, closed by `user_answered`.
// The human sits ABOVE the fleet: an agent asks, and the contract stays open
// until a person answers it — possibly days later, possibly from another host.
//
// This is deliberately NOT internal/supervisor/question.go. That path is an
// in-memory FIFO that blocks the asking agent's turn on a TUI modal and dies
// with the process; this one is durable and asynchronous, and the two answer
// different questions ("I need an answer to continue right now" versus "a person
// must decide this before the goal can close"). The seed carries the operational
// reason they are separate types rather than one type with recipient=user: the
// stall sweeper must NEVER poke a user_question, because poking a human is not
// something a timer gets to do.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UserQuestion is one open question waiting on a person.
type UserQuestion struct {
	// EventID is the user_question event — the CONTRACT id, and the id an
	// answer must reference. It is what `sprawl inbox` prints and what
	// `sprawl inbox answer` takes.
	EventID    uuid.UUID
	WorkflowID uuid.UUID
	Asker      string
	Question   string
	Context    string
	// GoalEventID is a string rather than a uuid.UUID: it is optional in the
	// seed, so the common case is absent, and a malformed one recorded by some
	// future writer must not make the whole inbox unreadable. Whether a person
	// can see their questions is not a good thing to hinge on a parse.
	GoalEventID string
	AskedAt     time.Time
}

// openUserQuestionsSQL finds every open user_question in the project.
//
// There is no recipient predicate, unlike openNotifiesSQL: `user_question` means
// "addressed to the human", and the fleet has one human inbox. Adding a
// recipient here would invent an addressing scheme the seed does not have, and
// the first mis-addressed question would sit unanswerable in a list nobody
// looks at.
const openUserQuestionsSQL = `
	SELECT e.id, e.workflow_instance_id,
	       COALESCE(e.payload->>'asker', ''), COALESCE(e.payload->>'question', ''),
	       COALESCE(e.payload->>'context', ''), COALESCE(e.payload->>'goal_event_id', ''),
	       oc.opened_at
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	 ORDER BY e.seq`

// PgUserInboxReader reads the human's open questions through a pgx pool.
type PgUserInboxReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

// OpenUserQuestions returns every unanswered question, oldest first.
func (r *PgUserInboxReader) OpenUserQuestions(ctx context.Context, projectID uuid.UUID) ([]UserQuestion, error) {
	rows, err := r.Pool.Query(ctx, openUserQuestionsSQL, projectID, schemaIDsFor(r.Registry, "user_question"))
	if err != nil {
		return nil, fmt.Errorf("store: reading the user inbox: %w", err)
	}
	defer rows.Close()

	var out []UserQuestion
	for rows.Next() {
		var q UserQuestion
		if err := rows.Scan(&q.EventID, &q.WorkflowID, &q.Asker, &q.Question, &q.Context, &q.GoalEventID, &q.AskedAt); err != nil {
			return nil, fmt.Errorf("store: scanning a user question: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// userInboxReader builds a reader bound to this Ledger, refusing a store that
// cannot answer. Same reasoning as goalReader: "there is nothing waiting on you"
// is an answer a person acts on by going home, so a store that cannot reach
// Postgres must not be able to produce it.
func (l *Ledger) userInboxReader() (*PgUserInboxReader, error) {
	if !l.Enabled() {
		return nil, fmt.Errorf("store: the event log is disabled on this host, so there is no user inbox; enable it with `sprawl config set event_log.enabled true`")
	}
	if l.degradedErr != nil {
		return nil, fmt.Errorf("store: the event log is unreachable, so an empty inbox would be a claim it cannot support: %w", l.degradedErr)
	}
	return &PgUserInboxReader{Pool: l.pool, Registry: l.registry}, nil
}

// OpenUserQuestions lists what is waiting on a person.
func (l *Ledger) OpenUserQuestions(ctx context.Context) ([]UserQuestion, error) {
	r, err := l.userInboxReader()
	if err != nil {
		return nil, err
	}
	return r.OpenUserQuestions(ctx, l.projectID)
}

// AskUser opens a user_question contract.
//
// goalEventID is optional and, when given, must be one of asker's own open
// goals: the question is then appended on THAT goal's workflow instance, so the
// instance log reads as one story and a replay of the goal sees the wait. A goal
// id that is not the asker's is refused rather than ignored, because silently
// dropping it would file the question against a fresh instance nobody is
// watching while the tool reported success.
func (l *Ledger) AskUser(ctx context.Context, asker, question, questionContext string, goalEventID *uuid.UUID) (uuid.UUID, error) {
	if asker == "" {
		// The seed requires `asker`, and an unattributed question is one the
		// human cannot answer usefully: the reply has nowhere to go.
		return uuid.Nil, fmt.Errorf("store: a user question needs an asker")
	}
	if question == "" {
		return uuid.Nil, fmt.Errorf("store: a user question needs a question")
	}

	var workflow uuid.UUID
	payload := map[string]any{"asker": asker, "question": question}
	if questionContext != "" {
		payload["context"] = questionContext
	}
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

	id := uuid.New()
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "user_question",
		TypeVersion:        1,
		EventID:            id,
		WorkflowInstanceID: workflow, // zero mints a fresh instance
		Payload:            payload,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: asking the user: %w", err)
	}
	return id, nil
}

// AnswerUserQuestion closes one open question.
//
// The question is looked up THROUGH the open set rather than closed by id, for
// the reason CloseGoalForAgent does it: closes_event_id removes the row and the
// log is monotone, so answering an already-answered question would append a
// second close against a contract that is gone. A person answering from the CLI
// is also working from a list that may be minutes stale.
func (l *Ledger) AnswerUserQuestion(ctx context.Context, questionEventID uuid.UUID, answer, answeredBy string) (uuid.UUID, error) {
	if answer == "" {
		return uuid.Nil, fmt.Errorf("store: an answer cannot be empty; the asking agent will act on whatever this says")
	}
	if answeredBy == "" {
		// Recorded per the seed's own note: a multi-operator fleet in which
		// every answer is attributed to "the user" cannot be audited.
		return uuid.Nil, fmt.Errorf("store: an answer needs to say which human gave it")
	}

	open, err := l.OpenUserQuestions(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	var found *UserQuestion
	for i := range open {
		if open[i].EventID == questionEventID {
			found = &open[i]
			break
		}
	}
	if found == nil {
		return uuid.Nil, fmt.Errorf("store: %s is not an open user question — it may already be answered; re-read `sprawl inbox`", questionEventID)
	}

	closeID := uuid.New()
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "user_answered",
		TypeVersion:        1,
		EventID:            closeID,
		WorkflowInstanceID: found.WorkflowID,
		ClosesEventID:      &questionEventID,
		Payload:            map[string]any{"answer": answer, "answered_by": answeredBy},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: answering user question %s: %w", questionEventID, err)
	}
	return closeID, nil
}
