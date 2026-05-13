package backend

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// blockingToolBridge is a ToolBridge whose HandleIncoming parks each call on
// a per-payload gate keyed by the JSON-RPC `id` field. The test releases each
// call independently, allowing simulation of concurrent / out-of-order MCP
// dispatch. Without async dispatch in session.readTurn, only the first call
// ever fires — later control_request frames stay parked in the transport.
type blockingToolBridge struct {
	mu     sync.Mutex
	calls  []json.RawMessage
	gates  map[string]chan json.RawMessage
	called chan struct{}
}

func newBlockingToolBridge() *blockingToolBridge {
	return &blockingToolBridge{
		gates:  make(map[string]chan json.RawMessage),
		called: make(chan struct{}, 16),
	}
}

func (b *blockingToolBridge) HandleIncoming(ctx context.Context, _ string, msg json.RawMessage) (json.RawMessage, error) {
	b.mu.Lock()
	b.calls = append(b.calls, msg)
	var parsed struct {
		ID json.Number `json:"id"`
	}
	_ = json.Unmarshal(msg, &parsed)
	key := parsed.ID.String()
	ch, ok := b.gates[key]
	if !ok {
		ch = make(chan json.RawMessage, 1)
		b.gates[key] = ch
	}
	b.mu.Unlock()

	b.called <- struct{}{}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockingToolBridge) release(key string, resp json.RawMessage) {
	b.mu.Lock()
	ch, ok := b.gates[key]
	if !ok {
		ch = make(chan json.RawMessage, 1)
		b.gates[key] = ch
	}
	b.mu.Unlock()
	ch <- resp
}

func (b *blockingToolBridge) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

// awaitBridgeEntry blocks until HandleIncoming has been entered or ctx ends.
func awaitBridgeEntry(ctx context.Context, t *testing.T, b *blockingToolBridge) {
	t.Helper()
	select {
	case <-b.called:
	case <-ctx.Done():
		t.Fatalf("bridge HandleIncoming never invoked: %v", ctx.Err())
	}
}

// TestAsyncDispatch_ReadTurnNotParkedByBridge proves that readTurn must
// continue consuming claude's stdout while ToolBridge.HandleIncoming is
// in flight. RED today: the synchronous dispatch in session.readTurn
// parks the loop inside HandleIncoming so the assistant frame queued
// after the control_request is never delivered until the bridge releases.
func TestAsyncDispatch_ReadTurnNotParkedByBridge(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newBlockingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := session.StartTurn(ctx, "go", TurnSpec{Init: InitSpec{ToolBridge: bridge}})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain the user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	awaitBridgeEntry(ctx, t, bridge)

	// Queue a normal assistant frame while the bridge is still blocked.
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`)

	// Assert the assistant frame is delivered BEFORE we release the bridge.
	select {
	case msg, ok := <-events:
		if !ok {
			t.Fatalf("events channel closed before assistant frame arrived")
		}
		if msg.Type != "assistant" {
			t.Fatalf("first event type = %q, want assistant", msg.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("readTurn parked inside ToolBridge.HandleIncoming: assistant frame not delivered while bridge is blocked")
	}

	// Cleanup so the goroutine exits cleanly.
	bridge.release("1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0}`)
	close(transport.recvCh)
	drainMessages(events)
}

// TestAsyncDispatch_InterruptObservableMidBridgeWait proves that:
//
//	(a) Session.Interrupt's control_request reaches the transport while a
//	    ToolBridge call is in flight (already true today — Interrupt sends
//	    directly via transport.Send and does not go through readTurn).
//	(b) A claude stdout frame queued on the transport during the bridge wait
//	    is consumed by readTurn and delivered on events BEFORE the bridge
//	    releases. RED today for the same reason as T1.
func TestAsyncDispatch_InterruptObservableMidBridgeWait(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newBlockingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := session.StartTurn(ctx, "go", TurnSpec{Init: InitSpec{ToolBridge: bridge}})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain the user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	awaitBridgeEntry(ctx, t, bridge)

	// Fire Interrupt from a separate goroutine while bridge is blocked.
	interruptCtx, interruptCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer interruptCancel()
	go func() { _ = session.Interrupt(interruptCtx) }()

	// (a) Interrupt frame reaches the transport's sent-frames log.
	interruptDeadline := time.After(1 * time.Second)
	interruptSeen := false
	for !interruptSeen {
		select {
		case sent := <-transport.sendCh:
			if isInterruptFrame(t, sent) {
				interruptSeen = true
			}
		case <-interruptDeadline:
			t.Fatalf("interrupt control_request did not reach transport during bridge wait")
		}
	}

	// (b) A stdout frame queued during the bridge wait is delivered.
	transport.feedMessage(t, `{"type":"assistant","uuid":"a-2","message":{"role":"assistant","content":[{"type":"text","text":"post-interrupt"}]}}`)
	select {
	case msg, ok := <-events:
		if !ok {
			t.Fatalf("events channel closed before post-interrupt frame arrived")
		}
		if msg.Type != "assistant" {
			t.Fatalf("event type = %q, want assistant", msg.Type)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("readTurn parked: post-interrupt stdout frame not delivered while bridge is blocked")
	}

	// Cleanup.
	bridge.release("1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0}`)
	close(transport.recvCh)
	drainMessages(events)
}

// TestAsyncDispatch_ResponseOrderingOutOfOrder is the canary for whether
// out-of-order control_response writes are safe / wired correctly. It feeds
// two distinct mcp_message control_requests back-to-back and resolves them
// in reverse order. RED today: the synchronous dispatch never reads the
// second control_request, so HandleIncoming is invoked exactly once and
// the test fails at the "await second bridge entry" step.
func TestAsyncDispatch_ResponseOrderingOutOfOrder(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newBlockingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := session.StartTurn(ctx, "go", TurnSpec{Init: InitSpec{ToolBridge: bridge}})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain the user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-2","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":2,"method":"tools/list"}}}`)

	// Both bridge invocations must happen concurrently — today only one fires.
	for i := 0; i < 2; i++ {
		select {
		case <-bridge.called:
		case <-ctx.Done():
			t.Fatalf("bridge entered %d time(s), want 2 (readTurn is parked in the first call): %v", bridge.callCount(), ctx.Err())
		}
	}

	// Resolve out of order: req 2 first, then req 1.
	bridge.release("2", json.RawMessage(`{"jsonrpc":"2.0","id":2,"result":{"v":2}}`))
	bridge.release("1", json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"v":1}}`))

	got := map[string]bool{}
	deadline := time.After(1 * time.Second)
	for len(got) < 2 {
		select {
		case sent := <-transport.sendCh:
			if id, ok := controlResponseRequestID(t, sent); ok {
				got[id] = true
			}
		case <-deadline:
			t.Fatalf("saw control_responses for %v; want mcp-1 and mcp-2", got)
		}
	}
	if !got["mcp-1"] || !got["mcp-2"] {
		t.Fatalf("missing control_responses; got %v", got)
	}

	// Cleanup.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0}`)
	close(transport.recvCh)
	drainMessages(events)
}

// TestAsyncDispatch_InterruptCancelsInflightHandler proves that calling
// Session.Interrupt cancels every in-flight async MCP handler's ctx so
// ctx-respecting tool handlers (retire/delegate/merge/...) actually
// unwind instead of continuing to wait. Asserts:
//
//  1. The blocking bridge — which returns ctx.Err() when its ctx is
//     cancelled — observes its ctx cancelled within ~1s of the
//     Interrupt call.
//  2. A control_response with subtype="error" carrying the cancellation
//     error reaches the transport.
func TestAsyncDispatch_InterruptCancelsInflightHandler(t *testing.T) {
	transport := newMockManagedTransport()
	bridge := newBlockingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := session.StartTurn(ctx, "go", TurnSpec{Init: InitSpec{ToolBridge: bridge}})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	awaitBridgeEntry(ctx, t, bridge)

	// Fire Interrupt — should cancel the bridge ctx.
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// Collect transport sends; expect both the interrupt frame AND an
	// error control_response carrying the ctx cancellation.
	gotInterrupt := false
	gotErrorResponse := false
	deadline := time.After(1 * time.Second)
	for !gotInterrupt || !gotErrorResponse {
		select {
		case sent := <-transport.sendCh:
			if isInterruptFrame(t, sent) {
				gotInterrupt = true
				continue
			}
			if isErrorControlResponse(t, sent, "mcp-1") {
				gotErrorResponse = true
				continue
			}
		case <-deadline:
			t.Fatalf("timeout waiting for {interrupt:%v, errorResponse:%v}", gotInterrupt, gotErrorResponse)
		}
	}

	// Cleanup.
	transport.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":1,"num_turns":1,"total_cost_usd":0.0}`)
	close(transport.recvCh)
	drainMessages(events)
}

func isErrorControlResponse(t *testing.T, sent any, wantRequestID string) bool {
	t.Helper()
	data, err := json.Marshal(sent)
	if err != nil {
		return false
	}
	var parsed struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	return parsed.Type == "control_response" &&
		parsed.Response.Subtype == "error" &&
		parsed.Response.RequestID == wantRequestID
}

func isInterruptFrame(t *testing.T, sent any) bool {
	t.Helper()
	data, err := json.Marshal(sent)
	if err != nil {
		return false
	}
	var parsed struct {
		Type    string `json:"type"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	return parsed.Type == "control_request" && parsed.Request.Subtype == "interrupt"
}

func controlResponseRequestID(t *testing.T, sent any) (string, bool) {
	t.Helper()
	data, err := json.Marshal(sent)
	if err != nil {
		return "", false
	}
	var parsed struct {
		Type     string `json:"type"`
		Response struct {
			RequestID string `json:"request_id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return "", false
	}
	if parsed.Type != "control_response" {
		return "", false
	}
	return parsed.Response.RequestID, true
}
