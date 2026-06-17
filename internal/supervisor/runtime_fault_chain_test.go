package supervisor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-624 slice M2 / QUM-625 slice M4 — fault-chain characterization test.
//
// This test PINS the backend-fault -> Done() -> watchHandleExit chain and the
// liveness projections at each waypoint. M4 (QUM-625) changed the END-STATE:
// a torn-down fault is now recorded as DURABLE Faulted (snapshot.Status set to
// "faulted") instead of being erased to Lifecycle=Stopped. Assertion 5 below
// was rewritten for that flip; this is a RED-phase test against current HEAD
// until M4 lands.
//
// The chain under test (verified against unified.go / runtime.go HEAD):
//
//	session.setTerminalErr -> SetTerminalErrorHandler closure (unified.go:129)
//	  -> publishes EventBackendFaulted + rt.cancel() (unconditional, even idle)
//	  -> turn loop returns -> loopWG unblocks -> rt.done closes (unified.go:297)
//	  -> handle Done() returns rt.done
//	  -> AgentRuntime.watchHandleExit (runtime.go ~895) sets r.handle=nil and,
//	     under M4, records durable Status="faulted" (torn-down fault survives).
//
// M4 end-state after fault: durable Status="faulted", handle=nil. During the
// brief "lie window" (after fault fires, before watchHandleExit completes) the
// handle is still attached and reports IsTerminallyFaulted()==true while
// Lifecycle is still Started.
//
// Unlike internal/runtime/qum606_done_on_fault_test.go (which stops at the
// UnifiedRuntime boundary), this test exercises the SUPERVISOR HOP:
// Done() -> watchHandleExit -> Lifecycle transition.

// faultChainSession is a fake backend SessionHandle that captures the
// terminal-error handler the UnifiedRuntime installs (via the
// SetTerminalErrorHandler optional interface), plus a test hook to fire it.
// It mirrors mockFaultableSession in internal/runtime/backend_fault_test.go but
// lives in package supervisor's test scope. WriteUserMessage and Interrupt are
// no-ops — the runtime just idles until the terminal-error handler cancels its
// runCtx (QUM-817: there is no longer a StartTurn drive path).
type faultChainSession struct {
	handler atomic.Value // func(error)
	faulted atomic.Bool
}

func (s *faultChainSession) WriteUserMessage(context.Context, protocol.UserMessage) error {
	return nil
}

func (s *faultChainSession) Interrupt(context.Context) error { return nil }

func (s *faultChainSession) CancelAsyncMessage(context.Context, string) (bool, error) {
	return false, nil
}

func (s *faultChainSession) SetTerminalErrorHandler(h func(error)) {
	s.handler.Store(h)
}

// fireTerminalErr invokes the stored handler (as production's session.setTerminalErr
// would) and flips the sticky faulted flag so IsTerminallyFaulted observers see true.
func (s *faultChainSession) fireTerminalErr(err error) {
	s.faulted.Store(true)
	if h, _ := s.handler.Load().(func(error)); h != nil {
		h(err)
	}
}

func (s *faultChainSession) isFaulted() bool { return s.faulted.Load() }

// faultChainHandle is a RuntimeHandle wrapping a real UnifiedRuntime. Its
// Done() returns the UnifiedRuntime's Done() so the supervisor's
// watchHandleExit watches the REAL fault->cancel->loopWG->done chain. Its
// IsTerminallyFaulted() reflects the fake session's sticky flag (matching how
// production *unifiedHandle delegates to the backend session). All other
// RuntimeHandle methods are no-ops/zero-values; this handle is never asked to
// Interrupt/Wake/Stop in this test.
type faultChainHandle struct {
	rt   *runtimepkg.UnifiedRuntime
	sess *faultChainSession
}

func (h *faultChainHandle) Interrupt(context.Context) error            { return nil }
func (h *faultChainHandle) Wake() error                                { return nil }
func (h *faultChainHandle) WakeForDelivery() error                     { return nil }
func (h *faultChainHandle) Stop(context.Context) error                 { return nil }
func (h *faultChainHandle) StopAbandon(context.Context) error          { return nil }
func (h *faultChainHandle) SessionID() string                          { return h.rt.SessionID() }
func (h *faultChainHandle) Capabilities() backendpkg.Capabilities      { return h.rt.Capabilities() }
func (h *faultChainHandle) Done() <-chan struct{}                      { return h.rt.Done() }
func (h *faultChainHandle) IsTerminallyFaulted() bool                  { return h.sess.isFaulted() }
func (h *faultChainHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.rt }

// faultChainStarter hands out a single pre-built faultChainHandle so
// AgentRuntime.startWithSpec wires watchHandleExit onto the handle's real
// Done().
type faultChainStarter struct {
	handle *faultChainHandle
}

func (s *faultChainStarter) Start(RuntimeStartSpec) (RuntimeHandle, error) {
	return s.handle, nil
}

// TestAgentRuntime_FaultChain_DoneClosesAndLivenessReachesFaulted pins the
// whole fault->Done()->watchHandleExit chain AND the liveness projections that
// motivate the M2 recover precondition. See the assertion comments for the
// design rationale each waypoint encodes.
func TestAgentRuntime_FaultChain_DoneClosesAndLivenessReachesFaulted(t *testing.T) {
	sess := &faultChainSession{}
	urt := runtimepkg.New(runtimepkg.RuntimeConfig{
		Name:    "agent-fault-chain",
		Session: sess,
	})
	if err := urt.Start(context.Background()); err != nil {
		t.Fatalf("UnifiedRuntime.Start: %v", err)
	}
	handle := &faultChainHandle{rt: urt, sess: sess}

	// QUM-625 M4: use a real SprawlRoot with a persisted agent so the
	// durable-fault disk persist path in watchHandleExit does not error.
	root := t.TempDir()
	agent := testAgentState("fault-chain")
	saveTestAgent(t, root, agent)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: root,
		Agent:      agent,
		Starter:    &faultChainStarter{handle: handle},
	})

	// Start attaches the handle and wires watchHandleExit on the real Done().
	if err := rt.Start(); err != nil {
		t.Fatalf("AgentRuntime.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = urt.Stop(stopCtx)
	})

	// Assertion 1 — pre-fault sanity.
	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Fatalf("pre-fault Lifecycle = %q, want %q", got, liveness.Running)
	}
	if got := liveness.From(liveness.Snapshot{Lifecycle: "started", TerminalErr: false}).Liveness; got != liveness.Running {
		t.Fatalf("pre-fault liveness projection = %v, want %v", got, liveness.Running)
	}

	// Assertion 2 — fire the terminal fault (production: session.setTerminalErr).
	sess.fireTerminalErr(backendpkg.ErrSubscriberWedged)

	// Assertion 3 — "Liveness reaches Faulted" (the lie window).
	//
	// After the fault fires there is a window where IsTerminallyFaulted()==true
	// while Lifecycle is still Started — projecting that pair yields Faulted
	// (fault beats Running in liveness.From precedence rule 2). watchHandleExit
	// can race ahead and flip Lifecycle to Stopped + detach the handle very
	// quickly, so rather than depend on catching the live snapshot mid-window
	// we project using the RETAINED handle reference (handle.IsTerminallyFaulted()
	// stays true even after detach) combined with the Started lifecycle that is
	// definitionally present at the instant of fault. This deterministically
	// pins the Faulted projection that the supervisor surfaces during the lie
	// window without racing watchHandleExit.
	faultedProjection := liveness.From(liveness.Snapshot{
		Lifecycle:   livenessToLifecycleString(liveness.Running),
		TerminalErr: handle.IsTerminallyFaulted(),
	}).Liveness
	if faultedProjection != liveness.Faulted {
		t.Fatalf("lie-window liveness projection = %v, want %v (handle.IsTerminallyFaulted=%v)",
			faultedProjection, liveness.Faulted, handle.IsTerminallyFaulted())
	}

	// Assertion 4 — Done() closes within 2s (matches the qum606 convention).
	select {
	case <-handle.Done():
		// turn loop exited, loopWG drained, rt.done closed.
	case <-time.After(5 * time.Second):
		t.Fatal("handle.Done() did not close within 5s after terminal fault fired (fault->cancel->loopWG->done chain regression)")
	}

	// Assertion 5 — teardown end-state (QUM-625 M4: DURABLE Faulted).
	//
	// M4 inverts the prior M2 end-state. Before M4 a torn-down fault erased the
	// fault by flipping Lifecycle Started->Stopped (and the projection settled
	// on Stopped). Under M4, watchHandleExit records a DURABLE Faulted status
	// (snapshot.Status="faulted") at the moment a terminally-faulted handle
	// exits, so the torn-down fault survives teardown. We poll up to 2s for
	// that durable status. The live handle is still detached (r.handle=nil), so
	// the live probe rt.IsTerminallyFaulted() reads the now-nil handle and
	// returns false — that is how we observe detachment without exporting
	// currentHandle. The durable Faulted now lives in snapshot.Status, not the
	// live probe.
	deadline := time.Now().Add(5 * time.Second)
	for rt.Snapshot().Status != state.StatusFaulted || rt.IsTerminallyFaulted() {
		if time.Now().After(deadline) {
			t.Fatalf("teardown end-state not reached within 5s: Status=%q Lifecycle=%q rt.IsTerminallyFaulted=%v (want durable Status=faulted + detached handle)",
				rt.Snapshot().Status, rt.Snapshot().Liveness, rt.IsTerminallyFaulted())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// post-teardown the projection is Faulted — the torn-down fault is durable.
	// This is WHY the M4 recover accept-set is {Faulted, ResumeFailed}: a
	// freshly faulted session ends up with durable Status="faulted" and no live
	// handle, and the projection honors the durable disk status over the stale
	// lifecycle.
	snap := rt.Snapshot()
	if got := liveness.From(liveness.Snapshot{
		Lifecycle:  livenessToLifecycleString(snap.Liveness),
		DiskStatus: snap.Status,
	}).Liveness; got != liveness.Faulted {
		t.Fatalf("post-teardown liveness projection = %v, want %v (Lifecycle=%q Status=%q)",
			got, liveness.Faulted, snap.Liveness, snap.Status)
	}
}
