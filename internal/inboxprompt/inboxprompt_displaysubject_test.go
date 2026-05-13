// Tests for the DisplaySubject helper. After QUM-555 slimmed the queue/
// interrupt flush prompts to a single `<system-notification>` line per entry,
// DisplaySubject is no longer wired into the rendered output — but it remains
// exported for callers that want a human-facing label for an inbox entry
// (e.g. TUI surfaces, future tooling). These tests pin the helper's own
// behavior.
package inboxprompt_test

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/inboxprompt"
)

// TestDisplaySubject_PrefersSubject_WhenNonEmpty pins the subject-wins case:
// existing entries persisted with a real Subject must still render that
// value (back-compat for messages enqueued before the QUM-550 rollout).
func TestDisplaySubject_PrefersSubject_WhenNonEmpty(t *testing.T) {
	e := inboxprompt.Entry{Subject: "hello", Body: "world"}
	got := inboxprompt.DisplaySubject(e)
	if got != "hello" {
		t.Errorf("DisplaySubject(subject=hello, body=world) = %q, want %q", got, "hello")
	}
}

// TestDisplaySubject_FallsBackToFirstBodyLine_WhenSubjectEmpty pins the
// fallback: an entry without a subject derives its display label from the
// first non-empty line of the body.
func TestDisplaySubject_FallsBackToFirstBodyLine_WhenSubjectEmpty(t *testing.T) {
	e := inboxprompt.Entry{Subject: "", Body: "first line\nsecond line"}
	got := inboxprompt.DisplaySubject(e)
	if got != "first line" {
		t.Errorf("DisplaySubject = %q, want %q (first non-empty body line)", got, "first line")
	}
}

// TestDisplaySubject_TruncatesLongBodyFirstLine pins the truncation cap:
// hard truncate at 80 bytes (no ellipsis) so the inbox prompt stays tidy.
func TestDisplaySubject_TruncatesLongBodyFirstLine(t *testing.T) {
	long := strings.Repeat("a", 200)
	e := inboxprompt.Entry{Subject: "", Body: long}
	got := inboxprompt.DisplaySubject(e)

	if len(got) != 80 {
		t.Errorf("DisplaySubject len = %d, want 80 (hard truncate)", len(got))
	}
	if got != strings.Repeat("a", 80) {
		t.Errorf("DisplaySubject = %q, want 80 'a' chars", got)
	}
}

// TestDisplaySubject_SkipsEmptyLeadingLines pins the "first non-empty line"
// semantic. A body that opens with blank lines should still produce a
// meaningful label.
func TestDisplaySubject_SkipsEmptyLeadingLines(t *testing.T) {
	e := inboxprompt.Entry{Subject: "", Body: "\n\n  \nactual content\nmore"}
	got := inboxprompt.DisplaySubject(e)
	if !strings.Contains(got, "actual content") {
		t.Errorf("DisplaySubject = %q, want to contain 'actual content' (skip blank leading lines)", got)
	}
}
