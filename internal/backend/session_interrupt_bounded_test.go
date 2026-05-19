// QUM-600: Session.Interrupt must be bounded so a wedged transport.Send
// (claude stdin write that does not honor ctx) cannot stall the caller
// indefinitely. The implementation must wrap transport.Send in a bounded
// goroutine + timer and return ErrInterruptTimeout on expiry, while still
// cancelling every in-flight async MCP handler ctx (the cancellation must
// not be gated on the wire send succeeding).
//
// These tests are RED until the implementer adds:
//   - const interruptSendTimeout (package-internal, this file in same package)
//   - var ErrInterruptTimeout (exported sentinel)
//   - the bounded Send wrapper inside session.Interrupt
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

// TestSession_Interrupt_CancelsInflightHandlersEvenOnTimeout pins the
// QUM-600 invariant that the in-flight async-MCP-handler cancellation must
// fire BEFORE the bounded Send wrapper waits on the wire — so a wedged
// transport.Send cannot also keep a long-running tool handler alive past
// Interrupt's return.
func TestSession_Interrupt_CancelsInflightHandlersEvenOnTimeout(t *testing.T) {
	// We need a session with an in-flight async MCP handler. Reuse the
	// blockingToolBridge pattern from session_async_dispatch_test.go but
	// swap the transport for the wedging one. Initialize uses transport.Send
	// itself — so we must use a normal transport for init, then somehow
	// arrange for the subsequent Interrupt to hit the wedge.
	//
	// Simpler approach: wire a wedging transport whose Send wedges ONLY
	// after the initial init+startTurn+control_request handshakes have
	// completed. We implement this by switching modes via an atomic gate.
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

	// Interrupt should return bounded with ErrInterruptTimeout AND it must
	// cancel the in-flight bridge ctx so HandleIncoming returns ctx.Err().
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

	// Direct observation: cancelObservableBridge publishes on cancelledCh
	// when its HandleIncoming ctx is cancelled (it returns ctx.Err() at
	// that point). This is the QUM-600 invariant — even with the wire
	// send wedged, the inflight cancel map must have been walked.
	select {
	case <-bridge.cancelledCh:
		// good
	case <-time.After(interruptSendTimeout + 1*time.Second):
		t.Fatalf("in-flight bridge ctx was not cancelled within Interrupt's timeout window (QUM-600: cancellation must fire even when transport.Send is wedged)")
	}

	_ = events // silence unused
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
