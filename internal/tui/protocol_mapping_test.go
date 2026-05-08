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
	// Assistant messages that only have thinking blocks should return nil
	// (they contain no displayable content)
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Let me think about this..."}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := MapProtocolMessage(&msg)
	if result != nil {
		t.Errorf("mapProtocolMessage for thinking-only assistant returned %T, want nil", result)
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
