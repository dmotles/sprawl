package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
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

	// Observer dispatch is async (QUM-595 F2). Close blocks on the
	// observer drain so post-Close reads see the fully-flushed queue.
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
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

// TestSession_AutonomousFrame_OpensOnSystemInitAndClosesOnResult (QUM-578)
// verifies that a system:init while no sprawl turn is active opens an
// autonomous turnFrame and that a subsequent result closes it. A
// follow-on StartTurn must then succeed (no leaked frame).
func TestSession_AutonomousFrame_OpensOnSystemInitAndClosesOnResult(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Feed an autonomous turn (no StartTurn yet).
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto"}]}}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	// Give the reader a moment to consume.
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("observer did not see 3 frames in time; got %v", observer.Types())
		case <-time.After(10 * time.Millisecond):
		}
	}

	gotObserver := observer.Types()
	want := []string{"system:init", "assistant", "result:success"}
	if len(gotObserver) != len(want) {
		t.Fatalf("observer = %v, want %v", gotObserver, want)
	}
	for i, w := range want {
		if gotObserver[i] != w {
			t.Errorf("observer[%d] = %q, want %q", i, gotObserver[i], w)
		}
	}

	// Drain initial user prompt that StartTurn will emit.
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	events, err := session.StartTurn(ctx, "after-auto")
	if err != nil {
		t.Fatalf("StartTurn() after autonomous frame closed: %v", err)
	}
	drainMessages(events)
}

// TestSession_AutonomousFrame_StrayFrameDoesNotOpenFrame (QUM-578) verifies
// that a stray assistant frame (no preceding system:init) does NOT allocate
// an autonomous turnFrame — StartTurn must not block waiting on a phantom.
func TestSession_AutonomousFrame_StrayFrameDoesNotOpenFrame(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"assistant","uuid":"stray-1","message":{"role":"assistant","content":[{"type":"text","text":"stray"}]}}`)

	// Wait until observer has seen the stray.
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw stray assistant frame")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// StartTurn must not block — no autonomous frame should have opened.
	startDone := make(chan error, 1)
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	go func() {
		events, err := session.StartTurn(ctx, "go")
		if err != nil {
			startDone <- err
			return
		}
		drainMessages(events)
		startDone <- nil
	}()

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("StartTurn() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn blocked despite stray assistant (autonomous frame should not have been allocated)")
	}
}

// TestSession_StartTurn_WaitsForAutonomousFrame (QUM-578) verifies that
// StartTurn ctx-cancellably waits on an open autonomous frame's done
// channel before allocating a sprawl turn.
func TestSession_StartTurn_WaitsForAutonomousFrame(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Open an autonomous frame.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)

	// Wait until observer has seen it (frame is open).
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw autonomous system:init")
		case <-time.After(10 * time.Millisecond):
		}
	}

	startDone := make(chan error, 1)
	go func() {
		events, err := session.StartTurn(ctx, "queued")
		if err != nil {
			startDone <- err
			return
		}
		drainMessages(events)
		startDone <- nil
	}()

	// StartTurn must NOT return within 50ms — autonomous frame still open.
	select {
	case err := <-startDone:
		t.Fatalf("StartTurn returned prematurely (err=%v) — should be blocked on autonomous frame", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Assert nothing sent on transport yet (no user-frame leaked).
	select {
	case sent := <-transport.sendCh:
		t.Fatalf("StartTurn sent user frame before autonomous frame closed: %v", sent)
	default:
	}

	// Close the autonomous frame.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	// Now StartTurn should allocate and send a user frame.
	var sent any
	select {
	case sent = <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn never sent user frame after autonomous frame closed")
	}
	data, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal sent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal sent: %v", err)
	}
	if parsed["type"] != "user" {
		t.Fatalf("sent type = %v, want user", parsed["type"])
	}

	// Close out the sprawl turn so StartTurn returns cleanly.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("StartTurn final error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn did not return after sprawl turn result")
	}
}

// TestSession_StartTurn_CtxCancelDuringAutonomousWait (QUM-578) verifies
// that StartTurn's wait on an autonomous frame respects ctx cancellation
// and does not send a user frame on cancel.
func TestSession_StartTurn_CtxCancelDuringAutonomousWait(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Open autonomous frame.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw autonomous system:init")
		case <-time.After(10 * time.Millisecond):
		}
	}

	startDone := make(chan error, 1)
	go func() {
		_, err := session.StartTurn(ctx, "queued")
		startDone <- err
	}()

	// Give StartTurn time to enter wait.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-startDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartTurn err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn did not return after ctx cancel")
	}

	// No user frame should have been sent. Drain sendCh for ~100ms post-cancel
	// and assert no frame had type=="user" — sendCh is buffered cap 100 so a
	// synchronous Send before ctx-check could land in the buffer.
	drainDeadline := time.After(100 * time.Millisecond)
DRAIN:
	for {
		select {
		case sent := <-transport.sendCh:
			data, _ := json.Marshal(sent)
			var parsed map[string]any
			_ = json.Unmarshal(data, &parsed)
			if parsed["type"] == "user" {
				t.Fatalf("user frame leaked despite ctx cancel: %v", parsed)
			}
		case <-drainDeadline:
			break DRAIN
		}
	}
}

// TestSession_AutonomousFrame_SecondSystemInitIgnored (QUM-578) verifies
// that a second system:init while an autonomous frame is already open is
// ignored (no double-allocation, no panic from double-close on result).
func TestSession_AutonomousFrame_SecondSystemInitIgnored(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("observer did not see 3 frames; got %v", observer.Types())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// StartTurn must succeed — no hung double-allocation.
	startDone := make(chan error, 1)
	go func() {
		<-transport.sendCh
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	}()
	go func() {
		events, err := session.StartTurn(ctx, "after-double-init")
		if err != nil {
			startDone <- err
			return
		}
		drainMessages(events)
		startDone <- nil
	}()

	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("StartTurn err: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn did not return — double system:init may have double-allocated or autonomous frame leaked")
	}
}

// --- QUM-582: race-safe InAutonomousTurn() accessor ---
//
// These tests assert the contract for Session.InAutonomousTurn(): it returns
// true iff a turn is currently in flight and that turn was created
// autonomously by the SDK (system:init while no StartTurn was pending) — not
// for sprawl-initiated turns and not when no turn is active at all. The
// accessor MUST take s.mu so it is race-safe against the persistent reader
// goroutine mutating s.currentTurn concurrently.

// TestSession_InAutonomousTurn_NoTurn verifies the accessor returns false on
// a freshly-constructed session with no frames in flight.
func TestSession_InAutonomousTurn_NoTurn(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	concrete, ok := sess.(*session)
	if !ok {
		t.Fatalf("session type = %T, want *session", sess)
	}
	if concrete.InAutonomousTurn() {
		t.Errorf("InAutonomousTurn() = true on fresh session, want false")
	}
}

// TestSession_InAutonomousTurn_DuringSprawlTurn verifies the accessor returns
// false during an explicit sprawl-initiated turn. The autonomous flag MUST
// only be set when the SDK opens a turn without our prompt.
func TestSession_InAutonomousTurn_DuringSprawlTurn(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	// Drain the user-frame send.
	select {
	case <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn did not send user frame")
	}

	// Feed an assistant frame so the turn is observably in-flight.
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	// Consume the assistant so we know the reader has progressed.
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("assistant frame never delivered to subscriber")
	}

	if sess.(*session).InAutonomousTurn() {
		t.Errorf("InAutonomousTurn() = true during sprawl turn, want false (sprawl-initiated turns are not autonomous)")
	}

	// Close out cleanly.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	drainMessages(events)
}

// TestSession_InAutonomousTurn_DuringAutonomousTurn verifies the accessor
// returns true while an autonomous frame (opened by system:init with no
// pending StartTurn) is in flight, and flips back to false after the matching
// result frame.
func TestSession_InAutonomousTurn_DuringAutonomousTurn(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	sess := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Feed a system:init while no StartTurn is in flight — opens autonomous frame.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)

	// Wait until observer has seen the init frame so we know the reader
	// has allocated the autonomous turnFrame.
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw autonomous system:init")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if !sess.(*session).InAutonomousTurn() {
		t.Errorf("InAutonomousTurn() = false during autonomous frame, want true")
	}

	// Close the autonomous frame with a result.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	// Wait for the reader to consume both init AND result frames. Observer is
	// notified AFTER the reader's per-message handler has run (so currentTurn
	// has already been cleared by the result-handler path) — deterministic
	// sync point, no timed polling.
	deadline = time.After(2 * time.Second)
	for len(observer.Types()) < 2 {
		select {
		case <-deadline:
			t.Fatal("observer never saw result frame")
		case <-time.After(10 * time.Millisecond):
		}
	}

	if sess.(*session).InAutonomousTurn() {
		t.Errorf("InAutonomousTurn() = true after autonomous frame's result, want false (frame should be cleared)")
	}
}

// TestSession_InAutonomousTurn_ClearedOnReaderError verifies that when the
// reader exits because transport.Recv returns an error (io.EOF), the
// orphan-frame teardown defer at session.go:310-329 clears currentTurn, so
// the accessor returns false.
func TestSession_InAutonomousTurn_ClearedOnReaderError(t *testing.T) {
	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	sess := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Open an autonomous frame.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw autonomous system:init")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !sess.(*session).InAutonomousTurn() {
		t.Fatalf("InAutonomousTurn() = false before reader exit, want true (precondition)")
	}

	// Force the reader to exit by closing recvCh — transport.Recv returns io.EOF.
	close(transport.recvCh)

	// Wait for the reader to unwind; the orphan teardown defer clears currentTurn.
	concrete, ok := sess.(*session)
	if !ok {
		t.Fatalf("session type = %T, want *session", sess)
	}
	select {
	case <-concrete.readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not exit after recvCh close")
	}

	if sess.(*session).InAutonomousTurn() {
		t.Errorf("InAutonomousTurn() = true after reader exit, want false (orphan teardown should clear currentTurn)")
	}
}

// TestSession_InAutonomousTurn_RaceSafeUnderConcurrentReader exists to be
// run under `go test -race`. A reader goroutine spins on
// session.InAutonomousTurn() while the test drives the transport reader
// through init+result frames. Any unsynchronized read of
// s.currentTurn.autonomous (i.e. an implementation that fails to take s.mu)
// MUST be reported by the race detector. Without -race this test is just a
// sanity loop — its real value is the race instrumentation.
func TestSession_InAutonomousTurn_RaceSafeUnderConcurrentReader(t *testing.T) {
	prev := runtime.GOMAXPROCS(2)
	defer runtime.GOMAXPROCS(prev)

	transport := newMockManagedTransport()
	observer := &recordingObserver{}
	sess := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
		Observer:  observer,
	})
	concrete, ok := sess.(*session)
	if !ok {
		t.Fatalf("session type = %T, want *session", sess)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				_ = concrete.InAutonomousTurn()
			}
		}
	}()

	// Drive a complete autonomous turn (init then result), each waiting on
	// observer-confirmed delivery so the reader has actually mutated
	// s.currentTurn under s.mu while the spinner is racing for reads.
	transport.feedMessage(t, `{"type":"system","subtype":"init","session_id":"sess-1"}`)
	deadline := time.After(2 * time.Second)
	for len(observer.Types()) < 1 {
		select {
		case <-deadline:
			t.Fatal("observer never saw init")
		case <-time.After(10 * time.Millisecond):
		}
	}
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	deadline = time.After(2 * time.Second)
	for len(observer.Types()) < 2 {
		select {
		case <-deadline:
			t.Fatal("observer never saw result")
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("spinner goroutine did not exit")
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
