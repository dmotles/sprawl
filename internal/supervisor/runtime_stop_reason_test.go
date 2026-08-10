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
// assertRestingStatus checks the snapshot immediately and the DISK with a
// bounded poll. QUM-1198.
//
// The single immediate disk read this replaces failed under `make validate`'s
// full-tree parallel -race — twice, in two sibling tests, both reading
// "active": the durable write had not landed when the test looked. It is a
// real window, not a missing write: watchHandleExit and stopWithFunc flip the
// in-memory snapshot under r.mu and then persist OUTSIDE the lock, so the
// snapshot leads the disk by design.
//
// Polling is justified by MEASUREMENT, not by making the red go away — that
// distinction matters, because "poll longer" is green under both the
// test-is-early hypothesis and the production-does-not-persist one. Measured
// on this host, under 8 concurrent -race packages: 40/40 iterations converged,
// worst case 291µs; a 60-iteration replica of the exact failing shape produced
// 0 stale reads. So production persists, promptly and reliably, and the
// assertion's subject is the VALUE written rather than the latency.
//
// The deadline stays SHORT for that reason. It is three orders of magnitude
// above the measured worst case, so a genuine "never persists" regression
// still fails this assertion rather than waiting it out. Positive control,
// recorded: deleting the state.SaveAgent call in watchHandleExit's durable
// block makes these tests fail with "on-disk Status never reached ... within
// 2s" — the poll did not disarm the gate.
//
// NOT fixed here, and filed as QUM-1199: that persist is best-effort. A
// LoadAgent/SaveAgent error is a WARN with no retry, so a transient I/O
// failure leaves the resting status permanently stale on disk and
// RecoverAgents misclassifies the agent at the next boot. This assertion
// cannot catch that — it polls until the write lands, and a write that never
// lands because it errored looks the same as one that never started.
func assertRestingStatus(t *testing.T, rt *AgentRuntime, root, name, want string) {
	t.Helper()
	if got := rt.Snapshot().Status; got != want {
		t.Errorf("snapshot.Status = %q, want %q", got, want)
	}
	waitDiskStatus(t, root, name, want)
}

// waitDiskStatus polls the agent's on-disk Status until it equals want, with a
// short bounded deadline. Shared by every assertion that checks the durable
// resting status after waiting on the in-memory snapshot. QUM-1199.
func waitDiskStatus(t *testing.T, root, name, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for {
		loaded, err := state.LoadAgent(root, name)
		if err != nil {
			t.Fatalf("LoadAgent(%q): %v", name, err)
		}
		last = loaded.Status
		if last == want {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("on-disk Status never reached %q within 2s (last = %q); the snapshot flipped but the durable write did not land", want, last)
			return
		}
		time.Sleep(2 * time.Millisecond)
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

// TestStopReason_NotInheritedByALaterReasonlessStop is the stale-reason guard:
// if a recorded reason could survive into the NEXT teardown, an agent
// reclaimed once would be stamped `idle` forever after, including on a
// shutdown that should say `suspended`.
//
// WHAT IT ACTUALLY PINS, stated precisely because an earlier version of this
// test did not discriminate. The load-bearing mechanism is that Stop() is a
// wrapper for StopWithReason(ctx, stopReasonNone) and therefore STORES the
// reason on every call — so a prior reason cannot leak into it. Mutation:
// remove the Store from StopWithReason and this test fires.
//
// The re-arms at startWithSpec / AttachHandle / Wake are belt-and-braces on
// top of that and are NOT what this test measures: removing the AttachHandle
// re-arm alone leaves it green, because the subsequent Stop() re-stores anyway.
// That is recorded here rather than hidden, so nobody reads this test as
// proving those re-arms are load-bearing. They are defensive, and correctly so
// — every teardown path currently sets a reason before it is read.
func TestStopReason_NotInheritedByALaterReasonlessStop(t *testing.T) {
	rt, _, root := newStopReasonRuntime(t, "stop-rearm")

	if err := rt.StopWithReason(context.Background(), stopReasonIdleReclaim); err != nil {
		t.Fatalf("StopWithReason(idleReclaim): %v", err)
	}
	assertRestingStatus(t, rt, root, "stop-rearm", state.StatusIdle)

	// Re-attach a fresh handle and clear the resting marker, which is what a
	// real wake does (Wake stamps StatusActive before the agent runs again).
	// Both halves are needed: AttachHandle re-arms the IN-MEMORY reason, and
	// clearing disk is required because the durable-persist preserve switch
	// deliberately protects an already-recorded StatusIdle — see
	// TestStopPreservesIdleReclaimMarker. Without the disk clear this test
	// would be asserting against that preserve rule rather than against the
	// re-arm it exists to pin.
	rt.AttachHandle(newStopReasonHandle())
	woken, err := state.LoadAgent(root, "stop-rearm")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	woken.Status = state.StatusActive
	if err := state.SaveAgent(root, woken); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// Stop with NO reason. The idle-reclaim reason must NOT still be in effect.
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

// TestStopPreservesIdleReclaimMarker pins tower's Q2 ruling: a teardown must
// not flatten an already-recorded StatusIdle back to StatusSuspended.
//
// The flatten would destroy the only durable record of WHY the process is
// gone, at exactly the moment the operator is most likely to look — a restart
// — and it makes the state non-idempotent: reclaim -> shutdown -> boot leaves
// the agent indistinguishable from one that was never reclaimed.
//
// D3's "shutdown -> StatusSuspended" governs an agent being stopped BY
// shutdown. It is not a licence to overwrite a reason already on disk.
func TestStopPreservesIdleReclaimMarker(t *testing.T) {
	rt, _, root := newStopReasonRuntime(t, "already-reaped")

	// Disk already records the reclaim, as lane 3's reaper will leave it.
	cur, err := state.LoadAgent(root, "already-reaped")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	cur.Status = state.StatusIdle
	if err := state.SaveAgent(root, cur); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// A shutdown-reason stop arrives afterwards.
	if err := rt.StopWithReason(context.Background(), stopReasonShutdown); err != nil {
		t.Fatalf("StopWithReason: %v", err)
	}

	loaded, err := state.LoadAgent(root, "already-reaped")
	if err != nil {
		t.Fatalf("LoadAgent after stop: %v", err)
	}
	if loaded.Status != state.StatusIdle {
		t.Errorf("on-disk Status = %q, want %q — a later stop must not erase the reclaim marker", loaded.Status, state.StatusIdle)
	}
}
