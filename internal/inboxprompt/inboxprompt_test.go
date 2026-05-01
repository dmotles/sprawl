// Package inboxprompt holds the inbox/interrupt prompt-formatter that both
// the legacy agentloop child harness and the unified-runtime supervisor path
// use to render pending queue entries into a turn prompt. Extracted from
// internal/agentloop in QUM-437 so the unified path no longer ships a stub
// "You have new messages" placeholder. The output must remain byte-identical
// to the prior agentloop implementation (the bottom test in this file pins
// that contract during the transition window).
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

func TestBuildQueueFlushPrompt_Contract(t *testing.T) {
	entries := []inboxprompt.Entry{
		{
			ID: "abc", Class: inboxprompt.ClassAsync, From: "child-alpha",
			Subject: "status", Body: "all green",
			Tags: []string{"fyi", "status"},
		},
	}
	p := inboxprompt.BuildQueueFlushPrompt(entries)
	for _, needle := range []string{
		"[inbox] You received 1 message(s) since the last turn",
		"from child-alpha",
		"[fyi,status]",
		"subject: status",
		"all green",
		"Continue your current work unless a message tells you otherwise.",
	} {
		if !strings.Contains(p, needle) {
			t.Errorf("prompt missing %q. full:\n%s", needle, p)
		}
	}
}

func TestBuildQueueFlushPrompt_Multiple(t *testing.T) {
	entries := []inboxprompt.Entry{
		{From: "a", Subject: "s1", Body: "b1"},
		{From: "b", Subject: "s2", Body: "b2"},
	}
	p := inboxprompt.BuildQueueFlushPrompt(entries)
	if !strings.Contains(p, "2 message(s)") {
		t.Errorf("expected '2 message(s)' in header, got:\n%s", p)
	}
	if !strings.Contains(p, "1. from a") || !strings.Contains(p, "2. from b") {
		t.Errorf("expected numbered entries, got:\n%s", p)
	}
}

func TestBuildQueueFlushPrompt_TruncatesLargeBody(t *testing.T) {
	big := strings.Repeat("x", inboxprompt.MaxQueueFlushBodyBytes+500)
	entries := []inboxprompt.Entry{{ID: "id1", ShortID: "sh1", From: "f", Subject: "s", Body: big}}
	p := inboxprompt.BuildQueueFlushPrompt(entries)
	if !strings.Contains(p, "truncated") {
		t.Errorf("expected truncation marker, got:\n%s", p[:200])
	}
	if !strings.Contains(p, "sprawl messages read sh1") {
		t.Errorf("expected truncation hint with ShortID, got:\n%s", p[:200])
	}
}

func TestBuildQueueFlushPrompt_TruncatedHintUsesShortID(t *testing.T) {
	big := strings.Repeat("y", inboxprompt.MaxQueueFlushBodyBytes+10)
	entries := []inboxprompt.Entry{{ID: "uuid-deadbeef", ShortID: "abc", From: "f", Subject: "s", Body: big}}
	p := inboxprompt.BuildQueueFlushPrompt(entries)
	if !strings.Contains(p, "sprawl messages read abc") {
		t.Errorf("expected hint to cite ShortID 'abc', got:\n%s", p)
	}
	if strings.Contains(p, "uuid-deadbeef") {
		t.Errorf("hint must not embed queue UUID, got:\n%s", p)
	}
}

func TestBuildQueueFlushPrompt_TruncatedHintFallsBackToID(t *testing.T) {
	big := strings.Repeat("z", inboxprompt.MaxQueueFlushBodyBytes+10)
	entries := []inboxprompt.Entry{{ID: "uuid-foo", ShortID: "", From: "f", Subject: "s", Body: big}}
	p := inboxprompt.BuildQueueFlushPrompt(entries)
	if !strings.Contains(p, "sprawl messages read uuid-foo") {
		t.Errorf("expected fallback to Entry.ID, got:\n%s", p)
	}
}

func TestBuildInterruptFlushPrompt_TruncatedHintUsesShortID(t *testing.T) {
	big := strings.Repeat("q", inboxprompt.MaxQueueFlushBodyBytes+10)
	entries := []inboxprompt.Entry{{ID: "uuid-cafef00d", ShortID: "xyz", From: "weave", Subject: "stop", Body: big}}
	p := inboxprompt.BuildInterruptFlushPrompt(entries)
	if !strings.Contains(p, "truncated") {
		t.Errorf("expected truncation marker, got:\n%s", p)
	}
	if !strings.Contains(p, "sprawl messages read xyz") {
		t.Errorf("expected hint to cite ShortID 'xyz', got:\n%s", p)
	}
	if strings.Contains(p, "uuid-cafef00d") {
		t.Errorf("hint must not embed queue UUID, got:\n%s", p)
	}
}

func TestBuildQueueFlushPrompt_AggregateCap(t *testing.T) {
	// Use a sentinel rune ('z') that appears nowhere in the prompt's
	// header, per-entry preamble, truncation marker, or footer — so
	// strings.Count(p, "z") returns *exactly* the surviving body bytes
	// after the aggregate cap is enforced, with no header-byte slack
	// needed. Six 2KB bodies of 'z' (12KB input) against a 10KB cap
	// must produce exactly 10KB of 'z' in the rendered prompt.
	const sentinel = "z"
	var entries []inboxprompt.Entry
	for i := 0; i < 6; i++ {
		entries = append(entries, inboxprompt.Entry{
			From:    "f",
			Subject: "s",
			Body:    strings.Repeat(sentinel, inboxprompt.MaxQueueFlushBodyBytes),
		})
	}
	p := inboxprompt.BuildQueueFlushPrompt(entries)
	bodyBytes := strings.Count(p, sentinel)
	if bodyBytes != inboxprompt.MaxQueueFlushTotalBytes {
		t.Errorf("aggregate body bytes after cap = %d, want exactly %d (cap = %d, input = %d)",
			bodyBytes, inboxprompt.MaxQueueFlushTotalBytes,
			inboxprompt.MaxQueueFlushTotalBytes,
			6*inboxprompt.MaxQueueFlushBodyBytes)
	}
}

func TestBuildInterruptFlushPrompt_Empty(t *testing.T) {
	if got := inboxprompt.BuildInterruptFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
}

func TestBuildInterruptFlushPrompt_SingleWithResumeHint(t *testing.T) {
	entries := []inboxprompt.Entry{
		{
			From: "weave", Subject: "stop", Body: "reprioritize",
			Tags: []string{"resume_hint:writing tests"},
		},
	}
	p := inboxprompt.BuildInterruptFlushPrompt(entries)
	for _, needle := range []string{
		"[interrupt]",
		"weave has injected",
		"writing tests",
		"Subject: stop",
		"reprioritize",
		"resume the interrupted work",
	} {
		if !strings.Contains(p, needle) {
			t.Errorf("prompt missing %q. full:\n%s", needle, p)
		}
	}
}

func TestBuildInterruptFlushPrompt_FallbackHint(t *testing.T) {
	entries := []inboxprompt.Entry{{From: "weave", Subject: "s", Body: "b"}}
	p := inboxprompt.BuildInterruptFlushPrompt(entries)
	if !strings.Contains(p, "your previous task") {
		t.Errorf("expected fallback 'your previous task', got:\n%s", p)
	}
}

func TestBuildInterruptFlushPrompt_MultipleSenders(t *testing.T) {
	entries := []inboxprompt.Entry{
		{From: "a", Subject: "s1", Body: "b1"},
		{From: "b", Subject: "s2", Body: "b2"},
	}
	p := inboxprompt.BuildInterruptFlushPrompt(entries)
	if !strings.Contains(p, "2 senders") {
		t.Errorf("expected '2 senders', got:\n%s", p)
	}
	if !strings.Contains(p, "--- 1 of 2") || !strings.Contains(p, "--- 2 of 2") {
		t.Errorf("expected per-message separators, got:\n%s", p)
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

// TestBuildPromptsMatchGolden pins the byte-exact wire format of the queue-
// flush and interrupt-flush prompts against committed golden files. The
// goldens were captured from the (pre-extraction) agentloop implementation
// so the inboxprompt output must remain byte-identical during and after the
// QUM-437 extraction. If a formatter change is intentional, regenerate the
// goldens via a one-shot scratch program — never via a self-updating test.
//
// Fixture covers: 2 async entries (1 short body w/ tags+ShortID, 1 oversized
// to exercise per-entry truncation) and 2 interrupt entries (resume_hint
// tag on the first, mixed senders, second oversized).
func TestBuildPromptsMatchGolden(t *testing.T) {
	bigAsync := strings.Repeat("a", inboxprompt.MaxQueueFlushBodyBytes+50)
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
			ShortID: "sh2",
			Class:   inboxprompt.ClassAsync,
			From:    "child-beta",
			Subject: "huge",
			Body:    bigAsync,
		},
	}
	bigInt := strings.Repeat("b", inboxprompt.MaxQueueFlushBodyBytes+50)
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
			ShortID: "si2",
			Class:   inboxprompt.ClassInterrupt,
			From:    "ratz",
			Subject: "huge",
			Body:    bigInt,
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
