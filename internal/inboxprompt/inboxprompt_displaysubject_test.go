// Tests for QUM-550 slice 1: displaySubject helper and its use in the
// flush-prompt builders. After the send_message overhaul drops the
// per-message subject from the API, queue entries persist with Subject="" —
// the flush-prompt formatters must fall back to the body's first non-empty
// line so the inbox notification still renders a meaningful label.
//
// RED phase: DisplaySubject does not exist yet; the file is intentional
// compile-fail.
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

// TestBuildQueueFlushPrompt_FallsBackToBodyFirstLine_WhenSubjectEmpty pins
// the integration of DisplaySubject into the async queue flush prompt. When
// an entry has no Subject, the prompt's per-message header must surface the
// first body line where the subject would have gone.
func TestBuildQueueFlushPrompt_FallsBackToBodyFirstLine_WhenSubjectEmpty(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID:      "id-1",
		ShortID: "abc",
		Class:   inboxprompt.ClassAsync,
		From:    "weave",
		Subject: "",
		Body:    "decision needed on X\nmore detail here",
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)

	if !strings.Contains(got, "decision needed on X") {
		t.Errorf("BuildQueueFlushPrompt with empty Subject should fall back to body's first line; got:\n%s", got)
	}
	// The literal placeholder "subject: " followed by an empty value must not
	// appear — the fallback should land in that slot.
	if strings.Contains(got, "subject: \n") || strings.Contains(got, "subject:  ") {
		t.Errorf("BuildQueueFlushPrompt rendered empty subject literally; got:\n%s", got)
	}
}

// TestBuildInterruptFlushPrompt_FallsBackToBodyFirstLine_WhenSubjectEmpty
// pins the same fallback for the interrupt flush prompt.
func TestBuildInterruptFlushPrompt_FallsBackToBodyFirstLine_WhenSubjectEmpty(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID:      "id-2",
		ShortID: "def",
		Class:   inboxprompt.ClassInterrupt,
		From:    "weave",
		Subject: "",
		Body:    "stop and switch tasks\nresume hint goes here",
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)

	if !strings.Contains(got, "stop and switch tasks") {
		t.Errorf("BuildInterruptFlushPrompt with empty Subject should fall back to body's first line; got:\n%s", got)
	}
	if strings.Contains(got, "Subject: \n") {
		t.Errorf("BuildInterruptFlushPrompt rendered empty 'Subject:' literally; got:\n%s", got)
	}
}
