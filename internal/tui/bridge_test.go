package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// mockSession implements BridgeSession for testing.
type mockSession struct {
	initErr     error
	sendErr     error
	closeErr    error
	closeCalled bool
	events      chan *protocol.Message
}

func newMockSession() *mockSession {
	return &mockSession{
		events: make(chan *protocol.Message, 100),
	}
}

func (m *mockSession) Initialize(ctx context.Context) error {
	return m.initErr
}

func (m *mockSession) SendUserMessage(ctx context.Context, prompt string) (<-chan *protocol.Message, error) {
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	return m.events, nil
}

func (m *mockSession) Close() error {
	m.closeCalled = true
	return m.closeErr
}

func (m *mockSession) feedMessage(t *testing.T, raw string) {
	t.Helper()
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("feedMessage: unmarshal error: %v", err)
	}
	msg.Raw = json.RawMessage(raw)
	m.events <- &msg
}

// --- Bridge construction tests ---

func TestNewBridge(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)
	if b == nil {
		t.Fatal("NewBridge() returned nil")
	}
}

func TestBridge_SessionID_DefaultEmpty(t *testing.T) {
	ms := newMockSession()
	b := NewBridge(context.Background(), ms)
	if got := b.SessionID(); got != "" {
		t.Errorf("SessionID() = %q, want empty string before SetSessionID", got)
	}
}

func TestBridge_SetSessionID_Roundtrip(t *testing.T) {
	ms := newMockSession()
	b := NewBridge(context.Background(), ms)
	b.SetSessionID("abcdef1234567890")
	if got := b.SessionID(); got != "abcdef1234567890" {
		t.Errorf("SessionID() = %q, want %q", got, "abcdef1234567890")
	}
}

// --- Initialize tests ---

func TestBridge_InitializeSuccess(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.Initialize()
	if cmd == nil {
		t.Fatal("Initialize() returned nil cmd")
	}

	msg := cmd()
	if _, ok := msg.(SessionInitializedMsg); !ok {
		t.Errorf("Initialize() returned %T, want SessionInitializedMsg", msg)
	}
}

func TestBridge_InitializeError(t *testing.T) {
	ms := newMockSession()
	ms.initErr = errors.New("connection refused")
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.Initialize()
	msg := cmd()

	errMsg, ok := msg.(SessionErrorMsg)
	if !ok {
		t.Fatalf("Initialize() returned %T, want SessionErrorMsg", msg)
	}
	if errMsg.Err.Error() != "initializing session: connection refused" {
		t.Errorf("error = %q, want %q", errMsg.Err.Error(), "initializing session: connection refused")
	}
}

// --- SendMessage tests ---

func TestBridge_SendMessageSuccess(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("Hello")
	if cmd == nil {
		t.Fatal("SendMessage() returned nil cmd")
	}

	msg := cmd()
	if _, ok := msg.(UserMessageSentMsg); !ok {
		t.Errorf("SendMessage() returned %T, want UserMessageSentMsg", msg)
	}
}

func TestBridge_SendMessageError(t *testing.T) {
	ms := newMockSession()
	ms.sendErr = errors.New("session closed")
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("Hello")
	msg := cmd()

	errMsg, ok := msg.(SessionErrorMsg)
	if !ok {
		t.Fatalf("SendMessage() returned %T, want SessionErrorMsg", msg)
	}
	if errMsg.Err.Error() != "sending message: session closed" {
		t.Errorf("error = %q, want %q", errMsg.Err.Error(), "sending message: session closed")
	}
}

// --- WaitForEvent tests ---

func TestBridge_WaitForEvent_AssistantText(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	// Send a message to set up the events channel
	cmd := b.SendMessage("test")
	cmd() // consume UserMessageSentMsg

	// Feed an assistant message with text content
	ms.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello world"}]}}`)

	waitCmd := b.WaitForEvent()
	if waitCmd == nil {
		t.Fatal("WaitForEvent() returned nil cmd")
	}

	msg := waitCmd()
	textMsg, ok := msg.(AssistantTextMsg)
	if !ok {
		t.Fatalf("WaitForEvent() returned %T, want AssistantTextMsg", msg)
	}
	if textMsg.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", textMsg.Text, "Hello world")
	}
}

func TestBridge_WaitForEvent_ToolCall(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("test")
	cmd()

	// Feed an assistant message with a tool_use content block
	ms.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"ls"}}]}}`)

	waitCmd := b.WaitForEvent()
	msg := waitCmd()

	toolMsg, ok := msg.(ToolCallMsg)
	if !ok {
		t.Fatalf("WaitForEvent() returned %T, want ToolCallMsg", msg)
	}
	if toolMsg.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", toolMsg.ToolName, "Bash")
	}
	if toolMsg.ToolID != "tool-1" {
		t.Errorf("ToolID = %q, want %q", toolMsg.ToolID, "tool-1")
	}
	if !toolMsg.Approved {
		t.Error("Approved = false, want true (auto-approved)")
	}
}

func TestBridge_WaitForEvent_Result(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("test")
	cmd()

	ms.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":200,"num_turns":1,"total_cost_usd":0.05}`)

	waitCmd := b.WaitForEvent()
	msg := waitCmd()

	resultMsg, ok := msg.(SessionResultMsg)
	if !ok {
		t.Fatalf("WaitForEvent() returned %T, want SessionResultMsg", msg)
	}
	if resultMsg.IsError {
		t.Error("IsError = true, want false")
	}
	if resultMsg.DurationMs != 200 {
		t.Errorf("DurationMs = %d, want 200", resultMsg.DurationMs)
	}
	if resultMsg.TotalCostUsd != 0.05 {
		t.Errorf("TotalCostUsd = %f, want 0.05", resultMsg.TotalCostUsd)
	}
}

func TestBridge_WaitForEvent_ChannelClosed(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("test")
	cmd()

	close(ms.events)

	waitCmd := b.WaitForEvent()
	msg := waitCmd()

	errMsg, ok := msg.(SessionErrorMsg)
	if !ok {
		t.Fatalf("WaitForEvent() on closed channel returned %T, want SessionErrorMsg", msg)
	}
	if !errors.Is(errMsg.Err, io.EOF) {
		t.Errorf("error = %v, want io.EOF", errMsg.Err)
	}
}

// TestBridge_WaitForEvent_EOF_WrapsIoEOF_ForErrorsIs locks in the contract that
// the app's auto-restart EOF branch relies on: when the session events channel
// closes, SessionErrorMsg.Err must be identifiable via errors.Is(err, io.EOF).
// If bridge.go later wraps the EOF, this test forces the wrapper to preserve
// the chain (fmt.Errorf with %w).
func TestBridge_WaitForEvent_EOF_WrapsIoEOF_ForErrorsIs(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("test")
	cmd()

	close(ms.events)

	msg := b.WaitForEvent()()
	errMsg, ok := msg.(SessionErrorMsg)
	if !ok {
		t.Fatalf("WaitForEvent returned %T, want SessionErrorMsg", msg)
	}
	if errMsg.Err == nil {
		t.Fatal("SessionErrorMsg.Err is nil, want an error wrapping io.EOF")
	}
	if !errors.Is(errMsg.Err, io.EOF) {
		t.Errorf("errors.Is(err, io.EOF) = false, want true (err=%v). The app EOF branch depends on this chain.", errMsg.Err)
	}
}

func TestBridge_WaitForEvent_ContextCancelled(t *testing.T) {
	ms := newMockSession()
	ctx, cancel := context.WithCancel(context.Background())
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("test")
	cmd()

	// Cancel context before waiting
	cancel()

	waitCmd := b.WaitForEvent()
	msg := waitCmd()

	errMsg, ok := msg.(SessionErrorMsg)
	if !ok {
		t.Fatalf("WaitForEvent() on cancelled ctx returned %T, want SessionErrorMsg", msg)
	}
	if errMsg.Err == nil {
		t.Error("error is nil, want context cancelled")
	}
}

func TestBridge_WaitForEvent_NoEvents(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	// WaitForEvent without SendMessage should return error
	waitCmd := b.WaitForEvent()
	msg := waitCmd()

	_, ok := msg.(SessionErrorMsg)
	if !ok {
		t.Fatalf("WaitForEvent() with no events returned %T, want SessionErrorMsg", msg)
	}
}

// --- MapProtocolMessage tests ---

func TestMapProtocolMessage_AssistantWithText(t *testing.T) {
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
	textMsg, ok := result.(AssistantTextMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantTextMsg", result)
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

	result := mapProtocolMessage(&msg)
	toolMsg, ok := result.(ToolCallMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want ToolCallMsg", result)
	}
	if toolMsg.ToolName != "Read" {
		t.Errorf("ToolName = %q, want %q", toolMsg.ToolName, "Read")
	}
	if toolMsg.ToolID != "t-1" {
		t.Errorf("ToolID = %q, want %q", toolMsg.ToolID, "t-1")
	}
}

func TestMapProtocolMessage_AssistantWithMixedContent(t *testing.T) {
	// When content has both text and tool_use, the first significant block wins.
	// Text blocks come first.
	raw := `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Let me check"},{"type":"tool_use","id":"t-1","name":"Bash","input":{}}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
	// Should return the first content block type found
	textMsg, ok := result.(AssistantTextMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want AssistantTextMsg", result)
	}
	if textMsg.Text != "Let me check" {
		t.Errorf("Text = %q, want %q", textMsg.Text, "Let me check")
	}
}

func TestMapProtocolMessage_Result(t *testing.T) {
	raw := `{"type":"result","subtype":"success","is_error":false,"duration_ms":100,"num_turns":2,"total_cost_usd":0.03}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
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

	result := mapProtocolMessage(&msg)
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

	result := mapProtocolMessage(&msg)
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

	result := mapProtocolMessage(&msg)
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

	result := mapProtocolMessage(&msg)
	// Unknown types should return nil (ignored)
	if result != nil {
		t.Errorf("mapProtocolMessage for unknown type returned %T, want nil", result)
	}
}

func TestMapProtocolMessage_SystemInit(t *testing.T) {
	// System messages during event streaming should return nil — they are
	// informational and session initialization is handled by Bridge.Initialize().
	raw := `{"type":"system","subtype":"init","session_id":"s-1","cwd":"/tmp","tools":["Bash"],"model":"claude-4","permissionMode":"auto"}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
	if result != nil {
		t.Errorf("mapProtocolMessage for system/init returned %T, want nil (system messages are skipped during event stream)", result)
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

func TestBridge_WaitForEvent_ToolCallWithInput(t *testing.T) {
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)

	cmd := b.SendMessage("test")
	cmd()

	ms.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-1","name":"Bash","input":{"command":"ls -la"}}]}}`)

	waitCmd := b.WaitForEvent()
	msg := waitCmd()

	toolMsg, ok := msg.(ToolCallMsg)
	if !ok {
		t.Fatalf("WaitForEvent() returned %T, want ToolCallMsg", msg)
	}
	if toolMsg.Input != "ls -la" {
		t.Errorf("Input = %q, want %q", toolMsg.Input, "ls -la")
	}
}

// --- Full lifecycle test ---

func TestBridge_FullTurnLifecycle(t *testing.T) {
	ms := newMockSession()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := NewBridge(ctx, ms)

	// Initialize
	initCmd := b.Initialize()
	initMsg := initCmd()
	if _, ok := initMsg.(SessionInitializedMsg); !ok {
		t.Fatalf("Initialize returned %T, want SessionInitializedMsg", initMsg)
	}

	// Send message
	sendCmd := b.SendMessage("What files are here?")
	sendMsg := sendCmd()
	if _, ok := sendMsg.(UserMessageSentMsg); !ok {
		t.Fatalf("SendMessage returned %T, want UserMessageSentMsg", sendMsg)
	}

	// Feed assistant text
	ms.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Let me check"}]}}`)
	waitCmd := b.WaitForEvent()
	msg := waitCmd()
	if textMsg, ok := msg.(AssistantTextMsg); !ok {
		t.Fatalf("expected AssistantTextMsg, got %T", msg)
	} else if textMsg.Text != "Let me check" {
		t.Errorf("Text = %q, want %q", textMsg.Text, "Let me check")
	}

	// Feed tool call
	ms.feedMessage(t, `{"type":"assistant","uuid":"a-2","message":{"role":"assistant","content":[{"type":"tool_use","id":"t-1","name":"Bash","input":{"command":"ls"}}]}}`)
	waitCmd = b.WaitForEvent()
	msg = waitCmd()
	if toolMsg, ok := msg.(ToolCallMsg); !ok {
		t.Fatalf("expected ToolCallMsg, got %T", msg)
	} else if toolMsg.ToolName != "Bash" {
		t.Errorf("ToolName = %q, want %q", toolMsg.ToolName, "Bash")
	}

	// Feed result
	ms.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":300,"num_turns":1,"total_cost_usd":0.04}`)
	waitCmd = b.WaitForEvent()
	msg = waitCmd()
	if resultMsg, ok := msg.(SessionResultMsg); !ok {
		t.Fatalf("expected SessionResultMsg, got %T", msg)
	} else if resultMsg.DurationMs != 300 {
		t.Errorf("DurationMs = %d, want 300", resultMsg.DurationMs)
	}
}
