package tui

import (
	"encoding/json"
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
	// QUM-676: trailing "Resumed from prior session" MessageStatus marker
	// was dropped; the resume hint flows via the status-bar transient label.
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (user + assistant); entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "hello" {
		t.Errorf("entries[0] = %+v, want MessageUser 'hello'", entries[0])
	}
	if entries[1].Type != MessageAssistant || entries[1].Content != "hi there" {
		t.Errorf("entries[1] = %+v, want MessageAssistant 'hi there'", entries[1])
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
	// QUM-676: trailing status marker dropped.
	if len(entries) != 3 {
		t.Fatalf("len(entries) = %d, want 3 (3 blocks); entries=%+v", len(entries), entries)
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
	// QUM-676: trailing status marker dropped.
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageAssistant || entries[0].Content != "visible" {
		t.Errorf("entries[0] = %+v", entries[0])
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
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1 (user; QUM-676 dropped trailing status); entries=%+v", len(entries), entries)
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
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
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
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
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
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2 (users only; QUM-676); entries=%+v", len(entries), entries)
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
	// QUM-676: leading "earlier messages truncated" + trailing "Resumed
	// from prior session" markers dropped. Expected: 5 users (msg5..msg9).
	if len(entries) != 5 {
		t.Fatalf("len(entries) = %d, want 5; entries=%+v", len(entries), entries)
	}
	for i := 0; i < 5; i++ {
		want := "msg" + string(rune('0'+5+i))
		if entries[i].Type != MessageUser || entries[i].Content != want {
			t.Errorf("entries[%d] = %+v, want MessageUser %q", i, entries[i], want)
		}
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
	// QUM-676: 10 users, no markers.
	if len(entries) != 10 {
		t.Fatalf("len(entries) = %d, want 10; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageUser || entries[0].Content != "msg0" {
		t.Errorf("entries[0] = %+v, want MessageUser 'msg0' (no truncation marker)", entries[0])
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
	// QUM-676: leading "earlier messages truncated" marker dropped; expected
	// to receive exactly 5 user entries (msg5..msg9).
	if len(entries) != 5 {
		t.Fatalf("len(entries) = %d, want 5; entries=%+v", len(entries), entries)
	}
	for i := 0; i < 5; i++ {
		want := "msg" + string(rune('0'+5+i))
		if entries[i].Type != MessageUser || entries[i].Content != want {
			t.Errorf("entries[%d] = %+v, want MessageUser %q", i, entries[i], want)
		}
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

// TestLoadTranscript_AgentNestingSetsParentToolID verifies that nested
// tool_use entries synthesized purely from a replayed transcript carry
// ParentToolID pointing at the enclosing Agent tool_use's ID — not just
// Depth. Without this linkage, viewport reseeding from a fresh JSONL
// replay would render nested calls at the top level instead of under
// their parent Agent container. (QUM-481)
func TestLoadTranscript_AgentNestingSetsParentToolID(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"a1","name":"Agent","input":{"prompt":"do stuff"}},` +
			`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"ls"}}` +
			`]}}`,
		`{"type":"user","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"a1","content":"agent done"}` +
			`]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"r1","name":"Read","input":{"path":"/tmp/x"}}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var bashEntry, readEntry, agentEntry *MessageEntry
	for i := range entries {
		if entries[i].Type != MessageToolCall {
			continue
		}
		switch entries[i].Content {
		case "Agent":
			agentEntry = &entries[i]
		case "Bash":
			bashEntry = &entries[i]
		case "Read":
			readEntry = &entries[i]
		}
	}
	if agentEntry == nil || bashEntry == nil || readEntry == nil {
		t.Fatalf("missing entries; agent=%v bash=%v read=%v", agentEntry, bashEntry, readEntry)
	}
	if agentEntry.ParentToolID != "" {
		t.Errorf("Agent ParentToolID = %q, want empty (top-level)", agentEntry.ParentToolID)
	}
	if bashEntry.ParentToolID != "a1" {
		t.Errorf("Bash ParentToolID = %q, want %q (nested under Agent a1)", bashEntry.ParentToolID, "a1")
	}
	if readEntry.ParentToolID != "" {
		t.Errorf("Read ParentToolID = %q, want empty (Agent already closed)", readEntry.ParentToolID)
	}
	// Cross-check: every Depth>0 entry must have a ParentToolID.
	for i, e := range entries {
		if e.Depth > 0 && e.ParentToolID == "" {
			t.Errorf("entries[%d] has Depth=%d but empty ParentToolID: %+v", i, e.Depth, e)
		}
	}
}

// TestLoadTranscript_NestedAgentDepth2ParentToolID verifies that with two
// levels of Agent nesting, the innermost tool_use's ParentToolID points at
// the most recent (innermost) Agent — matching the live-path behavior
// where lastActiveAgent is the most recently pushed Agent ID. (QUM-481)
func TestLoadTranscript_NestedAgentDepth2ParentToolID(t *testing.T) {
	lines := []string{
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
		t.Fatal("Bash entry not found")
	}
	if bashEntry.Depth != 2 {
		t.Errorf("Bash Depth = %d, want 2", bashEntry.Depth)
	}
	if bashEntry.ParentToolID != "a2" {
		t.Errorf("Bash ParentToolID = %q, want %q (innermost Agent)", bashEntry.ParentToolID, "a2")
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

// --- QUM-557 / QUM-562: replay path must surface <system-notification>
//     content as MessageSystemNotification entries (not MessageUser),
//     preserving both the parsed `type` attribute (defaults to "message" for
//     untyped legacy tags) and the interrupt flag so the renderer can
//     color-code on resume/restart. Both branches (string-content and
//     array-block-content) MUST behave symmetrically — that's the QUM-557
//     lesson (silent replay divergence). ---

func TestScanTranscript_SystemNotification_LegacyUntypedAsyncStringContent(t *testing.T) {
	// Back-compat: pre-QUM-562 transcripts persisted without the type attribute.
	line := `{"type":"user","message":{"role":"user","content":"<system-notification>From finn — msg id=9v6</system-notification>"}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageSystemNotification {
		t.Errorf("entries[0].Type = %v, want MessageSystemNotification", entries[0].Type)
	}
	if entries[0].NotificationType != NotificationKindMessage {
		t.Errorf("entries[0].NotificationType = %q, want %q (legacy defaults to message)", entries[0].NotificationType, NotificationKindMessage)
	}
	if entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = true, want false (async)")
	}
	if entries[0].Content != "From finn — msg id=9v6" {
		t.Errorf("entries[0].Content = %q, want %q (tags stripped)", entries[0].Content, "From finn — msg id=9v6")
	}
}

func TestScanTranscript_SystemNotification_LegacyUntypedInterruptStringContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":"<system-notification>[interrupt] From finn — msg id=9v6</system-notification>"}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageSystemNotification {
		t.Errorf("entries[0].Type = %v, want MessageSystemNotification", entries[0].Type)
	}
	if entries[0].NotificationType != NotificationKindMessage {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindMessage)
	}
	if !entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = false, want true ([interrupt] body)")
	}
	if entries[0].Content != "[interrupt] From finn — msg id=9v6" {
		t.Errorf("entries[0].Content = %q, want marker preserved", entries[0].Content)
	}
}

func TestScanTranscript_SystemNotification_TypedMessageStringContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":"<system-notification type=\"message\">From finn — msg id=9v6</system-notification>"}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].NotificationType != NotificationKindMessage {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindMessage)
	}
	if entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = true, want false")
	}
}

func TestScanTranscript_SystemNotification_TypedMessageInterruptStringContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":"<system-notification type=\"message\" interrupt=\"true\">[interrupt] From finn — msg id=9v6</system-notification>"}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].NotificationType != NotificationKindMessage {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindMessage)
	}
	if !entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = false, want true (interrupt=\"true\" attr)")
	}
}

func TestScanTranscript_SystemNotification_TypedStatusChangeStringContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":"<system-notification type=\"status_change\">finn changed status to working: doing X</system-notification>"}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].Type != MessageSystemNotification {
		t.Errorf("entries[0].Type = %v, want MessageSystemNotification", entries[0].Type)
	}
	if entries[0].NotificationType != NotificationKindStatusChange {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindStatusChange)
	}
	if entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = true, want false")
	}
	if entries[0].Content != "finn changed status to working: doing X" {
		t.Errorf("entries[0].Content = %q", entries[0].Content)
	}
}

func TestScanTranscript_SystemNotification_LegacyUntypedArrayBlockContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"<system-notification>x</system-notification>"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageSystemNotification {
		t.Errorf("entries[0].Type = %v, want MessageSystemNotification", entries[0].Type)
	}
	if entries[0].NotificationType != NotificationKindMessage {
		t.Errorf("entries[0].NotificationType = %q, want %q (legacy defaults)", entries[0].NotificationType, NotificationKindMessage)
	}
	if entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = true, want false")
	}
	if entries[0].Content != "x" {
		t.Errorf("entries[0].Content = %q, want %q", entries[0].Content, "x")
	}
}

// TestScanTranscript_SystemNotification_TypedStatusChangeArrayBlockContent —
// symmetry guard: the array-block branch (~replay.go:216) MUST match the
// string-content branch (~replay.go:163) when parsing the new typed form.
// QUM-557 lesson: replay divergence is real and silent.
func TestScanTranscript_SystemNotification_TypedStatusChangeArrayBlockContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"<system-notification type=\"status_change\">finn changed status to working: doing X</system-notification>"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageSystemNotification {
		t.Errorf("entries[0].Type = %v, want MessageSystemNotification", entries[0].Type)
	}
	if entries[0].NotificationType != NotificationKindStatusChange {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindStatusChange)
	}
}

// TestScanTranscript_SystemNotification_TypedMessageInterruptArrayBlockContent
// — symmetric coverage for interrupt-attr handling in the array-block branch.
func TestScanTranscript_SystemNotification_TypedMessageInterruptArrayBlockContent(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"<system-notification type=\"message\" interrupt=\"true\">[interrupt] body</system-notification>"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	if entries[0].NotificationType != NotificationKindMessage {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindMessage)
	}
	if !entries[0].Interrupt {
		t.Errorf("entries[0].Interrupt = false, want true")
	}
}

// TestScanTranscript_SystemNotification_BackToBackTwoEnvelopesStringContent —
// QUM-574 regression guard for the replay string-content branch. A single
// user message whose `content` is two consecutive `<system-notification>`
// envelopes (status_change + message) MUST replay as TWO distinct
// MessageSystemNotification entries with their respective types, not one
// blob with raw tags leaking into Content.
func TestScanTranscript_SystemNotification_BackToBackTwoEnvelopesStringContent(t *testing.T) {
	inner := `<system-notification type=\"status_change\">tower changed status to complete: phase 2 done</system-notification>` +
		`<system-notification type=\"message\">From tower — hello</system-notification>`
	line := `{"type":"user","message":{"role":"user","content":"` + inner + `"}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2; entries=%+v", len(entries), entries)
	}
	if entries[0].NotificationType != NotificationKindStatusChange {
		t.Errorf("entries[0].NotificationType = %q, want %q", entries[0].NotificationType, NotificationKindStatusChange)
	}
	if entries[0].Content != "tower changed status to complete: phase 2 done" {
		t.Errorf("entries[0].Content = %q (should NOT contain raw tags)", entries[0].Content)
	}
	if entries[1].NotificationType != NotificationKindMessage {
		t.Errorf("entries[1].NotificationType = %q, want %q", entries[1].NotificationType, NotificationKindMessage)
	}
	if entries[1].Content != "From tower — hello" {
		t.Errorf("entries[1].Content = %q", entries[1].Content)
	}
	for i, e := range entries {
		if strings.Contains(e.Content, "<system-notification") || strings.Contains(e.Content, "</system-notification>") {
			t.Errorf("entries[%d].Content leaks raw tag: %q", i, e.Content)
		}
	}
}

// TestScanTranscript_SystemNotification_BackToBackTwoEnvelopesArrayBlockContent
// — symmetry guard for the array-block branch (QUM-574). MUST match the
// string-content behavior above.
func TestScanTranscript_SystemNotification_BackToBackTwoEnvelopesArrayBlockContent(t *testing.T) {
	innerText := `<system-notification type=\"status_change\">A</system-notification>` +
		`<system-notification type=\"message\">B</system-notification>`
	line := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"text","text":"` + innerText + `"}` +
		`]}}`
	path := writeJSONL(t, []string{line})
	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2; entries=%+v", len(entries), entries)
	}
	if entries[0].NotificationType != NotificationKindStatusChange || entries[0].Content != "A" {
		t.Errorf("entries[0] = (%q, %q), want (status_change, A)", entries[0].NotificationType, entries[0].Content)
	}
	if entries[1].NotificationType != NotificationKindMessage || entries[1].Content != "B" {
		t.Errorf("entries[1] = (%q, %q), want (message, B)", entries[1].NotificationType, entries[1].Content)
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

// --- QUM-577: sidechain (sub-agent) records must be replayed for child viewport ---
//
// Claude Code emits inner Agent-tool activity (the Read/Bash/etc calls a
// sub-agent makes) as JSONL records with isSidechain=true and
// parent_tool_use_id pointing at the outer Agent tool_use. The current
// scanTranscript filter at internal/tui/replay.go:136-138 strips all such
// records, so Ctrl+N hydration of a child viewport shows the outer Agent
// entry but no inner activity. These tests assert the new behavior: sidechain
// records nested under an outer Agent are surfaced as MessageToolCall entries
// with Depth=1 and the correct ParentToolID, ordering is preserved, and the
// existing QUM-331 timestamp filter still discards stale records.

// TestLoadChildTranscript_IncludesSidechainSubAgentActivity verifies that
// inner sub-agent tool_use + tool_result records (isSidechain=true) are
// included in the replayed entries, nested under the outer Agent call.
func TestLoadChildTranscript_IncludesSidechainSubAgentActivity(t *testing.T) {
	lines := []string{
		// Outer Agent tool_use (top-level, not sidechain).
		`{"type":"assistant","timestamp":"2026-04-25T10:00:00Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"A1","name":"Agent","input":{"subagent_type":"Explore"}}` +
			`]}}`,
		// Inner sub-agent Read tool_use — emitted as sidechain by Claude Code.
		`{"type":"assistant","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T10:00:01Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"R1","name":"Read","input":{"file_path":"/tmp/foo"}}` +
			`]}}`,
		// Inner sub-agent tool_result for Read — also sidechain.
		`{"type":"user","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T10:00:02Z","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"R1","content":"contents","is_error":false}` +
			`]}}`,
		// Outer Agent tool_result — closes the agentStack frame.
		`{"type":"user","timestamp":"2026-04-25T10:00:03Z","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"A1","content":"agent done","is_error":false}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadChildTranscript(path, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var agentEntry, readEntry *MessageEntry
	for i := range entries {
		if entries[i].Type != MessageToolCall {
			continue
		}
		switch entries[i].ToolID {
		case "A1":
			agentEntry = &entries[i]
		case "R1":
			readEntry = &entries[i]
		}
	}
	if agentEntry == nil {
		t.Fatalf("Agent (A1) tool_use entry not found in entries=%+v", entries)
	}
	if readEntry == nil {
		t.Fatalf("Read (R1) sub-agent tool_use entry not found — sidechain filter is dropping inner activity; entries=%+v", entries)
	}
	if readEntry.Content != "Read" {
		t.Errorf("readEntry.Content = %q, want %q", readEntry.Content, "Read")
	}
	if readEntry.Depth != 1 {
		t.Errorf("readEntry.Depth = %d, want 1 (nested under outer Agent A1)", readEntry.Depth)
	}
	if readEntry.ParentToolID != "A1" {
		t.Errorf("readEntry.ParentToolID = %q, want %q (outer Agent)", readEntry.ParentToolID, "A1")
	}
	if readEntry.Result != "contents" {
		t.Errorf("readEntry.Result = %q, want %q (tool_result content should patch onto the tool call entry, QUM-388)", readEntry.Result, "contents")
	}
}

// TestLoadChildTranscript_SidechainNestedUnderAgent_PreservesOrdering
// verifies that when sidechain inner activity is included, it appears AFTER
// the outer Agent entry in the returned slice (JSONL order preserved).
func TestLoadChildTranscript_SidechainNestedUnderAgent_PreservesOrdering(t *testing.T) {
	lines := []string{
		`{"type":"assistant","timestamp":"2026-04-25T10:00:00Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"A1","name":"Agent","input":{"subagent_type":"Explore"}}` +
			`]}}`,
		`{"type":"assistant","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T10:00:01Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"R1","name":"Read","input":{"file_path":"/tmp/foo"}}` +
			`]}}`,
		`{"type":"user","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T10:00:02Z","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"R1","content":"contents","is_error":false}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadChildTranscript(path, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	agentIdx, readIdx := -1, -1
	for i := range entries {
		if entries[i].Type != MessageToolCall {
			continue
		}
		switch entries[i].ToolID {
		case "A1":
			agentIdx = i
		case "R1":
			readIdx = i
		}
	}
	if agentIdx == -1 {
		t.Fatalf("Agent (A1) entry not found in entries=%+v", entries)
	}
	if readIdx == -1 {
		t.Fatalf("Read (R1) sidechain entry not found in entries=%+v", entries)
	}
	if agentIdx >= readIdx {
		t.Errorf("ordering wrong: agentIdx=%d readIdx=%d, want agent before read", agentIdx, readIdx)
	}
}

// TestLoadChildTranscript_SidechainSinceFilterStillApplies verifies that the
// QUM-331 timestamp guard still filters out sidechain records whose
// timestamp predates `since` — the new sidechain inclusion must not regress
// the prior-incarnation pollution guard.
func TestLoadChildTranscript_SidechainSinceFilterStillApplies(t *testing.T) {
	lines := []string{
		// Outer Agent before cutoff — should be filtered too.
		`{"type":"assistant","timestamp":"2026-04-25T09:00:00Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"A0","name":"Agent","input":{"subagent_type":"Explore"}}` +
			`]}}`,
		// Stale sidechain record predates the cutoff — must be dropped.
		`{"type":"assistant","isSidechain":true,"parent_tool_use_id":"A0","timestamp":"2026-04-25T09:00:01Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"R0","name":"Read","input":{"file_path":"/tmp/stale"}}` +
			`]}}`,
		// Fresh outer Agent after cutoff.
		`{"type":"assistant","timestamp":"2026-04-25T11:00:00Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"A1","name":"Agent","input":{"subagent_type":"Explore"}}` +
			`]}}`,
		// Fresh sidechain Read after cutoff — must be included.
		`{"type":"assistant","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T11:00:01Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"R1","name":"Read","input":{"file_path":"/tmp/fresh"}}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	cutoff, _ := time.Parse(time.RFC3339, "2026-04-25T10:00:00Z")
	entries, err := LoadChildTranscript(path, cutoff, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, e := range entries {
		if e.Type != MessageToolCall {
			continue
		}
		if e.ToolID == "R0" || e.ToolID == "A0" {
			t.Errorf("stale pre-cutoff entry %q leaked through since-filter: %+v", e.ToolID, e)
		}
	}

	// Sanity: fresh sidechain Read entry must be present and depth-nested
	// under its outer Agent (the fresh A1 frame).
	var freshRead *MessageEntry
	for i := range entries {
		if entries[i].Type == MessageToolCall && entries[i].ToolID == "R1" {
			freshRead = &entries[i]
			break
		}
	}
	if freshRead == nil {
		t.Errorf("fresh sidechain Read (R1) not found in entries=%+v; sidechain inclusion may not be working", entries)
		return
	}
	if freshRead.Depth != 1 {
		t.Errorf("freshRead.Depth = %d, want 1 (nested under outer Agent A1)", freshRead.Depth)
	}
}

// TestLoadChildTranscript_SidechainParallelAgents_ParentToolIDFromWire
// verifies that when two outer Agent tool_use calls are emitted before
// either's sidechain children arrive (parallel sub-agents), the inner
// records are attributed to the correct outer Agent via the wire-level
// `parent_tool_use_id` field — NOT a "last active agent" heuristic. A
// stack-based heuristic would attribute both inner records to A2 (the most
// recent agent pushed); this test forces the implementation to read
// parent_tool_use_id from the JSONL record itself.
func TestLoadChildTranscript_SidechainParallelAgents_ParentToolIDFromWire(t *testing.T) {
	lines := []string{
		// Two outer Agent tool_uses queued before either child arrives.
		`{"type":"assistant","timestamp":"2026-04-25T10:00:00Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"A1","name":"Agent","input":{"subagent_type":"Explore"}}` +
			`]}}`,
		`{"type":"assistant","timestamp":"2026-04-25T10:00:01Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"A2","name":"Agent","input":{"subagent_type":"Explore"}}` +
			`]}}`,
		// Inner Read belongs to A1, but A2 is the most-recently-pushed agent.
		`{"type":"assistant","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T10:00:02Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"R1","name":"Read","input":{"file_path":"/tmp/foo"}}` +
			`]}}`,
		// Inner Bash belongs to A2.
		`{"type":"assistant","isSidechain":true,"parent_tool_use_id":"A2","timestamp":"2026-04-25T10:00:03Z","message":{"role":"assistant","content":[` +
			`{"type":"tool_use","id":"B2","name":"Bash","input":{"command":"ls"}}` +
			`]}}`,
		// tool_results for each.
		`{"type":"user","isSidechain":true,"parent_tool_use_id":"A1","timestamp":"2026-04-25T10:00:04Z","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"R1","content":"r1","is_error":false}` +
			`]}}`,
		`{"type":"user","isSidechain":true,"parent_tool_use_id":"A2","timestamp":"2026-04-25T10:00:05Z","message":{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"B2","content":"b2","is_error":false}` +
			`]}}`,
	}
	path := writeJSONL(t, lines)
	entries, err := LoadChildTranscript(path, time.Time{}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var readEntry, bashEntry *MessageEntry
	for i := range entries {
		if entries[i].Type != MessageToolCall {
			continue
		}
		switch entries[i].ToolID {
		case "R1":
			readEntry = &entries[i]
		case "B2":
			bashEntry = &entries[i]
		}
	}
	if readEntry == nil {
		t.Fatalf("Read (R1) sidechain entry not found in entries=%+v", entries)
	}
	if bashEntry == nil {
		t.Fatalf("Bash (B2) sidechain entry not found in entries=%+v", entries)
	}
	if readEntry.ParentToolID != "A1" {
		t.Errorf("readEntry.ParentToolID = %q, want %q (must read parent_tool_use_id from wire, not infer from lastActiveAgent stack)", readEntry.ParentToolID, "A1")
	}
	if bashEntry.ParentToolID != "A2" {
		t.Errorf("bashEntry.ParentToolID = %q, want %q (must read parent_tool_use_id from wire, not infer from lastActiveAgent stack)", bashEntry.ParentToolID, "A2")
	}
}

// TestReplay_TaskNotificationRendersAutoTrigger (QUM-634): on resume the JSONL
// records the autonomous-turn trigger as a `type:user` record whose string
// message.content is a `<task-notification>...</task-notification>` wrapper
// (origin.kind:"task-notification"). The replay user-string branch must detect
// this wrapper and emit a MessageAutoTrigger entry carrying just the <summary>
// text — NOT a plain MessageUser bubble leaking the raw XML.
func TestReplay_TaskNotificationRendersAutoTrigger(t *testing.T) {
	const summary = `Background command "Background sleep 30 for autonomous-turn smoke" completed (exit code 0)`
	wrapper := "<task-notification>\n" +
		"<task-id>b806ldj16</task-id>\n" +
		"<tool-use-id>toolu_014DaFwB6wR4DxoPkc2mWygJ</tool-use-id>\n" +
		"<output-file>/tmp/tasks/b806ldj16.output</output-file>\n" +
		"<status>completed</status>\n" +
		"<summary>" + summary + "</summary>\n" +
		"</task-notification>"
	rec := map[string]any{
		"type":   "user",
		"origin": map[string]any{"kind": "task-notification"},
		"message": map[string]any{
			"role":    "user",
			"content": wrapper,
		},
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	path := writeJSONL(t, []string{string(b)})

	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1; entries=%+v", len(entries), entries)
	}
	if entries[0].Type != MessageAutoTrigger {
		t.Errorf("entries[0].Type = %v, want MessageAutoTrigger", entries[0].Type)
	}
	if entries[0].Content != summary {
		t.Errorf("entries[0].Content = %q, want the summary text %q (not the raw XML)", entries[0].Content, summary)
	}
	for _, e := range entries {
		if e.Type == MessageUser {
			t.Errorf("a raw <task-notification> wrapper must NOT replay as a MessageUser bubble, got: %+v", e)
		}
		if strings.Contains(e.Content, "<task-notification>") || strings.Contains(e.Content, "<summary>") {
			t.Errorf("entry content must not leak raw task-notification XML, got: %q", e.Content)
		}
	}
}

// TestReplay_AutoTriggerPrecedesAssistantOnResume (QUM-634): pins RESUME
// ORDERING. When the JSONL records an autonomous-turn trigger
// (`type:user` task-notification wrapper) immediately followed by the
// `type:assistant` response, replay must emit the MessageAutoTrigger marker
// BEFORE the MessageAssistant entry — i.e. the marker introduces the turn it
// triggered, never trails it. Also re-asserts no MessageUser bubble leaks the
// raw XML.
func TestReplay_AutoTriggerPrecedesAssistantOnResume(t *testing.T) {
	const summary = `Background command "Background sleep 30 for autonomous-turn smoke" completed (exit code 0)`
	const assistantText = "Acknowledged — the background task finished, continuing."
	wrapper := "<task-notification>\n" +
		"<task-id>b806ldj16</task-id>\n" +
		"<tool-use-id>toolu_014DaFwB6wR4DxoPkc2mWygJ</tool-use-id>\n" +
		"<output-file>/tmp/tasks/b806ldj16.output</output-file>\n" +
		"<status>completed</status>\n" +
		"<summary>" + summary + "</summary>\n" +
		"</task-notification>"
	userRec := map[string]any{
		"type":   "user",
		"origin": map[string]any{"kind": "task-notification"},
		"message": map[string]any{
			"role":    "user",
			"content": wrapper,
		},
	}
	ub, err := json.Marshal(userRec)
	if err != nil {
		t.Fatal(err)
	}
	assistantLine := `{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"` + assistantText + `"}]}}`

	path := writeJSONL(t, []string{string(ub), assistantLine})

	entries, err := scanTranscript(path, time.Time{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	triggerIdx, assistantIdx := -1, -1
	for i := range entries {
		switch entries[i].Type {
		case MessageAutoTrigger:
			if entries[i].Content != summary {
				t.Errorf("MessageAutoTrigger.Content = %q, want summary %q", entries[i].Content, summary)
			}
			triggerIdx = i
		case MessageAssistant:
			if entries[i].Content == assistantText {
				assistantIdx = i
			}
		case MessageUser:
			t.Errorf("a raw <task-notification> wrapper must NOT replay as a MessageUser bubble, got: %+v", entries[i])
		}
		if strings.Contains(entries[i].Content, "<task-notification>") || strings.Contains(entries[i].Content, "<summary>") {
			t.Errorf("entry content must not leak raw task-notification XML, got: %q", entries[i].Content)
		}
	}
	if triggerIdx == -1 {
		t.Fatalf("no MessageAutoTrigger entry found; entries=%+v", entries)
	}
	if assistantIdx == -1 {
		t.Fatalf("no MessageAssistant entry with the resume text found; entries=%+v", entries)
	}
	if triggerIdx >= assistantIdx {
		t.Errorf("MessageAutoTrigger at index %d must precede MessageAssistant at index %d (the marker introduces the turn it triggered)", triggerIdx, assistantIdx)
	}
}
