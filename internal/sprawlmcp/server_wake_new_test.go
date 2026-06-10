package sprawlmcp

// QUM-724 — new MCP `wake` verb behavior tests.
//
// These tests reference symbols that do not exist yet:
//   - supervisor.Supervisor.Wake(ctx, agent) (*supervisor.WakeResult, error)
//   - supervisor.WakeResult{Mode, SessionRestored}
//   - supervisor.ErrWakeNotNeeded sentinel
//   - The "wake" tool catalog entry (replacing "recover").
//
// They compile-fail until QUM-724 lands the rename. That is the red-phase
// signal for TDD.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// wakeAwareSupervisor extends mockSupervisor with a Wake seam (matching the
// recoverAwareSupervisor pattern in server_recover_test.go).
type wakeAwareSupervisor struct {
	*mockSupervisor
	wakeCalls  int
	wakeAgent  string
	wakeReason agentpkg.WakeReason // QUM-726
	wakeBody   string              // QUM-726
	wakeResult *supervisor.WakeResult
	wakeErr    error
}

func (m *wakeAwareSupervisor) Wake(_ context.Context, agentName string, reason agentpkg.WakeReason, injectedBody string) (*supervisor.WakeResult, error) {
	m.wakeCalls++
	m.wakeAgent = agentName
	m.wakeReason = reason
	m.wakeBody = injectedBody
	if m.wakeErr != nil {
		return nil, m.wakeErr
	}
	return m.wakeResult, nil
}

func newWakeAware() *wakeAwareSupervisor {
	return &wakeAwareSupervisor{mockSupervisor: &mockSupervisor{}}
}

func resultContentText(t *testing.T, parsed map[string]any) (string, bool) {
	t.Helper()
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result; parsed=%v", parsed)
	}
	isErr, _ := result["isError"].(bool)
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content; result=%v", result)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	return text, isErr
}

func TestToolWake_Success_Resumed(t *testing.T) {
	mock := newWakeAware()
	mock.wakeResult = &supervisor.WakeResult{Mode: "resumed", SessionRestored: true}
	srv := New(mock)

	msg := makeJSONRPCRequest(720, "tools/call", map[string]any{
		"name":      "wake",
		"arguments": map[string]any{"agent": "alice"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.wakeCalls != 1 {
		t.Fatalf("supervisor.Wake calls = %d, want 1", mock.wakeCalls)
	}
	if mock.wakeAgent != "alice" {
		t.Errorf("Wake called with agent = %q, want alice", mock.wakeAgent)
	}

	parsed := parseJSONRPCResponse(t, resp)
	text, isErr := resultContentText(t, parsed)
	if isErr {
		t.Errorf("isError = true on success path; text=%q", text)
	}
	// Tool returns the marshaled WakeResult so caller can branch on Mode.
	if !contains(text, `"mode":"resumed"`) && !contains(text, `"mode": "resumed"`) {
		t.Errorf("wake success text missing mode=resumed; got %q", text)
	}
	if !contains(text, `"session_restored":true`) && !contains(text, `"session_restored": true`) {
		t.Errorf("wake success text missing session_restored=true; got %q", text)
	}
}

func TestToolWake_Success_Fresh(t *testing.T) {
	mock := newWakeAware()
	mock.wakeResult = &supervisor.WakeResult{Mode: "fresh", SessionRestored: false}
	srv := New(mock)

	msg := makeJSONRPCRequest(721, "tools/call", map[string]any{
		"name":      "wake",
		"arguments": map[string]any{"agent": "alice"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	text, isErr := resultContentText(t, parsed)
	if isErr {
		t.Errorf("isError = true on fresh-success path; text=%q", text)
	}
	if !contains(text, `"mode":"fresh"`) && !contains(text, `"mode": "fresh"`) {
		t.Errorf("wake fresh text missing mode=fresh; got %q", text)
	}
	if !contains(text, "session_restored") {
		t.Errorf("wake response must include session_restored field; got %q", text)
	}
}

// TestToolWake_NoOp verifies the ErrWakeNotNeeded path is surfaced as a
// success ack (isError=false) rather than as a tool error — calling wake on
// a healthy agent is a no-op success per QUM-724 acceptance criteria.
func TestToolWake_NoOp(t *testing.T) {
	mock := newWakeAware()
	mock.wakeErr = supervisor.ErrWakeNotNeeded
	srv := New(mock)

	msg := makeJSONRPCRequest(722, "tools/call", map[string]any{
		"name":      "wake",
		"arguments": map[string]any{"agent": "alice"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.wakeCalls != 1 {
		t.Fatalf("supervisor.Wake calls = %d, want 1", mock.wakeCalls)
	}

	parsed := parseJSONRPCResponse(t, resp)
	text, isErr := resultContentText(t, parsed)
	if isErr {
		t.Errorf("isError = true on ErrWakeNotNeeded path; want success ack. text=%q", text)
	}
	if !contains(text, "already") && !contains(text, "running") && !contains(text, "healthy") && !contains(text, "no-op") {
		t.Errorf("no-op ack text should mention already-running/healthy/no-op; got %q", text)
	}
}

func TestToolWake_PassesError(t *testing.T) {
	mock := newWakeAware()
	mock.wakeErr = fmt.Errorf("session id rejected and fresh start failed: %w", errors.New("ENOENT"))
	srv := New(mock)

	msg := makeJSONRPCRequest(723, "tools/call", map[string]any{
		"name":      "wake",
		"arguments": map[string]any{"agent": "alice"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.wakeCalls != 1 {
		t.Fatalf("supervisor.Wake calls = %d, want 1", mock.wakeCalls)
	}

	parsed := parseJSONRPCResponse(t, resp)
	text, isErr := resultContentText(t, parsed)
	if !isErr {
		t.Errorf("isError = false on supervisor-error path; want true. text=%q", text)
	}
	if !contains(text, "session id rejected") {
		t.Errorf("error text = %q, want supervisor error surfaced", text)
	}
}

func TestToolWake_MissingAgent(t *testing.T) {
	mock := newWakeAware()
	srv := New(mock)

	msg := makeJSONRPCRequest(724, "tools/call", map[string]any{
		"name":      "wake",
		"arguments": json.RawMessage(`{"agent": ""}`),
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.wakeCalls != 0 {
		t.Errorf("supervisor.Wake called %d times on empty agent; want 0", mock.wakeCalls)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		return // top-level JSON-RPC error is acceptable
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result and no top-level error")
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("empty-agent produced isError=false; want true. result=%v", result)
	}
}
