package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJSONL(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadTranscript_ReplayMaxMessagesConstant(t *testing.T) {
	if ReplayMaxMessages != 500 {
		t.Errorf("ReplayMaxMessages = %d, want 500", ReplayMaxMessages)
	}
}

func TestLoadTranscript_MissingFile(t *testing.T) {
	entries, err := LoadTranscript(filepath.Join(t.TempDir(), "nope.jsonl"), ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil", entries)
	}
}

func TestLoadTranscript_EmptyFile(t *testing.T) {
	path := writeJSONL(t, nil)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil", entries)
	}
}

func TestLoadTranscript_BasicUserAssistantText(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (user + assistant + trailing status); entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "hello" {
		t.Errorf("entries[0] = %+v, want MessageUser 'hello'", entries[0])
	}
	if entries[1].Type != MessageAssistant || entries[1].Content != "hi there" {
		t.Errorf("entries[1] = %+v, want MessageAssistant 'hi there'", entries[1])
	}
	if entries[2].Type != MessageStatus || entries[2].Content != "Resumed from prior session" {
		t.Errorf("entries[2] = %+v, want trailing status", entries[2])
	}
}

func TestLoadTranscript_AssistantMultipleBlocks(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"before"},` +
		`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls -la"}},` +
		`{"type":"text","text":"after"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("len(entries) = %d, want 4 (3 blocks + trailing status); entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageAssistant || entries[0].Content != "before" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if entries[1].Type != MessageToolCall {
		t.Errorf("entries[1].Type = %v, want MessageToolCall", entries[1].Type)
	}
	if entries[1].Content != "Bash" {
		t.Errorf("entries[1].Content = %q, want 'Bash'", entries[1].Content)
	}
	if !entries[1].Approved {
		t.Errorf("entries[1].Approved = false, want true")
	}
	if entries[1].ToolInput != "ls -la" {
		t.Errorf("entries[1].ToolInput = %q, want 'ls -la'", entries[1].ToolInput)
	}
	if entries[2].Type != MessageAssistant || entries[2].Content != "after" {
		t.Errorf("entries[2] = %+v", entries[2])
	}
	if entries[3].Type != MessageStatus {
		t.Errorf("entries[3].Type = %v, want MessageStatus", entries[3].Type)
	}
}

func TestLoadTranscript_SkipsThinkingBlocks(t *testing.T) {
	line := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"hmm"},` +
		`{"type":"text","text":"visible"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageAssistant || entries[0].Content != "visible" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
	if entries[1].Type != MessageStatus {
		t.Errorf("entries[1].Type = %v, want MessageStatus", entries[1].Type)
	}
}

func TestLoadTranscript_UserContentArray(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"hello"},` +
		`{"type":"tool_result","tool_use_id":"t1","content":"ignored"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (user + trailing status); entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "hello" {
		t.Errorf("entries[0] = %+v, want MessageUser 'hello'", entries[0])
	}
}

func TestLoadTranscript_SkipsMetadataTypes(t *testing.T) {
	lines := []string{
		`{"type":"custom-title","title":"x"}`,
		`{"type":"permission-mode","mode":"default"}`,
		`{"type":"system","subtype":"init"}`,
		`{"type":"summary","summary":"s"}`,
		`{"type":"last-prompt","prompt":"p"}`,
		`{"type":"file-history-snapshot","data":{}}`,
		`{"type":"attachment","path":"/x"}`,
		`{"type":"user","message":{"role":"user","content":"real"}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "real" {
		t.Errorf("entries[0] = %+v", entries[0])
	}
}

func TestLoadTranscript_SkipsSidechain(t *testing.T) {
	lines := []string{
		`{"type":"user","isSidechain":true,"message":{"role":"user","content":"sub-agent chatter"}}`,
		`{"type":"user","message":{"role":"user","content":"main"}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2; entries=%+v", len(entries), entries)
	}
	if entries[0].Content != "main" {
		t.Errorf("entries[0].Content = %q, want 'main'", entries[0].Content)
	}
}

func TestLoadTranscript_MalformedLinesIgnored(t *testing.T) {
	lines := []string{
		`not json at all`,
		`{"type":"user","message":{"role":"user","content":"one"}}`,
		`{broken json`,
		`{"type":"user","message":{"role":"user","content":"two"}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (2 users + trailing status); entries=%+v", len(entries), entries)
	}
	if entries[0].Content != "one" || entries[1].Content != "two" {
		t.Errorf("entries contents = %q, %q; want 'one', 'two'", entries[0].Content, entries[1].Content)
	}
}

func TestLoadTranscript_TruncationMarker(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"type":"user","message":{"role":"user","content":"msg`+string(rune('0'+i))+`"}}`)
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected: truncation marker + 5 users (msg5..msg9) + trailing status = 7
	if len(entries) != 7 {
		t.Fatalf("len(entries) = %d, want 7; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageStatus || entries[0].Content != "earlier messages truncated" {
		t.Errorf("entries[0] = %+v, want truncation marker", entries[0])
	}
	for i := 0; i < 5; i++ {
		want := "msg" + string(rune('0'+5+i))
		if entries[1+i].Type != MessageUser || entries[1+i].Content != want {
			t.Errorf("entries[%d] = %+v, want MessageUser %q", 1+i, entries[1+i], want)
		}
	}
	if entries[6].Type != MessageStatus || entries[6].Content != "Resumed from prior session" {
		t.Errorf("entries[6] = %+v, want trailing status", entries[6])
	}
}

func TestLoadTranscript_NoCapWhenMaxZero(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"type":"user","message":{"role":"user","content":"msg`+string(rune('0'+i))+`"}}`)
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 10 users + trailing status = 11, no truncation marker
	if len(entries) != 11 {
		t.Fatalf("len(entries) = %d, want 11; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "msg0" {
		t.Errorf("entries[0] = %+v, want MessageUser 'msg0' (no truncation marker)", entries[0])
	}
	if entries[10].Type != MessageStatus || entries[10].Content != "Resumed from prior session" {
		t.Errorf("entries[10] = %+v, want trailing status", entries[10])
	}
}

func TestLoadChildTranscript_NoTrailingResumedMarker(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-04-25T10:00:00Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"assistant","timestamp":"2026-04-25T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadChildTranscript(path, time.Time{}, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (user + assistant, no trailing marker); entries=%+v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Resumed from prior session") {
			t.Errorf("LoadChildTranscript should not emit 'Resumed from prior session' marker; got %+v", e)
		}
	}
	if entries[0].Type != MessageUser || entries[0].Content != "hello" {
		t.Errorf("entries[0] = %+v, want MessageUser 'hello'", entries[0])
	}
	if entries[1].Type != MessageAssistant || entries[1].Content != "hi" {
		t.Errorf("entries[1] = %+v, want MessageAssistant 'hi'", entries[1])
	}
}

func TestLoadChildTranscript_FiltersBySince(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-04-25T09:00:00Z","message":{"role":"user","content":"old"}}`,
		`{"type":"user","timestamp":"2026-04-25T11:00:00Z","message":{"role":"user","content":"new"}}`,
	}
	path := writeJSONL(t, lines)
	cutoff, _ := time.Parse(time.RFC3339, "2026-04-25T10:00:00Z")
	entries, err := LoadChildTranscript(path, cutoff, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (filtered); entries=%+v", len(entries), entries)
	}
	if entries[0].Content != "new" {
		t.Errorf("entries[0].Content = %q, want 'new' (older record should be filtered)", entries[0].Content)
	}
}

func TestLoadChildTranscript_ZeroSinceNoFilter(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-04-25T09:00:00Z","message":{"role":"user","content":"a"}}`,
		`{"type":"user","timestamp":"2026-04-25T11:00:00Z","message":{"role":"user","content":"b"}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadChildTranscript(path, time.Time{}, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2; entries=%+v", len(entries), entries)
	}
}

func TestLoadChildTranscript_MissingFile(t *testing.T) {
	entries, err := LoadChildTranscript(filepath.Join(t.TempDir(), "nope.jsonl"), time.Time{}, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %v, want nil", entries)
	}
}

func TestLoadChildTranscript_TruncationMarker(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"type":"user","timestamp":"2026-04-25T10:00:00Z","message":{"role":"user","content":"msg`+string(rune('0'+i))+`"}}`)
	}
	path := writeJSONL(t, lines)
	entries, err := LoadChildTranscript(path, time.Time{}, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expected: leading truncation marker + 5 user messages = 6 (no trailing marker)
	if len(entries) != 6 {
		t.Fatalf("len(entries) = %d, want 6; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageStatus || entries[0].Content != "earlier messages truncated" {
		t.Errorf("entries[0] = %+v, want truncation marker", entries[0])
	}
	last := entries[len(entries)-1]
	if last.Type == MessageStatus && strings.Contains(last.Content, "Resumed from prior session") {
		t.Errorf("LoadChildTranscript should not emit trailing 'Resumed' marker; got %+v", last)
	}
}

func TestLoadTranscript_NoEntriesSkipsMarkers(t *testing.T) {
	lines := []string{
		`{"type":"custom-title","title":"x"}`,
		`{"type":"system","subtype":"init"}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entries != nil {
		t.Errorf("entries = %+v, want nil (no markers when zero real records)", entries)
	}
}

// --- Tests for QUM-379: Agent nesting depth in transcript replay ---

// TestLoadTranscript_AgentNestingSetsDepth verifies that when an "Agent" tool_use
// appears in the transcript, subsequent tool_use entries get Depth 1, and after
// the corresponding tool_result, entries return to Depth 0.
func TestLoadTranscript_AgentNestingSetsDepth(t *testing.T) {
	lines := []string{
		// Assistant emits Agent tool_use and Bash tool_use.
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"a1","name":"Agent","input":{"prompt":"do stuff"}},` +
			`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"ls"}}` +
			`]}}`,
		// User turn with tool_result for the Agent call.
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"a1","content":"agent done"}` +
			`]}}`,
		// Assistant emits Read tool_use (should be Depth 0 now).
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"r1","name":"Read","input":{"path":"/tmp/x"}}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the Bash and Read entries.
	var bashEntry, readEntry *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].Content == "Bash" {
			bashEntry = &entries[i]
		}
		if entries[i].Type == MessageToolCall && entries[i].Content == "Read" {
			readEntry = &entries[i]
		}
	}
	if bashEntry == nil {
		t.Fatal("Bash tool call entry not found")
	}
	if readEntry == nil {
		t.Fatal("Read tool call entry not found")
	}
	if bashEntry.Depth != 1 {
		t.Errorf("Bash Depth = %d, want 1 (nested under Agent)", bashEntry.Depth)
	}
	if readEntry.Depth != 0 {
		t.Errorf("Read Depth = %d, want 0 (Agent result already received)", readEntry.Depth)
	}
}

// TestLoadTranscript_NestedAgentDepth2 verifies that two levels of Agent
// nesting in the transcript produce Depth 2 for the innermost tool calls.
func TestLoadTranscript_NestedAgentDepth2(t *testing.T) {
	lines := []string{
		// Two nested Agent tool_use blocks then a Bash.
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"a1","name":"Agent","input":{"prompt":"outer"}},` +
			`{"type":"tool_use","id":"a2","name":"Agent","input":{"prompt":"inner"}},` +
			`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"ls"}}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var bashEntry *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].Content == "Bash" {
			bashEntry = &entries[i]
			break
		}
	}
	if bashEntry == nil {
		t.Fatal("Bash tool call entry not found")
	}
	if bashEntry.Depth != 2 {
		t.Errorf("Bash Depth = %d, want 2 (nested under two Agents)", bashEntry.Depth)
	}
}

// --- Tests for QUM-388: Tool result patching in transcript replay ---

func TestLoadTranscript_ToolResultPatchesStringContent(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}` +
			`]}}`,
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"hello world"}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var toolEntry *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].ToolID == "t1" {
			toolEntry = &entries[i]
			break
		}
	}
	if toolEntry == nil {
		t.Fatal("tool call entry not found")
	}
	if toolEntry.Result != "hello world" {
		t.Errorf("Result = %q, want %q", toolEntry.Result, "hello world")
	}
	if toolEntry.Failed {
		t.Errorf("Failed = true, want false")
	}
}

func TestLoadTranscript_ToolResultPatchesArrayContent(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}` +
			`]}}`,
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var toolEntry *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].ToolID == "t1" {
			toolEntry = &entries[i]
			break
		}
	}
	if toolEntry == nil {
		t.Fatal("tool call entry not found")
	}
	want := "line1\nline2"
	if toolEntry.Result != want {
		t.Errorf("Result = %q, want %q", toolEntry.Result, want)
	}
}

func TestLoadTranscript_ToolResultIsError(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"false"}}` +
			`]}}`,
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"command failed","is_error":true}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var toolEntry *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].ToolID == "t1" {
			toolEntry = &entries[i]
			break
		}
	}
	if toolEntry == nil {
		t.Fatal("tool call entry not found")
	}
	if toolEntry.Result != "command failed" {
		t.Errorf("Result = %q, want %q", toolEntry.Result, "command failed")
	}
	if !toolEntry.Failed {
		t.Errorf("Failed = false, want true")
	}
}

func TestLoadTranscript_OrphanToolResultNoPanic(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"orphan","content":"whatever"}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No tool call entries should exist, and no panic should occur.
	for _, e := range entries {
		if e.Type == MessageToolCall && e.Result != "" {
			t.Errorf("unexpected non-empty Result on entry: %+v", e)
		}
	}
}

func TestLoadTranscript_MultipleToolCallsInterleavedResults(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo 1"}},` +
			`{"type":"tool_use","id":"t2","name":"Read","input":{"path":"/tmp/x"}}` +
			`]}}`,
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"t1","content":"result1"},` +
			`{"type":"tool_result","tool_use_id":"t2","content":"result2"}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var t1Entry, t2Entry *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall {
			switch entries[i].ToolID {
			case "t1":
				t1Entry = &entries[i]
			case "t2":
				t2Entry = &entries[i]
			}
		}
	}
	if t1Entry == nil {
		t.Fatal("t1 tool call entry not found")
	}
	if t2Entry == nil {
		t.Fatal("t2 tool call entry not found")
	}
	if t1Entry.Result != "result1" {
		t.Errorf("t1 Result = %q, want %q", t1Entry.Result, "result1")
	}
	if t2Entry.Result != "result2" {
		t.Errorf("t2 Result = %q, want %q", t2Entry.Result, "result2")
	}
}

func TestExtractToolResultContent(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"plain string", "hello", "hello"},
		{"empty string", "", ""},
		{"array of text blocks", []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "text", "text": "b"},
		}, "a\nb"},
		{"array with non-text blocks", []any{
			map[string]any{"type": "image", "data": "..."},
			map[string]any{"type": "text", "text": "visible"},
		}, "visible"},
		{"array with empty text", []any{
			map[string]any{"type": "text", "text": ""},
			map[string]any{"type": "text", "text": "ok"},
		}, "ok"},
		{"unexpected type", 42, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolResultContent(tt.in)
			if got != tt.want {
				t.Errorf("extractToolResultContent(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
