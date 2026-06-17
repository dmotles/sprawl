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

	// recvIgnoresCtx, when true, makes Recv block solely on recvCh without
	// selecting on ctx.Done() — simulating a real stdout read that does not
	// honor ctx cancellation (and survives Close, which never closes recvCh).
	// Used to exercise the bounded readerDone join in session.Close (QUM-636).
	recvIgnoresCtx bool

	// sendErr, when non-nil, makes Send return it immediately without
	// enqueuing onto sendCh — exercises wire-send failure paths.
	sendErr error

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
	if m.sendErr != nil {
		return m.sendErr
	}
	select {
	case m.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *mockManagedTransport) Recv(ctx context.Context) (*protocol.Message, error) {
	if m.recvIgnoresCtx {
		// Block solely on recvCh; deliberately ignore ctx cancellation.
		msg, ok := <-m.recvCh
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	}
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

func (m *mockManagedTransport) Pid() int { return 0 }

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

// --- QUM-582: race-safe InTurn() accessor ---
//
// These tests assert the contract for Session.InTurn(): it returns
// true iff a turn is currently in flight and that turn was created
// autonomously by the SDK (system:init while no StartTurn was pending) — not
// for sprawl-initiated turns and not when no turn is active at all. The
// accessor MUST take s.mu so it is race-safe against the persistent reader
// goroutine mutating s.currentTurn concurrently.

// TestSession_InTurn_NoTurn verifies the accessor returns false on
// a freshly-constructed session with no frames in flight.
func TestSession_InTurn_NoTurn(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-1"})
	concrete, ok := sess.(*session)
	if !ok {
		t.Fatalf("session type = %T, want *session", sess)
	}
	if concrete.InTurn() {
		t.Errorf("InTurn() = true on fresh session, want false")
	}
}

// TestSession_InTurn_DuringAutonomousTurn verifies the accessor
// returns true while an autonomous frame (opened by system:init with no
// pending StartTurn) is in flight, and flips back to false after the matching
// result frame.
func TestSession_InTurn_DuringAutonomousTurn(t *testing.T) {
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

	if !sess.(*session).InTurn() {
		t.Errorf("InTurn() = false during autonomous frame, want true")
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

	if sess.(*session).InTurn() {
		t.Errorf("InTurn() = true after autonomous frame's result, want false (frame should be cleared)")
	}
}

// TestSession_InTurn_ClearedOnReaderError verifies that when the
// reader exits because transport.Recv returns an error (io.EOF), the
// orphan-frame teardown defer at session.go:310-329 clears currentTurn, so
// the accessor returns false.
func TestSession_InTurn_ClearedOnReaderError(t *testing.T) {
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
	if !sess.(*session).InTurn() {
		t.Fatalf("InTurn() = false before reader exit, want true (precondition)")
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

	if sess.(*session).InTurn() {
		t.Errorf("InTurn() = true after reader exit, want false (orphan teardown should clear currentTurn)")
	}
}

// TestSession_InTurn_RaceSafeUnderConcurrentReader exists to be
// run under `go test -race`. A reader goroutine spins on
// session.InTurn() while the test drives the transport reader
// through init+result frames. Any unsynchronized read of
// s.currentTurn.autonomous (i.e. an implementation that fails to take s.mu)
// MUST be reported by the race detector. Without -race this test is just a
// sanity loop — its real value is the race instrumentation.
func TestSession_InTurn_RaceSafeUnderConcurrentReader(t *testing.T) {
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
				_ = concrete.InTurn()
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

// --- QUM-692: InTurn must reflect ANY open backend turn, not just autonomous ---
//
// TODO(QUM-692): the implementer should rename Session.InTurn() to
// Session.InTurn() (and the underlying TreeNode/AgentInfo fields) along with
// this test reference. Until then, the existing accessor name is used so the
// suite still compiles. The behaviour assertion below is the one that
// changes: the accessor must return TRUE during a sprawl-initiated turn, not
// false.
//
// This test will fail today because the current implementation only sets the
// flag for SDK-initiated (autonomous) turns. After the QUM-692 fix, every
// open turn — sprawl-initiated included — must show up as "in turn" so the
// tree's working indicator stays lit while the backend is busy.
func TestSession_InTurn_TrueDuringSprawlTurn(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	events, err := sess.StartTurn(ctx, "hello")
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	// Drain the user-frame send so we know StartTurn has populated currentTurn.
	select {
	case <-transport.sendCh:
	case <-time.After(2 * time.Second):
		t.Fatal("StartTurn did not send user frame")
	}

	// Feed an assistant frame so the turn is observably in flight, and wait
	// for the subscriber to see it — confirms the reader has progressed past
	// the point where InTurn() must be true.
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)
	select {
	case <-events:
	case <-time.After(2 * time.Second):
		t.Fatal("assistant frame never delivered to subscriber")
	}

	if !sess.(*session).InTurn() {
		t.Errorf("InTurn() = false during sprawl-initiated turn, want true (QUM-692: accessor must reflect ANY open turn)")
	}

	// Close out cleanly.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
	drainMessages(events)

	// After the result frame the turn is closed; accessor must flip back.
	if sess.(*session).InTurn() {
		t.Errorf("InTurn() = true after result frame, want false (turn is closed)")
	}
}

// QUM-760: an unsolicited transport.Recv error (subprocess died — e.g. SIGKILL
// closed claude's stdout, transport sees EOF) must promote to terminalErr so
// the runtime-side terminalErrHandler fires. Without this the supervisor's
// watchHandleExit is structurally blind to idle-subprocess death and disk
// Status never flips to "died".
func TestSession_RunReader_UnsolicitedEOFPromotesToTerminalErr(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-eof"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	handlerFired := make(chan error, 1)
	sess.(interface {
		SetTerminalErrorHandler(func(error))
	}).SetTerminalErrorHandler(func(err error) {
		select {
		case handlerFired <- err:
		default:
		}
	})

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate subprocess exit: close recvCh so transport.Recv returns io.EOF
	// without anyone having called sess.Close() first.
	close(transport.recvCh)

	select {
	case err := <-handlerFired:
		if !errors.Is(err, ErrSubprocessExited) {
			t.Errorf("terminalErrHandler fired with %v, want ErrSubprocessExited", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminalErrHandler did not fire within 2s after unsolicited EOF")
	}

	if got := sess.(*session).terminalErr; !errors.Is(got, ErrSubprocessExited) {
		t.Errorf("session.terminalErr = %v, want ErrSubprocessExited", got)
	}
}

// QUM-760: a planned Close() that races against EOF must NOT promote to
// terminalErr — the caller is committed to a clean teardown and a spurious
// Died classification would convert every retire/pause cleanup into a
// faulted-looking exit on disk.
func TestSession_RunReader_ClosedEOFDoesNotPromoteToTerminalErr(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-close"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	handlerFired := make(chan error, 1)
	sess.(interface {
		SetTerminalErrorHandler(func(error))
	}).SetTerminalErrorHandler(func(err error) {
		select {
		case handlerFired <- err:
		default:
		}
	})

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Initiate Close() first (sets s.closing=true, then cancels readerCtx).
	// The reader's transport.Recv unblocks via ctx-cancel and observes
	// closing=true → does NOT promote.
	_ = sess.Close()

	// Give the reader a chance to unwind and (incorrectly, if buggy) fire
	// the handler. We assert ABSENCE of handler fire within a generous
	// window.
	select {
	case err := <-handlerFired:
		t.Fatalf("terminalErrHandler fired during planned Close() with err=%v (should be suppressed by closing gate)", err)
	case <-time.After(200 * time.Millisecond):
		// Expected: no handler fire.
	}

	if got := sess.(*session).terminalErr; got != nil {
		t.Errorf("session.terminalErr = %v after planned Close(), want nil", got)
	}
}

// QUM-760: idempotency / sticky-once — if an unsolicited EOF promotes to
// terminalErr and then a real Close() runs, the handler must NOT fire a
// second time (setTerminalErr is sticky-once by contract; ErrSubprocessExited
// stays observable).
func TestSession_RunReader_EOFPromoteIsStickyOnce(t *testing.T) {
	transport := newMockManagedTransport()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-sticky"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var fireCount int
	var fireMu sync.Mutex
	sess.(interface {
		SetTerminalErrorHandler(func(error))
	}).SetTerminalErrorHandler(func(error) {
		fireMu.Lock()
		fireCount++
		fireMu.Unlock()
	})

	if err := sess.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	close(transport.recvCh) // first promotion
	// Wait until the handler has fired once.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fireMu.Lock()
		got := fireCount
		fireMu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = sess.Close()
	// Settle.
	time.Sleep(100 * time.Millisecond)

	fireMu.Lock()
	got := fireCount
	fireMu.Unlock()
	if got != 1 {
		t.Errorf("terminalErrHandler fired %d times, want exactly 1 (sticky-once)", got)
	}
}

// TestSession_ControlCancelRequest_CancelsInflight proves an inbound
// control_cancel_request (CLI→client) cancels the matching in-flight async
// MCP handler, keyed by request_id. The blocking bridge returns ctx.Err()
// when its ctx is cancelled, which surfaces as an error control_response.
func TestSession_ControlCancelRequest_CancelsInflight(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newBlockingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, session, transport, bridge)
	events, err := session.StartTurn(ctx, "go")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	awaitBridgeEntry(ctx, t, bridge)

	// CLI cancels the control request it issued, keyed by request_id.
	transport.feedMessage(t, `{"type":"control_cancel_request","request_id":"mcp-1"}`)

	gotErrorResponse := false
	deadline := time.After(1 * time.Second)
	for !gotErrorResponse {
		select {
		case sent := <-transport.sendCh:
			if isErrorControlResponse(t, sent, "mcp-1") {
				gotErrorResponse = true
			}
		case <-deadline:
			t.Fatal("control_cancel_request did not cancel the in-flight handler (no error control_response for mcp-1)")
		}
	}
	if err := session.LastTurnError(); err != nil {
		t.Errorf("LastTurnError = %v, want nil (cancel must not fault the session)", err)
	}

	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0}`)
	close(transport.recvCh)
	drainMessages(events)
}

// TestSession_ControlCancelRequest_UnknownIDNoOpAndNoFault proves a cancel
// for an unrelated (or empty) request_id is a no-op: the live handler stays
// parked and the session never faults. This is the zero-behavior-change
// guard — the installed CLI does not emit this frame today.
func TestSession_ControlCancelRequest_UnknownIDNoOpAndNoFault(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newBlockingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, session, transport, bridge)
	events, err := session.StartTurn(ctx, "go")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	awaitBridgeEntry(ctx, t, bridge)

	// A cancel for an unrelated id and a malformed (empty-id) cancel must
	// both be no-ops.
	transport.feedMessage(t, `{"type":"control_cancel_request","request_id":"other"}`)
	transport.feedMessage(t, `{"type":"control_cancel_request"}`)

	// No error control_response for mcp-1 should appear: the handler stays
	// parked because its ctx was never cancelled.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case sent := <-transport.sendCh:
			if isErrorControlResponse(t, sent, "mcp-1") {
				t.Fatal("non-matching control_cancel_request cancelled the in-flight handler")
			}
		case <-deadline:
			goto settled
		}
	}
settled:
	if bridge.callCount() != 1 {
		t.Errorf("bridge callCount = %d, want 1 (handler must stay parked)", bridge.callCount())
	}
	if err := session.LastTurnError(); err != nil {
		t.Errorf("LastTurnError = %v, want nil after no-op cancels", err)
	}

	// Cleanup: release the still-parked handler, end the turn.
	bridge.release("1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0}`)
	close(transport.recvCh)
	drainMessages(events)
}
