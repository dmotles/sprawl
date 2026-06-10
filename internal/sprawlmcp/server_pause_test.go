package sprawlmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// QUM-722: pause MCP tool dispatch.
//
// Schema (tools.go) — required arg `agent`; optional `timeout_seconds`,
// `cascade` (default true). Dispatcher calls supervisor.Pause and surfaces
// PauseResult.Outcome in the success text.

// pauseSupervisorMock embeds mockSupervisor and adds Pause recording.
type pauseSupervisorMock struct {
	mockSupervisor

	pauseAgent  string
	pauseOpts   supervisor.PauseOptions
	pauseResult *supervisor.PauseResult
	pauseErr    error
	pauseCalls  int
}

func (p *pauseSupervisorMock) Pause(_ context.Context, name string, opts supervisor.PauseOptions) (*supervisor.PauseResult, error) {
	p.pauseCalls++
	p.pauseAgent = name
	p.pauseOpts = opts
	if p.pauseErr != nil {
		return nil, p.pauseErr
	}
	if p.pauseResult != nil {
		return p.pauseResult, nil
	}
	return &supervisor.PauseResult{Outcome: "paused"}, nil
}

func TestServer_ToolPause_HappyPath(t *testing.T) {
	mock := &pauseSupervisorMock{
		pauseResult: &supervisor.PauseResult{Outcome: "paused"},
	}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(30, "tools/call", map[string]any{
		"name": "pause",
		"arguments": map[string]any{
			"agent": "ratz",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.pauseCalls != 1 {
		t.Errorf("Pause calls = %d, want 1", mock.pauseCalls)
	}
	if mock.pauseAgent != "ratz" {
		t.Errorf("pauseAgent = %q, want ratz", mock.pauseAgent)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", parsed["error"])
	}
	result, _ := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Errorf("isError=true on happy-path pause: %v", result)
	}
	text := extractToolText(t, resp)
	if !strings.Contains(text, "paused") {
		t.Errorf("response text = %q, want it to mention outcome 'paused'", text)
	}
}

func TestServer_ToolPause_EscalatesReadsConfig(t *testing.T) {
	mock := &pauseSupervisorMock{}
	srv := New(mock)
	ctx := context.Background()

	// Omit timeout_seconds — server must fall back to config
	// PauseTimeoutSeconds (default 30).
	msg := makeJSONRPCRequest(31, "tools/call", map[string]any{
		"name": "pause",
		"arguments": map[string]any{
			"agent": "ratz",
		},
	})
	_, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.pauseOpts.Timeout.Seconds() != 30 {
		t.Errorf("Pause timeout = %v, want 30s (config default)", mock.pauseOpts.Timeout)
	}
}

func TestServer_ToolPause_CascadeDefaultTrue(t *testing.T) {
	mock := &pauseSupervisorMock{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(32, "tools/call", map[string]any{
		"name": "pause",
		"arguments": map[string]any{
			"agent": "ratz",
		},
	})
	if _, err := srv.HandleMessage(ctx, msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !mock.pauseOpts.Cascade {
		t.Errorf("Cascade default = false, want true")
	}
}

func TestServer_Tools_PauseRegistered(t *testing.T) {
	srv := New(&pauseSupervisorMock{})
	msg := makeJSONRPCRequest(33, "tools/list", nil)
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result, _ := parsed["result"].(map[string]any)
	toolsRaw, _ := result["tools"].([]any)

	for _, raw := range toolsRaw {
		tool, _ := raw.(map[string]any)
		if name, _ := tool["name"].(string); name == "pause" {
			schema, ok := tool["inputSchema"].(map[string]any)
			if !ok {
				t.Fatal("pause tool missing inputSchema")
			}
			props, _ := schema["properties"].(map[string]any)
			for _, want := range []string{"agent", "timeout_seconds", "cascade"} {
				if _, ok := props[want]; !ok {
					t.Errorf("pause inputSchema missing property %q", want)
				}
			}
			required, _ := schema["required"].([]any)
			sawAgent := false
			for _, r := range required {
				if rs, _ := r.(string); rs == "agent" {
					sawAgent = true
				}
			}
			if !sawAgent {
				t.Error("pause inputSchema required must include 'agent'")
			}
			return
		}
	}
	t.Error("pause tool not registered in tools/list")
}

// TestServer_ToolPause_ConfigOverridesDefault asserts the dispatcher honors
// the project config's PauseTimeoutSeconds when timeout_seconds is omitted
// (QUM-722 code-review nit #1).
func TestServer_ToolPause_ConfigOverridesDefault(t *testing.T) {
	mock := &pauseSupervisorMock{}
	srv := New(mock).WithConfig(&config.Config{PauseTimeoutSeconds: 7})

	msg := makeJSONRPCRequest(35, "tools/call", map[string]any{
		"name": "pause",
		"arguments": map[string]any{
			"agent": "ratz",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.pauseOpts.Timeout != 7*time.Second {
		t.Errorf("Pause timeout = %v, want 7s (config override)", mock.pauseOpts.Timeout)
	}
}

// TestServer_ToolPause_NilConfigFallsBackToDefault asserts that when no
// config has been attached, the dispatcher falls back to
// config.DefaultPauseTimeoutSeconds (QUM-722 code-review nit #1).
func TestServer_ToolPause_NilConfigFallsBackToDefault(t *testing.T) {
	mock := &pauseSupervisorMock{}
	srv := New(mock) // no WithConfig

	msg := makeJSONRPCRequest(36, "tools/call", map[string]any{
		"name": "pause",
		"arguments": map[string]any{
			"agent": "ratz",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	want := time.Duration(config.DefaultPauseTimeoutSeconds) * time.Second
	if mock.pauseOpts.Timeout != want {
		t.Errorf("Pause timeout = %v, want %v (default fallback)", mock.pauseOpts.Timeout, want)
	}
}

// Anchor a compile-time check that tools.go exports a JSON schema description
// that mentions the escalation behavior, so prompt callers (and the sync test)
// can rely on the wording.
func TestServer_ToolPause_DescriptionMentionsEscalation(t *testing.T) {
	srv := New(&pauseSupervisorMock{})
	msg := makeJSONRPCRequest(34, "tools/list", nil)
	resp, _ := srv.HandleMessage(context.Background(), msg)
	parsed := parseJSONRPCResponse(t, resp)
	result, _ := parsed["result"].(map[string]any)
	toolsRaw, _ := result["tools"].([]any)
	for _, raw := range toolsRaw {
		tool, _ := raw.(map[string]any)
		if name, _ := tool["name"].(string); name == "pause" {
			desc, _ := tool["description"].(string)
			if !strings.Contains(strings.ToLower(desc), "kill") {
				t.Errorf("pause description should mention escalation to kill; got %q", desc)
			}
			return
		}
	}
}

// Compile-time hint: the JSON-RPC payload encoder needs to round-trip the
// new PauseResult — guard with a tiny marshal test.
func TestPauseResult_JSONShape(t *testing.T) {
	res := supervisor.PauseResult{Outcome: "paused", WaitMs: 42, Cascade: []string{"kidA"}}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{`"outcome":"paused"`, `"wait_ms":42`, `"cascade":["kidA"]`} {
		if !strings.Contains(got, want) {
			t.Errorf("Marshal(PauseResult) = %s, want substring %q", got, want)
		}
	}
}
