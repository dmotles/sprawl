package host

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// mockTransport implements Transport for router tests.
type mockTransport struct {
	sendCh chan any
	recvCh chan *protocol.Message
	closed bool
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		sendCh: make(chan any, 100),
		recvCh: make(chan *protocol.Message, 100),
	}
}

func (m *mockTransport) Send(ctx context.Context, msg any) error {
	select {
	case m.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *mockTransport) Recv(ctx context.Context) (*protocol.Message, error) {
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

func (m *mockTransport) Close() error {
	m.closed = true
	return nil
}

// feedMessage injects a message into the mock transport's receive channel.
func (m *mockTransport) feedMessage(t *testing.T, raw string) {
	t.Helper()
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("feedMessage: unmarshal error: %v", err)
	}
	msg.Raw = json.RawMessage(raw)
	m.recvCh <- &msg
}

func TestRouter_RegisterHandler(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	called := false
	r.RegisterHandler("can_use_tool", func(ctx context.Context, requestID string, payload json.RawMessage) error {
		called = true
		return nil
	})

	// Verify the handler was stored - we do this by dispatching a message
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mt.feedMessage(t, `{"type":"control_request","request_id":"req-1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}`)
	close(mt.recvCh) // EOF after one message

	r.ReadLoop(ctx)

	if !called {
		t.Error("registered handler was not called")
	}
}

func TestRouter_ReadLoopDispatchesControlRequest(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	var gotRequestID string
	var gotPayload json.RawMessage
	r.RegisterHandler("can_use_tool", func(ctx context.Context, requestID string, payload json.RawMessage) error {
		gotRequestID = requestID
		gotPayload = payload
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mt.feedMessage(t, `{"type":"control_request","request_id":"req-42","request":{"subtype":"can_use_tool","tool_name":"Read"}}`)
	close(mt.recvCh)

	r.ReadLoop(ctx)

	if gotRequestID != "req-42" {
		t.Errorf("handler requestID = %q, want %q", gotRequestID, "req-42")
	}
	if gotPayload == nil {
		t.Fatal("handler payload is nil")
	}

	var payload map[string]any
	if err := json.Unmarshal(gotPayload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["tool_name"] != "Read" {
		t.Errorf("payload tool_name = %v, want %q", payload["tool_name"], "Read")
	}
}

func TestRouter_ReadLoopDeliversControlResponse(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	// Set up a pending control request that expects a response
	responseCh := make(chan json.RawMessage, 1)
	r.AddPendingControl("req-99", responseCh)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mt.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"req-99"}}`)
	close(mt.recvCh)

	r.ReadLoop(ctx)

	select {
	case resp := <-responseCh:
		if resp == nil {
			t.Fatal("response is nil")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for control response")
	}
}

func TestRouter_ReadLoopHandlesControlCancelRequest(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	// Register a handler that blocks until its context is cancelled
	handlerStarted := make(chan struct{})
	handlerCancelled := make(chan struct{})

	r.RegisterHandler("long_running", func(ctx context.Context, requestID string, payload json.RawMessage) error {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerCancelled)
		return ctx.Err()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Feed the control request, then a cancel for it
	mt.feedMessage(t, `{"type":"control_request","request_id":"req-slow","request":{"subtype":"long_running"}}`)

	// Start ReadLoop in background
	done := make(chan struct{})
	go func() {
		r.ReadLoop(ctx)
		close(done)
	}()

	// Wait for handler to start, then send cancel
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	mt.feedMessage(t, `{"type":"control_cancel_request","request_id":"req-slow"}`)
	close(mt.recvCh)

	select {
	case <-handlerCancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("handler context was not cancelled")
	}

	<-done
}

func TestRouter_ReadLoopPassesNormalMessages(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mt.feedMessage(t, `{"type":"assistant","uuid":"msg-1","message":{"role":"assistant","content":[{"type":"text","text":"Hello"}]}}`)
	mt.feedMessage(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":100,"num_turns":1,"total_cost_usd":0.01}`)
	close(mt.recvCh)

	// Read messages concurrently since ReadLoop closes the channel when done
	ch := r.MessagesChan()
	var got []*protocol.Message
	done := make(chan struct{})
	go func() {
		for msg := range ch {
			got = append(got, msg)
		}
		close(done)
	}()

	r.ReadLoop(ctx)
	<-done

	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}
	if got[0].Type != "assistant" {
		t.Errorf("first message Type = %q, want %q", got[0].Type, "assistant")
	}
	if got[1].Type != "result" {
		t.Errorf("second message Type = %q, want %q", got[1].Type, "result")
	}
}

func TestRouter_ReadLoopIgnoresKeepAlive(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mt.feedMessage(t, `{"type":"keep_alive"}`)
	mt.feedMessage(t, `{"type":"assistant","uuid":"msg-1","message":{"role":"assistant","content":[]}}`)
	close(mt.recvCh)

	ch := r.MessagesChan()
	var got []*protocol.Message
	done := make(chan struct{})
	go func() {
		for msg := range ch {
			got = append(got, msg)
		}
		close(done)
	}()

	r.ReadLoop(ctx)
	<-done

	// Only the assistant message should appear (keep_alive filtered out)
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	if got[0].Type != "assistant" {
		t.Errorf("message Type = %q, want %q", got[0].Type, "assistant")
	}
}

func TestRouter_SendControlRequestAndWait(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start ReadLoop in background
	go func() {
		// Simulate a response arriving after a short delay
		time.Sleep(50 * time.Millisecond)
		mt.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"ctrl-1","data":"ok"}}`)
		close(mt.recvCh)
	}()

	go r.ReadLoop(ctx)

	resp, err := r.SendControlRequest(ctx, "ctrl-1", map[string]string{"subtype": "initialize"})
	if err != nil {
		t.Fatalf("SendControlRequest() error: %v", err)
	}
	if resp == nil {
		t.Fatal("SendControlRequest() returned nil response")
	}
}

func TestRouter_SendControlRequestRespectsContext(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// ReadLoop running but no response will arrive
	go r.ReadLoop(ctx)

	_, err := r.SendControlRequest(ctx, "ctrl-timeout", map[string]string{"subtype": "initialize"})
	if err == nil {
		t.Fatal("SendControlRequest() expected context error, got nil")
	}
}

func TestRouter_ConcurrentHandlerRegistration(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			subtype := "handler_" + string(rune('a'+n))
			r.RegisterHandler(subtype, func(ctx context.Context, requestID string, payload json.RawMessage) error {
				return nil
			})
		}(i)
	}
	wg.Wait()

	// No panics or data races - test passes if we get here
}
