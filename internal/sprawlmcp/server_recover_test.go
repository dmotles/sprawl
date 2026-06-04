package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// QUM-601: the new `recover` MCP tool dispatches to Supervisor.Recover.
// On success the tool returns a short ack string. On supervisor error the
// tool returns IsError=true with the error message in the content text.

// recoverAware extends mockSupervisor with Recover recording + an injectable
// error. We declare it as a separate file in the same package so server_test.go
// stays untouched. The fields live on the embedded *mockSupervisor; we extend
// behavior here.
type recoverAwareSupervisor struct {
	*mockSupervisor
	recoverCalls int
	recoverAgent string
	recoverErr   error
}

func (m *recoverAwareSupervisor) Recover(_ context.Context, agentName string) error {
	m.recoverCalls++
	m.recoverAgent = agentName
	return m.recoverErr
}

func newRecoverAware() *recoverAwareSupervisor {
	return &recoverAwareSupervisor{mockSupervisor: &mockSupervisor{}}
}

func TestToolRecover_Success(t *testing.T) {
	mock := newRecoverAware()
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(101, "tools/call", map[string]any{
		"name": "recover",
		"arguments": map[string]any{
			"agent": "alice",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.recoverCalls != 1 {
		t.Fatalf("supervisor.Recover calls = %d, want 1", mock.recoverCalls)
	}
	if mock.recoverAgent != "alice" {
		t.Errorf("Recover called with agent = %q, want %q", mock.recoverAgent, "alice")
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", parsed["error"])
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Errorf("isError = true on success path: %v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("missing content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" {
		t.Error("empty content text on Recover success — expected an ack string")
	}
}

func TestToolRecover_PassesError(t *testing.T) {
	mock := newRecoverAware()
	mock.recoverErr = fmt.Errorf("backend session is healthy")
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(102, "tools/call", map[string]any{
		"name": "recover",
		"arguments": map[string]any{
			"agent": "alice",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.recoverCalls != 1 {
		t.Fatalf("supervisor.Recover calls = %d, want 1", mock.recoverCalls)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("isError = false on supervisor-error path; want true. result=%v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("missing content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" || !contains(text, "backend session is healthy") {
		t.Errorf("error text = %q, want it to include the supervisor error message", text)
	}
}

func TestToolRecover_MissingAgentName(t *testing.T) {
	mock := newRecoverAware()
	srv := New(mock)
	ctx := context.Background()

	// Send malformed args (a string instead of an object). The server must
	// reject it without calling supervisor.Recover.
	msg := makeJSONRPCRequest(103, "tools/call", map[string]any{
		"name":      "recover",
		"arguments": json.RawMessage(`"not-an-object"`),
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.recoverCalls != 0 {
		t.Errorf("supervisor.Recover called %d times on malformed args; want 0", mock.recoverCalls)
	}

	parsed := parseJSONRPCResponse(t, resp)
	// Either a top-level JSON-RPC error or a result with isError=true
	// is acceptable; we just want the call surfaced as an error.
	if _, ok := parsed["error"]; ok {
		return
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result and no top-level error")
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("malformed args produced isError=false; want true. result=%v", result)
	}
}

// contains is a tiny stand-in for strings.Contains kept local to avoid
// importing strings just for this single helper.
func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
