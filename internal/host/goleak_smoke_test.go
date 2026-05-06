package host

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// TestGoleakSmoke_RouterShutsDownCleanly exercises the Router spawn-and-teardown
// path to confirm the goleak harness does not false-positive on clean code:
//   - Constructs a Router with a mock transport.
//   - Starts ReadLoop in a goroutine.
//   - Sends one control_request that triggers the per-request handler goroutine
//     (router.go:113).
//   - Closes the transport's recvCh to drive ReadLoop to EOF.
//   - Asserts ReadLoop returns and all spawned handler goroutines have joined.
func TestGoleakSmoke_RouterShutsDownCleanly(t *testing.T) {
	mt := newMockTransport()
	r := NewRouter(mt)

	handlerDone := make(chan struct{})
	r.RegisterHandler("can_use_tool", func(ctx context.Context, requestID string, payload json.RawMessage) error {
		close(handlerDone)
		return nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Feed one control_request that will spawn a handler goroutine.
	mt.feedMessage(t, `{"type":"control_request","request_id":"smoke-1","request":{"subtype":"can_use_tool","tool_name":"Bash"}}`)

	// Run ReadLoop in a goroutine so we can drive shutdown.
	readLoopDone := make(chan struct{})
	go func() {
		r.ReadLoop(ctx)
		close(readLoopDone)
	}()

	// Wait for handler to run.
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not run")
	}

	// Drive ReadLoop to EOF by closing recvCh.
	close(mt.recvCh)

	select {
	case <-readLoopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ReadLoop did not return after EOF")
	}

	// ReadLoop's deferred close(r.messagesCh) and wg.Wait() guarantees
	// no handler goroutines remain at this point — goleak will assert it.
}
