package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// HintError exists because the primary consumer of this CLI is an agent
// (/cli-ux-best-practices). An error with no next action costs a round trip at
// best and an invented remedy at worst, so these tests pin BOTH that the hint is
// rendered and that wrapping still works — an error that renders beautifully but
// breaks errors.Is would make every caller's branch on ErrDegraded silently
// stop matching.

func TestHintError_RendersTheNextAction(t *testing.T) {
	err := &HintError{Err: errors.New("the thing broke"), Hint: "do the other thing"}
	got := err.Error()
	if !strings.Contains(got, "the thing broke") {
		t.Errorf("the rendering dropped the underlying error: %q", got)
	}
	if !strings.Contains(got, "do the other thing") {
		t.Errorf("the rendering dropped the hint: %q", got)
	}
	if !strings.Contains(got, "next:") {
		t.Errorf("the hint is not labelled, so a reader cannot tell diagnosis from remedy: %q", got)
	}
}

// TestHintError_WithoutAHintRendersOnlyTheError pins that an empty hint does not
// produce a dangling "next:" with nothing after it, which reads as a truncated
// message and sends the reader looking for what got cut off.
func TestHintError_WithoutAHintRendersOnlyTheError(t *testing.T) {
	err := &HintError{Err: errors.New("the thing broke")}
	if got := err.Error(); got != "the thing broke" {
		t.Errorf("Error() = %q, want just the underlying error", got)
	}
}

// TestHintError_PreservesTheErrorChain is the assertion that matters most.
//
// Callers branch on errors.Is(err, ErrDegraded) to decide whether an outage is
// survivable. If HintError did not unwrap, every one of those branches would
// stop matching and the degraded-mode split would silently invert — a goal-open
// failure would look like an ordinary error and a spillable one like a fatal.
func TestHintError_PreservesTheErrorChain(t *testing.T) {
	wrapped := &HintError{
		Err:  fmt.Errorf("%w: while doing the thing", ErrDegraded),
		Hint: "fix the DSN",
	}
	if !errors.Is(wrapped, ErrDegraded) {
		t.Error("errors.Is could not see ErrDegraded through a HintError; every caller's degraded-mode branch would stop matching")
	}

	// And it must be reachable as a *HintError from further up a chain, since
	// callers that want to surface the hint wrap it again on the way out.
	outer := fmt.Errorf("opening the ledger: %w", wrapped)
	var hint *HintError
	if !errors.As(outer, &hint) {
		t.Fatal("errors.As could not recover the HintError from an outer wrap, so the hint would be unprintable")
	}
	if hint.Hint != "fix the DSN" {
		t.Errorf("recovered hint = %q, want %q", hint.Hint, "fix the DSN")
	}
}

// TestSentinelErrorsAreDistinct pins that the sentinels do not alias each other.
//
// ErrSchemaViolation and ErrDegraded drive OPPOSITE decisions — never spill vs
// spill — so if a refactor ever made one wrap the other, the spill logic would
// take the wrong branch and no other test in this package would notice, because
// each is only ever asserted positively.
func TestSentinelErrorsAreDistinct(t *testing.T) {
	all := map[string]error{
		"ErrSchemaViolation":          ErrSchemaViolation,
		"ErrUnsupportedSchemaKeyword": ErrUnsupportedSchemaKeyword,
		"ErrInsecureSecrets":          ErrInsecureSecrets,
		"ErrDegraded":                 ErrDegraded,
		"ErrNoOpenContract":           ErrNoOpenContract,
		"ErrAppendOnlyNotEnforced":    ErrAppendOnlyNotEnforced,
	}
	for nameA, a := range all {
		for nameB, b := range all {
			if nameA == nameB {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("errors.Is(%s, %s) is true; these sentinels drive different decisions and must not alias", nameA, nameB)
			}
		}
	}
}
