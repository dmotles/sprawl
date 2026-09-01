//go:build store_pg

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The human's inbox against a real Postgres (QUM-1252, M3a slice 7a).
//
// What only a real database establishes: that an answered question really does
// leave open_contracts (the projection is maintained inside the append
// transaction, so "it left the set" is a claim about the appender, not about the
// query), and that a question filed against a goal lands on the GOAL's workflow
// instance rather than one of its own.

// TestUserInboxPg_AnswerRemovesTheQuestionFromTheInbox.
func TestUserInboxPg_AnswerRemovesTheQuestionFromTheInbox(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	first, err := e.ledger.AskUser(ctx, "boss", "ship or wait?", "the branch is green", nil)
	if err != nil {
		t.Fatalf("asking: %v", err)
	}
	second, err := e.ledger.AskUser(ctx, "boss", "which host?", "", nil)
	if err != nil {
		t.Fatalf("asking a second question: %v", err)
	}
	// A third, purely so the order assertion below is decisive. Event ids are
	// random uuids, so with two questions an ordering defect lands in ask order
	// by chance half the time — a control that passes on a coin flip is not a
	// control.
	third, err := e.ledger.AskUser(ctx, "boss", "and the timeout?", "", nil)
	if err != nil {
		t.Fatalf("asking a third question: %v", err)
	}

	open, err := e.ledger.OpenUserQuestions(ctx)
	if err != nil {
		t.Fatalf("reading the inbox: %v", err)
	}
	if len(open) != 3 {
		t.Fatalf("the inbox holds %d questions, want 3", len(open))
	}
	// Oldest first: a person works an inbox top-down, and a list whose order
	// drifts between reads cannot be answered by position.
	if open[0].EventID != first || open[1].EventID != second || open[2].EventID != third {
		t.Errorf("the inbox is not in ask order: got %s, %s, %s; want %s, %s, %s",
			open[0].EventID, open[1].EventID, open[2].EventID, first, second, third)
	}
	if open[0].Asker != "boss" || open[0].Question != "ship or wait?" || open[0].Context != "the branch is green" {
		t.Errorf("the question came back as asker=%q question=%q context=%q", open[0].Asker, open[0].Question, open[0].Context)
	}
	if open[0].AskedAt.IsZero() {
		t.Error("asked_at is zero, so the inbox cannot show how long someone has been waiting")
	}

	// Answer the SECOND question, not the first. Answering open[0] cannot
	// distinguish "looked the question up by id" from "took whichever question
	// came back first", and the two differ only in which workflow instance the
	// answer is appended to — checked below.
	secondWF := open[1].WorkflowID
	if _, err := e.ledger.AnswerUserQuestion(ctx, second, "ship it", "dmotles"); err != nil {
		t.Fatalf("answering: %v", err)
	}
	after, err := e.ledger.OpenUserQuestions(ctx)
	if err != nil {
		t.Fatalf("re-reading the inbox: %v", err)
	}
	// The control for the assertion below: the OTHER question must still be
	// there. An answer that emptied the whole inbox would pass a bare
	// "first is gone" check perfectly.
	if len(after) != 2 || after[0].EventID != first || after[1].EventID != third {
		t.Fatalf("after answering one question the inbox holds %d, want %s and %s", len(after), first, third)
	}

	// The answer is on the answered question's own instance. Without this, a
	// lookup that returned the wrong question would still close the right
	// contract — the id comes from the argument — and file the reply where
	// nobody replaying that question will read it.
	log, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, secondWF, 0, 100)
	if err != nil {
		t.Fatalf("reading the answered question's instance: %v", err)
	}
	if len(log) != 2 || log[1].SchemaName != "user_answered" {
		t.Errorf("the answered question's instance holds %d events ending in %v; the answer went to another instance",
			len(log), log[len(log)-1].SchemaName)
	}

	// Answering it again is refused: closes_event_id already removed the row.
	if _, err := e.ledger.AnswerUserQuestion(ctx, second, "ship it twice", "dmotles"); err == nil {
		t.Error("a question was answered twice")
	}
}

// TestUserInboxPg_AQuestionAgainstAGoalLandsOnThatGoalsInstance.
//
// A question filed on a fresh instance is well-formed and invisible: a replay of
// the goal would not see that it is waiting on a person.
func TestUserInboxPg_AQuestionAgainstAGoalLandsOnThatGoalsInstance(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	wf := uuid.New()
	goal := e.openGoalFor(t, "boss", "research", wf)

	qid, err := e.ledger.AskUser(ctx, "boss", "which datacenter?", "", &goal)
	if err != nil {
		t.Fatalf("asking against my own goal: %v", err)
	}
	events, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, wf, 0, 100)
	if err != nil {
		t.Fatalf("reading the instance log: %v", err)
	}
	if len(events) != 2 || events[1].ID != qid {
		t.Fatalf("the goal's instance holds %d events and ends with %v; the question landed elsewhere", len(events), events[len(events)-1].ID)
	}
	if !strings.Contains(string(events[1].Payload), goal.String()) {
		t.Errorf("the question does not record which goal it belongs to: %s", events[1].Payload)
	}

	// Someone else's goal is refused rather than ignored. Filing it silently on
	// a fresh instance would report success while the question sat where nobody
	// replaying that goal would find it.
	theirs := e.openGoalFor(t, "someone-else", "research", uuid.New())
	if _, err := e.ledger.AskUser(ctx, "boss", "and this?", "", &theirs); err == nil {
		t.Error("a question was filed against another agent's goal")
	} else if !strings.Contains(err.Error(), "not an open goal") {
		t.Errorf("the refusal should say the goal is not the asker's; got: %v", err)
	}

	// Control: the answer closes it and it leaves the inbox even though it was
	// filed on the goal's instance rather than its own.
	if _, err := e.ledger.AnswerUserQuestion(ctx, qid, "us-east", "dmotles"); err != nil {
		t.Fatalf("answering a goal-scoped question: %v", err)
	}
	open, err := e.ledger.OpenUserQuestions(ctx)
	if err != nil {
		t.Fatalf("re-reading the inbox: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("the inbox still holds %d question(s) after answering the only one", len(open))
	}

	// The answer belongs on the goal's instance too. Closing the contract is not
	// enough: an agent rereading its goal's log is how it learns the human
	// replied, and an answer appended to a fresh instance closes the question
	// while leaving that log saying only that a question was asked.
	after, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, wf, 0, 100)
	if err != nil {
		t.Fatalf("re-reading the instance log: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("the goal's instance holds %d events, want 3 (goal, question, answer) — the answer landed off the goal's log", len(after))
	}
	if after[2].SchemaName != "user_answered" || !strings.Contains(string(after[2].Payload), "us-east") {
		t.Errorf("the last event on the goal's instance is %s/%s, want the answer", after[2].SchemaName, after[2].Payload)
	}
}
