package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestUserInbox_RefuseWhenTheStoreCannotAnswer.
//
// "Nothing is waiting on you" is what a person acts on by going home, so a
// store that cannot reach Postgres must not be able to say it. Both the read and
// the write are checked against all three unusable states, because a refusal on
// only one of them is the shape that looks correct in review.
func TestUserInbox_RefuseWhenTheStoreCannotAnswer(t *testing.T) {
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
			if _, err := tc.ledger.OpenUserQuestions(ctx); err == nil {
				t.Error("OpenUserQuestions answered from a store that cannot read")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say the store is %s; got: %v", tc.want, err)
			}
			if _, err := tc.ledger.AnswerUserQuestion(ctx, uuid.New(), "yes", "dmotles"); err == nil {
				t.Error("AnswerUserQuestion closed a contract from a store that cannot read")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should say the store is %s; got: %v", tc.want, err)
			}
		})
	}
}

// TestAnswerUserQuestion_RefusesAnUnusableAnswer.
//
// Both refusals run BEFORE the store is consulted, so a disabled ledger
// distinguishes them: an unusable answer says so, and a usable one falls through
// to "disabled". Nothing here can reach a nil pool.
func TestAnswerUserQuestion_RefusesAnUnusableAnswer(t *testing.T) {
	ctx := context.Background()
	l := &Ledger{} // disabled on purpose — see above
	for _, tc := range []struct {
		name, answer, by, want string
	}{
		{"empty answer", "", "dmotles", "cannot be empty"},
		{"unattributed", "ship it", "", "which human"},
		{"both missing", "", "", "cannot be empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := l.AnswerUserQuestion(ctx, uuid.New(), tc.answer, tc.by)
			if err == nil {
				t.Fatal("the answer was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q; got: %v", tc.want, err)
			}
		})
	}
	// Paired control: a usable answer gets past these checks and is refused for
	// a DIFFERENT reason. Without it, a method that refused everything would
	// satisfy every case above.
	if _, err := l.AnswerUserQuestion(ctx, uuid.New(), "ship it", "dmotles"); err == nil {
		t.Error("a disabled ledger answered a question")
	} else if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("a usable answer should reach the store gate; got: %v", err)
	}
}

// TestAskUser_RefusesAnUnusableQuestion — same two-directional shape.
func TestAskUser_RefusesAnUnusableQuestion(t *testing.T) {
	ctx := context.Background()
	l := &Ledger{}
	if _, err := l.AskUser(ctx, "", "why?", "", nil); err == nil || !strings.Contains(err.Error(), "asker") {
		t.Errorf("an unattributed question should be refused for its asker; got: %v", err)
	}
	if _, err := l.AskUser(ctx, "boss", "", "", nil); err == nil || !strings.Contains(err.Error(), "needs a question") {
		t.Errorf("an empty question should be refused; got: %v", err)
	}
	// Control: a well-formed ask with no goal id gets past validation. A
	// disabled Emit records nothing and returns (0, nil), so this succeeds and
	// returns an id — which is exactly what proves validation is what fired
	// above and not the store.
	id, err := l.AskUser(ctx, "boss", "why?", "", nil)
	if err != nil {
		t.Fatalf("a well-formed question was refused: %v", err)
	}
	if id == uuid.Nil {
		t.Error("AskUser returned a nil event id, so nothing can reference the question")
	}
}
