// QUM-600: Session.Interrupt must be bounded so a wedged transport.Send
// (claude stdin write that does not honor ctx) cannot stall the caller
// indefinitely — it wraps transport.Send in a bounded goroutine + timer and
// returns ErrInterruptTimeout on expiry.
//
// QUM-827: Interrupt must NOT cancel in-flight async MCP handlers (a user Esc
// aborts the model turn only; cancelling handlers crashes the CLI via an error
// control_response). In-flight handler cancellation happens only at genuine
// teardown (Close → drainInflight). The tests below pin both contracts.
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

// wedgingTransport is a transport whose Send blocks forever and does NOT
// honor ctx — matching the real-world wedge mode observed when claude's
// stdin pipe is stuck and the OS-level write blocks below the ctx-checking
// layer in the adapter.
type wedgingTransport struct {
	recvCh chan *protocol.Message
	mu     sync.Mutex
	sends  int
	// release, if non-nil, is signalled by callers to let any blocked
	// Send returns cleanly during test teardown. Production wedge mode
	// never releases.
	release chan struct{}
}

func newWedgingTransport() *wedgingTransport {
	return &wedgingTransport{
		recvCh:  make(chan *protocol.Message, 16),
		release: make(chan struct{}),
	}
}

func (w *wedgingTransport) Send(_ context.Context, _ any) error {
	w.mu.Lock()
	w.sends++
	w.mu.Unlock()
	// Intentionally ignore ctx — mirrors the wedge mode.
	<-w.release
	return nil
}

func (w *wedgingTransport) Recv(ctx context.Context) (*protocol.Message, error) {
	select {
	case msg, ok := <-w.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *wedgingTransport) Close() error { return nil }
func (w *wedgingTransport) Wait() error  { return nil }
func (w *wedgingTransport) Kill() error  { return nil }
func (w *wedgingTransport) Pid() int     { return 0 }

// TestSession_Interrupt_BoundedOnWedgedTransportSend pins the QUM-600
// contract: even when transport.Send wedges and does not honor ctx, a call
// to Session.Interrupt must return within interruptSendTimeout + slack and
// surface ErrInterruptTimeout. Without the bounded wrapper, Interrupt
// would block forever in transport.Send.
func TestSession_Interrupt_BoundedOnWedgedTransportSend(t *testing.T) {
	transport := newWedgingTransport()
	defer close(transport.release) // unblock the wedged Send so the test goroutine exits

	sess := NewSession(transport, SessionConfig{SessionID: "sess-wedge"})

	start := time.Now()
	err := sess.Interrupt(context.Background())
	elapsed := time.Since(start)

	upper := interruptSendTimeout + 500*time.Millisecond
	if elapsed > upper {
		t.Fatalf("Interrupt returned after %v; want <= %v (QUM-600: bounded by interruptSendTimeout)", elapsed, upper)
	}
	if !errors.Is(err, ErrInterruptTimeout) {
		t.Fatalf("Interrupt err = %v, want errors.Is(_, ErrInterruptTimeout)", err)
	}
}

// TestSession_Interrupt_DoesNotCancelInflightHandler_WedgedSend pins the
// QUM-827 fix while preserving the QUM-600 bounded-return guarantee. A user
// Esc-abort (Session.Interrupt) must NOT cancel in-flight async MCP handlers:
// cancelling them makes a ctx-respecting handler return ctx.Err(), which
// dispatchMCPAsync turns into an `error` control_response that crashes the
// claude CLI subprocess → spurious "Session Error" / resume churn. Interrupt
// must send ONLY the SDK interrupt control_request (still bounded so a wedged
// transport.Send cannot stall the caller). In-flight handlers are cancelled
// only at genuine teardown (drainInflight) — see
// TestSession_DrainInflight_CancelsInflightHandler.
func TestSession_Interrupt_DoesNotCancelInflightHandler_WedgedSend(t *testing.T) {
	transport := newDelayedWedgeTransport()
	defer transport.releaseAll()

	bridge := newCancelObservableBridge()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-wedge-inflight"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithDelayedTransport(ctx, t, sess, transport, bridge)
	events, err := sess.StartTurn(ctx, "go")
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	// drain the user-prompt send
	transport.consumeOneSend(t)

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	bridge.awaitEntry(ctx, t)

	// Engage wedge mode — any future transport.Send will block indefinitely
	// without honoring ctx.
	transport.engageWedge()

	// Interrupt must still return bounded with ErrInterruptTimeout (QUM-600).
	start := time.Now()
	ierr := sess.Interrupt(context.Background())
	elapsed := time.Since(start)
	upper := interruptSendTimeout + 500*time.Millisecond
	if elapsed > upper {
		t.Fatalf("Interrupt returned after %v; want <= %v", elapsed, upper)
	}
	if !errors.Is(ierr, ErrInterruptTimeout) {
		t.Fatalf("Interrupt err = %v, want errors.Is(_, ErrInterruptTimeout)", ierr)
	}

	// QUM-827: the in-flight bridge ctx must NOT have been cancelled by the
	// Interrupt. cancelObservableBridge publishes on cancelledCh only when its
	// HandleIncoming ctx is cancelled.
	select {
	case <-bridge.cancelledCh:
		t.Fatal("QUM-827: Session.Interrupt cancelled an in-flight MCP handler; a user Esc must abort the model turn only, not cancel handlers")
	case <-time.After(300 * time.Millisecond):
		// good — handler ctx stayed alive
	}

	_ = events // silence unused
}

// TestSession_Interrupt_DoesNotCancelInflightHandler is the QUM-827 core guard
// on the normal (non-wedged) path: with the reader live, an Interrupt while an
// async MCP handler is in flight aborts the turn (sends the interrupt frame)
// but leaves the handler ctx alive and the session usable — no terminalErr.
func TestSession_Interrupt_DoesNotCancelInflightHandler(t *testing.T) {
	transport := newDelayedWedgeTransport()
	defer transport.releaseAll()

	bridge := newCancelObservableBridge()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-inflight-esc"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithDelayedTransport(ctx, t, sess, transport, bridge)
	if _, err := sess.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	transport.consumeOneSend(t) // drain the user-prompt send

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	bridge.awaitEntry(ctx, t)

	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// The interrupt control_request must be the next frame on the wire.
	sent := transport.consumeOneSend(t)
	data, _ := json.Marshal(sent)
	var parsed struct {
		Type    string `json:"type"`
		Request struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal interrupt frame: %v", err)
	}
	if parsed.Type != "control_request" || parsed.Request.Subtype != "interrupt" {
		t.Fatalf("Interrupt sent %s/%s; want control_request/interrupt", parsed.Type, parsed.Request.Subtype)
	}

	// The in-flight handler ctx must stay alive (no spurious cancellation →
	// no error control_response → no CLI crash).
	select {
	case <-bridge.cancelledCh:
		t.Fatal("QUM-827: Session.Interrupt cancelled an in-flight MCP handler on the normal path")
	case <-time.After(300 * time.Millisecond):
	}

	// The session must not have been torn down.
	if sess.IsTerminallyFaulted() {
		t.Fatal("QUM-827: session faulted after a bare Esc Interrupt; it must stay alive")
	}
}

// TestSession_DrainInflight_CancelsInflightHandler preserves the QUM-552/QUM-600
// teardown guarantee: in-flight async MCP handlers ARE cancelled at genuine
// session teardown (Close cancels the detached reader ctx → runReader's defer
// drainInflight runs), independent of the now-decoupled Interrupt path.
func TestSession_DrainInflight_CancelsInflightHandler(t *testing.T) {
	transport := newDelayedWedgeTransport()
	defer transport.releaseAll()

	bridge := newCancelObservableBridge()
	sess := NewSession(transport, SessionConfig{SessionID: "sess-inflight-teardown"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithDelayedTransport(ctx, t, sess, transport, bridge)
	if _, err := sess.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	transport.consumeOneSend(t)

	transport.feedMessage(t, `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`)
	bridge.awaitEntry(ctx, t)

	// Genuine teardown: Close cancels the detached reader ctx → runReader
	// exits → defer drainInflight() cancels every in-flight handler.
	go func() { _ = sess.Close() }()

	select {
	case <-bridge.cancelledCh:
		// good — teardown cancelled the in-flight handler
	case <-time.After(inflightDrainTimeout + 1*time.Second):
		t.Fatal("teardown did not cancel the in-flight MCP handler (QUM-552/QUM-600 drainInflight guarantee broken)")
	}
}

// cancelObservableBridge is a ToolBridge that blocks until ctx is cancelled
// and publishes the cancellation on cancelledCh so tests can deterministically
// observe inflight-handler cancellation.
type cancelObservableBridge struct {
	entered     chan struct{}
	cancelledCh chan struct{}
}

func newCancelObservableBridge() *cancelObservableBridge {
	return &cancelObservableBridge{
		entered:     make(chan struct{}, 1),
		cancelledCh: make(chan struct{}, 1),
	}
}

func (b *cancelObservableBridge) HandleIncoming(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case b.cancelledCh <- struct{}{}:
	default:
	}
	return nil, ctx.Err()
}

func (b *cancelObservableBridge) awaitEntry(ctx context.Context, t *testing.T) {
	t.Helper()
	select {
	case <-b.entered:
	case <-ctx.Done():
		t.Fatalf("bridge HandleIncoming never invoked: %v", ctx.Err())
	}
}

// delayedWedgeTransport wraps mockManagedTransport with a switchable wedge
// mode: until engageWedge() is called, Send behaves like mockManagedTransport
// (ctx-respecting, records to sendCh). After engageWedge(), Send blocks
// forever ignoring ctx — matching the QUM-600 wedge scenario.
type delayedWedgeTransport struct {
	sendCh  chan any
	recvCh  chan *protocol.Message
	wedge   chan struct{} // closed when wedge mode engages
	release chan struct{} // closed in teardown to unblock any wedged sends
	wedgeMu sync.Mutex
	engaged bool
}

func newDelayedWedgeTransport() *delayedWedgeTransport {
	return &delayedWedgeTransport{
		sendCh:  make(chan any, 100),
		recvCh:  make(chan *protocol.Message, 100),
		wedge:   make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (d *delayedWedgeTransport) Send(ctx context.Context, msg any) error {
	d.wedgeMu.Lock()
	engaged := d.engaged
	d.wedgeMu.Unlock()
	if engaged {
		// Wedge mode: ignore ctx, block on release.
		<-d.release
		return nil
	}
	select {
	case d.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *delayedWedgeTransport) Recv(ctx context.Context) (*protocol.Message, error) {
	select {
	case msg, ok := <-d.recvCh:
		if !ok {
			return nil, io.EOF
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *delayedWedgeTransport) Close() error { return nil }
func (d *delayedWedgeTransport) Wait() error  { return nil }
func (d *delayedWedgeTransport) Kill() error  { return nil }
func (d *delayedWedgeTransport) Pid() int     { return 0 }

func (d *delayedWedgeTransport) engageWedge() {
	d.wedgeMu.Lock()
	d.engaged = true
	d.wedgeMu.Unlock()
	close(d.wedge)
}

func (d *delayedWedgeTransport) releaseAll() {
	select {
	case <-d.release:
	default:
		close(d.release)
	}
}

func (d *delayedWedgeTransport) feedMessage(t *testing.T, raw string) {
	t.Helper()
	var msg protocol.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("feedMessage: %v", err)
	}
	msg.Raw = json.RawMessage(raw)
	d.recvCh <- &msg
}

func (d *delayedWedgeTransport) consumeOneSend(t *testing.T) any {
	t.Helper()
	select {
	case sent := <-d.sendCh:
		return sent
	case <-time.After(1 * time.Second):
		t.Fatalf("no send observed within 1s")
		return nil
	}
}

// initSessionWithDelayedTransport runs the Initialize handshake against a
// delayedWedgeTransport. Mirrors initSessionWithBridge but reads/writes
// against delayedWedgeTransport's channels.
func initSessionWithDelayedTransport(ctx context.Context, t *testing.T, s Session, transport *delayedWedgeTransport, bridge ToolBridge) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sent := <-transport.sendCh
		data, err := json.Marshal(sent)
		if err != nil {
			t.Errorf("marshal initialize: %v", err)
			return
		}
		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Errorf("unmarshal initialize: %v", err)
			return
		}
		reqID, _ := parsed["request_id"].(string)
		transport.feedMessage(t, `{"type":"control_response","response":{"subtype":"success","request_id":"`+reqID+`"}}`)
	}()
	if err := s.Initialize(ctx, InitSpec{ToolBridge: bridge}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	<-done
}
