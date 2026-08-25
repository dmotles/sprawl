package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The card read path, hermetic half (QUM-1251, M2).
//
// CardForType is on the SPAWN path: every launch resolves the card for an agent
// type. That places it under the store's standing constraint — "agents never
// brick on the store" — so the interesting cases here are not the happy read
// (that needs Postgres, see cards_integration_test.go) but the three ways the
// Ledger can be unable to answer. Each must produce an ERROR the caller can fall
// back on, never a panic and never a zero-valued card.
//
// A zero-valued card is the outcome worth naming, because it is the one that
// looks like success: it would render an EMPTY system prompt, and an agent
// launched with no prompt is an agent with no safety instructions at all. The
// resolver above this treats any error as "use the embedded seed", so returning
// an error is what routes that case to the compiled-in floor.

// TestCardForType_UnusableLedgerReturnsAnErrorNotAPanic covers the three shapes
// a caller can hold when the store is not usable.
//
// A nil *Ledger is the store-disabled path (the default), and every other Ledger
// method in this package is explicitly nil-safe — see Enabled, ProjectID,
// DegradedError, Pool. A method that panicked instead would take down the spawn
// path for the majority configuration.
//
// A degraded Ledger is the store-enabled-but-unreachable path, and it is a
// distinct case rather than a duplicate of nil: it is ENABLED, so any guard
// keyed on Enabled() alone waves it through and then dereferences a nil pool.
func TestCardForType_UnusableLedgerReturnsAnErrorNotAPanic(t *testing.T) {
	ctx := context.Background()

	// Each subtest names the Ledger shape rather than sharing one, because the
	// guard that catches each is a different guard.
	t.Run("nil ledger", func(t *testing.T) {
		var l *Ledger
		got, err := l.CardForType(ctx, "engineer")
		if err == nil {
			t.Fatalf("a nil Ledger must report that it cannot answer; got card %+v", got)
		}
		if got != nil {
			t.Errorf("a failed lookup must return no card, not a zero-valued one (an empty card renders an empty system prompt); got %+v", got)
		}
	})

	t.Run("degraded ledger", func(t *testing.T) {
		l, _ := newDegradedLedger(t, nil)
		// The premise this subtest rests on. Without it, a fixture change that
		// gave the degraded Ledger a live pool would leave this asserting
		// something else entirely and still passing.
		if l.Pool() != nil {
			t.Fatalf("the degraded fixture has a non-nil pool, so this subtest is no longer about an unreachable store")
		}
		if !l.Enabled() {
			t.Fatalf("the degraded fixture is not Enabled, so it does not exercise the enabled-but-unreachable case this subtest exists for")
		}
		got, err := l.CardForType(ctx, "engineer")
		if err == nil {
			t.Fatalf("a degraded Ledger must report that it cannot answer; got card %+v", got)
		}
		if got != nil {
			t.Errorf("a failed lookup must return no card, not a zero-valued one; got %+v", got)
		}
	})

	t.Run("disabled ledger with no pool", func(t *testing.T) {
		l := &Ledger{enabled: false, projectID: uuid.Nil}
		got, err := l.CardForType(ctx, "engineer")
		if err == nil {
			t.Fatalf("a Ledger with no pool must report that it cannot answer; got card %+v", got)
		}
		if got != nil {
			t.Errorf("a failed lookup must return no card, not a zero-valued one; got %+v", got)
		}
	})
}

// TestCardForType_ErrorNamesTheAgentType pins that the failure is diagnosable.
//
// "store: card lookup failed" in a spawn log tells an operator nothing about
// which agent silently fell back to a compiled-in seed. The fallback is
// deliberately silent at the behaviour level — a launch must not fail — so the
// error text is the only channel that carries which type was affected.
//
// Only the unusable-Ledger errors are reachable hermetically. The other two an
// operator meets — "no row for this type" and "render is not fully specified" —
// need a database, and each is asserted to name the type or the column in
// cards_integration_test.go.
func TestCardForType_ErrorNamesTheAgentType(t *testing.T) {
	for name, l := range map[string]*Ledger{
		"nil ledger": nil,
		"no pool":    {enabled: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := l.CardForType(context.Background(), "researcher")
			if err == nil {
				t.Fatalf("expected an error from a %s", name)
			}
			if !strings.Contains(err.Error(), "researcher") {
				t.Errorf("the error must name the agent type whose lookup failed, so a spawn log says which agent fell back; got: %v", err)
			}
		})
	}
}
