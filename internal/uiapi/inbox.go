package uiapi

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Question is one open `user_question` waiting on a person.
//
// "Open" is the PRESENCE of a row in open_contracts. There is no status column
// and no boolean anywhere in this schema — presence is the state, and a closing
// `user_answered` event removes the projection row inside the same transaction.
//
// GoalEventID is a string rather than a uuid.UUID, copied from
// store.UserQuestion's reasoning: it is optional in the seed, so the common
// case is absent, and a malformed one written by some future producer must not
// make the whole inbox unreadable. Whether a person can see the questions
// addressed to them is not a good thing to hinge on a parse.
//
// There is no `host` field. The original QUM-1349 contract listed one as
// always-null; `user_question` payloads carry no host key at all, so a
// permanently-null column is noise rather than information.
type Question struct {
	EventID            uuid.UUID `json:"event_id"`
	Seq                int64     `json:"seq"`
	OpenedAt           time.Time `json:"opened_at"`
	ProjectID          uuid.UUID `json:"project_id"`
	ProjectName        string    `json:"project_name"`
	WorkflowInstanceID uuid.UUID `json:"workflow_instance_id"`
	Asker              string    `json:"asker"`
	Question           string    `json:"question"`
	Context            string    `json:"context"`
	GoalEventID        string    `json:"goal_event_id"`
}

// QuestionReader reads the human's open questions.
type QuestionReader interface {
	ListQuestions(ctx context.Context, opts ListOptions) ([]Question, error)
}

// PgQuestionReader is the Postgres implementation of QuestionReader.
type PgQuestionReader struct{ Pool Pool }

// There is no recipient predicate, mirroring store/userinbox.go: `user_question`
// means "addressed to the human", and the fleet has one human inbox. Adding one
// here would invent an addressing scheme the seed does not have, and the first
// mis-addressed question would sit unanswerable in a list nobody looks at.
//
// ORDER BY seq ASC — oldest first, and the only endpoint here that is not
// newest-first. A question that has been waiting longest is the one that most
// needs answering; an inbox sorted newest-first buries it.
const listQuestionsSQL = `
	SELECT e.id, e.seq, oc.opened_at, e.project_id, COALESCE(p.remote_url, ''),
	       e.workflow_instance_id,
	       COALESCE(e.payload->>'asker', ''),
	       COALESCE(e.payload->>'question', ''),
	       COALESCE(e.payload->>'context', ''),
	       COALESCE(e.payload->>'goal_event_id', '')
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	  JOIN event_type_schemas s ON s.id = e.schema_id
	  LEFT JOIN projects p ON p.id = e.project_id
	 WHERE s.name = 'user_question'
	   AND ($1::uuid IS NULL OR e.project_id = $1::uuid)
	 ORDER BY e.seq
	 LIMIT $2`

// ListQuestions returns the open questions, oldest first.
func (r PgQuestionReader) ListQuestions(ctx context.Context, opts ListOptions) ([]Question, error) {
	rows, err := r.Pool.Query(ctx, listQuestionsSQL, opts.ProjectID, opts.Limit)
	if err != nil {
		return nil, fmt.Errorf("uiapi: querying the inbox: %w", err)
	}
	defer rows.Close()

	out := []Question{}
	for rows.Next() {
		var (
			q         Question
			remoteURL string
		)
		if err := rows.Scan(&q.EventID, &q.Seq, &q.OpenedAt, &q.ProjectID, &remoteURL,
			&q.WorkflowInstanceID, &q.Asker, &q.Question, &q.Context, &q.GoalEventID); err != nil {
			return nil, fmt.Errorf("uiapi: scanning a question: %w", err)
		}
		q.ProjectName = ProjectName(remoteURL)
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("uiapi: reading the inbox: %w", err)
	}
	return out, nil
}
