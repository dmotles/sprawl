package sprawlmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
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

	// SendAsync / Peek recording + seams
	sendAsyncTo       string
	sendAsyncSubject  string
	sendAsyncBody     string
	sendAsyncReplyTo  string
	sendAsyncTags     []string
	sendAsyncResult   *supervisor.SendAsyncResult
	sendAsyncErr      error
	peekAgent         string
	peekTail          int
	peekResult        *supervisor.PeekResult
	peekErr           error
	peekActivityAgent string
	peekActivityTail  int
	peekActivityRes   []agentloop.ActivityEntry
	peekActivityErr   error

	// ReportStatus recording + seams
	reportStatusAgent   string
	reportStatusState   string
	reportStatusSummary string
	reportStatusDetail  string
	reportStatusResult  *supervisor.ReportStatusResult
	reportStatusErr     error

	// SendInterrupt recording + seams
	sendInterruptTo         string
	sendInterruptSubject    string
	sendInterruptBody       string
	sendInterruptResumeHint string
	sendInterruptResult     *supervisor.SendInterruptResult
	sendInterruptErr        error

	// Messages* recording + seams (QUM-316)
	messagesListFilter    string
	messagesListLimit     int
	messagesListResult    *supervisor.MessagesListResult
	messagesListErr       error
	messagesReadID        string
	messagesReadResult    *supervisor.MessagesReadResult
	messagesReadErr       error
	messagesArchiveID     string
	messagesArchiveResult *supervisor.MessagesArchiveResult
	messagesArchiveErr    error
	messagesPeekCalled    bool
	messagesPeekResult    *supervisor.MessagesPeekResult
	messagesPeekErr       error
}

func (m *mockSupervisor) MessagesList(_ context.Context, filter string, limit int) (*supervisor.MessagesListResult, error) {
	m.messagesListFilter = filter
	m.messagesListLimit = limit
	if m.messagesListErr != nil {
		return nil, m.messagesListErr
	}
	if m.messagesListResult != nil {
		return m.messagesListResult, nil
	}
	return &supervisor.MessagesListResult{Agent: "weave", Filter: filter}, nil
}

func (m *mockSupervisor) MessagesRead(_ context.Context, id string) (*supervisor.MessagesReadResult, error) {
	m.messagesReadID = id
	if m.messagesReadErr != nil {
		return nil, m.messagesReadErr
	}
	if m.messagesReadResult != nil {
		return m.messagesReadResult, nil
	}
	return &supervisor.MessagesReadResult{ID: id}, nil
}

func (m *mockSupervisor) MessagesArchive(_ context.Context, id string) (*supervisor.MessagesArchiveResult, error) {
	m.messagesArchiveID = id
	if m.messagesArchiveErr != nil {
		return nil, m.messagesArchiveErr
	}
	if m.messagesArchiveResult != nil {
		return m.messagesArchiveResult, nil
	}
	return &supervisor.MessagesArchiveResult{ID: id, Archived: true}, nil
}

func (m *mockSupervisor) MessagesPeek(_ context.Context) (*supervisor.MessagesPeekResult, error) {
	m.messagesPeekCalled = true
	if m.messagesPeekErr != nil {
		return nil, m.messagesPeekErr
	}
	if m.messagesPeekResult != nil {
		return m.messagesPeekResult, nil
	}
	return &supervisor.MessagesPeekResult{Agent: "weave"}, nil
}

func (m *mockSupervisor) ReportStatus(_ context.Context, agentName, reportState, summary, detail string) (*supervisor.ReportStatusResult, error) {
	m.reportStatusAgent = agentName
	m.reportStatusState = reportState
	m.reportStatusSummary = summary
	m.reportStatusDetail = detail
	if m.reportStatusErr != nil {
		return nil, m.reportStatusErr
	}
	if m.reportStatusResult != nil {
		return m.reportStatusResult, nil
	}
	return &supervisor.ReportStatusResult{ReportedAt: "2026-04-21T10:00:00Z"}, nil
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

func (m *mockSupervisor) PeekActivity(_ context.Context, agentName string, tail int) ([]agentloop.ActivityEntry, error) {
	m.peekActivityAgent = agentName
	m.peekActivityTail = tail
	return m.peekActivityRes, m.peekActivityErr
}

func (m *mockSupervisor) SendAsync(_ context.Context, to, subject, body, replyTo string, tags []string) (*supervisor.SendAsyncResult, error) {
	m.sendAsyncTo = to
	m.sendAsyncSubject = subject
	m.sendAsyncBody = body
	m.sendAsyncReplyTo = replyTo
	m.sendAsyncTags = tags
	if m.sendAsyncErr != nil {
		return nil, m.sendAsyncErr
	}
	if m.sendAsyncResult != nil {
		return m.sendAsyncResult, nil
	}
	return &supervisor.SendAsyncResult{MessageID: "msg_stub", QueuedAt: "2026-04-21T00:00:00Z"}, nil
}

func (m *mockSupervisor) Peek(_ context.Context, agentName string, tail int) (*supervisor.PeekResult, error) {
	m.peekAgent = agentName
	m.peekTail = tail
	if m.peekErr != nil {
		return nil, m.peekErr
	}
	if m.peekResult != nil {
		return m.peekResult, nil
	}
	return &supervisor.PeekResult{Status: "active"}, nil
}

func (m *mockSupervisor) SendInterrupt(_ context.Context, to, subject, body, resumeHint string) (*supervisor.SendInterruptResult, error) {
	m.sendInterruptTo = to
	m.sendInterruptSubject = subject
	m.sendInterruptBody = body
	m.sendInterruptResumeHint = resumeHint
	if m.sendInterruptErr != nil {
		return nil, m.sendInterruptErr
	}
	if m.sendInterruptResult != nil {
		return m.sendInterruptResult, nil
	}
	return &supervisor.SendInterruptResult{MessageID: "msg_int", DeliveredAt: "2026-04-21T00:00:00Z", Interrupted: true}, nil
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
		"sprawl_send_async",
		"sprawl_send_interrupt",
		"sprawl_peek",
		"sprawl_report_status",
		"sprawl_message",
		"sprawl_merge",
		"sprawl_retire",
		"sprawl_kill",
		"sprawl_handoff",
		"sprawl_messages_list",
		"sprawl_messages_read",
		"sprawl_messages_archive",
		"sprawl_messages_peek",
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

	// Deprecated alias now routes through SendAsync, so Message() should not be called.
	if mock.messageAgent != "" {
		t.Errorf("legacy Message() was called but alias must route through SendAsync; got agent %q", mock.messageAgent)
	}
	if mock.sendAsyncTo != "ghost" {
		t.Errorf("sendAsync to = %q, want ghost", mock.sendAsyncTo)
	}
	if mock.sendAsyncSubject != "status update" {
		t.Errorf("sendAsync subject = %q, want 'status update'", mock.sendAsyncSubject)
	}
	if mock.sendAsyncBody != "work is done" {
		t.Errorf("sendAsync body = %q, want 'work is done'", mock.sendAsyncBody)
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

func TestServer_ToolsCall_SprawlSendAsync(t *testing.T) {
	mock := &mockSupervisor{
		sendAsyncResult: &supervisor.SendAsyncResult{
			MessageID: "abc-123",
			QueuedAt:  "2026-04-21T10:00:00Z",
		},
	}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(30, "tools/call", map[string]any{
		"name": "sprawl_send_async",
		"arguments": map[string]any{
			"to":       "ghost",
			"subject":  "status",
			"body":     "all done",
			"reply_to": "prev-msg",
			"tags":     []string{"status", "fyi"},
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.sendAsyncTo != "ghost" {
		t.Errorf("to = %q, want ghost", mock.sendAsyncTo)
	}
	if mock.sendAsyncSubject != "status" {
		t.Errorf("subject = %q", mock.sendAsyncSubject)
	}
	if mock.sendAsyncReplyTo != "prev-msg" {
		t.Errorf("reply_to = %q", mock.sendAsyncReplyTo)
	}
	if len(mock.sendAsyncTags) != 2 || mock.sendAsyncTags[0] != "status" {
		t.Errorf("tags = %v", mock.sendAsyncTags)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Fatalf("unexpected error: %v", parsed["error"])
	}
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError=true: %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)

	// Response body should contain the returned message_id and queued_at.
	var body struct {
		MessageID string `json:"message_id"`
		QueuedAt  string `json:"queued_at"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal response text as JSON: %v (text=%q)", err, text)
	}
	if body.MessageID != "abc-123" {
		t.Errorf("message_id = %q, want abc-123", body.MessageID)
	}
	if body.QueuedAt != "2026-04-21T10:00:00Z" {
		t.Errorf("queued_at = %q", body.QueuedAt)
	}
}

func TestServer_ToolsCall_SprawlSendAsync_SupervisorError(t *testing.T) {
	mock := &mockSupervisor{sendAsyncErr: fmt.Errorf("agent not found")}
	srv := New(mock)

	msg := makeJSONRPCRequest(31, "tools/call", map[string]any{
		"name":      "sprawl_send_async",
		"arguments": map[string]any{"to": "x", "subject": "s", "body": "b"},
	})
	resp, _ := srv.HandleMessage(context.Background(), msg)

	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("expected MCP content result")
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true")
	}
}

func TestServer_ToolsCall_SprawlSendInterrupt(t *testing.T) {
	mock := &mockSupervisor{
		sendInterruptResult: &supervisor.SendInterruptResult{
			MessageID:   "int-42",
			DeliveredAt: "2026-04-21T11:00:00Z",
			Interrupted: true,
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(40, "tools/call", map[string]any{
		"name": "sprawl_send_interrupt",
		"arguments": map[string]any{
			"to":          "ghost",
			"subject":     "urgent",
			"body":        "stop what you are doing",
			"resume_hint": "you were implementing X",
		},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.sendInterruptTo != "ghost" {
		t.Errorf("to = %q, want ghost", mock.sendInterruptTo)
	}
	if mock.sendInterruptSubject != "urgent" {
		t.Errorf("subject = %q", mock.sendInterruptSubject)
	}
	if mock.sendInterruptBody != "stop what you are doing" {
		t.Errorf("body = %q", mock.sendInterruptBody)
	}
	if mock.sendInterruptResumeHint != "you were implementing X" {
		t.Errorf("resume_hint = %q", mock.sendInterruptResumeHint)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)

	var body supervisor.SendInterruptResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v (text=%q)", err, text)
	}
	if body.MessageID != "int-42" {
		t.Errorf("message_id = %q, want int-42", body.MessageID)
	}
	if !body.Interrupted {
		t.Error("interrupted = false, want true")
	}
}

func TestServer_ToolsCall_SprawlSendInterrupt_SupervisorError(t *testing.T) {
	mock := &mockSupervisor{sendInterruptErr: fmt.Errorf("not an ancestor")}
	srv := New(mock)

	msg := makeJSONRPCRequest(41, "tools/call", map[string]any{
		"name": "sprawl_send_interrupt",
		"arguments": map[string]any{
			"to":      "ghost",
			"subject": "s",
			"body":    "b",
		},
	})
	resp, _ := srv.HandleMessage(context.Background(), msg)
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true for ancestor-gate rejection")
	}
}

func TestServer_ToolsList_IncludesSendInterrupt(t *testing.T) {
	srv := New(&mockSupervisor{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(42, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)
	found := false
	for _, tAny := range tools {
		tm := tAny.(map[string]any)
		if tm["name"] == "sprawl_send_interrupt" {
			found = true
			break
		}
	}
	if !found {
		t.Error("sprawl_send_interrupt missing from tools/list")
	}
}

func TestServer_ToolsCall_SprawlPeek(t *testing.T) {
	mock := &mockSupervisor{
		peekResult: &supervisor.PeekResult{
			Status:     "active",
			LastReport: supervisor.LastReport{Type: "status", Message: "working", At: "2026-04-21T09:00:00Z"},
			Activity:   []agentloop.ActivityEntry{{Kind: "assistant_text", Summary: "hi"}},
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(32, "tools/call", map[string]any{
		"name":      "sprawl_peek",
		"arguments": map[string]any{"agent": "ghost", "tail": 50},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.peekAgent != "ghost" {
		t.Errorf("agent = %q, want ghost", mock.peekAgent)
	}
	if mock.peekTail != 50 {
		t.Errorf("tail = %d, want 50", mock.peekTail)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)

	var body supervisor.PeekResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Status != "active" {
		t.Errorf("status = %q", body.Status)
	}
	if body.LastReport.Type != "status" {
		t.Errorf("last_report.type = %q", body.LastReport.Type)
	}
	if len(body.Activity) != 1 || body.Activity[0].Kind != "assistant_text" {
		t.Errorf("activity = %v", body.Activity)
	}
}

func TestServer_ToolsCall_SprawlPeek_DefaultTail(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(33, "tools/call", map[string]any{
		"name":      "sprawl_peek",
		"arguments": map[string]any{"agent": "ghost"},
	})
	_, _ = srv.HandleMessage(context.Background(), msg)

	if mock.peekTail != 20 {
		t.Errorf("default tail = %d, want 20", mock.peekTail)
	}
}

func TestServer_ToolsCall_SprawlReportStatus(t *testing.T) {
	mock := &mockSupervisor{
		reportStatusResult: &supervisor.ReportStatusResult{ReportedAt: "2026-04-21T10:00:00Z"},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(35, "tools/call", map[string]any{
		"name": "sprawl_report_status",
		"arguments": map[string]any{
			"state":   "working",
			"summary": "halfway done",
			"detail":  "wrote 3 tests",
		},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.reportStatusState != "working" {
		t.Errorf("state = %q", mock.reportStatusState)
	}
	if mock.reportStatusSummary != "halfway done" {
		t.Errorf("summary = %q", mock.reportStatusSummary)
	}
	if mock.reportStatusDetail != "wrote 3 tests" {
		t.Errorf("detail = %q", mock.reportStatusDetail)
	}
	// MCP tool passes empty agentName — supervisor uses its own callerName.
	if mock.reportStatusAgent != "" {
		t.Errorf("agentName = %q, want empty (caller-resolved)", mock.reportStatusAgent)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)

	var body struct {
		ReportedAt string `json:"reported_at"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v (text=%q)", err, text)
	}
	if body.ReportedAt != "2026-04-21T10:00:00Z" {
		t.Errorf("reported_at = %q", body.ReportedAt)
	}
}

func TestServer_ToolsCall_SprawlReportStatus_SupervisorError(t *testing.T) {
	mock := &mockSupervisor{reportStatusErr: fmt.Errorf("invalid state")}
	srv := New(mock)

	msg := makeJSONRPCRequest(36, "tools/call", map[string]any{
		"name":      "sprawl_report_status",
		"arguments": map[string]any{"state": "bogus", "summary": "x"},
	})
	resp, _ := srv.HandleMessage(context.Background(), msg)

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true for supervisor error")
	}
}

func TestServer_ToolsList_IncludesReportStatus(t *testing.T) {
	srv := New(&mockSupervisor{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(37, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)
	found := false
	for _, tool := range tools {
		m := tool.(map[string]any)
		if m["name"] == "sprawl_report_status" {
			found = true
			break
		}
	}
	if !found {
		t.Error("sprawl_report_status not in tools/list")
	}
}

func TestServer_ToolsCall_SprawlPeek_TailClamp(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(34, "tools/call", map[string]any{
		"name":      "sprawl_peek",
		"arguments": map[string]any{"agent": "ghost", "tail": 9999},
	})
	_, _ = srv.HandleMessage(context.Background(), msg)

	if mock.peekTail != 200 {
		t.Errorf("clamped tail = %d, want 200", mock.peekTail)
	}
}

// --- QUM-316: sprawl_messages_list / _read / _archive / _peek ---

func TestServer_ToolsCall_SprawlMessagesList(t *testing.T) {
	mock := &mockSupervisor{
		messagesListResult: &supervisor.MessagesListResult{
			Agent:  "weave",
			Filter: "unread",
			Count:  1,
			Messages: []supervisor.MessageSummary{
				{ID: "abc", FullID: "1700000000.ratz.deadbeef", From: "ratz", Subject: "hi", Timestamp: "2026-04-21T10:00:00Z", Read: false, Dir: "new"},
			},
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(50, "tools/call", map[string]any{
		"name":      "sprawl_messages_list",
		"arguments": map[string]any{"filter": "unread", "limit": 25},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.messagesListFilter != "unread" {
		t.Errorf("filter = %q, want unread", mock.messagesListFilter)
	}
	if mock.messagesListLimit != 25 {
		t.Errorf("limit = %d, want 25", mock.messagesListLimit)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var body supervisor.MessagesListResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v (text=%q)", err, text)
	}
	if body.Count != 1 || len(body.Messages) != 1 || body.Messages[0].ID != "abc" {
		t.Errorf("body = %+v", body)
	}
}

func TestServer_ToolsCall_SprawlMessagesList_DefaultFilter(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(51, "tools/call", map[string]any{
		"name":      "sprawl_messages_list",
		"arguments": map[string]any{},
	})
	_, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.messagesListFilter != "" {
		t.Errorf("default filter = %q, want empty (supervisor treats as all)", mock.messagesListFilter)
	}
	if mock.messagesListLimit != 0 {
		t.Errorf("default limit = %d, want 0", mock.messagesListLimit)
	}
}

func TestServer_ToolsCall_SprawlMessagesList_SupervisorError(t *testing.T) {
	mock := &mockSupervisor{messagesListErr: fmt.Errorf("bad filter")}
	srv := New(mock)

	msg := makeJSONRPCRequest(52, "tools/call", map[string]any{
		"name":      "sprawl_messages_list",
		"arguments": map[string]any{"filter": "bogus"},
	})
	resp, _ := srv.HandleMessage(context.Background(), msg)
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true")
	}
}

func TestServer_ToolsCall_SprawlMessagesRead(t *testing.T) {
	mock := &mockSupervisor{
		messagesReadResult: &supervisor.MessagesReadResult{
			ID: "abc", FullID: "1700000000.ratz.deadbeef",
			From: "ratz", To: "weave", Subject: "hi", Body: "hello body",
			Timestamp: "2026-04-21T10:00:00Z", Dir: "cur", WasUnread: true,
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(53, "tools/call", map[string]any{
		"name":      "sprawl_messages_read",
		"arguments": map[string]any{"id": "abc"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.messagesReadID != "abc" {
		t.Errorf("id = %q, want abc", mock.messagesReadID)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var body supervisor.MessagesReadResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Body != "hello body" {
		t.Errorf("body = %q", body.Body)
	}
	if !body.WasUnread {
		t.Error("was_unread = false, want true")
	}
}

func TestServer_ToolsCall_SprawlMessagesRead_NotFound(t *testing.T) {
	mock := &mockSupervisor{messagesReadErr: fmt.Errorf("not found")}
	srv := New(mock)

	msg := makeJSONRPCRequest(54, "tools/call", map[string]any{
		"name":      "sprawl_messages_read",
		"arguments": map[string]any{"id": "nope"},
	})
	resp, _ := srv.HandleMessage(context.Background(), msg)
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Error("expected isError=true")
	}
}

func TestServer_ToolsCall_SprawlMessagesArchive(t *testing.T) {
	mock := &mockSupervisor{
		messagesArchiveResult: &supervisor.MessagesArchiveResult{ID: "abc", FullID: "full", Archived: true},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(55, "tools/call", map[string]any{
		"name":      "sprawl_messages_archive",
		"arguments": map[string]any{"id": "abc"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.messagesArchiveID != "abc" {
		t.Errorf("id = %q", mock.messagesArchiveID)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var body supervisor.MessagesArchiveResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !body.Archived {
		t.Error("archived = false")
	}
}

func TestServer_ToolsCall_SprawlMessagesPeek(t *testing.T) {
	mock := &mockSupervisor{
		messagesPeekResult: &supervisor.MessagesPeekResult{
			Agent:       "weave",
			UnreadCount: 3,
			Preview: []supervisor.MessageSummary{
				{ID: "a", From: "ratz", Subject: "one"},
			},
		},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(56, "tools/call", map[string]any{
		"name":      "sprawl_messages_peek",
		"arguments": map[string]any{},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if !mock.messagesPeekCalled {
		t.Error("MessagesPeek not invoked")
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var body supervisor.MessagesPeekResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.UnreadCount != 3 {
		t.Errorf("unread_count = %d", body.UnreadCount)
	}
	if len(body.Preview) != 1 {
		t.Fatalf("preview len = %d", len(body.Preview))
	}
}
