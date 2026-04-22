package agentloop

import (
	"strings"
	"testing"
)

func TestBuildQueueFlushPrompt_Empty(t *testing.T) {
	if got := BuildQueueFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
	if got := BuildQueueFlushPrompt([]Entry{}); got != "" {
		t.Errorf("expected empty prompt for empty entries, got %q", got)
	}
}

func TestBuildQueueFlushPrompt_Contract(t *testing.T) {
	entries := []Entry{
		{
			ID: "abc", Class: ClassAsync, From: "child-alpha",
			Subject: "status", Body: "all green",
			Tags: []string{"fyi", "status"},
		},
	}
	p := BuildQueueFlushPrompt(entries)
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
	entries := []Entry{
		{From: "a", Subject: "s1", Body: "b1"},
		{From: "b", Subject: "s2", Body: "b2"},
	}
	p := BuildQueueFlushPrompt(entries)
	if !strings.Contains(p, "2 message(s)") {
		t.Errorf("expected '2 message(s)' in header, got:\n%s", p)
	}
	if !strings.Contains(p, "1. from a") || !strings.Contains(p, "2. from b") {
		t.Errorf("expected numbered entries, got:\n%s", p)
	}
}

func TestBuildQueueFlushPrompt_TruncatesLargeBody(t *testing.T) {
	big := strings.Repeat("x", MaxQueueFlushBodyBytes+500)
	entries := []Entry{{ID: "id1", From: "f", Subject: "s", Body: big}}
	p := BuildQueueFlushPrompt(entries)
	if !strings.Contains(p, "truncated") {
		t.Errorf("expected truncation marker, got:\n%s", p[:200])
	}
	if !strings.Contains(p, "sprawl messages read id1") {
		t.Errorf("expected truncation hint with entry ID, got:\n%s", p[:200])
	}
}

func TestBuildQueueFlushPrompt_AggregateCap(t *testing.T) {
	// 6 entries of MaxQueueFlushBodyBytes each = 12KiB of body, exceeds the 10KiB total cap.
	var entries []Entry
	for i := 0; i < 6; i++ {
		entries = append(entries, Entry{
			From:    "f",
			Subject: "s",
			Body:    strings.Repeat(string(rune('a'+i)), MaxQueueFlushBodyBytes),
		})
	}
	p := BuildQueueFlushPrompt(entries)
	// Rough sanity: total body bytes in the prompt must be within cap + margin.
	bodyBytes := 0
	for _, r := range p {
		if r >= 'a' && r <= 'f' {
			bodyBytes++
		}
	}
	if bodyBytes > MaxQueueFlushTotalBytes+512 {
		t.Errorf("aggregate body bytes %d exceeds cap %d+margin", bodyBytes, MaxQueueFlushTotalBytes)
	}
}

func TestBuildInterruptFlushPrompt_Empty(t *testing.T) {
	if got := BuildInterruptFlushPrompt(nil); got != "" {
		t.Errorf("expected empty prompt for nil entries, got %q", got)
	}
}

func TestBuildInterruptFlushPrompt_SingleWithResumeHint(t *testing.T) {
	entries := []Entry{
		{
			From: "weave", Subject: "stop", Body: "reprioritize",
			Tags: []string{"resume_hint:writing tests"},
		},
	}
	p := BuildInterruptFlushPrompt(entries)
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
	entries := []Entry{{From: "weave", Subject: "s", Body: "b"}}
	p := BuildInterruptFlushPrompt(entries)
	if !strings.Contains(p, "your previous task") {
		t.Errorf("expected fallback 'your previous task', got:\n%s", p)
	}
}

func TestBuildInterruptFlushPrompt_MultipleSenders(t *testing.T) {
	entries := []Entry{
		{From: "a", Subject: "s1", Body: "b1"},
		{From: "b", Subject: "s2", Body: "b2"},
	}
	p := BuildInterruptFlushPrompt(entries)
	if !strings.Contains(p, "2 senders") {
		t.Errorf("expected '2 senders', got:\n%s", p)
	}
	if !strings.Contains(p, "--- 1 of 2") || !strings.Contains(p, "--- 2 of 2") {
		t.Errorf("expected per-message separators, got:\n%s", p)
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
