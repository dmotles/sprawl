package backend

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// readCancelRequest pulls one frame off the mock transport's sendCh and asserts
// it is a cancel_async_message control_request for the given uuid, returning its
// request_id so the test can feed back a matching control_response.
func readCancelRequest(t *testing.T, transport *mockManagedTransport, wantUUID string) string {
	t.Helper()
	select {
	case raw := <-transport.sendCh:
		req, ok := raw.(protocol.CancelAsyncMessageRequest)
		if !ok {
			t.Fatalf("sent frame = %T, want protocol.CancelAsyncMessageRequest", raw)
		}
		if req.Type != "control_request" {
			t.Errorf("Type = %q, want control_request", req.Type)
		}
		if req.Request.Subtype != "cancel_async_message" {
			t.Errorf("Subtype = %q, want cancel_async_message", req.Request.Subtype)
		}
		if req.Request.MessageUUID != wantUUID {
			t.Errorf("MessageUUID = %q, want %q", req.Request.MessageUUID, wantUUID)
		}
		if req.RequestID == "" {
			t.Fatal("RequestID is empty")
		}
		return req.RequestID
	case <-time.After(2 * time.Second):
		t.Fatal("no cancel control_request observed on sendCh")
		return ""
	}
}

// feedCancelAck feeds a control_response carrying the {cancelled} ack for the
// given request_id, matching the CLI 2.1.173 wire shape (the payload is nested
// under response.response).
func feedCancelAck(t *testing.T, transport *mockManagedTransport, requestID string, cancelled bool) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   map[string]any{"cancelled": cancelled},
		},
	})
	if err != nil {
		t.Fatalf("marshal cancel ack: %v", err)
	}
	transport.feedMessage(t, string(raw))
}

type cancelResult struct {
	cancelled bool
	err       error
}

func TestSession_CancelAsyncMessage_AwaitsAck_Cancelled(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-cancel"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = session.Close() }()

	resCh := make(chan cancelResult, 1)
	go func() {
		c, err := session.CancelAsyncMessage(ctx, "uuid-A")
		resCh <- cancelResult{c, err}
	}()

	reqID := readCancelRequest(t, transport, "uuid-A")
	feedCancelAck(t, transport, reqID, true)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("CancelAsyncMessage err = %v", r.err)
		}
		if !r.cancelled {
			t.Error("cancelled = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAsyncMessage did not return after ack")
	}
}

func TestSession_CancelAsyncMessage_Cancelled_False(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-cancel"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = session.Close() }()

	resCh := make(chan cancelResult, 1)
	go func() {
		c, err := session.CancelAsyncMessage(ctx, "uuid-B")
		resCh <- cancelResult{c, err}
	}()

	reqID := readCancelRequest(t, transport, "uuid-B")
	feedCancelAck(t, transport, reqID, false)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("CancelAsyncMessage err = %v", r.err)
		}
		if r.cancelled {
			t.Error("cancelled = true, want false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAsyncMessage did not return after ack")
	}
}

// TestSession_CancelAsyncMessage_RequestIDCorrelation proves the pendingControl
// map correlates each waiter to its own request_id: two concurrent cancels get
// out-of-order acks and each must observe the ack matching its own request.
func TestSession_CancelAsyncMessage_RequestIDCorrelation(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-cancel"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = session.Close() }()

	resA := make(chan bool, 1)
	resB := make(chan bool, 1)
	go func() {
		c, _ := session.CancelAsyncMessage(ctx, "uuid-A")
		resA <- c
	}()
	reqA := readCancelRequest(t, transport, "uuid-A")
	go func() {
		c, _ := session.CancelAsyncMessage(ctx, "uuid-B")
		resB <- c
	}()
	reqB := readCancelRequest(t, transport, "uuid-B")

	// Ack B first (out of order), then A — each with a distinct cancelled value.
	feedCancelAck(t, transport, reqB, false)
	feedCancelAck(t, transport, reqA, true)

	select {
	case c := <-resA:
		if !c {
			t.Error("A cancelled = false, want true (request_id mismatch?)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("A did not return")
	}
	select {
	case c := <-resB:
		if c {
			t.Error("B cancelled = true, want false (request_id mismatch?)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("B did not return")
	}
}

// TestSession_CancelAsyncMessage_SendError surfaces a wire-send failure as a
// non-nil error WITHOUT blocking on an ack, and must not leave a pending waiter
// behind (a subsequent reader exit must not double-unblock / panic).
func TestSession_CancelAsyncMessage_SendError(t *testing.T) {
	transport := newMockManagedTransport()
	sentinel := errors.New("boom: stdin pipe broken")
	transport.sendErr = sentinel
	session := NewSession(transport, SessionConfig{SessionID: "sess-cancel"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = session.Close() }()

	errCh := make(chan error, 1)
	go func() {
		_, err := session.CancelAsyncMessage(ctx, "uuid-E")
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, sentinel) {
			t.Errorf("err = %v, want send sentinel %v", err, sentinel)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAsyncMessage did not return on send error (blocked on ack?)")
	}
}

// TestSession_CancelAsyncMessage_ReaderExit_Unblocks verifies a parked cancel
// returns the reader-exited sentinel (does not hang) when the reader exits
// before the ack.
func TestSession_CancelAsyncMessage_ReaderExit_Unblocks(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-cancel"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := session.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := session.CancelAsyncMessage(ctx, "uuid-C")
		errCh <- err
	}()
	readCancelRequest(t, transport, "uuid-C") // ensure the waiter is registered

	// Close the session -> reader exits, readerDone closes, waiter must unblock.
	_ = session.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrReaderExited) {
			t.Errorf("err = %v, want ErrReaderExited", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAsyncMessage hung after reader exit")
	}
}

// TestSession_CancelAsyncMessage_CtxTimeout verifies the call honors ctx when no
// ack ever arrives, returning a deadline error (not a reader/send error).
func TestSession_CancelAsyncMessage_CtxTimeout(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-cancel"})
	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	if err := session.Start(startCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = session.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := session.CancelAsyncMessage(ctx, "uuid-D")
		errCh <- err
	}()
	readCancelRequest(t, transport, "uuid-D")

	select {
	case err := <-errCh:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CancelAsyncMessage did not honor ctx timeout")
	}
}
