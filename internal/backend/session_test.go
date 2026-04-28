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
	serverName string
	payload    string
	response   json.RawMessage
}

func (b *mockToolBridge) HandleIncoming(_ context.Context, serverName string, msg json.RawMessage) (json.RawMessage, error) {
	b.serverName = serverName
	b.payload = string(msg)
	return b.response, nil
}

func drainMessages(ch <-chan *protocol.Message) {
	for msg := range ch {
		_ = msg
	}
}

func TestSession_InitializeTreatsEOFAsSuccessAndSendsInitSpec(t *testing.T) {
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
		} else if len(servers) != 1 || servers[0] != "sprawl-ops" {
			t.Errorf("sdkMcpServers = %v, want [sprawl-ops]", servers)
		}

		close(transport.recvCh)
	}()

	if err := session.Initialize(ctx, InitSpec{MCPServerNames: []string{"sprawl-ops"}}); err != nil {
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
	bridge := &mockToolBridge{
		response: json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`),
	}
	session := NewSession(transport, SessionConfig{
		SessionID: "sess-1",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		<-transport.sendCh

		transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl-ops","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
		transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)
		close(transport.recvCh)
	}()

	events, err := session.StartTurn(ctx, "list tools", TurnSpec{
		Init: InitSpec{
			MCPServerNames: []string{"sprawl-ops"},
			ToolBridge:     bridge,
		},
	})
	if err != nil {
		t.Fatalf("StartTurn() error: %v", err)
	}
	drainMessages(events)

	if bridge.serverName != "sprawl-ops" {
		t.Errorf("bridge server = %q, want sprawl-ops", bridge.serverName)
	}
	if bridge.payload == "" {
		t.Error("bridge payload should not be empty")
	}

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
