package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

func TestSession_InitializeSendsControlRequest(t *testing.T) {
	mt := newMockTransport()

	cfg := SessionConfig{
		SystemPrompt: "You are a helpful assistant.",
	}
	sess := NewSession(mt, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Simulate the transport responding with an initialize response
	go func() {
		// Wait for the initialize request to be sent
		select {
		case sent := <-mt.sendCh:
			// Verify it's an initialize control request
			data, err := json.Marshal(sent)
			if err != nil {
				t.Errorf("marshal sent message: %v", err)
				return
			}
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Errorf("unmarshal sent message: %v", err)
				return
			}
			if parsed["type"] != "control_request" {
				t.Errorf("sent type = %v, want control_request", parsed["type"])
			}

			// Feed back a control_response
			mt.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"init-1"}}`)
			close(mt.recvCh)
		case <-ctx.Done():
			return
		}
	}()

	err := sess.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}
}

func TestSession_SendUserMessageReturnsEvents(t *testing.T) {
	mt := newMockTransport()
	cfg := SessionConfig{}
	sess := NewSession(mt, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Feed messages that the session should relay as events
	go func() {
		// Wait for the user message to be sent
		<-mt.sendCh

		mt.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`)
		mt.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":100,"num_turns":1,"total_cost_usd":0.01}`)
		close(mt.recvCh)
	}()

	events, err := sess.SendUserMessage(ctx, "Hi there")
	if err != nil {
		t.Fatalf("SendUserMessage() error: %v", err)
	}
	if events == nil {
		t.Fatal("SendUserMessage() returned nil events channel")
	}

	var received []*protocol.Message
	for msg := range events {
		received = append(received, msg)
		if msg.Type == "result" {
			break
		}
	}

	if len(received) < 2 {
		t.Fatalf("received %d events, want at least 2", len(received))
	}
	if received[0].Type != "assistant" {
		t.Errorf("first event Type = %q, want %q", received[0].Type, "assistant")
	}
	if received[1].Type != "result" {
		t.Errorf("second event Type = %q, want %q", received[1].Type, "result")
	}
}

func TestSession_SendUserMessageEventsEndOnResult(t *testing.T) {
	mt := newMockTransport()
	cfg := SessionConfig{}
	sess := NewSession(mt, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		<-mt.sendCh
		mt.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[]}}`)
		mt.feedMessage(t, `{"type":"assistant","uuid":"a-2","message":{"role":"assistant","content":[]}}`)
		mt.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":50,"num_turns":1,"total_cost_usd":0.01}`)
		close(mt.recvCh)
	}()

	events, err := sess.SendUserMessage(ctx, "test")
	if err != nil {
		t.Fatalf("SendUserMessage() error: %v", err)
	}
	if events == nil {
		t.Fatal("SendUserMessage() returned nil events channel")
	}

	count := 0
	for range events {
		count++
	}

	if count != 3 {
		t.Errorf("received %d events, want 3 (2 assistant + 1 result)", count)
	}
}

func TestSession_InterruptSendsRequest(t *testing.T) {
	mt := newMockTransport()
	cfg := SessionConfig{}
	sess := NewSession(mt, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := sess.Interrupt(ctx)
	if err != nil {
		t.Fatalf("Interrupt() error: %v", err)
	}

	// Verify an interrupt was sent
	select {
	case sent := <-mt.sendCh:
		data, err := json.Marshal(sent)
		if err != nil {
			t.Fatalf("marshal sent: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("unmarshal sent: %v", err)
		}
		if parsed["type"] != "control_request" {
			t.Errorf("sent type = %v, want control_request", parsed["type"])
		}
	case <-time.After(time.Second):
		t.Fatal("no interrupt message sent")
	}
}

func TestSession_CloseSendsEndSession(t *testing.T) {
	mt := newMockTransport()
	cfg := SessionConfig{}
	sess := NewSession(mt, cfg)

	err := sess.Close()
	if err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if !mt.closed {
		t.Error("transport was not closed")
	}
}

func TestSession_FullLifecycle(t *testing.T) {
	mt := newMockTransport()
	cfg := SessionConfig{
		SystemPrompt: "You are a test assistant.",
	}
	sess := NewSession(mt, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Phase 1: Initialize
	go func() {
		// Consume the initialize request
		select {
		case <-mt.sendCh:
		case <-ctx.Done():
			return
		}
		// Respond with success
		mt.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"init-1"}}`)
	}()

	if err := sess.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	// Phase 2: Send user message and receive events
	// Need a fresh recvCh since the old one may still be open
	mt.recvCh = make(chan *protocol.Message, 100)

	go func() {
		// Consume the user message
		select {
		case <-mt.sendCh:
		case <-ctx.Done():
			return
		}
		mt.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"I can help!"}]}}`)
		mt.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":200,"num_turns":1,"total_cost_usd":0.02}`)
		close(mt.recvCh)
	}()

	events, err := sess.SendUserMessage(ctx, "Help me with something")
	if err != nil {
		t.Fatalf("SendUserMessage() error: %v", err)
	}
	if events == nil {
		t.Fatal("SendUserMessage() returned nil events channel")
	}

	var lastType string
	for msg := range events {
		lastType = msg.Type
	}
	if lastType != "result" {
		t.Errorf("last event Type = %q, want %q", lastType, "result")
	}

	// Phase 3: Close
	if err := sess.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if !mt.closed {
		t.Error("transport not closed after session Close()")
	}
}

func TestSession_SendUserMessageAutoApprovesToolUse(t *testing.T) {
	mt := newMockTransport()
	cfg := SessionConfig{}
	sess := NewSession(mt, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		// Consume the user message
		<-mt.sendCh

		// Feed a can_use_tool control request
		mt.feedMessage(t, `{"type":"control_request","request_id":"cr-1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}`)

		// Give time for the approval to be sent
		time.Sleep(50 * time.Millisecond)

		// End the turn
		mt.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":50,"num_turns":1,"total_cost_usd":0.01}`)
		close(mt.recvCh)
	}()

	events, err := sess.SendUserMessage(ctx, "run a command")
	if err != nil {
		t.Fatalf("SendUserMessage() error: %v", err)
	}

	// Drain events
	for range events { //nolint:revive // intentionally draining channel
	}

	// Read the approval response that session sent back via transport
	var approvalMsg any
	select {
	case approvalMsg = <-mt.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no approval response sent for can_use_tool control request")
	}

	// Marshal and verify the nested JSON structure
	data, err := json.Marshal(approvalMsg)
	if err != nil {
		t.Fatalf("marshal approval: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal approval: %v", err)
	}

	if parsed["type"] != "control_response" {
		t.Fatalf("approval type = %v, want control_response", parsed["type"])
	}

	resp, ok := parsed["response"].(map[string]any)
	if !ok {
		t.Fatal("response field is not an object")
	}

	if resp["request_id"] != "cr-1" {
		t.Errorf("response.request_id = %v, want cr-1", resp["request_id"])
	}

	// The bug: the approval response must include a nested "response" field
	// with behavior:"allow" for can_use_tool requests. Without it, the TUI hangs.
	innerResp, ok := resp["response"].(map[string]any)
	if !ok {
		t.Fatal("response.response field is missing or not an object — approval payload not included")
	}

	if innerResp["behavior"] != "allow" {
		t.Errorf("response.response.behavior = %v, want allow", innerResp["behavior"])
	}
}
