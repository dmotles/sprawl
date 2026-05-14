// Package inboxprompt holds the inbox/interrupt prompt-formatter that both
// the legacy agentloop child harness and the unified-runtime supervisor path
// use to render pending queue entries into a turn prompt. QUM-555 slimmed the
// frames to one `<system-notification>` line per entry — no inlined body,
// no footer prose.
package inboxprompt_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/inboxprompt"
)

func TestBuildQueueFlushPrompt_Empty(t *testing.T) {
	if got := inboxprompt.BuildQueueFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
	if got := inboxprompt.BuildQueueFlushPrompt([]inboxprompt.Entry{}); got != "" {
		t.Errorf("expected empty prompt for empty entries, got %q", got)
	}
}

// TestBuildQueueFlushPrompt_SingleEntry pins the exact one-line shape per
// QUM-556: `<system-notification>From $FROM — mcp__sprawl__messages_read(id=$SHORT_ID)</system-notification>\n`.
// No subject, no body, no footer prose. The fully-qualified MCP tool name
// is the primary pattern-match anchor for the recipient agent.
func TestBuildQueueFlushPrompt_SingleEntry(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-1", ShortID: "abc", Class: inboxprompt.ClassAsync,
		From: "child-alpha", Subject: "status", Body: "all green",
		Tags: []string{"fyi"},
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	want := "<system-notification type=\"message\">From child-alpha — mcp__sprawl__messages_read(id=abc)</system-notification>\n"
	if got != want {
		t.Errorf("BuildQueueFlushPrompt mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_SingleEntry_FallsBackToID covers entries whose
// ShortID is empty (legacy enqueues): the line must cite Entry.ID instead.
func TestBuildQueueFlushPrompt_SingleEntry_FallsBackToID(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-foo", ShortID: "", Class: inboxprompt.ClassAsync,
		From: "weave", Body: "hello",
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	want := "<system-notification type=\"message\">From weave — mcp__sprawl__messages_read(id=uuid-foo)</system-notification>\n"
	if got != want {
		t.Errorf("BuildQueueFlushPrompt fallback mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_Multiple pins N entries → N lines, one per entry.
func TestBuildQueueFlushPrompt_Multiple(t *testing.T) {
	entries := []inboxprompt.Entry{
		{ID: "u1", ShortID: "s1", From: "a", Body: "b1"},
		{ID: "u2", ShortID: "s2", From: "b", Body: "b2"},
		{ID: "u3", ShortID: "s3", From: "c", Body: "b3"},
	}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	want := "<system-notification type=\"message\">From a — mcp__sprawl__messages_read(id=s1)</system-notification>\n" +
		"<system-notification type=\"message\">From b — mcp__sprawl__messages_read(id=s2)</system-notification>\n" +
		"<system-notification type=\"message\">From c — mcp__sprawl__messages_read(id=s3)</system-notification>\n"
	if got != want {
		t.Errorf("BuildQueueFlushPrompt multi mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildQueueFlushPrompt_NoBodyInlined guards against any reintroduction
// of the verbose pre-QUM-555 frame. Body text, subject, tags, and the legacy
// footer must NOT appear in the output.
func TestBuildQueueFlushPrompt_NoBodyInlined(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "s1", From: "a",
		Subject: "secret-subject", Body: "secret-body-content",
		Tags: []string{"secret-tag"},
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	for _, banned := range []string{
		"secret-subject", "secret-body-content", "secret-tag",
		"Continue your current work", "[inbox]", "subject:",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("BuildQueueFlushPrompt leaked %q in output: %q", banned, got)
		}
	}
}

// TestBuildQueueFlushPrompt_NamesMCPTool is the QUM-556 regression guard:
// the rendered line MUST contain the fully-qualified MCP tool name
// `mcp__sprawl__messages_read` with the id in function-call shape, and MUST
// NOT use the bare verb "Read " (which was ambiguous with the legacy CLI
// form and triggered the wrong path).
func TestBuildQueueFlushPrompt_NamesMCPTool(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "abc", From: "weave", Body: "hi",
	}}
	got := inboxprompt.BuildQueueFlushPrompt(entries)
	if !strings.Contains(got, "mcp__sprawl__messages_read(id=abc)") {
		t.Errorf("queue flush missing MCP tool citation: %q", got)
	}
	if strings.Contains(got, "Read abc") {
		t.Errorf("queue flush still uses bare 'Read' verb (QUM-556 regression): %q", got)
	}
}

// TestBuildInterruptFlushPrompt_NamesMCPTool — QUM-556 regression guard for
// the interrupt path. Same anchors as the async path.
func TestBuildInterruptFlushPrompt_NamesMCPTool(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "xyz", Class: inboxprompt.ClassInterrupt,
		From: "weave", Body: "stop",
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	if !strings.Contains(got, "mcp__sprawl__messages_read(id=xyz)") {
		t.Errorf("interrupt flush missing MCP tool citation: %q", got)
	}
	if strings.Contains(got, "Read xyz") {
		t.Errorf("interrupt flush still uses bare 'Read' verb (QUM-556 regression): %q", got)
	}
}

func TestBuildInterruptFlushPrompt_Empty(t *testing.T) {
	if got := inboxprompt.BuildInterruptFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
}

// TestBuildInterruptFlushPrompt_SingleEntry pins the interrupt-line shape:
// same tag, prefixed inside with `[interrupt] `.
func TestBuildInterruptFlushPrompt_SingleEntry(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-int-1", ShortID: "xyz", Class: inboxprompt.ClassInterrupt,
		From: "weave", Subject: "stop", Body: "reprioritize",
		Tags: []string{"resume_hint:writing tests"},
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	want := "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From weave — mcp__sprawl__messages_read(id=xyz)</system-notification>\n"
	if got != want {
		t.Errorf("BuildInterruptFlushPrompt mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_SingleEntry_FallsBackToID covers entries
// without a ShortID; the interrupt line falls back to Entry.ID.
func TestBuildInterruptFlushPrompt_SingleEntry_FallsBackToID(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "uuid-bar", ShortID: "", Class: inboxprompt.ClassInterrupt,
		From: "weave", Body: "stop",
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	want := "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From weave — mcp__sprawl__messages_read(id=uuid-bar)</system-notification>\n"
	if got != want {
		t.Errorf("BuildInterruptFlushPrompt fallback mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_Multiple pins N interrupt entries → N lines.
func TestBuildInterruptFlushPrompt_Multiple(t *testing.T) {
	entries := []inboxprompt.Entry{
		{ID: "u1", ShortID: "s1", From: "a", Body: "b1"},
		{ID: "u2", ShortID: "s2", From: "b", Body: "b2"},
	}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
	want := "<system-notification type=\"message\" interrupt=\"true\">[interrupt] From a — mcp__sprawl__messages_read(id=s1)</system-notification>\n" +
		"<system-notification type=\"message\" interrupt=\"true\">[interrupt] From b — mcp__sprawl__messages_read(id=s2)</system-notification>\n"
	if got != want {
		t.Errorf("BuildInterruptFlushPrompt multi mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestBuildInterruptFlushPrompt_NoBodyInlined guards against any reintroduction
// of the verbose pre-QUM-555 interrupt frame.
func TestBuildInterruptFlushPrompt_NoBodyInlined(t *testing.T) {
	entries := []inboxprompt.Entry{{
		ID: "u1", ShortID: "s1", From: "weave",
		Subject: "secret-subject", Body: "secret-body-content",
		Tags: []string{"resume_hint:secret-hint"},
	}}
	got := inboxprompt.BuildInterruptFlushPrompt(entries)
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

// --- QUM-559: BuildStatusNotification tests ---
//
// The new formatter renders a single `<system-notification>` line per status
// report, distinct from the message-queue formatters above. The line has the
// shape:
//   <system-notification>$AGENT changed status to $STATE: $SUMMARY</system-notification>\n
// No body inlining, no `mcp__sprawl__messages_read` citation (this is a status
// channel, not a mail channel).

// TestBuildStatusNotification_Shape pins the exact wire format for each of
// the four canonical report states.
func TestBuildStatusNotification_Shape(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{
			state: "working",
			want:  "<system-notification type=\"status_change\">finn changed status to working: doing X</system-notification>\n",
		},
		{
			state: "blocked",
			want:  "<system-notification type=\"status_change\">finn changed status to blocked: doing X</system-notification>\n",
		},
		{
			state: "complete",
			want:  "<system-notification type=\"status_change\">finn changed status to complete: doing X</system-notification>\n",
		},
		{
			state: "failure",
			want:  "<system-notification type=\"status_change\">finn changed status to failure: doing X</system-notification>\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			got := inboxprompt.BuildStatusNotification("finn", tc.state, "doing X")
			if got != tc.want {
				t.Errorf("BuildStatusNotification(%q) mismatch\n got: %q\nwant: %q", tc.state, got, tc.want)
			}
		})
	}
}

// TestBuildStatusNotification_NoToolCitation guards against copy-paste from
// the queue-flush formatter: the status notification is its own channel and
// must NOT cite `mcp__sprawl__messages_read` (no maildir entry exists for
// reports after QUM-559).
func TestBuildStatusNotification_NoToolCitation(t *testing.T) {
	got := inboxprompt.BuildStatusNotification("finn", "working", "doing X")
	if strings.Contains(got, "mcp__sprawl__messages_read") {
		t.Errorf("status notification must not cite mcp__sprawl__messages_read: %q", got)
	}
	// And no maildir id= shape either.
	if strings.Contains(got, "id=") {
		t.Errorf("status notification must not include id= citation: %q", got)
	}
}

// TestBuildStatusNotification_FailureBlockedSubstrings — the QUM-557
// TUI color-coder triggers on the literal substrings " to failure: " and
// " to blocked: " in the rendered line. Pin those substrings so any future
// rewording of the formatter must update the color-coder in lockstep.
func TestBuildStatusNotification_FailureBlockedSubstrings(t *testing.T) {
	failureLine := inboxprompt.BuildStatusNotification("finn", "failure", "oops")
	if !strings.Contains(failureLine, " to failure: ") {
		t.Errorf("failure status missing ' to failure: ' substring: %q", failureLine)
	}
	blockedLine := inboxprompt.BuildStatusNotification("finn", "blocked", "waiting")
	if !strings.Contains(blockedLine, " to blocked: ") {
		t.Errorf("blocked status missing ' to blocked: ' substring: %q", blockedLine)
	}
}

func TestSplitByClass(t *testing.T) {
	entries := []inboxprompt.Entry{
		{ID: "1", Class: inboxprompt.ClassAsync},
		{ID: "2", Class: inboxprompt.ClassInterrupt},
		{ID: "3", Class: inboxprompt.ClassAsync},
		{ID: "4", Class: inboxprompt.ClassInterrupt},
	}
	interrupts, asyncs := inboxprompt.SplitByClass(entries)
	if len(interrupts) != 2 || interrupts[0].ID != "2" || interrupts[1].ID != "4" {
		t.Errorf("unexpected interrupts: %+v", interrupts)
	}
	if len(asyncs) != 2 || asyncs[0].ID != "1" || asyncs[1].ID != "3" {
		t.Errorf("unexpected asyncs: %+v", asyncs)
	}
}

func TestSplitByClass_Empty(t *testing.T) {
	interrupts, asyncs := inboxprompt.SplitByClass(nil)
	if interrupts != nil || asyncs != nil {
		t.Errorf("expected nil slices for nil input, got %+v / %+v", interrupts, asyncs)
	}
}

// TestBuildPromptsMatchGolden pins the byte-exact wire format of the
// slim QUM-555 frames against committed golden files. Fixture covers two
// async entries (one with ShortID, one with empty ShortID to exercise the
// fallback path) and two interrupt entries (likewise).
func TestBuildPromptsMatchGolden(t *testing.T) {
	asyncs := []inboxprompt.Entry{
		{
			ID:      "uuid-async-1",
			ShortID: "sh1",
			Class:   inboxprompt.ClassAsync,
			From:    "child-alpha",
			Subject: "status",
			Body:    "all green",
			Tags:    []string{"fyi", "status"},
		},
		{
			ID:      "uuid-async-2",
			ShortID: "",
			Class:   inboxprompt.ClassAsync,
			From:    "child-beta",
			Subject: "ping",
			Body:    "hi",
		},
	}
	interrupts := []inboxprompt.Entry{
		{
			ID:      "uuid-int-1",
			ShortID: "si1",
			Class:   inboxprompt.ClassInterrupt,
			From:    "weave",
			Subject: "stop",
			Body:    "reprioritize now",
			Tags:    []string{"resume_hint:writing tests"},
		},
		{
			ID:      "uuid-int-2",
			ShortID: "",
			Class:   inboxprompt.ClassInterrupt,
			From:    "ratz",
			Subject: "urgent",
			Body:    "halt",
		},
	}

	cases := []struct {
		name   string
		golden string
		got    string
	}{
		{"queue_flush", "queue_flush.golden", inboxprompt.BuildQueueFlushPrompt(asyncs)},
		{"interrupt_flush", "interrupt_flush.golden", inboxprompt.BuildInterruptFlushPrompt(interrupts)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join("testdata", tc.golden)
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v", path, err)
			}
			if tc.got != string(want) {
				t.Errorf("%s: byte mismatch with %s\n--- got (%d bytes) ---\n%s\n--- want (%d bytes) ---\n%s",
					tc.name, path, len(tc.got), tc.got, len(want), string(want))
			}
		})
	}
}
