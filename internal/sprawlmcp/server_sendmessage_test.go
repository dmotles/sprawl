// Tests for QUM-550 slice 1: the new send_message MCP tool that collapses
// send_async + send_interrupt. These tests pin both the new dispatch and the
// deprecation routing of the old tools through Supervisor.SendMessage.
//
// RED phase: these tests reference symbols that do not exist yet —
// supervisor.SendMessageResult, mockSupervisor.SendMessage routing, and the
// "send_message" tool registration. They are intentional compile-fail
// markers. When the implementation lands the missing symbols come with it.
package sprawlmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// mockSupervisorSendMessage is an extension shim around mockSupervisor that
// records SendMessage calls separately so tests can prove which path
// send_async / send_interrupt routes through. Production mockSupervisor (in
// server_test.go) will gain a real SendMessage field; this file uses a
// derived type with the recording surface we need.
//
// QUM-550 design: both send_async and send_interrupt internally call
// Supervisor.SendMessage with interrupt={false,true}. The legacy SendAsync /
// SendInterrupt Supervisor methods should NOT be invoked from the MCP tool
// dispatch path anymore.
type mockSupervisorSendMessage struct {
	mockSupervisor

	sendMessageCalls         int
	sendMessageTo            string
	sendMessageBody          string
	sendMessageInterrupt     bool
	sendMessageWakeIfOffline bool // QUM-726
	sendMessageResult        *supervisor.SendMessageResult
	sendMessageErr           error
}

func (m *mockSupervisorSendMessage) SendMessage(_ context.Context, to, body string, interrupt, wakeIfOffline bool) (*supervisor.SendMessageResult, error) {
	m.sendMessageCalls++
	m.sendMessageTo = to
	m.sendMessageBody = body
	m.sendMessageInterrupt = interrupt
	m.sendMessageWakeIfOffline = wakeIfOffline
	if m.sendMessageErr != nil {
		return nil, m.sendMessageErr
	}
	if m.sendMessageResult != nil {
		return m.sendMessageResult, nil
	}
	return &supervisor.SendMessageResult{
		MessageID:   "msg_sm_stub",
		QueuedAt:    "2026-05-12T00:00:00Z",
		Interrupted: interrupt,
	}, nil
}

// TestServer_ToolsCall_SendMessage_InterruptFalse pins the cooperative path
// of the new tool: interrupt:false forwards to Supervisor.SendMessage with
// the corresponding flag.
func TestServer_ToolsCall_SendMessage_InterruptFalse(t *testing.T) {
	mock := &mockSupervisorSendMessage{
		sendMessageResult: &supervisor.SendMessageResult{
			MessageID: "msg-cooperative-1",
			QueuedAt:  "2026-05-12T10:00:00Z",
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(60, "tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"to":        "child",
			"body":      "hi",
			"interrupt": false,
		},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.sendMessageCalls != 1 {
		t.Fatalf("SendMessage call count = %d, want 1", mock.sendMessageCalls)
	}
	if mock.sendMessageTo != "child" {
		t.Errorf("to = %q, want child", mock.sendMessageTo)
	}
	if mock.sendMessageBody != "hi" {
		t.Errorf("body = %q, want hi", mock.sendMessageBody)
	}
	if mock.sendMessageInterrupt != false {
		t.Errorf("interrupt = %v, want false", mock.sendMessageInterrupt)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "msg-cooperative-1") {
		t.Errorf("response body %q should contain message_id 'msg-cooperative-1'", text)
	}
}

// TestServer_ToolsCall_SendMessage_InterruptTrue pins the force-interrupt
// path: interrupt:true forwards through SendMessage with the flag set.
func TestServer_ToolsCall_SendMessage_InterruptTrue(t *testing.T) {
	mock := &mockSupervisorSendMessage{
		sendMessageResult: &supervisor.SendMessageResult{
			MessageID:   "msg-force-1",
			QueuedAt:    "2026-05-12T10:01:00Z",
			Interrupted: true,
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(61, "tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"to":        "child",
			"body":      "stop now",
			"interrupt": true,
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.sendMessageCalls != 1 {
		t.Fatalf("SendMessage call count = %d, want 1", mock.sendMessageCalls)
	}
	if !mock.sendMessageInterrupt {
		t.Errorf("interrupt = %v, want true", mock.sendMessageInterrupt)
	}
}

// TestServer_ToolsCall_SendMessage_InterruptDefaultsFalse pins the
// default-false contract: when interrupt is omitted the supervisor receives
// false (cooperative is the safe default).
func TestServer_ToolsCall_SendMessage_InterruptDefaultsFalse(t *testing.T) {
	mock := &mockSupervisorSendMessage{}
	srv := New(mock)

	msg := makeJSONRPCRequest(62, "tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"to":   "child",
			"body": "hi",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.sendMessageCalls != 1 {
		t.Fatalf("SendMessage calls = %d, want 1", mock.sendMessageCalls)
	}
	if mock.sendMessageInterrupt {
		t.Error("interrupt = true, want false when omitted from args")
	}
}

// TestServer_ToolsList_IncludesSendMessage asserts the tool list advertises
// send_message with required `to` and `body` properties and an optional
// `interrupt`.
func TestServer_ToolsList_IncludesSendMessage(t *testing.T) {
	srv := New(&mockSupervisorSendMessage{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(70, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)

	var found map[string]any
	for _, tAny := range tools {
		tm := tAny.(map[string]any)
		if tm["name"] == "send_message" {
			found = tm
			break
		}
	}
	if found == nil {
		t.Fatal("send_message missing from tools/list")
	}

	schema, ok := found["inputSchema"].(map[string]any)
	if !ok {
		t.Fatal("send_message: inputSchema missing")
	}
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["to"]; !ok {
		t.Error("send_message inputSchema: missing 'to' property")
	}
	if _, ok := props["body"]; !ok {
		t.Error("send_message inputSchema: missing 'body' property")
	}
	if _, ok := props["interrupt"]; !ok {
		t.Error("send_message inputSchema: missing 'interrupt' property")
	}

	required, _ := schema["required"].([]any)
	gotRequired := map[string]bool{}
	for _, r := range required {
		gotRequired[r.(string)] = true
	}
	if !gotRequired["to"] {
		t.Error("send_message required: missing 'to'")
	}
	if !gotRequired["body"] {
		t.Error("send_message required: missing 'body'")
	}
	if gotRequired["interrupt"] {
		t.Error("send_message required: 'interrupt' must be optional (defaults to false)")
	}
}

// TestServer_ToolsList_DeprecatedToolsRemoved pins the slice-5 invariant:
// after the deprecated send_async / send_interrupt / message tools are
// deleted, tools/list must no longer advertise them. send_message remains
// the sole survivor. This test is RED today (the deprecated tools still
// exist post-slice-4) and turns GREEN when the implementer removes the
// registrations in slice 5.
func TestServer_ToolsList_DeprecatedToolsRemoved(t *testing.T) {
	srv := New(&mockSupervisorSendMessage{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(90, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)

	present := map[string]bool{}
	for _, tAny := range tools {
		tm := tAny.(map[string]any)
		if name, ok := tm["name"].(string); ok {
			present[name] = true
		}
	}

	// Sanity: send_message IS present.
	if !present["send_message"] {
		t.Error("send_message missing from tools/list (sanity check failed)")
	}

	// Slice-5 invariant: deprecated tools must be gone.
	if present["send_async"] {
		t.Error("send_async still present in tools/list; slice 5 must remove it")
	}
	if present["send_interrupt"] {
		t.Error("send_interrupt still present in tools/list; slice 5 must remove it")
	}
	if present["message"] {
		t.Error("message (legacy alias) still present in tools/list; slice 5 must remove it")
	}
}

// TestSendMessageResult_ShapeContract pins the SendMessageResult JSON shape.
// The fields MessageID, QueuedAt, and Interrupted are the contract — if the
// struct loses any of them, this test compile-fails before the production
// callers do.
func TestSendMessageResult_ShapeContract(t *testing.T) {
	r := supervisor.SendMessageResult{
		MessageID:   "id",
		QueuedAt:    "ts",
		Interrupted: true,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(b), `"message_id"`) {
		t.Errorf("SendMessageResult JSON missing message_id: %s", b)
	}
	if !strings.Contains(string(b), `"queued_at"`) {
		t.Errorf("SendMessageResult JSON missing queued_at: %s", b)
	}
	if !strings.Contains(string(b), `"interrupted"`) {
		t.Errorf("SendMessageResult JSON missing interrupted: %s", b)
	}
}
