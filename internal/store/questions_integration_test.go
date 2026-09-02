//go:build store_pg

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Agent-to-agent question contracts against a real Postgres (QUM-1252, slice 8).
//
// What only a real database establishes: that `questions` survives the round
// trip through jsonb as an ordered []string (the unit tests never serialise it,
// so a codec that stringified it would go unnoticed), that an answered ask
// really leaves open_contracts, that the recipient predicate is what selects a
// question rather than the ask order, and that a goal-scoped ask and its answer
// both land on the GOAL's instance.

// TestQuestionsPg_OnlyTheRecipientSeesAndAnswers.
func TestQuestionsPg_OnlyTheRecipientSeesAndAnswers(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	first, err := e.ledger.AskQuestions(ctx, "weave", "finn", []string{"which host?"}, nil, nil)
	if err != nil {
		t.Fatalf("asking: %v", err)
	}
	// A two-question ask, second in order, so the positional-arity check below
	// has something to disagree with and the ordering assertion is decisive.
	second, err := e.ledger.AskQuestions(ctx, "weave", "finn", []string{"ship or wait?", "and the timeout?"}, nil, nil)
	if err != nil {
		t.Fatalf("asking a second time: %v", err)
	}
	third, err := e.ledger.AskQuestions(ctx, "weave", "finn", []string{"why?"}, nil, nil)
	if err != nil {
		t.Fatalf("asking a third time: %v", err)
	}
	// Addressed to somebody else. This is the control for the recipient
	// predicate: without it, a query with no predicate at all reads identically.
	if _, err := e.ledger.AskQuestions(ctx, "weave", "ratz", []string{"and you?"}, nil, nil); err != nil {
		t.Fatalf("asking another agent: %v", err)
	}

	open, err := e.ledger.OpenQuestionsForAgent(ctx, "finn")
	if err != nil {
		t.Fatalf("reading finn's questions: %v", err)
	}
	if len(open) != 3 {
		t.Fatalf("finn has %d open questions, want 3 — the fourth was addressed to ratz", len(open))
	}
	if open[0].EventID != first || open[1].EventID != second || open[2].EventID != third {
		t.Errorf("questions are not in ask order: got %s, %s, %s; want %s, %s, %s",
			open[0].EventID, open[1].EventID, open[2].EventID, first, second, third)
	}
	// The payload round trip, in order. A jsonb codec that dropped or reordered
	// the array would leave every other assertion here intact.
	if len(open[1].Questions) != 2 || open[1].Questions[0] != "ship or wait?" || open[1].Questions[1] != "and the timeout?" {
		t.Errorf("the questions came back as %q, want the two asked in order", open[1].Questions)
	}
	if open[0].Asker != "weave" || open[0].Recipient != "finn" {
		t.Errorf("the question came back as asker=%q recipient=%q", open[0].Asker, open[0].Recipient)
	}
	if open[0].AskedAt.IsZero() {
		t.Error("asked_at is zero, so nothing can tell how long the asker has been waiting")
	}

	// Answer arity is checked against the ask that is actually being closed, not
	// against whatever came back first: `second` asks two.
	if _, err := e.ledger.AnswerQuestions(ctx, "finn", second, []string{"ship it"}); err == nil {
		t.Error("a 2-question ask was closed with 1 answer")
	} else if !strings.Contains(err.Error(), "asks 2 question(s) but 1 answer(s)") {
		t.Errorf("the arity refusal should name both counts; got: %v", err)
	}

	// The wrong agent cannot answer, even naming a real open ask.
	if _, err := e.ledger.AnswerQuestions(ctx, "ratz", first, []string{"us-east"}); err == nil {
		t.Error("an agent answered a question addressed to somebody else")
	} else if !strings.Contains(err.Error(), "asked of another agent") {
		t.Errorf("the refusal should say it was not addressed to ratz; got: %v", err)
	}

	// Answer the SECOND ask, not the first: answering open[0] cannot distinguish
	// "looked it up by id" from "took whichever came back first", and the two
	// differ only in which instance the answer lands on.
	secondWF := open[1].WorkflowID
	if _, err := e.ledger.AnswerQuestions(ctx, "finn", second, []string{"ship it", "60s"}); err != nil {
		t.Fatalf("answering: %v", err)
	}
	after, err := e.ledger.OpenQuestionsForAgent(ctx, "finn")
	if err != nil {
		t.Fatalf("re-reading finn's questions: %v", err)
	}
	if len(after) != 2 || after[0].EventID != first || after[1].EventID != third {
		t.Fatalf("after answering one ask finn has %d open, want %s and %s", len(after), first, third)
	}

	log, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, secondWF, 0, 100)
	if err != nil {
		t.Fatalf("reading the answered ask's instance: %v", err)
	}
	// Fatal rather than Error: the payload check below indexes log[1], and a
	// failure here means it is not there. A panic would still fail the run, but
	// it would bury the assertion that actually diagnosed the defect.
	if len(log) != 2 || log[1].SchemaName != "answer_questions" {
		t.Fatalf("the answered ask's instance holds %d events ending in %v; the answer went elsewhere",
			len(log), log[len(log)-1].SchemaName)
	}
	if !strings.Contains(string(log[1].Payload), "60s") {
		t.Errorf("the answers did not survive the round trip: %s", log[1].Payload)
	}

	// Answering it again is refused: closes_event_id already removed the row.
	if _, err := e.ledger.AnswerQuestions(ctx, "finn", second, []string{"ship it", "60s"}); err == nil {
		t.Error("an ask was answered twice")
	}
}

// TestQuestionsPg_AGoalScopedAskLandsOnThatGoalsInstance.
//
// An ask filed on a fresh instance is well-formed and invisible: a replay of the
// goal would not see that its owner is waiting on another agent.
func TestQuestionsPg_AGoalScopedAskLandsOnThatGoalsInstance(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	wf := uuid.New()
	goal := e.openGoalFor(t, "weave", "research", wf)
	earlier := uuid.New()

	askID, err := e.ledger.AskQuestions(ctx, "weave", "finn", []string{"which datacenter?"}, &goal, &earlier)
	if err != nil {
		t.Fatalf("asking against my own goal: %v", err)
	}
	events, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, wf, 0, 100)
	if err != nil {
		t.Fatalf("reading the instance log: %v", err)
	}
	if len(events) != 2 || events[1].ID != askID {
		t.Fatalf("the goal's instance holds %d events and ends with %v; the ask landed elsewhere", len(events), events[len(events)-1].ID)
	}
	if !strings.Contains(string(events[1].Payload), goal.String()) {
		t.Errorf("the ask does not record which goal it belongs to: %s", events[1].Payload)
	}
	// follow_up_of is recorded rather than validated — the ask it names is
	// usually already closed — so the only thing to check is that it survives.
	if !strings.Contains(string(events[1].Payload), earlier.String()) {
		t.Errorf("follow_up_of was dropped, so the thread cannot be reconstructed: %s", events[1].Payload)
	}

	// Somebody else's goal is refused rather than ignored.
	theirs := e.openGoalFor(t, "someone-else", "research", uuid.New())
	if _, err := e.ledger.AskQuestions(ctx, "weave", "finn", []string{"and this?"}, &theirs, nil); err == nil {
		t.Error("a question was filed against another agent's goal")
	} else if !strings.Contains(err.Error(), "not an open goal") {
		t.Errorf("the refusal should say the goal is not the asker's; got: %v", err)
	}

	if _, err := e.ledger.AnswerQuestions(ctx, "finn", askID, []string{"us-east"}); err != nil {
		t.Fatalf("answering a goal-scoped ask: %v", err)
	}
	open, err := e.ledger.OpenQuestionsForAgent(ctx, "finn")
	if err != nil {
		t.Fatalf("re-reading finn's questions: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("finn still has %d open question(s) after answering the only one", len(open))
	}

	// The answer belongs on the goal's instance too: rereading the goal's log is
	// how the ASKER learns it was answered, and an answer appended to a fresh
	// instance closes the contract while leaving that log saying only that a
	// question was asked.
	after, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, wf, 0, 100)
	if err != nil {
		t.Fatalf("re-reading the instance log: %v", err)
	}
	if len(after) != 3 {
		t.Fatalf("the goal's instance holds %d events, want 3 (goal, ask, answer) — the answer landed off the goal's log", len(after))
	}
	if after[2].SchemaName != "answer_questions" || !strings.Contains(string(after[2].Payload), "us-east") {
		t.Errorf("the last event on the goal's instance is %s/%s, want the answer", after[2].SchemaName, after[2].Payload)
	}
}
