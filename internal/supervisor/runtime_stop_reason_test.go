package supervisor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-1186 (D3) — teardown classification must branch on an in-memory stop
// REASON, never on a disk field.
//
// Before this change, stopWithFunc and watchHandleExit picked StatusComplete
// vs StatusFaulted by reading AgentState.LastReportState off disk. That field
// is being deleted with report_status. Once it is gone it reads "" for every
// agent, both classifiers take their else arm, and EVERY clean teardown gets
// stamped `faulted` — a defect the deletion introduces entirely on its own.
//
// The replacement rule, and the thing these tests exist to hold:
//
//	an EXPECTED exit is NEVER a fault.
//
// A clean subprocess exit that nobody labelled rests at StatusSuspended.
// Only an unexpected exit (Died) or a probed terminal fault (Faulted) may
// record a non-resting status.

// stopReasonHandle is a minimal RuntimeHandle whose Done() channel the test
// closes by hand, so watchHandleExit's classifier can be driven directly.
type stopReasonHandle struct {
	doneCh    chan struct{}
	faulted   bool
	stopCalls int64
}

func newStopReasonHandle() *stopReasonHandle {
	return &stopReasonHandle{doneCh: make(chan struct{})}
}

func (h *stopReasonHandle) Interrupt(context.Context) error { return nil }
func (h *stopReasonHandle) Wake() error                     { return nil }
func (h *stopReasonHandle) WakeForDelivery() error          { return nil }
func (h *stopReasonHandle) Stop(context.Context) error {
	atomic.AddInt64(&h.stopCalls, 1)
	return nil
}

func (h *stopReasonHandle) StopAbandon(context.Context) error {
	atomic.AddInt64(&h.stopCalls, 1)
	return nil
}
func (h *stopReasonHandle) SessionID() string { return "sess-stopreason" }
func (h *stopReasonHandle) Capabilities() backendpkg.Capabilities {
	return backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true}
}
func (h *stopReasonHandle) Done() <-chan struct{}     { return h.doneCh }
func (h *stopReasonHandle) InTurn() bool              { return false }
func (h *stopReasonHandle) IsTerminallyFaulted() bool { return h.faulted }

// newStopReasonRuntime builds an AgentRuntime with an attached handle, a real
// SprawlRoot and a persisted agent, so both the in-memory snapshot and the
// durable disk write are observable.
func newStopReasonRuntime(t *testing.T, name string) (*AgentRuntime, *stopReasonHandle, string) {
	t.Helper()
	root := t.TempDir()
	agent := testAgentState(name)
	saveTestAgent(t, root, agent)

	handle := newStopReasonHandle()
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: root,
		Agent:      agent,
	})
	rt.AttachHandle(handle)
	return rt, handle, root
}

// assertRestingStatus checks the in-memory snapshot AND the durable disk
// record. Both matter: the snapshot drives the live TUI, and disk is what a
// `sprawl enter` restart reads to decide whether to auto-resume the agent.
func assertRestingStatus(t *testing.T, rt *AgentRuntime, root, name, want string) {
	t.Helper()
	if got := rt.Snapshot().Status; got != want {
		t.Errorf("snapshot.Status = %q, want %q", got, want)
	}
	loaded, err := state.LoadAgent(root, name)
	if err != nil {
		t.Fatalf("LoadAgent(%q): %v", name, err)
	}
	if loaded.Status != want {
		t.Errorf("on-disk Status = %q, want %q", loaded.Status, want)
	}
}

// TestStopWithFunc_ExpectedExitNoReason_RestsSuspendedNotFaulted is the
// headline assertion of D3. An ordinary Stop() with no reason recorded is a
// clean, deliberate teardown; calling that a fault is both wrong and
// destructive, because `faulted` is outside the auto-resume accept-set.
func TestStopWithFunc_ExpectedExitNoReason_RestsSuspendedNotFaulted(t *testing.T) {
	rt, _, root := newStopReasonRuntime(t, "stop-noreason")

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertRestingStatus(t, rt, root, "stop-noreason", state.StatusSuspended)
}

// TestStopWithReason_IdleReclaim_RestsIdle covers the reason lane 3's reaper
// will pass. Without it the reaper cannot mark WHY it tore an agent down, and
// the TUI cannot distinguish reclaimed from shut down.
func TestStopWithReason_IdleReclaim_RestsIdle(t *testing.T) {
	rt, _, root := newStopReasonRuntime(t, "stop-reclaim")

	if err := rt.StopWithReason(context.Background(), stopReasonIdleReclaim); err != nil {
		t.Fatalf("StopWithReason: %v", err)
	}
	assertRestingStatus(t, rt, root, "stop-reclaim", state.StatusIdle)
}

func TestStopWithReason_Shutdown_RestsSuspended(t *testing.T) {
	rt, _, root := newStopReasonRuntime(t, "stop-shutdown")

	if err := rt.StopWithReason(context.Background(), stopReasonShutdown); err != nil {
		t.Fatalf("StopWithReason: %v", err)
	}
	assertRestingStatus(t, rt, root, "stop-shutdown", state.StatusSuspended)
}

// TestStopReason_ClearedOnRestart is the stale-reason guard, and nothing else
// in the suite catches it. If a reason survives across a restart, the NEXT
// teardown inherits it — an agent reclaimed once would be stamped `idle`
// forever after, including on a shutdown that should say `suspended`.
func TestStopReason_ClearedOnRestart(t *testing.T) {
	rt, _, root := newStopReasonRuntime(t, "stop-rearm")

	if err := rt.StopWithReason(context.Background(), stopReasonIdleReclaim); err != nil {
		t.Fatalf("StopWithReason(idleReclaim): %v", err)
	}
	assertRestingStatus(t, rt, root, "stop-rearm", state.StatusIdle)

	// Re-attach a fresh handle, as a wake/restart does, then stop with no
	// reason. The idle-reclaim reason must NOT still be in effect.
	rt.AttachHandle(newStopReasonHandle())
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after re-attach: %v", err)
	}
	assertRestingStatus(t, rt, root, "stop-rearm", state.StatusSuspended)
}

// TestWatchHandleExit_ExpectedExit_RestsSuspendedNotFaulted is the same rule
// on the other classifier. A paired Stop + done-close is the ordinary polite
// teardown and must not be recorded as a fault.
func TestWatchHandleExit_ExpectedExit_RestsSuspendedNotFaulted(t *testing.T) {
	rt, handle, root := newStopReasonRuntime(t, "exit-expected")

	// Mark the exit expected the way Stop does, then close done as the
	// subprocess would, WITHOUT going through stopWithFunc — this drives
	// watchHandleExit's classifier in isolation.
	rt.expectingExit.Store(true)
	close(handle.doneCh)

	waitForStatus(t, rt, state.StatusSuspended)
	assertRestingStatus(t, rt, root, "exit-expected", state.StatusSuspended)
}

// TestWatchHandleExit_UnexpectedExit_StillDies is a NEGATIVE control,
// direction: must stay quiet. An exit nobody asked for is still Died. This is
// the arm that must NOT be swept into the new "expected => suspended" rule —
// if it were, a crashed agent would look cleanly parked.
func TestWatchHandleExit_UnexpectedExit_StillDies(t *testing.T) {
	rt, handle, root := newStopReasonRuntime(t, "exit-unexpected")

	close(handle.doneCh) // no expectingExit store

	waitForStatus(t, rt, state.StatusDied)
	assertRestingStatus(t, rt, root, "exit-unexpected", state.StatusDied)
}

// TestWatchHandleExit_TerminalFault_StillFaulted is a NEGATIVE control,
// direction: must stay quiet. A probed terminal fault outranks everything,
// including an expected exit carrying a reason. Losing this would erase real
// faults, which is the precise regression QUM-625 M4 introduced the durable
// fault status to prevent.
func TestWatchHandleExit_TerminalFault_StillFaulted(t *testing.T) {
	rt, handle, root := newStopReasonRuntime(t, "exit-faulted")

	handle.faulted = true
	rt.expectingExit.Store(true)
	close(handle.doneCh)

	waitForStatus(t, rt, state.StatusFaulted)
	assertRestingStatus(t, rt, root, "exit-faulted", state.StatusFaulted)
	if got := rt.Snapshot().Liveness; got != liveness.Faulted {
		t.Errorf("snapshot.Liveness = %v, want %v", got, liveness.Faulted)
	}
}

// waitForStatus polls the snapshot until it reaches want. watchHandleExit runs
// on its own goroutine, so the classification is not synchronous with the
// done-close.
func waitForStatus(t *testing.T, rt *AgentRuntime, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rt.Snapshot().Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("status did not reach %q within 5s (last = %q)", want, rt.Snapshot().Status)
}
