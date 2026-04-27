package tui

import (
	"encoding/json"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-336: a `user` protocol message that contains a tool_result content block
// (Claude's wire format for tool outputs) maps to a ToolResultMsg carrying the
// matching tool_use_id, the result text, and IsError=false.
func TestMapProtocolMessage_UserToolResult_StringContent(t *testing.T) {
	raw := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-1","content":"hello world","is_error":false}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
	rm, ok := result.(ToolResultMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want ToolResultMsg", result)
	}
	if rm.ToolID != "t-1" {
		t.Errorf("ToolID = %q, want %q", rm.ToolID, "t-1")
	}
	if rm.Content != "hello world" {
		t.Errorf("Content = %q, want %q", rm.Content, "hello world")
	}
	if rm.IsError {
		t.Errorf("IsError = true, want false")
	}
}

// QUM-336: tool_result's content can also be an array of {type:"text",text:...}
// blocks. The bridge joins the text fragments with newlines.
func TestMapProtocolMessage_UserToolResult_ArrayContent(t *testing.T) {
	raw := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-2","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}],"is_error":false}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
	rm, ok := result.(ToolResultMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want ToolResultMsg", result)
	}
	if rm.ToolID != "t-2" {
		t.Errorf("ToolID = %q, want %q", rm.ToolID, "t-2")
	}
	if rm.Content != "line1\nline2" {
		t.Errorf("Content = %q, want %q", rm.Content, "line1\nline2")
	}
}

// QUM-336: is_error=true on a tool_result block surfaces as IsError=true so the
// viewport can render the failure indicator.
func TestMapProtocolMessage_UserToolResult_Error(t *testing.T) {
	raw := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t-3","content":"boom","is_error":true}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	result := mapProtocolMessage(&msg)
	rm, ok := result.(ToolResultMsg)
	if !ok {
		t.Fatalf("mapProtocolMessage returned %T, want ToolResultMsg", result)
	}
	if !rm.IsError {
		t.Errorf("IsError = false, want true")
	}
	if rm.Content != "boom" {
		t.Errorf("Content = %q, want %q", rm.Content, "boom")
	}
}

// QUM-336: a `user` message carrying a plain-string content (echo of the
// user's typed prompt — already rendered by the InputModel via SubmitMsg)
// must NOT produce a ToolResultMsg. The bridge returns nil so WaitForEvent
// skips it and waits for the next event.
func TestMapProtocolMessage_UserMessage_PlainString_ReturnsNil(t *testing.T) {
	raw := `{"type":"user","message":{"role":"user","content":"hello"}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	if got := mapProtocolMessage(&msg); got != nil {
		t.Errorf("mapProtocolMessage(user/plain-string) = %T, want nil", got)
	}
}

// QUM-336: a `user` message whose content array contains only non-tool_result
// blocks (e.g. a plain text block) returns nil as well.
func TestMapProtocolMessage_UserMessage_NoToolResult_ReturnsNil(t *testing.T) {
	raw := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"howdy"}]}}`
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatal(err)
	}
	msg.Raw = json.RawMessage(raw)

	if got := mapProtocolMessage(&msg); got != nil {
		t.Errorf("mapProtocolMessage(user/no-tool-result) = %T, want nil", got)
	}
}

func TestToolResultMsg_FieldAccess(t *testing.T) {
	msg := ToolResultMsg{ToolID: "t-1", Content: "out", IsError: true}
	if msg.ToolID != "t-1" {
		t.Errorf("ToolID = %q, want %q", msg.ToolID, "t-1")
	}
	if msg.Content != "out" {
		t.Errorf("Content = %q, want %q", msg.Content, "out")
	}
	if !msg.IsError {
		t.Error("IsError = false, want true")
	}
}
