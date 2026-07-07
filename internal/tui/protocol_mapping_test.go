package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
)

// --- Initialize tests ---

// --- SendMessage tests ---

// --- WaitForEvent tests ---

// TestBridge_WaitForEvent_EOF_WrapsIoEOF_ForErrorsIs locks in the contract that
// the app's auto-restart EOF branch relies on: when the session events channel
// closes, SessionErrorMsg.Err must be identifiable via errors.Is(err, io.EOF).
// If bridge.go later wraps the EOF, this test forces the wrapper to preserve
// the chain (fmt.Errorf with %w).

// --- MapProtocolMessage tests ---

func TestMapProtocolMessage_AssistantWithText(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	// QUM-386: all assistant messages now return AssistantContentMsg.
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 1 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 1", len(acm.Msgs))
	}
	textMsg, ok := acm.Msgs[0].(AssistantTextMsg)
	if !ok {
		t.Fatalf("Msgs[0] is %T, want AssistantTextMsg", acm.Msgs[0])
	}
	if textMsg.Text != "Hello" {
		t.Errorf("Text = %q, want %q", textMsg.Text, "Hello")
	}
}

func TestMapProtocolMessage_AssistantWithToolUse(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-1","name":"Read","input":{}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	// QUM-386: all assistant messages now return AssistantContentMsg.
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 1 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 1", len(acm.Msgs))
	}
	toolMsg, ok := acm.Msgs[0].(ToolCallMsg)
	if !ok {
		t.Fatalf("Msgs[0] is %T, want ToolCallMsg", acm.Msgs[0])
	}
	if toolMsg.ToolName != "Read" {
		t.Errorf("ToolName = %q, want %q", toolMsg.ToolName, "Read")
	}
	if toolMsg.ToolID != "t-1" {
		t.Errorf("ToolID = %q, want %q", toolMsg.ToolID, "t-1")
	}
}

func TestMapProtocolMessage_AssistantWithMixedContent(t *testing.T) {
	// QUM-386: when content has both text and tool_use, ALL blocks are
	// returned in AssistantContentMsg (not just the first).
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"t-1","name":"Bash","input":{}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 2 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 2", len(acm.Msgs))
	}
	textMsg, ok := acm.Msgs[0].(AssistantTextMsg)
	if !ok {
		t.Fatalf("Msgs[0] is %T, want AssistantTextMsg", acm.Msgs[0])
	}
	if textMsg.Text != "Let me check" {
		t.Errorf("Text = %q, want %q", textMsg.Text, "Let me check")
	}
	if _, ok := acm.Msgs[1].(ToolCallMsg); !ok {
		t.Errorf("Msgs[1] is %T, want ToolCallMsg", acm.Msgs[1])
	}
}

func TestMapProtocolMessage_Result(t *testing.T) {
	raw := `{"type":"result","subtype":"success","is_error":false,"duration_ms":100,"num_turns":2,"total_cost_usd":0.03}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	resultMsg, ok := result.(SessionResultMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want SessionResultMsg", result)
	}
	if resultMsg.DurationMs != 100 {
		t.Errorf("DurationMs = %d, want 100", resultMsg.DurationMs)
	}
	if resultMsg.NumTurns != 2 {
		t.Errorf("NumTurns = %d, want 2", resultMsg.NumTurns)
	}
	if resultMsg.TotalCostUsd != 0.03 {
		t.Errorf("TotalCostUsd = %f, want 0.03", resultMsg.TotalCostUsd)
	}
}

func TestMapProtocolMessage_ResultWithError(t *testing.T) {
	raw := `{"type":"result","subtype":"error","is_error":true,"result":"something went wrong","duration_ms":50,"num_turns":1,"total_cost_usd":0.01}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	resultMsg, ok := result.(SessionResultMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want SessionResultMsg", result)
	}
	if !resultMsg.IsError {
		t.Error("IsError = false, want true")
	}
	if resultMsg.Result != "something went wrong" {
		t.Errorf("Result = %q, want %q", resultMsg.Result, "something went wrong")
	}
}

func TestMapProtocolMessage_AssistantWithOnlyThinking(t *testing.T) {
	// QUM-677 S7 pivot: every thinking block (even empty-bodied ones, which
	// is the realistic Claude/Opus case) produces a ThinkingMsg. The Text
	// field is no longer load-bearing — what matters is the count of
	// ThinkingMsgs emitted.
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":""}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 1 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 1", len(acm.Msgs))
	}
	if _, ok := acm.Msgs[0].(ThinkingMsg); !ok {
		t.Fatalf("Msgs[0] is %T, want ThinkingMsg", acm.Msgs[0])
	}
}

func TestMapProtocolMessage_AssistantThinkingThenText(t *testing.T) {
	// QUM-677 S7 pivot: a mixed thinking+text message must still emit a
	// ThinkingMsg followed by an AssistantTextMsg, in order.
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"text","text":"Here is the answer."}` +
		`]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 2 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 2", len(acm.Msgs))
	}
	if _, ok := acm.Msgs[0].(ThinkingMsg); !ok {
		t.Errorf("Msgs[0] = %T, want ThinkingMsg", acm.Msgs[0])
	}
	if atm, ok := acm.Msgs[1].(AssistantTextMsg); !ok || atm.Text != "Here is the answer." {
		t.Errorf("Msgs[1] = %#v, want AssistantTextMsg{Text:\"Here is the answer.\"}", acm.Msgs[1])
	}
}

func TestMapProtocolMessage_AssistantWithEmptyThinkingNotSkipped(t *testing.T) {
	// QUM-677 S7 pivot: empty thinking bodies are NOT skipped — the marker
	// is count-based and the empty case is the realistic Claude/Opus shape
	// (redacted server-side).
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"thinking","thinking":""},` +
		`{"type":"text","text":"hi"}` +
		`]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)
	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("got %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 3 {
		t.Fatalf("len(Msgs) = %d, want 3 (two ThinkingMsgs + one AssistantTextMsg)", len(acm.Msgs))
	}
	if _, ok := acm.Msgs[0].(ThinkingMsg); !ok {
		t.Errorf("Msgs[0] = %T, want ThinkingMsg", acm.Msgs[0])
	}
	if _, ok := acm.Msgs[1].(ThinkingMsg); !ok {
		t.Errorf("Msgs[1] = %T, want ThinkingMsg", acm.Msgs[1])
	}
	if _, ok := acm.Msgs[2].(AssistantTextMsg); !ok {
		t.Errorf("Msgs[2] = %T, want AssistantTextMsg", acm.Msgs[2])
	}
}

func TestMapProtocolMessage_ResultWithResultText(t *testing.T) {
	// Result messages should preserve the Result field text
	raw := `{"type":"result","subtype":"success","is_error":false,"result":"\n\npong","duration_ms":100,"num_turns":1,"total_cost_usd":0.03}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	resultMsg, ok := result.(SessionResultMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want SessionResultMsg", result)
	}
	if resultMsg.Result != "\n\npong" {
		t.Errorf("Result = %q, want %q", resultMsg.Result, "\n\npong")
	}
}

func TestMapProtocolMessage_UnknownType(t *testing.T) {
	raw := `{"type":"unknown_thing","data":"foo"}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	// Unknown types should return nil (ignored)
	if result != nil {
		t.Errorf("mapProtocolMessage for unknown type returned %T, want nil", result)
	}
}

func TestMapProtocolMessage_SystemInit_EmitsModel(t *testing.T) {
	// QUM-385: system/init now emits SessionModelMsg so the TUI can derive
	// the context window limit from the model name.
	raw := `{"type":"system","subtype":"init","session_id":"s-1","cwd":"/tmp","tools":["Bash"],"model":"claude-opus-4-7-20260301","permissionMode":"auto"}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	modelMsg, ok := result.(SessionModelMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage for system/init returned %T, want SessionModelMsg", result)
	}
	if modelMsg.Model != "claude-opus-4-7-20260301" {
		t.Errorf("Model = %q, want %q", modelMsg.Model, "claude-opus-4-7-20260301")
	}
}

func TestMapProtocolMessage_SystemInit_EmptyModel(t *testing.T) {
	// system/init with empty model should return nil (no SessionModelMsg).
	raw := `{"type":"system","subtype":"init","session_id":"s-1","cwd":"/tmp","tools":["Bash"],"model":"","permissionMode":"auto"}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	if result != nil {
		t.Errorf("mapProtocolMessage for system/init with empty model returned %T, want nil", result)
	}
}

// TestMapProtocolMessage_SystemTaskNotification (QUM-634 / QUM-857): a
// system/task_notification frame carrying a non-empty summary maps to an
// AutoContinueMsg so the TUI renders a trigger marker for the autonomous turn
// the harness self-reprompted into. QUM-857: the presence of a summary is the
// gate, but the summary body is no longer propagated into the marker.
func TestMapProtocolMessage_SystemTaskNotification(t *testing.T) {
	raw := `{"type":"system","subtype":"task_notification","task_id":"bzgr4iuq0","status":"completed","summary":"Background command \"sleep 30\" completed (exit code 0)"}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	if _, ok := result.(AutoContinueMsg); !ok {
		t.Fatalf("MapProtocolMessage for system/task_notification returned %T, want AutoContinueMsg", result)
	}
}

// TestMapProtocolMessage_SystemTaskNotification_EmptySummary (QUM-634): a
// task_notification with no summary carries nothing renderable, so it maps to
// nil (no marker emitted).
func TestMapProtocolMessage_SystemTaskNotification_EmptySummary(t *testing.T) {
	raw := `{"type":"system","subtype":"task_notification","task_id":"bzgr4iuq0","status":"completed","summary":""}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	if result := MapProtocolMessage(&msg); result != nil {
		t.Errorf("MapProtocolMessage for task_notification with empty summary returned %T, want nil", result)
	}
}

// TestMapProtocolMessage_SystemTaskOtherSubtypes (QUM-634): sibling task_*
// subtypes (task_updated, task_started) are noisy and must NOT map to an
// AutoContinueMsg — only task_notification triggers a marker.
func TestMapProtocolMessage_SystemTaskOtherSubtypes(t *testing.T) {
	for _, sub := range []string{"task_updated", "task_started"} {
		t.Run(sub, func(t *testing.T) {
			raw := `{"type":"system","subtype":"` + sub + `","task_id":"bzgr4iuq0","status":"in_progress","summary":"Background command running"}`
			var msg protocol.Message
			if err := json.Unmarshal([]byte(raw), &msg); err != nil {
				t.Fatal(err)
			}
			msg.Raw = json.RawMessage(raw)

			if result := MapProtocolMessage(&msg); result != nil {
				t.Errorf("MapProtocolMessage for system/%s returned %T, want nil (only task_notification renders a marker)", sub, result)
			}
		})
	}
}

func TestMapProtocolMessage_SystemNonInit(t *testing.T) {
	// Non-init system messages should still return nil.
	raw := `{"type":"system","subtype":"session_state_changed","state":"idle"}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	if result != nil {
		t.Errorf("mapProtocolMessage for system/session_state_changed returned %T, want nil", result)
	}
}

// --- Tool input summary tests ---

func TestSummarizeToolInput_Bash(t *testing.T) {
	input := []byte(`{"command":"ls -la /tmp"}`)
	result := summarizeToolInput("Bash", input)
	if result != "ls -la /tmp" {
		t.Errorf("summarizeToolInput(Bash) = %q, want %q", result, "ls -la /tmp")
	}
}

func TestSummarizeToolInput_Read(t *testing.T) {
	input := []byte(`{"file_path":"/home/user/main.go"}`)
	result := summarizeToolInput("Read", input)
	if result != "/home/user/main.go" {
		t.Errorf("summarizeToolInput(Read) = %q, want %q", result, "/home/user/main.go")
	}
}

func TestSummarizeToolInput_Edit(t *testing.T) {
	input := []byte(`{"file_path":"/home/user/main.go","old_string":"foo","new_string":"bar"}`)
	result := summarizeToolInput("Edit", input)
	if result != "/home/user/main.go" {
		t.Errorf("summarizeToolInput(Edit) = %q, want %q", result, "/home/user/main.go")
	}
}

func TestSummarizeToolInput_Unknown(t *testing.T) {
	input := []byte(`{"key":"value"}`)
	result := summarizeToolInput("CustomTool", input)
	if result == "" {
		t.Error("summarizeToolInput for unknown tool should return JSON summary, got empty")
	}
}

func TestSummarizeToolInput_EmptyInput(t *testing.T) {
	result := summarizeToolInput("Bash", nil)
	if result != "" {
		t.Errorf("summarizeToolInput with nil input = %q, want empty", result)
	}
}

func TestTruncateString(t *testing.T) {
	short := "hello"
	if truncateString(short, 10) != "hello" {
		t.Errorf("truncateString short = %q, want %q", truncateString(short, 10), "hello")
	}
	long := strings.Repeat("x", 200)
	result := truncateString(long, 50)
	if len(result) != 50 {
		t.Errorf("truncateString long len = %d, want 50", len(result))
	}
	if !strings.HasSuffix(result, "...") {
		t.Errorf("truncateString long should end with '...', got %q", result)
	}
}

// QUM-335: expandToolInput returns the verbatim Bash command (no
// truncation, newlines preserved) so multi-line one-liners are visible
// when the user toggles the expand flag.
func TestExpandToolInput_BashVerbatim(t *testing.T) {
	cmd := "find /var/log -type f -name '*.log' -mtime -7 \\\n  -size +1M | sort | uniq -c"
	raw := []byte(`{"command":` + mustJSONString(cmd) + `}`)
	got := expandToolInput("Bash", raw)
	if got != cmd {
		t.Errorf("expandToolInput(Bash) = %q, want %q", got, cmd)
	}
}

// QUM-335: expandToolInput renders non-Bash tools as pretty-printed JSON
// so every parameter (not just the summary field) is visible when expanded.
func TestExpandToolInput_NonBashPrettyJSON(t *testing.T) {
	raw := []byte(`{"file_path":"/a/b.go","old_string":"foo","new_string":"bar"}`)
	got := expandToolInput("Edit", raw)
	if !strings.Contains(got, "\n") {
		t.Errorf("expandToolInput(Edit) should be multi-line pretty JSON, got %q", got)
	}
	for _, k := range []string{"\"file_path\"", "\"old_string\"", "\"new_string\""} {
		if !strings.Contains(got, k) {
			t.Errorf("expandToolInput(Edit) missing key %s, got %q", k, got)
		}
	}
}

// QUM-335: empty input → empty expansion (no panic, no spurious "{}").
func TestExpandToolInput_Empty(t *testing.T) {
	if got := expandToolInput("Bash", nil); got != "" {
		t.Errorf("expandToolInput nil = %q, want empty", got)
	}
}

// QUM-335: mapAssistantMessage populates BOTH the truncated Input and the
// full FullInput on the resulting ToolCallMsg so the viewport can flip
// between them via the global expand toggle.
func TestMapAssistantMessage_PopulatesFullInput(t *testing.T) {
	longCmd := strings.Repeat("echo abcdef ", 20) // > 120 chars when joined
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-1","name":"Bash","input":{"command":` + mustJSONString(longCmd) + `}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	// QUM-386: all assistant messages now return AssistantContentMsg.
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 1 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 1", len(acm.Msgs))
	}
	tc, ok := acm.Msgs[0].(ToolCallMsg)
	if !ok {
		t.Fatalf("Msgs[0] is %T, want ToolCallMsg", acm.Msgs[0])
	}
	if tc.Input == "" || len(tc.Input) > 120 {
		t.Errorf("Input summary should be present and ≤120 chars, got %q (len=%d)", tc.Input, len(tc.Input))
	}
	if tc.FullInput != longCmd {
		t.Errorf("FullInput should be the verbatim command, got %q", tc.FullInput)
	}
}

func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// QUM-386: mapAssistantMessage must return ALL tool_use blocks from a single
// assistant message, not just the first. Parallel Agent calls produce multiple
// tool_use blocks.
func TestMapAssistantMessage_MultipleToolUse_ReturnsAll(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-1","name":"Agent","input":{"prompt":"task A"}},{"type":"tool_use","id":"t-2","name":"Agent","input":{"prompt":"task B"}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 2 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 2", len(acm.Msgs))
	}
	tc1, ok := acm.Msgs[0].(ToolCallMsg)
	if !ok {
		t.Fatalf("Msgs[0] is %T, want ToolCallMsg", acm.Msgs[0])
	}
	if tc1.ToolID != "t-1" || tc1.ToolName != "Agent" {
		t.Errorf("Msgs[0] = {ID:%q, Name:%q}, want {t-1, Agent}", tc1.ToolID, tc1.ToolName)
	}
	tc2, ok := acm.Msgs[1].(ToolCallMsg)
	if !ok {
		t.Fatalf("Msgs[1] is %T, want ToolCallMsg", acm.Msgs[1])
	}
	if tc2.ToolID != "t-2" || tc2.ToolName != "Agent" {
		t.Errorf("Msgs[1] = {ID:%q, Name:%q}, want {t-2, Agent}", tc2.ToolID, tc2.ToolName)
	}
}

// QUM-386: assistant message with text + tool_use returns both in AssistantContentMsg.
func TestMapAssistantMessage_TextAndToolUse_ReturnsBoth(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"t-1","name":"Bash","input":{"command":"ls"}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 2 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 2", len(acm.Msgs))
	}
	if _, ok := acm.Msgs[0].(AssistantTextMsg); !ok {
		t.Errorf("Msgs[0] is %T, want AssistantTextMsg", acm.Msgs[0])
	}
	if _, ok := acm.Msgs[1].(ToolCallMsg); !ok {
		t.Errorf("Msgs[1] is %T, want ToolCallMsg", acm.Msgs[1])
	}
}

// QUM-386: even a single tool_use block should be wrapped in AssistantContentMsg
// for consistent handling in the App's Update loop.
func TestMapAssistantMessage_SingleToolUse_WrappedInContentMsg(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-1","name":"Bash","input":{"command":"ls"}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	if len(acm.Msgs) != 1 {
		t.Fatalf("AssistantContentMsg has %d msgs, want 1", len(acm.Msgs))
	}
}

// --- Full lifecycle test ---

// --- QUM-385: Usage extraction tests ---

func TestMapAssistantMessage_ExtractsUsage(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":15000,"output_tokens":500,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	// Should contain both AssistantTextMsg and SessionUsageMsg.
	var foundUsage bool
	for _, inner := range acm.Msgs {
		if u, ok := inner.(SessionUsageMsg); ok {
			foundUsage = true
			if u.InputTokens != 15000 {
				t.Errorf("InputTokens = %d, want 15000", u.InputTokens)
			}
			if u.OutputTokens != 500 {
				t.Errorf("OutputTokens = %d, want 500", u.OutputTokens)
			}
			// QUM-385: true context window usage requires summing in cache
			// fields. The plumbing must surface them on SessionUsageMsg so
			// the status bar can compute input + cache_read + cache_creation.
			if u.CacheReadInputTokens != 50 {
				t.Errorf("CacheReadInputTokens = %d, want 50", u.CacheReadInputTokens)
			}
			if u.CacheCreationInputTokens != 100 {
				t.Errorf("CacheCreationInputTokens = %d, want 100", u.CacheCreationInputTokens)
			}
		}
	}
	if !foundUsage {
		t.Error("AssistantContentMsg should contain a SessionUsageMsg when usage is present")
	}
}

// TestMapAssistantMessage_ColdCacheUsage verifies the formula on a cold turn
// (no cache reads, no cache creation): SessionUsageMsg propagates zeros for
// the cache fields, leaving the input-token snapshot as the sole context
// window contributor. (QUM-385)
func TestMapAssistantMessage_ColdCacheUsage(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":4000,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	acm, ok := MapProtocolMessage(&msg).(AssistantContentMsg)
	if !ok {
		t.Fatalf("expected AssistantContentMsg")
	}
	var u SessionUsageMsg
	for _, inner := range acm.Msgs {
		if su, ok := inner.(SessionUsageMsg); ok {
			u = su
		}
	}
	if u.InputTokens != 4000 || u.CacheReadInputTokens != 0 || u.CacheCreationInputTokens != 0 {
		t.Errorf("cold-cache usage = %+v, want {Input:4000, CacheRead:0, CacheCreation:0}", u)
	}
}

// TestMapAssistantMessage_CacheCreationUsage verifies the cache-write turn:
// cache_creation_input_tokens is non-zero. The status bar formula must add
// it into the displayed total. (QUM-385)
func TestMapAssistantMessage_CacheCreationUsage(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":2000,"output_tokens":50,"cache_creation_input_tokens":8000,"cache_read_input_tokens":0}}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	acm, ok := MapProtocolMessage(&msg).(AssistantContentMsg)
	if !ok {
		t.Fatalf("expected AssistantContentMsg")
	}
	var u SessionUsageMsg
	for _, inner := range acm.Msgs {
		if su, ok := inner.(SessionUsageMsg); ok {
			u = su
		}
	}
	if u.InputTokens != 2000 || u.CacheCreationInputTokens != 8000 || u.CacheReadInputTokens != 0 {
		t.Errorf("cache-creation usage = %+v, want {Input:2000, CacheRead:0, CacheCreation:8000}", u)
	}
}

func TestMapAssistantMessage_NoUsage(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	for _, inner := range acm.Msgs {
		if _, ok := inner.(SessionUsageMsg); ok {
			t.Error("AssistantContentMsg should NOT contain SessionUsageMsg when usage is absent")
		}
	}
}

// TestMapAssistantMessage_ParallelSidechain_ParentToolIDFromWire verifies the
// QUM-386 live-path fix: when the wire envelope carries `parent_tool_use_id`,
// the resulting ToolCallMsg.ParentToolUseID must reflect that value verbatim
// so the viewport can attribute the inner tool call to the correct outer
// Agent. The replay-side sibling is
// TestLoadChildTranscript_SidechainParallelAgents_ParentToolIDFromWire.
//
// Red-phase: today ToolCallMsg has no ParentToolUseID field, so the .ParentToolUseID
// references below are an intentional compile-time failure — the implementer
// must add the field as the first step of the fix.
func TestMapAssistantMessage_ParallelSidechain_ParentToolIDFromWire(t *testing.T) {
	cases := []struct {
		name             string
		raw              string
		wantToolID       string
		wantToolName     string
		wantParentToolID string
	}{
		{
			name:             "parent_tool_use_id A1 with Read",
			raw:              `{"type":"assistant","uuid":"s-1","parent_tool_use_id":"A1","message":{"role":"assistant","content":[{"type":"tool_use","id":"R1","name":"Read","input":{"file_path":"/tmp/foo"}}]}}`,
			wantToolID:       "R1",
			wantToolName:     "Read",
			wantParentToolID: "A1",
		},
		{
			name:             "parent_tool_use_id A2 with Bash",
			raw:              `{"type":"assistant","uuid":"s-2","parent_tool_use_id":"A2","message":{"role":"assistant","content":[{"type":"tool_use","id":"B2","name":"Bash","input":{"command":"ls"}}]}}`,
			wantToolID:       "B2",
			wantToolName:     "Bash",
			wantParentToolID: "A2",
		},
		{
			name:             "no parent_tool_use_id (top-level envelope)",
			raw:              `{"type":"assistant","uuid":"a-top","message":{"role":"assistant","content":[{"type":"tool_use","id":"T1","name":"Read","input":{"file_path":"/tmp/top"}}]}}`,
			wantToolID:       "T1",
			wantToolName:     "Read",
			wantParentToolID: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var msg protocol.Message
			if err := json.Unmarshal([]byte(tc.raw), &msg); err != nil {
				t.Fatal(err)
			}
			msg.Raw = json.RawMessage(tc.raw)

			result := MapProtocolMessage(&msg)
			acm, ok := result.(AssistantContentMsg)
			if !ok {
				t.Fatalf("MapProtocolMessage returned %T, want AssistantContentMsg", result)
			}
			var picked ToolCallMsg
			var foundTC bool
			for _, inner := range acm.Msgs {
				if tcm, isToolCall := inner.(ToolCallMsg); isToolCall && tcm.ToolID == tc.wantToolID {
					picked = tcm
					foundTC = true
					break
				}
			}
			if !foundTC {
				t.Fatalf("no ToolCallMsg with ToolID=%q in AssistantContentMsg=%+v", tc.wantToolID, acm.Msgs)
			}
			if picked.ToolName != tc.wantToolName {
				t.Errorf("ToolName = %q, want %q", picked.ToolName, tc.wantToolName)
			}
			// QUM-386 live-path: parent_tool_use_id must be plumbed from the wire
			// envelope through to ToolCallMsg.ParentToolUseID, not inferred from
			// activeAgents state in the viewport.
			if picked.ParentToolUseID != tc.wantParentToolID {
				t.Errorf("ParentToolUseID = %q, want %q (must read parent_tool_use_id from wire envelope)", picked.ParentToolUseID, tc.wantParentToolID)
			}
		})
	}
}

func TestMapAssistantMessage_UsageAlongsideContent(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}],"usage":{"input_tokens":5000,"output_tokens":200}}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	acm, ok := result.(AssistantContentMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantContentMsg", result)
	}
	var hasText, hasUsage bool
	for _, inner := range acm.Msgs {
		switch inner.(type) {
		case AssistantTextMsg:
			hasText = true
		case SessionUsageMsg:
			hasUsage = true
		}
	}
	if !hasText {
		t.Error("expected AssistantTextMsg in batch")
	}
	if !hasUsage {
		t.Error("expected SessionUsageMsg in batch")
	}
}
