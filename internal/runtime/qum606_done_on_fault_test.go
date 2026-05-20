package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
)

// TestUnifiedRuntime_TerminalFault_ClosesDone pins QUM-606 R2: when the
// backend session's terminal-error handler fires, UnifiedRuntime must
// cancel its runCtx so the turn loop exits, loopWG unblocks, and
// Done() closes. Before R2, Done() only closed on Stop — so a faulted
// session left AgentRuntime.watchHandleExit structurally blind to
// backend-session death, leaving Lifecycle stuck at Started after a
// silent re-fault (QUM-602 latent gap and the secondary failure mode
// of QUM-606).
func TestUnifiedRuntime_TerminalFault_ClosesDone(t *testing.T) {
	mock := &mockFaultableSession{}
	rt := New(RuntimeConfig{
		Name:    "agent-done-on-fault",
		Session: mock,
	})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(stopCtx)
	})

	// Sanity: Done() must NOT be closed before the fault fires.
	select {
	case <-rt.Done():
		t.Fatal("Done() closed before Start completed — runtime should still be live")
	default:
	}

	// Fire the terminal-error handler the way production code does
	// (session.setTerminalErr → registered handler). R2 contract: this
	// must cancel runCtx, unwinding the turn loop and closing Done().
	mock.fireTerminalErr(backend.ErrSubscriberWedged)

	select {
	case <-rt.Done():
		// Expected: turn loop exited, loopWG drained, done channel closed.
	case <-time.After(2 * time.Second):
		t.Fatal("UnifiedRuntime.Done() did not close within 2s after terminal-error handler fired (QUM-606 R2 regression)")
	}
}
