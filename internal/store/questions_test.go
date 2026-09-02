package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestQuestions_RefuseWhenTheStoreCannotAnswer.
//
// "Nobody is waiting on you" is what an agent acts on by moving to other work,
// so a store that cannot reach Postgres must not be able to say it. All three
// unusable states, both entry points: a refusal on only one of them is the shape
// that looks correct in review.
func TestQuestions_RefuseWhenTheStoreCannotAnswer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		ledger *Ledger
		want   string
	}{
		{"nil ledger is the disabled store", nil, "disabled"},
		{"zero ledger", &Ledger{}, "disabled"},
		{"degraded", &Ledger{enabled: true, degradedErr: errors.New("dial tcp: refused")}, "unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.ledger.OpenQuestionsForAgent(ctx, "finn"); err == nil {
				t.Error("OpenQuestionsForAgent answered from a store that cannot read")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say the store is %s; got: %v", tc.want, err)
			}
			if _, err := tc.ledger.AnswerQuestions(ctx, "finn", uuid.New(), []string{"yes"}); err == nil {
				t.Error("AnswerQuestions closed a contract from a store that cannot read")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say the store is %s; got: %v", tc.want, err)
			}
		})
	}
}

// TestAskQuestions_RefusesAnUnaskableQuestion.
//
// Every refusal here runs BEFORE the store is consulted, so a disabled ledger
// separates them cleanly: a bad ask says why, and a good one falls through to
// the Emit no-op. The paired control at the bottom is what stops a method that
// refused everything from satisfying the whole table.
func TestAskQuestions_RefusesAnUnaskableQuestion(t *testing.T) {
	ctx := context.Background()
	l := &Ledger{} // disabled: Emit records nothing and returns (0, nil)
	for _, tc := range []struct {
		name             string
		asker, recipient string
		questions        []string
		want             string
	}{
		{"unattributed", "", "finn", []string{"why?"}, "needs an asker"},
		{"unaddressed", "weave", "", []string{"why?"}, "needs a recipient"},
		{"self-addressed", "finn", "finn", []string{"why?"}, "cannot ask itself"},
		{"no questions", "weave", "finn", nil, "at least one question"},
		{"empty questions slice", "weave", "finn", []string{}, "at least one question"},
		{"a blank question", "weave", "finn", []string{"why?", ""}, "question 2 is empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l.AskQuestions(ctx, tc.asker, tc.recipient, tc.questions, nil, nil)
			if err == nil {
				t.Fatal("the ask was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q; got: %v", tc.want, err)
			}
		})
	}

	// Control: a well-formed ask gets past validation and returns an id, which
	// is what proves validation is what fired above rather than the store.
	id, err := l.AskQuestions(ctx, "weave", "finn", []string{"why?"}, nil, nil)
	if err != nil {
		t.Fatalf("a well-formed ask was refused: %v", err)
	}
	if id == uuid.Nil {
		t.Error("AskQuestions returned a nil event id, so no answer can reference the question")
	}
}

// TestAnswerQuestions_RefusesAnUnusableAnswer — the same two-directional shape.
// The control answer is refused for a DIFFERENT reason (the store gate), which
// is how these cases are shown to be the validation and not the store.
func TestAnswerQuestions_RefusesAnUnusableAnswer(t *testing.T) {
	ctx := context.Background()
	l := &Ledger{}
	for _, tc := range []struct {
		name, answerer string
		answers        []string
		want           string
	}{
		{"unattributed", "", []string{"yes"}, "which agent"},
		{"no answers", "finn", nil, "at least one answer"},
		{"empty answers slice", "finn", []string{}, "at least one answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l.AnswerQuestions(ctx, tc.answerer, uuid.New(), tc.answers)
			if err == nil {
				t.Fatal("the answer was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q; got: %v", tc.want, err)
			}
		})
	}
	if _, err := l.AnswerQuestions(ctx, "finn", uuid.New(), []string{"yes"}); err == nil {
		t.Error("a disabled ledger answered a question")
	} else if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("a usable answer should reach the store gate; got: %v", err)
	}
}
