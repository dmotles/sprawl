// QUM-726: wake-on-traffic MCP-level tests.
//
// These tests pin the new wake-on-traffic dispatch:
//   - toolSendMessage forwards wake_if_offline to Supervisor.SendMessage.
//   - toolDelegate forwards wake_if_offline to Supervisor.Delegate.
//   - toolWake invokes Supervisor.Wake with WakeReasonBare and an empty body
//     (the wake verb has no payload of its own).
//   - The send_message and delegate input schemas advertise the new
//     wake_if_offline boolean property.
//
// RED phase: these tests reference symbols/behavior that do not exist yet —
// the wake_if_offline flag plumb-through, the WakeReason argument to
// Supervisor.Wake, and the schema fields. They will fail until the
// implementation in server.go / tools.go is wired.
package sprawlmcp

import (
	"context"
	"testing"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
)

// TestToolSendMessage_ForwardsWakeIfOffline verifies the new boolean flag is
// passed through to the supervisor.
func TestToolSendMessage_ForwardsWakeIfOffline(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(700, "tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"to":              "alice",
			"body":            "hi",
			"interrupt":       false,
			"wake_if_offline": true,
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.sendMessageCalls != 1 {
		t.Fatalf("SendMessage call count = %d, want 1", mock.sendMessageCalls)
	}
	if !mock.sendMessageWakeIfOffline {
		t.Errorf("supervisor saw wakeIfOffline = false, want true (QUM-726 plumb-through)")
	}
}

// TestToolSendMessage_WakeIfOfflineDefaultsFalse verifies omission of the
// flag yields wake_if_offline=false (the existing default behavior — fail-
// closed for offline targets).
func TestToolSendMessage_WakeIfOfflineDefaultsFalse(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(701, "tools/call", map[string]any{
		"name": "send_message",
		"arguments": map[string]any{
			"to":   "alice",
			"body": "hi",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.sendMessageWakeIfOffline {
		t.Errorf("supervisor saw wakeIfOffline = true; want false default")
	}
}

// TestToolDelegate_ForwardsWakeIfOffline pins delegate's plumb-through of the
// new flag.
func TestToolDelegate_ForwardsWakeIfOffline(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(702, "tools/call", map[string]any{
		"name": "delegate",
		"arguments": map[string]any{
			"agent":           "alice",
			"task":            "implement X",
			"wake_if_offline": true,
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.delegateAgent != "alice" {
		t.Fatalf("Delegate target = %q, want alice", mock.delegateAgent)
	}
	if !mock.delegateWakeIfOffline {
		t.Errorf("supervisor saw delegate wakeIfOffline = false, want true (QUM-726)")
	}
}

// TestToolDelegate_WakeIfOfflineDefaultsFalse — flag omitted ⇒ false.
func TestToolDelegate_WakeIfOfflineDefaultsFalse(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(703, "tools/call", map[string]any{
		"name": "delegate",
		"arguments": map[string]any{
			"agent": "alice",
			"task":  "implement X",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.delegateWakeIfOffline {
		t.Errorf("supervisor saw delegate wakeIfOffline = true; want false default")
	}
}

// TestToolWake_DefaultsToBareReason pins that the wake MCP verb passes
// WakeReasonBare + empty body to Supervisor.Wake. The wake tool itself does
// not (and is intentionally not designed to) carry a payload — combine with
// delegate/send_message for wake-with-work.
func TestToolWake_DefaultsToBareReason(t *testing.T) {
	mock := newWakeAware()
	srv := New(mock)

	msg := makeJSONRPCRequest(704, "tools/call", map[string]any{
		"name": "wake",
		"arguments": map[string]any{
			"agent": "alice",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.wakeCalls != 1 {
		t.Fatalf("Wake calls = %d, want 1", mock.wakeCalls)
	}
	if mock.wakeReason != agentpkg.WakeReasonBare {
		t.Errorf("Wake reason = %q, want %q", mock.wakeReason, agentpkg.WakeReasonBare)
	}
	if mock.wakeBody != "" {
		t.Errorf("Wake injected body = %q, want empty (bare wake has no payload)", mock.wakeBody)
	}
}

// TestSendMessageSchema_HasWakeIfOffline asserts the send_message tool
// catalog entry advertises wake_if_offline as a boolean property.
func TestSendMessageSchema_HasWakeIfOffline(t *testing.T) {
	var def map[string]any
	for _, d := range baseToolDefinitions() {
		if d["name"] == "send_message" {
			def = d
			break
		}
	}
	if def == nil {
		t.Fatal("send_message tool definition not found")
	}
	schema, ok := def["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema type = %T", def["inputSchema"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", schema["properties"])
	}
	woi, ok := props["wake_if_offline"].(map[string]any)
	if !ok {
		t.Fatalf("send_message.properties.wake_if_offline missing or wrong type: %T", props["wake_if_offline"])
	}
	if woi["type"] != "boolean" {
		t.Errorf("wake_if_offline.type = %v, want boolean", woi["type"])
	}
}

// TestDelegateSchema_HasWakeIfOffline asserts the delegate tool catalog entry
// advertises wake_if_offline as a boolean property.
func TestDelegateSchema_HasWakeIfOffline(t *testing.T) {
	var def map[string]any
	for _, d := range baseToolDefinitions() {
		if d["name"] == "delegate" {
			def = d
			break
		}
	}
	if def == nil {
		t.Fatal("delegate tool definition not found")
	}
	schema, ok := def["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema type = %T", def["inputSchema"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T", schema["properties"])
	}
	woi, ok := props["wake_if_offline"].(map[string]any)
	if !ok {
		t.Fatalf("delegate.properties.wake_if_offline missing or wrong type: %T", props["wake_if_offline"])
	}
	if woi["type"] != "boolean" {
		t.Errorf("wake_if_offline.type = %v, want boolean", woi["type"])
	}
}
