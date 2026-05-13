// Package sprawlmcp tests pin consistency between MCP tool descriptions
// (tools.go) and prompt mentions in internal/agent/prompt_mode.go. After
// QUM-550 slice 5, the deprecated send_async / send_interrupt tools are
// removed entirely; send_message is the sole messaging tool surfaced in
// the prompt templates.
package sprawlmcp

import (
	"os"
	"strings"
	"testing"
)

// promptModeSource reads internal/agent/prompt_mode.go relative to this test
// package. We intentionally avoid adding a new export from internal/agent;
// the test asserts the source file's literal contents stay in sync with the
// MCP tool surface.
func promptModeSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../agent/prompt_mode.go")
	if err != nil {
		t.Fatalf("read prompt_mode.go: %v", err)
	}
	return string(b)
}

// canonicalMessagingTools is the subset of canonical tools we expect to be
// surfaced in the TUI-mode prompt templates.
var canonicalMessagingTools = []string{
	"send_message",
	"report_status",
	"delegate",
	"peek",
	"spawn",
	"merge",
	"retire",
}

// TestPromptModeDescriptions_InSyncWithMCPTools verifies the canonical
// messaging surface is mentioned in prompt_mode.go and that the canonical
// `send_message(to, body, interrupt)` argument shape is preserved.
func TestPromptModeDescriptions_InSyncWithMCPTools(t *testing.T) {
	src := promptModeSource(t)

	// 1. Every canonical messaging tool must appear by name in prompt_mode.go.
	for _, name := range canonicalMessagingTools {
		if !strings.Contains(src, name) {
			t.Errorf("prompt_mode.go missing canonical MCP tool mention: %q", name)
		}
	}

	// 2. send_message: must appear with `interrupt` referenced nearby
	//    (within 500 chars of a `send_message(` call site). This pins
	//    the canonical (to, body, interrupt) shape.
	if idx := strings.Index(src, "send_message("); idx < 0 {
		t.Errorf("prompt_mode.go must reference `send_message(` (canonical messaging tool)")
	} else {
		start := idx
		end := idx + 500
		if end > len(src) {
			end = len(src)
		}
		window := src[start:end]
		if !strings.Contains(window, "interrupt") {
			t.Errorf("prompt_mode.go mentions send_message( but not `interrupt` argument within 500 chars; window=%q", window)
		}
	}

	// 3. Deprecated tools must NOT appear anywhere in prompt_mode.go.
	for _, banned := range []string{"send_async", "send_interrupt"} {
		if strings.Contains(src, banned) {
			t.Errorf("prompt_mode.go must not reference removed tool %q (QUM-550 slice 5)", banned)
		}
	}

	// 4. Negative: send_message must NOT carry deprecated send_async
	//    argument shape (subject:/reply_to:/tags:).
	bannedNearSendMessage := []string{"subject:", "reply_to:", "tags:"}
	searchFrom := 0
	for {
		i := strings.Index(src[searchFrom:], "send_message(")
		if i < 0 {
			break
		}
		abs := searchFrom + i
		lo := abs
		hi := abs + 200
		if hi > len(src) {
			hi = len(src)
		}
		window := src[lo:hi]
		for _, banned := range bannedNearSendMessage {
			if strings.Contains(window, banned) {
				t.Errorf("prompt_mode.go has banned key %q within 200 chars of send_message(; window=%q", banned, window)
			}
		}
		searchFrom = abs + len("send_message(")
	}

	// 5. report_status mentions must NOT include `detail:` (slice 2/5
	//    dropped the field).
	searchFrom = 0
	for {
		i := strings.Index(src[searchFrom:], "report_status(")
		if i < 0 {
			break
		}
		abs := searchFrom + i
		lo := abs
		hi := abs + 200
		if hi > len(src) {
			hi = len(src)
		}
		window := src[lo:hi]
		if strings.Contains(window, "detail:") {
			t.Errorf("prompt_mode.go mentions report_status with banned `detail:` field at offset %d; window=%q", abs, window)
		}
		searchFrom = abs + len("report_status(")
	}
}

// TestPromptModeDescriptions_SendMessageMentionedInTUITemplates pins the
// literal `send_message(` call-shape into prompt_mode.go.
func TestPromptModeDescriptions_SendMessageMentionedInTUITemplates(t *testing.T) {
	src := promptModeSource(t)
	if !strings.Contains(src, "send_message(") {
		t.Fatalf("prompt_mode.go must reference `send_message(` — canonical messaging tool after QUM-550.")
	}
}

// TestPromptModeDescriptions_ReportStatusHasNoDetailField locks in the
// slice-2 contract: report_status no longer accepts `detail:`.
func TestPromptModeDescriptions_ReportStatusHasNoDetailField(t *testing.T) {
	src := promptModeSource(t)
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "report_status(") {
			continue
		}
		window := line
		if i+1 < len(lines) {
			window += "\n" + lines[i+1]
		}
		if strings.Contains(window, "detail:") {
			t.Errorf("prompt_mode.go line %d: report_status carries banned `detail:` field; line=%q", i+1, line)
		}
	}
}
