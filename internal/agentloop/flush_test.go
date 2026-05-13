package agentloop

import (
	"regexp"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/messages"
)

func TestBuildQueueFlushPrompt_Empty(t *testing.T) {
	if got := BuildQueueFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
	if got := BuildQueueFlushPrompt([]Entry{}); got != "" {
		t.Errorf("expected empty prompt for empty entries, got %q", got)
	}
}

// TestBuildQueueFlushPrompt_SingleEntry verifies the agentloop re-export still
// emits the QUM-555 one-line `<system-notification>` shape via inboxprompt.
func TestBuildQueueFlushPrompt_SingleEntry(t *testing.T) {
	entries := []Entry{{
		ID: "u1", ShortID: "abc", Class: ClassAsync,
		From: "child-alpha", Subject: "ignored", Body: "ignored",
		Tags: []string{"fyi"},
	}}
	got := BuildQueueFlushPrompt(entries)
	want := "<system-notification>New message from child-alpha. Read abc.</system-notification>\n"
	if got != want {
		t.Errorf("mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_FallsBackToID covers entries without a ShortID.
func TestBuildQueueFlushPrompt_FallsBackToID(t *testing.T) {
	entries := []Entry{{ID: "uuid-foo", ShortID: "", From: "f", Body: "b"}}
	got := BuildQueueFlushPrompt(entries)
	want := "<system-notification>New message from f. Read uuid-foo.</system-notification>\n"
	if got != want {
		t.Errorf("mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_Multiple verifies N entries → N lines.
func TestBuildQueueFlushPrompt_Multiple(t *testing.T) {
	entries := []Entry{
		{ID: "u1", ShortID: "s1", From: "a", Body: "b1"},
		{ID: "u2", ShortID: "s2", From: "b", Body: "b2"},
	}
	got := BuildQueueFlushPrompt(entries)
	want := "<system-notification>New message from a. Read s1.</system-notification>\n" +
		"<system-notification>New message from b. Read s2.</system-notification>\n"
	if got != want {
		t.Errorf("mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_NoBodyInlined guards against the verbose pre-
// QUM-555 frame leaking back into the agentloop re-export.
func TestBuildQueueFlushPrompt_NoBodyInlined(t *testing.T) {
	entries := []Entry{{
		ID: "u1", ShortID: "s1", From: "a",
		Subject: "secret-subject", Body: "secret-body-content",
		Tags: []string{"secret-tag"},
	}}
	got := BuildQueueFlushPrompt(entries)
	for _, banned := range []string{
		"secret-subject", "secret-body-content", "secret-tag",
		"Continue your current work", "[inbox]", "subject:",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("BuildQueueFlushPrompt leaked %q in output: %q", banned, got)
		}
	}
}

// TestBuildQueueFlushPrompt_HintIDResolvesViaMessages is an integration test:
// deliver a real maildir message via messages.Send, surface the resulting
// ShortID through an agentloop.Entry, build the flush prompt, parse the ID
// out of the `Read $ID.` clause, and confirm messages.ResolvePrefix can
// resolve it. This guards the contract that the notification cites an ID
// format the documented `sprawl messages read` flow actually accepts.
func TestBuildQueueFlushPrompt_HintIDResolvesViaMessages(t *testing.T) {
	root := t.TempDir()
	const recipient = "child-alpha"

	shortID, err := messages.Send(root, "weave", recipient, "subj", "body")
	if err != nil {
		t.Fatalf("messages.Send: %v", err)
	}
	if shortID == "" {
		t.Fatalf("messages.Send returned empty shortID")
	}

	entries := []Entry{{ID: "uuid-irrelevant", ShortID: shortID, From: "weave", Subject: "subj", Body: "body"}}
	p := BuildQueueFlushPrompt(entries)

	re := regexp.MustCompile(`Read ([^.\s]+)\.`)
	m := re.FindStringSubmatch(p)
	if m == nil {
		t.Fatalf("could not find Read $ID. clause in prompt:\n%s", p)
	}
	cited := m[1]

	full, err := messages.ResolvePrefix(root, recipient, cited)
	if err != nil {
		t.Fatalf("ResolvePrefix(%q): %v", cited, err)
	}
	if full == "" {
		t.Fatalf("ResolvePrefix(%q) returned empty full ID", cited)
	}
}

func TestBuildInterruptFlushPrompt_Empty(t *testing.T) {
	if got := BuildInterruptFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
}

// TestBuildInterruptFlushPrompt_SingleEntry verifies the agentloop re-export
// emits the QUM-555 interrupt-tagged one-line shape.
func TestBuildInterruptFlushPrompt_SingleEntry(t *testing.T) {
	entries := []Entry{{
		ID: "u1", ShortID: "xyz", Class: ClassInterrupt,
		From: "weave", Body: "stop", Tags: []string{"resume_hint:writing"},
	}}
	got := BuildInterruptFlushPrompt(entries)
	want := "<system-notification>[interrupt] New message from weave. Read xyz.</system-notification>\n"
	if got != want {
		t.Errorf("mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_Multiple verifies N interrupt entries → N lines.
func TestBuildInterruptFlushPrompt_Multiple(t *testing.T) {
	entries := []Entry{
		{ID: "u1", ShortID: "s1", From: "a", Body: "b1"},
		{ID: "u2", ShortID: "s2", From: "b", Body: "b2"},
	}
	got := BuildInterruptFlushPrompt(entries)
	want := "<system-notification>[interrupt] New message from a. Read s1.</system-notification>\n" +
		"<system-notification>[interrupt] New message from b. Read s2.</system-notification>\n"
	if got != want {
		t.Errorf("mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_NoBodyInlined guards against the verbose
// pre-QUM-555 interrupt frame leaking back.
func TestBuildInterruptFlushPrompt_NoBodyInlined(t *testing.T) {
	entries := []Entry{{
		ID: "u1", ShortID: "s1", From: "weave",
		Subject: "secret-subject", Body: "secret-body-content",
		Tags: []string{"resume_hint:secret-hint"},
	}}
	got := BuildInterruptFlushPrompt(entries)
	for _, banned := range []string{
		"secret-subject", "secret-body-content", "secret-hint",
		"After reading, decide", "has injected", "your previous task",
		"Subject:", "resume the interrupted",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("BuildInterruptFlushPrompt leaked %q in output: %q", banned, got)
		}
	}
}

func TestSplitByClass(t *testing.T) {
	entries := []Entry{
		{ID: "1", Class: ClassAsync},
		{ID: "2", Class: ClassInterrupt},
		{ID: "3", Class: ClassAsync},
		{ID: "4", Class: ClassInterrupt},
	}
	interrupts, asyncs := SplitByClass(entries)
	if len(interrupts) != 2 || interrupts[0].ID != "2" || interrupts[1].ID != "4" {
		t.Errorf("unexpected interrupts: %+v", interrupts)
	}
	if len(asyncs) != 2 || asyncs[0].ID != "1" || asyncs[1].ID != "3" {
		t.Errorf("unexpected asyncs: %+v", asyncs)
	}
}

func TestSplitByClass_Empty(t *testing.T) {
	interrupts, asyncs := SplitByClass(nil)
	if interrupts != nil || asyncs != nil {
		t.Errorf("expected nil slices for nil input, got %+v / %+v", interrupts, asyncs)
	}
}
