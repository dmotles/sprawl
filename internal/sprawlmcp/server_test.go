package sprawlmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/sprawlmcp/calllog"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/supervisor/supervisortest"
)

// withTestCallerIdentity injects a caller identity into context, matching what
// the backend session does for child agents (QUM-387).
func withTestCallerIdentity(ctx context.Context, identity string) context.Context {
	return backendpkg.WithCallerIdentity(ctx, identity)
}

// mockSupervisor implements supervisor.Supervisor for testing. It embeds
// supervisortest.NoopSupervisor so new interface methods (QUM-531) don't
// require updating this mock when they're not exercised by these tests.
type mockSupervisor struct {
	supervisortest.NoopSupervisor

	statusResult []supervisor.AgentInfo
	statusErr    error
	spawnResult  *supervisor.AgentInfo
	spawnErr     error
	delegateErr  error
	mergeErr     error
	retireErr    error
	killErr      error
	shutdownErr  error

	// Recorded calls
	spawnCalled   *supervisor.SpawnRequest
	delegateAgent string
	delegateTask  string
	mergeCaller   string
	mergeAgent    string
	mergeMessage  string
	mergeNoVal    bool
	// mergeNoOp — QUM-511: when true, the mock should report a no-op
	// merge outcome (zero new commits) to toolMerge so it can surface
	// "Nothing to merge: <agent> has no new commits". Wired through once
	// Supervisor.Merge returns an outcome value (currently unused while
	// the signature is still error-only — that's the bug we're fixing).
	mergeNoOp        bool
	retireCaller     string
	retireAgent      string
	retireMerge      bool
	retireAbandon    bool
	retireCascade    bool
	retireNoValidate bool
	killAgent        string
	handoffSummary   string
	handoffErr       error
	handoffCh        chan struct{}

	// Peek recording + seams
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
	reportStatusResult  *supervisor.ReportStatusResult
	reportStatusErr     error

	// SendMessage recording + seams (QUM-550)
	sendMessageCalls     int
	sendMessageTo        string
	sendMessageBody      string
	sendMessageInterrupt bool
	sendMessageResult    *supervisor.SendMessageResult
	sendMessageErr       error

	// Messages* recording + seams (QUM-316)
	messagesListFilter       string
	messagesListLimit        int
	messagesListResult       *supervisor.MessagesListResult
	messagesListErr          error
	messagesReadID           string
	messagesReadResult       *supervisor.MessagesReadResult
	messagesReadErr          error
	messagesArchiveID        string
	messagesArchiveResult    *supervisor.MessagesArchiveResult
	messagesArchiveErr       error
	messagesArchiveAllMode   string
	messagesArchiveAllResult *supervisor.MessagesArchiveAllResult
	messagesArchiveAllErr    error
	messagesPeekCalled       bool
	messagesPeekResult       *supervisor.MessagesPeekResult
	messagesPeekErr          error

	// AskUserQuestion recording + seams (QUM-527 slice 2b)
	askQuestionCalls  int
	askQuestionLast   supervisor.QuestionRequest
	askQuestionResult supervisor.QuestionResponse
	askQuestionErr    error
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

func (m *mockSupervisor) MessagesArchiveAll(_ context.Context, mode string) (*supervisor.MessagesArchiveAllResult, error) {
	m.messagesArchiveAllMode = mode
	if m.messagesArchiveAllErr != nil {
		return nil, m.messagesArchiveAllErr
	}
	if m.messagesArchiveAllResult != nil {
		return m.messagesArchiveAllResult, nil
	}
	return &supervisor.MessagesArchiveAllResult{ArchivedCount: 0, Archived: true}, nil
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

func (m *mockSupervisor) ReportStatus(_ context.Context, agentName, reportState, summary string) (*supervisor.ReportStatusResult, error) {
	m.reportStatusAgent = agentName
	m.reportStatusState = reportState
	m.reportStatusSummary = summary
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

func (m *mockSupervisor) Merge(_ context.Context, caller, agentName, message string, noValidate bool) (*supervisor.MergeOutcome, error) {
	m.mergeCaller = caller
	m.mergeAgent = agentName
	m.mergeMessage = message
	m.mergeNoVal = noValidate
	if m.mergeErr != nil {
		return nil, m.mergeErr
	}
	return &supervisor.MergeOutcome{NoOp: m.mergeNoOp}, nil
}

func (m *mockSupervisor) Retire(_ context.Context, caller, agentName string, merge, abandon, cascade, noValidate bool) error {
	m.retireCaller = caller
	m.retireAgent = agentName
	m.retireMerge = merge
	m.retireAbandon = abandon
	m.retireCascade = cascade
	m.retireNoValidate = noValidate
	return m.retireErr
}

func (m *mockSupervisor) Kill(_ context.Context, agentName string) error {
	m.killAgent = agentName
	return m.killErr
}

func (m *mockSupervisor) Shutdown(_ context.Context) error {
	return m.shutdownErr
}

func (m *mockSupervisor) AskUserQuestion(_ context.Context, req supervisor.QuestionRequest) (supervisor.QuestionResponse, error) {
	m.askQuestionCalls++
	m.askQuestionLast = req
	if m.askQuestionErr != nil {
		return supervisor.QuestionResponse{}, m.askQuestionErr
	}
	return m.askQuestionResult, nil
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

func (m *mockSupervisor) SendMessage(_ context.Context, to, body string, interrupt bool) (*supervisor.SendMessageResult, error) {
	m.sendMessageCalls++
	m.sendMessageTo = to
	m.sendMessageBody = body
	m.sendMessageInterrupt = interrupt
	if m.sendMessageErr != nil {
		return nil, m.sendMessageErr
	}
	if m.sendMessageResult != nil {
		return m.sendMessageResult, nil
	}
	return &supervisor.SendMessageResult{MessageID: "msg_sm_stub", QueuedAt: "2026-05-12T00:00:00Z", Interrupted: interrupt}, nil
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
	if serverInfo["name"] != "sprawl" {
		t.Errorf("serverInfo.name = %v, want sprawl", serverInfo["name"])
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
		"spawn",
		"status",
		"delegate",
		"send_message",
		"peek",
		"report_status",
		"merge",
		"retire",
		"kill",
		"handoff",
		"messages_list",
		"messages_read",
		"messages_archive",
		"messages_peek",
		"ask_user_question",
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
	alive := true
	mock := &mockSupervisor{
		statusResult: []supervisor.AgentInfo{
			{Name: "ratz", Type: "engineer", Family: "engineering", Status: "active", Branch: "dmotles/feature", ProcessAlive: &alive},
			{Name: "ghost", Type: "researcher", Family: "engineering", Status: "active", Branch: "dmotles/research"},
		},
	}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(3, "tools/call", map[string]any{
		"name":      "status",
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
	if !strings.Contains(text, "process_alive") {
		t.Errorf("status text should include process_alive when the supervisor returns it, got:\n%s", text)
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
		"name": "spawn",
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
		"name": "delegate",
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

func TestServer_ToolsCall_SprawlMerge(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(7, "tools/call", map[string]any{
		"name": "merge",
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
	// Baseline: when no caller identity is in context, the server passes
	// an empty caller; the supervisor will fall back to its callerName.
	if mock.mergeCaller != "" {
		t.Errorf("mergeCaller = %q, want empty (no ctx identity)", mock.mergeCaller)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

// extractToolText pulls the text from the JSON-RPC tools/call response
// content[0].text field. Fatals on shape mismatch.
func extractToolText(t *testing.T, resp json.RawMessage) string {
	t.Helper()
	parsed := parseJSONRPCResponse(t, resp)
	if e, ok := parsed["error"]; ok {
		t.Fatalf("unexpected JSON-RPC error: %v", e)
	}
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatal("missing result")
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("missing or empty content array")
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatal("content[0] is not an object")
	}
	text, ok := first["text"].(string)
	if !ok {
		t.Fatal("content[0].text is not a string")
	}
	return text
}

// TestToolMerge_SuccessReturnsMergedAgent — QUM-511 baseline: a non-no-op
// merge should yield text "Merged agent <name>".
func TestToolMerge_SuccessReturnsMergedAgent(t *testing.T) {
	mock := &mockSupervisor{} // mergeNoOp defaults to false
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(700, "tools/call", map[string]any{
		"name": "merge",
		"arguments": map[string]any{
			"agent_name": "engX",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	text := extractToolText(t, resp)
	if text != "Merged agent engX" {
		t.Errorf("text = %q, want %q", text, "Merged agent engX")
	}
}

// TestToolMerge_NoOpReturnsNothingToMerge — QUM-511/QUM-489: when the
// supervisor reports the merge was a no-op (zero new commits, e.g. because
// the agent's branch is already merged into the parent), toolMerge must
// surface the truth to the caller rather than flattening to a generic
// "Merged agent <name>" success text — that flattening hides the
// QUM-511 stale-spawn-branch bug.
func TestToolMerge_NoOpReturnsNothingToMerge(t *testing.T) {
	mock := &mockSupervisor{mergeNoOp: true}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(701, "tools/call", map[string]any{
		"name": "merge",
		"arguments": map[string]any{
			"agent_name": "engX",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	text := extractToolText(t, resp)
	want := "Nothing to merge: engX has no new commits"
	if text != want {
		t.Errorf("text = %q, want %q (no-op merges must NOT report success)", text, want)
	}
}

func TestServer_ToolsCall_SprawlRetire(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(8, "tools/call", map[string]any{
		"name": "retire",
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
	if mock.retireCascade {
		t.Error("retire cascade = true, want false")
	}
	if mock.retireNoValidate {
		t.Error("retire noValidate = true, want false (validate defaults to true)")
	}
	if mock.retireCaller != "" {
		t.Errorf("retireCaller = %q, want empty (no ctx identity)", mock.retireCaller)
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
}

func TestServer_ToolsCall_SprawlRetire_Cascade(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(80, "tools/call", map[string]any{
		"name": "retire",
		"arguments": map[string]any{
			"agent_name": "manager-x",
			"cascade":    true,
			"abandon":    true,
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if !mock.retireCascade {
		t.Error("retire cascade = false, want true")
	}
	if !mock.retireAbandon {
		t.Error("retire abandon = false, want true")
	}

	parsed := parseJSONRPCResponse(t, resp)
	if _, ok := parsed["error"]; ok {
		t.Errorf("unexpected error: %v", parsed["error"])
	}
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Errorf("unexpected isError=true: %v", result)
	}
	content := result["content"].([]any)
	text, _ := content[0].(map[string]any)["text"].(string)
	if text == "" || text == "Retired agent manager-x" {
		t.Errorf("expected descendants mentioned in success text, got %q", text)
	}
}

func TestServer_ToolsCall_SprawlRetire_ValidateFalse(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(81, "tools/call", map[string]any{
		"name": "retire",
		"arguments": map[string]any{
			"agent_name": "ratz",
			"merge":      true,
			"validate":   false,
		},
	})
	if _, err := srv.HandleMessage(ctx, msg); err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}
	if !mock.retireNoValidate {
		t.Error("retire noValidate = false, want true (validate=false)")
	}
}

func TestServer_ToolsCall_SprawlRetire_MergeAndAbandonMutuallyExclusive(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(82, "tools/call", map[string]any{
		"name": "retire",
		"arguments": map[string]any{
			"agent_name": "ratz",
			"merge":      true,
			"abandon":    true,
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage() error: %v", err)
	}

	if mock.retireAgent != "" {
		t.Error("supervisor.Retire should not have been invoked when merge+abandon both set")
	}
	parsed := parseJSONRPCResponse(t, resp)
	result, ok := parsed["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result: %v", parsed)
	}
	isErr, _ := result["isError"].(bool)
	if !isErr {
		t.Errorf("expected isError=true, got result=%v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("missing error content")
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if text == "" {
		t.Error("expected error message text")
	}
}

func TestServer_ToolsCall_SprawlKill(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)
	ctx := context.Background()

	msg := makeJSONRPCRequest(9, "tools/call", map[string]any{
		"name": "kill",
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
		"name": "handoff",
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
		"name":      "handoff",
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
		"name": "delegate",
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
		"name":      "peek",
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
		"name":      "peek",
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
		"name": "report_status",
		"arguments": map[string]any{
			"state":   "working",
			"summary": "halfway done",
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
		"name":      "report_status",
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
		if m["name"] == "report_status" {
			found = true
			break
		}
	}
	if !found {
		t.Error("report_status not in tools/list")
	}
}

// QUM-550 slice 2: report_status tool input schema must NOT advertise a
// `detail` property. The field is removed from the surface, and only
// `state` + `summary` are required (and accepted).
func TestServer_ToolsList_ReportStatus_NoDetailField(t *testing.T) {
	srv := New(&mockSupervisor{})
	resp, err := srv.HandleMessage(context.Background(), makeJSONRPCRequest(137, "tools/list", nil))
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	tools := result["tools"].([]any)

	var entry map[string]any
	for _, tool := range tools {
		m := tool.(map[string]any)
		if m["name"] == "report_status" {
			entry = m
			break
		}
	}
	if entry == nil {
		t.Fatal("report_status not in tools/list")
	}
	schema, ok := entry["inputSchema"].(map[string]any)
	if !ok {
		t.Fatalf("inputSchema missing or wrong type: %T", entry["inputSchema"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing or wrong type: %T", schema["properties"])
	}
	if _, exists := props["detail"]; exists {
		t.Errorf("report_status inputSchema.properties.detail must be absent (QUM-550 slice 2)")
	}
	if _, exists := props["state"]; !exists {
		t.Errorf("report_status inputSchema.properties.state missing")
	}
	if _, exists := props["summary"]; !exists {
		t.Errorf("report_status inputSchema.properties.summary missing")
	}

	required, ok := schema["required"].([]any)
	if !ok {
		// Some encoders preserve []string — accept either.
		if reqS, okS := schema["required"].([]string); okS {
			required = make([]any, 0, len(reqS))
			for _, s := range reqS {
				required = append(required, s)
			}
		} else {
			t.Fatalf("required missing or wrong type: %T", schema["required"])
		}
	}
	if len(required) != 2 {
		t.Errorf("required len = %d, want 2 (state, summary)", len(required))
	}
	for _, r := range required {
		if r == "detail" {
			t.Errorf("report_status inputSchema.required must not contain \"detail\"")
		}
	}
}

func TestServer_ToolsCall_SprawlPeek_TailClamp(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(34, "tools/call", map[string]any{
		"name":      "peek",
		"arguments": map[string]any{"agent": "ghost", "tail": 9999},
	})
	_, _ = srv.HandleMessage(context.Background(), msg)

	if mock.peekTail != 200 {
		t.Errorf("clamped tail = %d, want 200", mock.peekTail)
	}
}

// --- QUM-387: child MCP identity propagation ---

func TestServer_ToolsCall_ReportStatus_UsesContextIdentity(t *testing.T) {
	mock := &mockSupervisor{
		reportStatusResult: &supervisor.ReportStatusResult{ReportedAt: "2026-04-30T10:00:00Z"},
	}
	srv := New(mock)

	// Simulate a child agent calling report_status via MCP — context carries child identity.
	ctx := context.Background()
	ctx = withTestCallerIdentity(ctx, "finn")

	msg := makeJSONRPCRequest(100, "tools/call", map[string]any{
		"name": "report_status",
		"arguments": map[string]any{
			"state":   "complete",
			"summary": "task done",
		},
	})
	resp, err := srv.HandleMessage(ctx, msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	// The supervisor should receive the child's identity, not empty string.
	if mock.reportStatusAgent != "finn" {
		t.Errorf("reportStatusAgent = %q, want %q (child identity from context)", mock.reportStatusAgent, "finn")
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
}

func TestServer_ToolsCall_ReportStatus_EmptyContextFallsBack(t *testing.T) {
	mock := &mockSupervisor{
		reportStatusResult: &supervisor.ReportStatusResult{ReportedAt: "2026-04-30T10:00:00Z"},
	}
	srv := New(mock)

	// Root weave session — no identity in context.
	msg := makeJSONRPCRequest(101, "tools/call", map[string]any{
		"name": "report_status",
		"arguments": map[string]any{
			"state":   "working",
			"summary": "still going",
		},
	})
	_, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	// Empty agentName means supervisor uses its own callerName (backward compat).
	if mock.reportStatusAgent != "" {
		t.Errorf("reportStatusAgent = %q, want empty (supervisor falls back to callerName)", mock.reportStatusAgent)
	}
}

// --- QUM-316: messages_list / _read / _archive / _peek ---

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
		"name":      "messages_list",
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
		"name":      "messages_list",
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
		"name":      "messages_list",
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
		"name":      "messages_read",
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
		"name":      "messages_read",
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
		"name":      "messages_archive",
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

func TestServer_ToolsCall_MessagesArchiveBulkAll(t *testing.T) {
	mock := &mockSupervisor{
		messagesArchiveAllResult: &supervisor.MessagesArchiveAllResult{ArchivedCount: 13, Archived: true},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(57, "tools/call", map[string]any{
		"name":      "messages_archive",
		"arguments": map[string]any{"all": true},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.messagesArchiveAllMode != "all" {
		t.Errorf("mode = %q, want %q", mock.messagesArchiveAllMode, "all")
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("unexpected isError: %v", result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var body supervisor.MessagesArchiveAllResult
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ArchivedCount != 13 {
		t.Errorf("archived_count = %d, want 13", body.ArchivedCount)
	}
	if !body.Archived {
		t.Error("archived = false")
	}
}

func TestServer_ToolsCall_MessagesArchiveNoParams(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	msg := makeJSONRPCRequest(58, "tools/call", map[string]any{
		"name":      "messages_archive",
		"arguments": map[string]any{},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	parsed := parseJSONRPCResponse(t, resp)
	result := parsed["result"].(map[string]any)
	isErr, _ := result["isError"].(bool)
	if !isErr {
		t.Fatal("expected isError for missing params")
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "either 'id' or 'all' must be provided") {
		t.Errorf("unexpected error text: %s", text)
	}
}

func TestServer_ToolsCall_MessagesArchiveSingleStillWorks(t *testing.T) {
	mock := &mockSupervisor{
		messagesArchiveResult: &supervisor.MessagesArchiveResult{ID: "xyz", FullID: "full-xyz", Archived: true},
	}
	srv := New(mock)

	msg := makeJSONRPCRequest(59, "tools/call", map[string]any{
		"name":      "messages_archive",
		"arguments": map[string]any{"id": "xyz"},
	})
	resp, err := srv.HandleMessage(context.Background(), msg)
	if err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}
	if mock.messagesArchiveID != "xyz" {
		t.Errorf("id = %q, want %q", mock.messagesArchiveID, "xyz")
	}
	// Ensure bulk was NOT called
	if mock.messagesArchiveAllMode != "" {
		t.Errorf("bulk archive was called unexpectedly with mode %q", mock.messagesArchiveAllMode)
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
	if !body.Archived || body.ID != "xyz" {
		t.Errorf("unexpected body: %+v", body)
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
		"name":      "messages_peek",
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

// --- QUM-487: caller identity propagation for merge/retire ---
//
// The supervisor's Merge/Retire interface accepts a caller-identity arg so
// child-agent MCP requests don't leak the supervisor process's identity
// (always "weave") into agentops's parent-equality check. The MCP server's
// merge / retire tool handlers are responsible for extracting that caller
// from the request context (set by the backend session bridge per QUM-387)
// and forwarding it.

func TestServer_ToolsCall_SprawlMerge_PassesCallerFromContext(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	// Manager "tower" invoking merge through MCP — context carries tower's identity.
	ctx := withTestCallerIdentity(context.Background(), "tower")

	msg := makeJSONRPCRequest(110, "tools/call", map[string]any{
		"name": "merge",
		"arguments": map[string]any{
			"agent_name": "finn",
			"message":    "merge finn",
		},
	})
	if _, err := srv.HandleMessage(ctx, msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.mergeCaller != "tower" {
		t.Errorf("mergeCaller = %q, want %q (server must forward caller from ctx — QUM-487)", mock.mergeCaller, "tower")
	}
	if mock.mergeAgent != "finn" {
		t.Errorf("mergeAgent = %q, want finn", mock.mergeAgent)
	}
}

func TestServer_ToolsCall_SprawlRetire_PassesCallerFromContext(t *testing.T) {
	mock := &mockSupervisor{}
	srv := New(mock)

	ctx := withTestCallerIdentity(context.Background(), "tower")

	msg := makeJSONRPCRequest(111, "tools/call", map[string]any{
		"name": "retire",
		"arguments": map[string]any{
			"agent_name": "finn",
			"merge":      true,
		},
	})
	if _, err := srv.HandleMessage(ctx, msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if mock.retireCaller != "tower" {
		t.Errorf("retireCaller = %q, want %q (server must forward caller from ctx — QUM-487)", mock.retireCaller, "tower")
	}
	if mock.retireAgent != "finn" {
		t.Errorf("retireAgent = %q, want finn", mock.retireAgent)
	}
}

// --- QUM-494: per-call observability — JSONL call log ---

// readCallLogLines reads the mcp-calls.jsonl under root and returns
// each line decoded as a map. Used by per-call observability tests.
func readCallLogLines(t *testing.T, root string) []map[string]any {
	t.Helper()
	path := filepath.Join(root, ".sprawl", "logs", "mcp-calls.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open call log %s: %v", path, err)
	}
	defer f.Close()

	var out []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			t.Fatalf("malformed JSONL line %q: %v", string(line), err)
		}
		out = append(out, obj)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}

func TestHandleToolsCall_EmitsBeginEnd(t *testing.T) {
	dir := t.TempDir()
	logger, err := calllog.Open(dir)
	if err != nil {
		t.Fatalf("calllog.Open: %v", err)
	}
	defer logger.Close()

	mock := &mockSupervisor{
		peekResult: &supervisor.PeekResult{Status: "active"},
	}
	srv := New(mock).WithCallLog(logger)

	msg := makeJSONRPCRequest(200, "tools/call", map[string]any{
		"name":      "peek",
		"arguments": map[string]any{"agent": "ghost"},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	// Force flush to disk before reading.
	if err := logger.Close(); err != nil {
		t.Fatalf("logger.Close: %v", err)
	}

	lines := readCallLogLines(t, dir)
	if len(lines) < 2 {
		t.Fatalf("got %d log lines, want >= 2", len(lines))
	}

	var start, end map[string]any
	for _, ln := range lines {
		switch ln["phase"] {
		case "start":
			start = ln
		case "end":
			end = ln
		}
	}
	if start == nil {
		t.Fatal("missing start line")
	}
	if end == nil {
		t.Fatal("missing end line")
	}
	if start["call_id"] != end["call_id"] {
		t.Errorf("call_id mismatch: start=%v end=%v", start["call_id"], end["call_id"])
	}
	if start["tool"] != "peek" {
		t.Errorf("tool = %v, want peek", start["tool"])
	}
	if end["status"] != "ok" {
		t.Errorf("end.status = %v, want ok", end["status"])
	}
}

func TestHandleToolsCall_EndOnError(t *testing.T) {
	dir := t.TempDir()
	logger, err := calllog.Open(dir)
	if err != nil {
		t.Fatalf("calllog.Open: %v", err)
	}
	defer logger.Close()

	mock := &mockSupervisor{
		delegateErr: fmt.Errorf("agent not found"),
	}
	srv := New(mock).WithCallLog(logger)

	msg := makeJSONRPCRequest(201, "tools/call", map[string]any{
		"name": "delegate",
		"arguments": map[string]any{
			"agent_name": "nonexistent",
			"task":       "do something",
		},
	})
	if _, err := srv.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("logger.Close: %v", err)
	}

	lines := readCallLogLines(t, dir)
	var end map[string]any
	for _, ln := range lines {
		if ln["phase"] == "end" {
			end = ln
		}
	}
	if end == nil {
		t.Fatal("missing end line")
	}
	if end["status"] != "error" {
		t.Errorf("end.status = %v, want error", end["status"])
	}
	errMsg, _ := end["error"].(string)
	if errMsg == "" {
		t.Error("end.error should be non-empty for tool error")
	}
	if !strings.Contains(errMsg, "agent not found") {
		t.Errorf("end.error = %q, want to contain 'agent not found'", errMsg)
	}
}
