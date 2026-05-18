package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

type mockManagedTransport struct {
	sendCh chan any
	recvCh chan *protocol.Message

	mu          sync.Mutex
	closeCalled bool
	waitCalled  bool
	killCalled  bool
}

func newMockManagedTransport() *mockManagedTransport {
	return &mockManagedTransport{
		sendCh: make(chan any, 100),
		recvCh: make(chan *protocol.Message, 100),
	}
}

func (m *mockManagedTransport) Send(ctx context.Context, msg any) error {
	select {
	case m.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *mockManagedTransport) Recv(ctx context.Context) (*protocol.Message, error) {
	select {
	case msg, ok := <-m.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *mockManagedTransport) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalled = true
	return nil
}

func (m *mockManagedTransport) Wait() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.waitCalled = true
	return nil
}

func (m *mockManagedTransport) Kill() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killCalled = true
	return nil
}

func (m *mockManagedTransport) feedMessage(t *testing.T, raw string) {
	t.Helper()
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("feedMessage: unmarshal error: %v", err)
	}
	msg.Raw = json.RawMessage(raw)
	m.recvCh <- &msg
}

type recordingObserver struct {
	mu       sync.Mutex
	messages []*protocol.Message
}

func (o *recordingObserver) OnMessage(msg *protocol.Message) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.messages = append(o.messages, msg)
}

func (o *recordingObserver) Types() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	types := make([]string, 0, len(o.messages))
	for _, msg := range o.messages {
		label := msg.Type
		if msg.Subtype != "" {
			label += ":" + msg.Subtype
		}
		types = append(types, label)
	}
	return types
}

type mockToolBridge struct {
	mu         sync.Mutex
	serverName string
	payload    string
	response   json.RawMessage
	lastCtx    context.Context
	called     chan struct{}
}

func newMockToolBridge(response json.RawMessage) *mockToolBridge {
	return &mockToolBridge{
		response: response,
		called:   make(chan struct{}, 8),
	}
}

func (b *mockToolBridge) HandleIncoming(ctx context.Context, serverName string, msg json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.serverName = serverName
	b.payload = string(msg)
	b.lastCtx = ctx
	b.mu.Unlock()
	select {
	case b.called <- struct{}{}:
	default:
	}
	return b.response, nil
}

func (b *mockToolBridge) waitCalled(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case <-b.called:
	case <-time.After(d):
		t.Fatal("mockToolBridge.HandleIncoming was not called within timeout")
	}
}

// feedInitResponse decodes the initialize control_request from `sent`
// and feeds back a matching control_response so Initialize's handshake
// wait unblocks under the persistent-reader model.
func feedInitResponse(t *testing.T, m *mockManagedTransport, sent any) {
	t.Helper()
	data, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal initialize request: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal initialize request: %v", err)
	}
	reqID, _ := parsed["request_id"].(string)
	m.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+reqID+`"}}`)
}

func drainMessages(ch <-chan *protocol.Message) {
	for msg := range ch {
		_ = msg
	}
}

func TestSession_InitializeSendsInitSpecAndAwaitsHandshake(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Capabilities: Capabilities{
			SupportsInterrupt: true,
			SupportsResume:    true,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		sent := <-transport.sendCh
		data, err := json.Marshal(sent)
		if err != nil {
			t.Errorf("marshal initialize request: %v", err)
			return
		}

		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Errorf("unmarshal initialize request: %v", err)
			return
		}

		if parsed["type"] != "control_request" {
			t.Errorf("type = %v, want control_request", parsed["type"])
		}

		request, ok := parsed["request"].(map[string]any)
		if !ok {
			t.Error("request field missing from initialize payload")
			return
		}

		servers, ok := request["sdkMcpServers"].([]any)
		if !ok {
			t.Error("sdkMcpServers not present")
		} else if len(servers) != 1 || servers[0] != "sprawl" {
			t.Errorf("sdkMcpServers = %v, want [sprawl]", servers)
		}

		// Echo back a control_response with the matching request_id so
		// Initialize's persistent-reader handshake completes.
		reqID, _ := parsed["request_id"].(string)
		transport.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+reqID+`"}}`)
	}()

	if err := session.Initialize(ctx, InitSpec{MCPServerNames: []string{"sprawl"}}); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}
}

func TestSession_StartTurnDoesNotInitializeImplicitly(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		sent := <-transport.sendCh
		data, err := json.Marshal(sent)
		if err != nil {
			t.Errorf("marshal start-turn message: %v", err)
			return
		}

		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Errorf("unmarshal start-turn message: %v", err)
			return
		}
		if parsed["type"] != "user" {
			t.Errorf("type = %v, want user (child sessions must not auto-initialize)", parsed["type"])
		}

		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
		close(transport.recvCh)
	}()

	events, err := session.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}

	drainMessages(events)
}

func TestSession_StartTurnObserverSeesRawMessagesBeforeControlHandling(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		<-transport.sendCh

		transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
		transport.feedMessage(t, `{"type":"control_request","request_id":"tool-1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}`)
		transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
		close(transport.recvCh)
	}()

	events, err := session.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}

	var eventTypes []string
	for msg := range events {
		label := msg.Type
		if msg.Subtype != "" {
			label += ":" + msg.Subtype
		}
		eventTypes = append(eventTypes, label)
	}

	wantObserver := []string{"system:init", "control_request", "assistant", "result:success"}
	gotObserver := observer.Types()
	if len(gotObserver) != len(wantObserver) {
		t.Fatalf("observer saw %v, want %v", gotObserver, wantObserver)
	}
	for i, want := range wantObserver {
		if gotObserver[i] != want {
			t.Errorf("observer[%d] = %q, want %q", i, gotObserver[i], want)
		}
	}

	wantEvents := []string{"system:init", "assistant", "result:success"}
	if len(eventTypes) != len(wantEvents) {
		t.Fatalf("events = %v, want %v", eventTypes, wantEvents)
	}
	for i, want := range wantEvents {
		if eventTypes[i] != want {
			t.Errorf("events[%d] = %q, want %q", i, eventTypes[i], want)
		}
	}

	var approval any
	select {
	case approval = <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected tool approval response")
	}

	data, err := json.Marshal(approval)
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
}

func TestSession_StartTurnRoutesMCPMessagesThroughToolBridge(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newMockToolBridge(json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		// Init send → echo handshake response.
		sent := <-transport.sendCh
		feedInitResponse(t, transport, sent)
		// User-prompt send.
		<-transport.sendCh

		transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
		close(transport.recvCh)
	}()

	if err := session.Initialize(ctx, InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge,
	}); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}

	events, err := session.StartTurn(ctx, "list tools")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	drainMessages(events)
	bridge.waitCalled(t, 2*time.Second)

	bridge.mu.Lock()
	if bridge.serverName != "sprawl" {
		t.Errorf("bridge server = %q, want sprawl", bridge.serverName)
	}
	if bridge.payload == "" {
		t.Error("bridge payload should not be empty")
	}
	bridge.mu.Unlock()

	var response any
	select {
	case response = <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("expected MCP response to be sent")
	}

	data, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if parsed["type"] != "control_response" {
		t.Fatalf("response type = %v, want control_response", parsed["type"])
	}
}

func TestSession_MCPMessageCarriesCallerIdentity(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newMockToolBridge(json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-child",
		Identity:  "finn",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		sent := <-transport.sendCh
		feedInitResponse(t, transport, sent)
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"report_status","arguments":{"state":"working","summary":"test"}}}}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
		close(transport.recvCh)
	}()

	if err := session.Initialize(ctx, InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge,
	}); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}
	events, err := session.StartTurn(ctx, "do work")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	drainMessages(events)
	bridge.waitCalled(t, 2*time.Second)

	bridge.mu.Lock()
	defer bridge.mu.Unlock()

	if bridge.lastCtx == nil {
		t.Fatal("bridge was not called")
	}
	identity := CallerIdentity(bridge.lastCtx)
	if identity != "finn" {
		t.Errorf("CallerIdentity(bridge ctx) = %q, want %q", identity, "finn")
	}
}

func TestSession_MCPMessageNoIdentityWhenEmpty(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newMockToolBridge(json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	// No Identity set — simulates root weave session
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-root",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		sent := <-transport.sendCh
		feedInitResponse(t, transport, sent)
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
		close(transport.recvCh)
	}()

	if err := session.Initialize(ctx, InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge,
	}); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}
	events, err := session.StartTurn(ctx, "list tools")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	drainMessages(events)
	bridge.waitCalled(t, 2*time.Second)

	bridge.mu.Lock()
	defer bridge.mu.Unlock()

	if bridge.lastCtx == nil {
		t.Fatal("bridge was not called")
	}
	identity := CallerIdentity(bridge.lastCtx)
	if identity != "" {
		t.Errorf("CallerIdentity(bridge ctx) = %q, want empty for root session", identity)
	}
}

func TestSession_StartTurnRejectsConcurrentTurns(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	firstEvents, err := session.StartTurn(ctx, "first")
	if err != nil {
		t.Fatalf("first StartTurn() error: %v", err)
	}
	if firstEvents == nil {
		t.Fatal("first StartTurn() returned nil channel")
	}

	_, err = session.StartTurn(ctx, "second")
	if !errors.Is(err, ErrTurnInProgress) {
		t.Fatalf("second StartTurn() error = %v, want ErrTurnInProgress", err)
	}

	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	close(transport.recvCh)
	drainMessages(firstEvents)
}

// signalingToolBridge wraps mockToolBridge and signals on a channel
// every time HandleIncoming is called. Used by the persistent-reader
// repro to detect autonomous-turn MCP dispatch with a timeout.
type signalingToolBridge struct {
	mu         sync.Mutex
	serverName string
	payload    string
	response   json.RawMessage
	called     chan struct{}
}

func (b *signalingToolBridge) HandleIncoming(ctx context.Context, serverName string, msg json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.serverName = serverName
	b.payload = string(msg)
	b.mu.Unlock()
	select {
	case b.called <- struct{}{}:
	default:
	}
	return b.response, nil
}

// TestSession_AutonomousTurnDispatchesMCPToolUse is the QUM-570 repro.
//
// After sprawl drives a StartTurn to completion (we see `result:success`),
// the Claude Code SDK can autonomously start a *new* turn (system:init +
// control_request{mcp_message} + result). On current code, readTurn exits
// at the first `result` and no goroutine reads transport.Recv between turns,
// so the autonomous control_request is never seen and the ToolBridge is
// never invoked — MCP tool_use calls vanish.
//
// The persistent-stream refactor must service the transport continuously
// while the session is alive, dispatching autonomous control_requests to
// the host ToolBridge just like in-turn ones.
func TestSession_AutonomousTurnDispatchesMCPToolUse(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := &signalingToolBridge{
		response: json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`),
		called:   make(chan struct{}, 4),
	}
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Feed BOTH the sprawl-initiated turn frames AND the subsequent
	// autonomous-turn frames up front. On the current implementation
	// readTurn returns at the first `result`, leaving the autonomous
	// frames parked in recvCh forever. On the refactored persistent
	// reader, the autonomous control_request is dispatched to the
	// ToolBridge and the response is written back to the transport.
	go func() {
		sent := <-transport.sendCh // initialize
		feedInitResponse(t, transport, sent)
		<-transport.sendCh // user prompt

		// Sprawl-initiated turn.
		transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
		transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

		// Autonomous turn driven by the SDK (no sprawl StartTurn).
		transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
		transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-auto-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"report_status","arguments":{"state":"working","summary":"auto"}}}}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()

	if err := session.Initialize(ctx, InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge,
	}); err != nil {
		t.Fatalf("Initialize() error: %v", err)
	}
	events, err := session.StartTurn(ctx, "hi")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	drainMessages(events)

	// Load-bearing assertion: the persistent reader must dispatch the
	// autonomous MCP control_request to the ToolBridge. On current code
	// the goroutine exited at the first `result` and never sees it,
	// so this times out.
	select {
	case <-bridge.called:
	case <-time.After(2 * time.Second):
		t.Fatal("ToolBridge.HandleIncoming was not called for autonomous-turn MCP control_request (persistent reader missing)")
	}

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.serverName != "sprawl" {
		t.Errorf("bridge server = %q, want sprawl", bridge.serverName)
	}
	if bridge.payload == "" {
		t.Error("bridge payload should not be empty")
	}

	// Drain any pending sends and assert at least one control_response
	// corresponds to the autonomous request_id.
	deadline := time.After(2 * time.Second)
	var sawAutoResponse bool
DRAIN:
	for {
		select {
		case sent := <-transport.sendCh:
			data, err := json.Marshal(sent)
			if err != nil {
				t.Fatalf("marshal sent: %v", err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				continue
			}
			if parsed["type"] != "control_response" {
				continue
			}
			resp, _ := parsed["response"].(map[string]any)
			if resp == nil {
				continue
			}
			if reqID, _ := resp["request_id"].(string); reqID == "mcp-auto-1" {
				sawAutoResponse = true
				break DRAIN
			}
		case <-deadline:
			break DRAIN
		}
	}
	if !sawAutoResponse {
		t.Fatal("did not see control_response for autonomous mcp-auto-1 request_id on transport")
	}
}

func TestSession_StartTurn_AllowsSecondTurnAfterFirstResult(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	firstEvents, err := session.StartTurn(ctx, "first")
	if err != nil {
		t.Fatalf("first StartTurn() error: %v", err)
	}
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	drainMessages(firstEvents)

	secondEvents, err := session.StartTurn(ctx, "second")
	if err != nil {
		t.Fatalf("second StartTurn() error: %v", err)
	}
	if secondEvents == nil {
		t.Fatal("second StartTurn() returned nil channel")
	}
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	close(transport.recvCh)
	drainMessages(secondEvents)
}
