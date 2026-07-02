// QUM-600: Real.Retire(abandon=true) must take the teardown-only stop path
// (handle.StopAbandon) so the polite Session.Interrupt issued by Stop is
// skipped. abandon=false continues to use the polite Stop path. These tests
// are RED until:
//   - RuntimeHandle interface gains StopAbandon(ctx) error
//   - AgentRuntime gains StopAbandon(ctx) error that delegates to handle
//   - Real.Retire branches on the abandon flag and calls StopAbandon instead
//     of Stop on the runtime when abandon=true
package supervisor

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRealRetire_AbandonTrue_CallsStopAbandonNotStop pins the QUM-600
// abandon path: handle.StopAbandon is invoked, handle.Stop is not.
func TestRealRetire_AbandonTrue_CallsStopAbandonNotStop(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Worktree = ""
	agentState.Branch = ""
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, name string, _, _, _, _, _, _ bool) ([]string, error) {
		return []string{name}, state.DeleteAgent(tmpDir, name)
	}

	// abandon=true. Other flags: mergeFirst=false cascade=false noValidate=true.
	if _, err := r.Retire(context.Background(), "", "alice", false, true, false, true); err != nil {
		t.Fatalf("Retire(abandon=true) error: %v", err)
	}

	if got := session.stopAbandonCalls.Load(); got != 1 {
		t.Errorf("session.stopAbandonCalls = %d, want 1 (QUM-600: abandon=true must route through StopAbandon)", got)
	}
	if got := session.stopCalls.Load(); got != 0 {
		t.Errorf("session.stopCalls = %d, want 0 (abandon path must NOT call Stop)", got)
	}
}

// TestRealRetire_AbandonFalse_CallsStopNotStopAbandon pins the legacy
// polite path: abandon=false continues to call handle.Stop, never
// StopAbandon.
func TestRealRetire_AbandonFalse_CallsStopNotStopAbandon(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	agentState.Worktree = ""
	agentState.Branch = ""
	saveTestAgent(t, tmpDir, agentState)
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: session})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	r.retireFn = func(_ context.Context, _ *agentops.RetireDeps, name string, _, _, _, _, _, _ bool) ([]string, error) {
		return []string{name}, state.DeleteAgent(tmpDir, name)
	}

	// abandon=false.
	if _, err := r.Retire(context.Background(), "", "alice", false, false, false, true); err != nil {
		t.Fatalf("Retire(abandon=false) error: %v", err)
	}

	if got := session.stopCalls.Load(); got != 1 {
		t.Errorf("session.stopCalls = %d, want 1 (abandon=false must route through Stop)", got)
	}
	if got := session.stopAbandonCalls.Load(); got != 0 {
		t.Errorf("session.stopAbandonCalls = %d, want 0 (polite path must NOT call StopAbandon)", got)
	}
}
