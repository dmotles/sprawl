package backend

// QUM-636 Part 1: failing unit tests (TDD red phase) for the drainInflight
// bound on shutdown. These tests reference symbols that do not yet exist as
// the right kind: in particular `overrideInflightDrainTimeout` references the
// not-yet-`var` package symbol `inflightDrainTimeout` (today it is a `const`,
// so assigning to it does not compile). This file fails to compile until the
// implementer converts `const inflightDrainTimeout` to a package `var`.
//
// Behavior under test:
//   - On Close()-induced shutdown, drainInflight must NOT block forever on a
//     wedged async MCP handler that ignores ctx; it returns within
//     inflightDrainTimeout.
//   - A ctx-respecting handler cancelled via Interrupt decrements the inflight
//     WaitGroup, so Close completes promptly (well under the drain timeout).

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// overrideInflightDrainTimeout overrides the package var and restores on
// cleanup. References the (currently `const`) package symbol
// `inflightDrainTimeout` so this file fails to compile until it becomes a
// `var`.
func overrideInflightDrainTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := inflightDrainTimeout
	inflightDrainTimeout = d
	t.Cleanup(func() { inflightDrainTimeout = prev })
}

// ctxRespectingToolBridge is a ToolBridge whose HandleIncoming parks until
// EITHER its ctx is cancelled OR the test releases it. Unlike
// blockingToolBridge it honors ctx.Done(), so cancelling the dispatch ctx
// (via Interrupt) unwinds the handler and decrements inflightWG.
type ctxRespectingToolBridge struct {
	called  chan struct{}
	release chan struct{}
}

func newCtxRespectingToolBridge() *ctxRespectingToolBridge {
	return &ctxRespectingToolBridge{
		called:  make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (b *ctxRespectingToolBridge) HandleIncoming(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	b.called <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), nil
	}
}

func awaitCtxBridgeEntry(ctx context.Context, t *testing.T, b *ctxRespectingToolBridge) {
	t.Helper()
	select {
	case <-b.called:
	case <-ctx.Done():
		t.Fatalf("ctx-respecting bridge HandleIncoming never invoked: %v", ctx.Err())
	}
}

// ctxIgnoringToolBridge is a ToolBridge whose HandleIncoming parks purely on a
// release channel and NEVER selects on ctx.Done(). It simulates a genuinely
// wedged async MCP handler: cancelling the dispatch ctx (as Close does) cannot
// unwind it, so drainInflight is forced to hit its time.After(inflightDrainTimeout)
// branch — which is the actual behavior under test in Test 1.
type ctxIgnoringToolBridge struct {
	called  chan struct{}
	release chan struct{}
}

func newCtxIgnoringToolBridge() *ctxIgnoringToolBridge {
	return &ctxIgnoringToolBridge{
		called:  make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (b *ctxIgnoringToolBridge) HandleIncoming(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	b.called <- struct{}{}
	<-b.release // deliberately ignores ctx — simulates a wedged handler
	return json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`), nil
}

func awaitIgnoringBridgeEntry(ctx context.Context, t *testing.T, b *ctxIgnoringToolBridge) {
	t.Helper()
	select {
	case <-b.called:
	case <-ctx.Done():
		t.Fatalf("ctx-ignoring bridge HandleIncoming never invoked: %v", ctx.Err())
	}
}

// mcpControlRequest is the control_request frame that triggers an async MCP
// dispatch (registers inflightWG.Add(1)).
const mcpControlRequest = `{"type":"control_request","request_id":"mcp-1","request":{"subtype":"mcp_message","server_name":"sprawl","message":{"jsonrpc":"2.0","id":1,"method":"tools/list"}}}`

// TestSession_Close_ReturnsWithinBound_WhenDispatchStuck proves that a wedged
// async MCP handler (one that ignores ctx) cannot make Close() block forever.
// drainInflight must give up after inflightDrainTimeout.
//
// RED today: inflightDrainTimeout is a `const`, so overrideInflightDrainTimeout
// fails to compile.
func TestSession_Close_ReturnsWithinBound_WhenDispatchStuck(t *testing.T) {
	overrideInflightDrainTimeout(t, 50*time.Millisecond)

	transport := newMockManagedTransport()
	bridge := newCtxIgnoringToolBridge() // parks on release; never selects on ctx
	session := NewSession(transport, SessionConfig{SessionID: "sess-drain-stuck"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, session, transport, bridge)
	if _, err := session.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain user-prompt send

	// One async dispatch in flight, parked inside the bridge. Because the
	// bridge ignores ctx, Close()'s ctx cancel cannot unwind it — drainInflight
	// MUST hit its time.After(inflightDrainTimeout) branch to return.
	transport.feedMessage(t, mcpControlRequest)
	awaitIgnoringBridgeEntry(ctx, t, bridge)

	// Close must return within a generous bound despite the wedged handler.
	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- session.Close() }()

	select {
	case <-closed:
		elapsed := time.Since(start)
		if elapsed > 2*time.Second {
			t.Fatalf("Close took %v; drainInflight blocked on the stuck dispatch (want <= ~inflightDrainTimeout)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Close did not return within 2s; drainInflight is blocking forever on the wedged dispatch")
	}

	// Release the parked (leaked) dispatch goroutine so it can exit cleanly and
	// not leak across tests.
	close(bridge.release)
}

// TestSession_Close_ReturnsWithinBound_WhenReaderRecvWedged proves that
// Close() does not hang forever when the reader goroutine is wedged inside a
// transport.Recv that ignores ctx cancellation (the real claude-stdout read
// does not honor ctx, and survives transport.Close while the subprocess holds
// the pipe). This is the QUM-636 root-cause teardown hang: the `<-readerDone`
// join in Close must be bounded by inflightDrainTimeout, not unbounded.
//
// RED today: Close blocks forever on the unbounded `<-s.readerDone` at
// session.go:1051 (the test times out via the 2s arm below).
func TestSession_Close_ReturnsWithinBound_WhenReaderRecvWedged(t *testing.T) {
	overrideInflightDrainTimeout(t, 50*time.Millisecond)

	transport := newMockManagedTransport()
	transport.recvIgnoresCtx = true // reader will park in Recv, ignoring ctx
	bridge := newMockToolBridge(json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`))
	session := NewSession(transport, SessionConfig{SessionID: "sess-reader-wedge"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize so the persistent reader is running and parked in Recv.
	initSessionWithBridge(ctx, t, session, transport, bridge)

	// Close must return within a generous bound even though runReader is
	// wedged on a ctx-ignoring Recv and transport.Close does not unblock it.
	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- session.Close() }()

	select {
	case <-closed:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Close took %v; the <-readerDone join is not bounded (want <= ~inflightDrainTimeout)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s; the <-readerDone join is blocking forever on the wedged reader")
	}

	// Unwedge the reader so its goroutine can exit cleanly.
	close(transport.recvCh)
}

// TestSession_Interrupt_DecrementsInflightWaitGroup_OnCtxCancel proves that a
// ctx-respecting async handler cancelled by Interrupt unwinds and decrements
// inflightWG, so the subsequent Close completes well under inflightDrainTimeout
// (i.e. the WaitGroup is already drained — Close does not have to wait out the
// timeout).
//
// RED today: same compile failure on overrideInflightDrainTimeout.
func TestSession_Interrupt_DecrementsInflightWaitGroup_OnCtxCancel(t *testing.T) {
	// Large drain timeout: a passing Close must complete FAST because the WG
	// was already drained by Interrupt, not because the timeout elapsed.
	overrideInflightDrainTimeout(t, 10*time.Second)

	transport := newMockManagedTransport()
	bridge := newCtxRespectingToolBridge()
	session := NewSession(transport, SessionConfig{SessionID: "sess-drain-intr"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	initSessionWithBridge(ctx, t, session, transport, bridge)
	if _, err := session.StartTurn(ctx, "go"); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	<-transport.sendCh // drain user-prompt send

	transport.feedMessage(t, mcpControlRequest)
	awaitCtxBridgeEntry(ctx, t, bridge)

	// Interrupt cancels every in-flight dispatch ctx (session.go ~1002).
	if err := session.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	// With the dispatch ctx cancelled, the handler returns ctx.Err() and the
	// WaitGroup decrements. Close should complete promptly (well under the
	// 10s drain timeout). If it takes anywhere near the timeout, the WG was
	// not decremented on cancel.
	closed := make(chan error, 1)
	start := time.Now()
	go func() { closed <- session.Close() }()

	select {
	case <-closed:
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Close took %v; cancelled ctx-respecting dispatch did not decrement inflightWG", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Close did not return within 2s; inflightWG still held after Interrupt cancelled the dispatch ctx")
	}
}
