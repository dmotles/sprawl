package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// mockSupervisor implements supervisor.Supervisor for testing.
type mockSupervisor struct {
	statusResult []supervisor.AgentInfo
	statusErr    error
	spawnResult  *supervisor.AgentInfo
	spawnErr     error
	delegateErr  error
	messageErr   error
	mergeErr     error
	retireErr    error
	killErr      error
	shutdownErr  error

	// Recorded calls
	spawnCalled    *supervisor.SpawnRequest
	delegateAgent  string
	delegateTask   string
	messageAgent   string
	messageSubject string
	messageBody    string
	mergeAgent     string
	mergeMessage   string
	mergeNoVal     bool
	retireAgent    string
	retireMerge    bool
	retireAbandon  bool
	killAgent      string
	handoffSummary string
	handoffErr     error
	handoffCh      chan struct{}
}

func (m *mockSupervisor) Spawn(_ context.Context, req supervisor.SpawnRequest) (*supervisor.AgentInfo, error) {
	m.spawnCalled = &req
	return m.spawnResult, m.spawnErr
}

func (m *mockSupervisor) Status(_ context.Context) ([]supervisor.AgentInfo, error) {
	return m.statusResult, m.statusErr
}

func (m *mockSupervisor) Delegate(_ context.Context, agentName, task string) error {
	m.delegateAgent = agentName
	m.delegateTask = task
	return m.delegateErr
}

func (m *mockSupervisor) Message(_ context.Context, agentName, subject, body string) error {
	m.messageAgent = agentName
	m.messageSubject = subject
	m.messageBody = body
	return m.messageErr
}

func (m *mockSupervisor) Merge(_ context.Context, agentName, message string, noValidate bool) error {
	m.mergeAgent = agentName
	m.mergeMessage = message
	m.mergeNoVal = noValidate
	return m.mergeErr
}

func (m *mockSupervisor) Retire(_ context.Context, agentName string, merge, abandon bool) error {
	m.retireAgent = agentName
	m.retireMerge = merge
	m.retireAbandon = abandon
	return m.retireErr
}

func (m *mockSupervisor) Kill(_ context.Context, agentName string) error {
	m.killAgent = agentName
	return m.killErr
}

func (m *mockSupervisor) Shutdown(_ context.Context) error {
	return m.shutdownErr
}

func (m *mockSupervisor) Handoff(_ context.Context, summary string) error {
	m.handoffSummary = summary
	return m.handoffErr
}

func (m *mockSupervisor) HandoffRequested() <-chan struct{} {
	if m.handoffCh == nil {
		m.handoffCh = make(chan struct{}, 1)
	}
	return m.handoffCh
}

// makeJSONRPCRequest builds a raw JSON-RPC request for testing.
func makeJSONRPCRequest(id int, method string, params any) json.RawMessage {
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	return data
}

// parseJSONRPCResponse parses a JSON-RPC response.
func parseJSONRPCResponse(t *testing.T, data json.RawMessage) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

func TestServer_Initialize(t *testing.T) {
	srv := New(&mockSupervisor{})
	ctx := context.Background()

	msg := makeJSONRPCRequest(1, "initialize", nil)
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if parsed["jsonrpc"] != "2.0" {
		t.Errorf("jsonrpc = %v, want 2.0", parsed["jsonrpc"])
	}

	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result in initialize response")
	}

	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", result["protocolVersion"])
	}

	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatal("missing serverInfo")
	}
	if serverInfo["name"] != "sprawl-ops" {
		t.Errorf("serverInfo.name = %v, want sprawl-ops", serverInfo["name"])
	}
}

func TestServer_ToolsList(t *testing.T) {
	srv := New(&mockSupervisor{})
	ctx := context.Background()

	msg := makeJSONRPCRequest(2, "tools/list", nil)
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}

	toolsRaw, ok := result["tools"].([]any)
	if !ok {
		t.Fatal("missing tools array")
	}

	expectedTools := []string{
		"sprawl_spawn",
		"sprawl_status",
		"sprawl_delegate",
		"sprawl_message",
		"sprawl_merge",
		"sprawl_retire",
		"sprawl_kill",
		"sprawl_handoff",
	}

	if len(toolsRaw) != len(expectedTools) {
		t.Fatalf("got %d tools, want %d", len(toolsRaw), len(expectedTools))
	}

	toolNames := make(map[string]bool)
	for _, raw := range toolsRaw {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("tool is not an object")
		}
		name, ok := tool["name"].(string)
		if !ok {
			t.Fatal("tool name is not a string")
		}
		toolNames[name] = true

		// Every tool must have a description and inputSchema
		if _, ok := tool["description"].(string); !ok {
			t.Errorf("tool %s missing description", name)
		}
		if _, ok := tool["inputSchema"].(map[string]any); !ok {
			t.Errorf("tool %s missing inputSchema", name)
		}
	}

	for _, name := range expectedTools {
		if !toolNames[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestServer_ToolsCall_SprawlStatus(t *testing.T) {
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "ratz", Type: "engineer", Family: "engineering", Status: "active", Branch: "dmotles/feature"},
			{Name: "ghost", Type: "researcher", Family: "engineering", Status: "active", Branch: "dmotles/research"},
		},
	}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(3, "tools/call", map[string]any{
		"name":      "sprawl_status",
		"arguments": map[string]any{},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}

	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("missing or empty content array")
	}

	// Content should contain text with agent info
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatal("content[0] is not an object")
	}
	if first["type"] != "text" {
		t.Errorf("content[0].type = %v, want text", first["type"])
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatal("content[0].text is not a string")
	}

	// Verify the text contains agent information
	if len(text) == 0 {
		t.Error("status text is empty")
	}
}

func TestServer_ToolsCall_SprawlSpawn(t *testing.T) {
	mock := &mockSupervisor{
		spawnResult: &supervisor.AgentInfo{
			Name:   "chip",
			Type:   "engineer",
			Family: "engineering",
			Status: "active",
			Branch: "dmotles/feature",
		},
	}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(4, "tools/call", map[string]any{
		"name": "sprawl_spawn",
		"arguments": map[string]any{
			"family": "engineering",
			"type":   "engineer",
			"prompt": "implement feature X",
			"branch": "dmotles/feature",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.spawnCalled == nil {
		t.Fatal("Spawn was not called")
	}
	if mock.spawnCalled.Family != "engineering" {
		t.Errorf("spawn family = %q, want engineering", mock.spawnCalled.Family)
	}
	if mock.spawnCalled.Type != "engineer" {
		t.Errorf("spawn type = %q, want engineer", mock.spawnCalled.Type)
	}
	if mock.spawnCalled.Prompt != "implement feature X" {
		t.Errorf("spawn prompt = %q, want 'implement feature X'", mock.spawnCalled.Prompt)
	}
	if mock.spawnCalled.Branch != "dmotles/feature" {
		t.Errorf("spawn branch = %q, want dmotles/feature", mock.spawnCalled.Branch)
	}

	// Verify response is successful
	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error in response: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlDelegate(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(5, "tools/call", map[string]any{
		"name": "sprawl_delegate",
		"arguments": map[string]any{
			"agent_name": "ratz",
			"task":       "implement feature Y",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.delegateAgent != "ratz" {
		t.Errorf("delegate agent = %q, want ratz", mock.delegateAgent)
	}
	if mock.delegateTask != "implement feature Y" {
		t.Errorf("delegate task = %q, want 'implement feature Y'", mock.delegateTask)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlMessage(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(6, "tools/call", map[string]any{
		"name": "sprawl_message",
		"arguments": map[string]any{
			"agent_name": "ghost",
			"subject":    "status update",
			"body":       "work is done",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.messageAgent != "ghost" {
		t.Errorf("message agent = %q, want ghost", mock.messageAgent)
	}
	if mock.messageSubject != "status update" {
		t.Errorf("message subject = %q, want 'status update'", mock.messageSubject)
	}
	if mock.messageBody != "work is done" {
		t.Errorf("message body = %q, want 'work is done'", mock.messageBody)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlMerge(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(7, "tools/call", map[string]any{
		"name": "sprawl_merge",
		"arguments": map[string]any{
			"agent_name":  "ratz",
			"message":     "merge commit msg",
			"no_validate": true,
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.mergeAgent != "ratz" {
		t.Errorf("merge agent = %q, want ratz", mock.mergeAgent)
	}
	if mock.mergeMessage != "merge commit msg" {
		t.Errorf("merge message = %q, want 'merge commit msg'", mock.mergeMessage)
	}
	if !mock.mergeNoVal {
		t.Error("merge noValidate = false, want true")
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlRetire(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(8, "tools/call", map[string]any{
		"name": "sprawl_retire",
		"arguments": map[string]any{
			"agent_name": "ratz",
			"merge":      true,
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.retireAgent != "ratz" {
		t.Errorf("retire agent = %q, want ratz", mock.retireAgent)
	}
	if !mock.retireMerge {
		t.Error("retire merge = false, want true")
	}
	if mock.retireAbandon {
		t.Error("retire abandon = true, want false")
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlKill(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(9, "tools/call", map[string]any{
		"name": "sprawl_kill",
		"arguments": map[string]any{
			"agent_name": "ratz",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.killAgent != "ratz" {
		t.Errorf("kill agent = %q, want ratz", mock.killAgent)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlHandoff(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(20, "tools/call", map[string]any{
		"name": "sprawl_handoff",
		"arguments": map[string]any{
			"summary": "## What happened\nmerged QUM-263",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.handoffSummary != "## What happened\nmerged QUM-263" {
		t.Errorf("handoff summary = %q, want the posted body", mock.handoffSummary)
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
		t.Errorf("unexpected isError=true result: %v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("missing content")
	}
	first := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if text == "" {
		t.Error("empty text response — weave needs some acknowledgement string")
	}
}

func TestServer_ToolsCall_SprawlHandoff_SupervisorError(t *testing.T) {
	mock := &mockSupervisor{handoffErr: fmt.Errorf("no session")}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(21, "tools/call", map[string]any{
		"name":      "sprawl_handoff",
		"arguments": map[string]any{"summary": "body"},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("expected MCP content result, not JSON-RPC error")
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true for supervisor error")
	}
}

func TestServer_ToolsCall_UnknownTool(t *testing.T) {
	srv := New(&mockSupervisor{})
	ctx := context.Background()

	msg := makeJSONRPCRequest(10, "tools/call", map[string]any{
		"name":      "unknown_tool",
		"arguments": map[string]any{},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("expected JSON-RPC error for unknown tool")
	}
	if errObj["code"] == nil {
		t.Error("error missing code")
	}
}

func TestServer_ToolsCall_SupervisorError(t *testing.T) {
	mock := &mockSupervisor{
		delegateErr: fmt.Errorf("agent not found"),
	}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(11, "tools/call", map[string]any{
		"name": "sprawl_delegate",
		"arguments": map[string]any{
			"agent_name": "nonexistent",
			"task":       "do something",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	// Supervisor errors should be returned as MCP tool error content, not JSON-RPC errors
	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result - supervisor errors should be MCP content errors, not JSON-RPC errors")
	}

	isError, _ := result["isError"].(bool)
	if !isError {
		t.Error("expected isError=true for supervisor error")
	}
}

func TestServer_NotificationsInitialized(t *testing.T) {
	srv := New(&mockSupervisor{})
	ctx := context.Background()

	// Notification has no id field
	msg := json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	// Notifications should return nil (no response)
	if resp != nil {
		t.Errorf("expected nil response for notification, got: %s", string(resp))
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	srv := New(&mockSupervisor{})
	ctx := context.Background()

	msg := makeJSONRPCRequest(12, "unknown/method", nil)
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	errObj, ok := parsed["error"].(map[string]any)
	if !ok {
		t.Fatal("expected JSON-RPC error for unknown method")
	}
	code, _ := errObj["code"].(float64)
	if code != -32601 {
		t.Errorf("error code = %v, want -32601 (method not found)", code)
	}
}
