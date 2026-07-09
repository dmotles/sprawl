// QUM-866: AgentRuntime.StopAfterTurn is the reusable "defer teardown to the
// genuine turn-end" primitive. report_status(complete/failure) wires it so a
// follow-on send_message emitted in the same turn is not silently cut off by
// an immediate drainInflight teardown. The mechanism is generic (a later issue
// reuses it for handoff), so these tests pin the state machine directly on
// AgentRuntime as well as through the Real.ReportStatus wiring.

package supervisor

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// stopAfterTurnHandle is a RuntimeHandle that exposes BOTH a UnifiedRuntime
// (so StopAfterTurn can subscribe to its EventBus) AND a settable InTurn()
// probe (so the defer-vs-immediate decision is controllable). Stop/StopAbandon
// call counts come from the embedded runtimeTestSession's atomic counters, so
// they are safe to read from a different goroutine than StopAfterTurn runs on.
type stopAfterTurnHandle struct {
	*runtimeTestSession
	urt    *runtimepkg.UnifiedRuntime
	inTurn atomic.Bool
}

func (h *stopAfterTurnHandle) InTurn() bool { return h.inTurn.Load() }

func (h *stopAfterTurnHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.urt }

func newStopAfterTurnRuntime(t *testing.T, tmp string, inTurn bool) (*AgentRuntime, *stopAfterTurnHandle) {
	t.Helper()
	saveTestAgentForRuntime(t, tmp, "alice")
	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := &stopAfterTurnHandle{
		runtimeTestSession: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
		urt: urt,
	}
	handle.inTurn.Store(inTurn)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(handle)
	return rt, handle
}

// TestStopAfterTurn_InTurnDefersUntilTurnEnd is the core invariant: when the
// runtime is in-turn at the time StopAfterTurn is called, the underlying Stop
// MUST NOT fire until a genuine turn-end event (EventTurnCompleted) is
// observed on the EventBus.
func TestStopAfterTurn_InTurnDefersUntilTurnEnd(t *testing.T) {
	tmp := t.TempDir()
	rt, handle := newStopAfterTurnRuntime(t, tmp, true /* inTurn */)

	done := make(chan error, 1)
	go func() {
		done <- rt.StopAfterTurn(context.Background(), 5*time.Second)
	}()

	// Give StopAfterTurn time to subscribe and enter its wait. Stop must be
	// deferred — the turn has not ended.
	time.Sleep(150 * time.Millisecond)
	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("Stop fired while still in-turn: stopCalls = %d, want 0 (teardown must defer to turn-end)", got)
	}
	if !rt.SubprocessAlive() {
		t.Fatalf("SubprocessAlive() = false while in-turn; teardown fired prematurely")
	}

	// Genuine turn-end: routeFrame publishes EventTurnCompleted on the bus.
	handle.urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("StopAfterTurn: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StopAfterTurn did not return after EventTurnCompleted")
	}

	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want exactly 1 (Stop must fire once, at turn-end)", got)
	}
	if rt.SubprocessAlive() {
		t.Errorf("SubprocessAlive() = true after turn-end teardown, want false")
	}
}

// TestStopAfterTurn_InTurnFiresOnTerminalEvents proves the wait unblocks on
// ANY of the terminal turn events in the select set, not just
// EventTurnCompleted — i.e. an Esc-interrupt, a failed turn, or a backend
// fault all end the deferral and trigger teardown.
func TestStopAfterTurn_InTurnFiresOnTerminalEvents(t *testing.T) {
	events := []struct {
		name   string
		evType runtimepkg.RuntimeEventType
	}{
		{"TurnCompleted", runtimepkg.EventTurnCompleted},
		{"Interrupted", runtimepkg.EventInterrupted},
		{"TurnFailed", runtimepkg.EventTurnFailed},
		{"BackendFaulted", runtimepkg.EventBackendFaulted},
	}
	for _, ev := range events {
		ev := ev
		evType := ev.evType
		t.Run(ev.name, func(t *testing.T) {
			tmp := t.TempDir()
			rt, handle := newStopAfterTurnRuntime(t, tmp, true)

			done := make(chan error, 1)
			go func() { done <- rt.StopAfterTurn(context.Background(), 5*time.Second) }()

			time.Sleep(150 * time.Millisecond)
			if got := handle.stopCalls.Load(); got != 0 {
				t.Fatalf("Stop fired before any turn-end event: stopCalls = %d, want 0", got)
			}

			handle.urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: evType})

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("StopAfterTurn: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("StopAfterTurn did not return after %s", ev.name)
			}
			if got := handle.stopCalls.Load(); got != 1 {
				t.Errorf("stopCalls = %d, want 1", got)
			}
		})
	}
}

// TestStopAfterTurn_NotInTurnStopsImmediately: when the runtime is idle at
// report time (the report truly was the last action), StopAfterTurn tears down
// promptly — it must NOT block on the timeout.
func TestStopAfterTurn_NotInTurnStopsImmediately(t *testing.T) {
	tmp := t.TempDir()
	rt, handle := newStopAfterTurnRuntime(t, tmp, false /* inTurn */)

	start := time.Now()
	if err := rt.StopAfterTurn(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("StopAfterTurn: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("StopAfterTurn(idle) elapsed = %v, want near-instant (must not wait on the timeout)", elapsed)
	}
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want 1 (idle → immediate Stop)", got)
	}
	if rt.SubprocessAlive() {
		t.Errorf("SubprocessAlive() = true after idle StopAfterTurn, want false")
	}
}

// TestStopAfterTurn_RunawayBoundedByTimeout is the runaway guard: an agent that
// keeps emitting past its own complete-report (turn never ends) must still be
// torn down by the bounded deadline so RSS is not pinned indefinitely.
func TestStopAfterTurn_RunawayBoundedByTimeout(t *testing.T) {
	tmp := t.TempDir()
	rt, handle := newStopAfterTurnRuntime(t, tmp, true /* inTurn, never ends */)

	start := time.Now()
	if err := rt.StopAfterTurn(context.Background(), 150*time.Millisecond); err != nil {
		t.Fatalf("StopAfterTurn: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed < 150*time.Millisecond {
		t.Errorf("StopAfterTurn returned in %v, before the %v runaway deadline", elapsed, 150*time.Millisecond)
	}
	if elapsed > 2*time.Second {
		t.Errorf("StopAfterTurn elapsed = %v, want bounded near the timeout (runaway guard broken)", elapsed)
	}
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want 1 (runaway must still tear down)", got)
	}
}

// TestStopAfterTurn_NoUnifiedRuntimeStopsImmediately pins the fallback that
// preserves the existing teardown unit tests: a handle that exposes no
// UnifiedRuntime (the plain fake session) short-circuits to an immediate Stop.
func TestStopAfterTurn_NoUnifiedRuntimeStopsImmediately(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(session)

	if err := rt.StopAfterTurn(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("StopAfterTurn: %v", err)
	}
	if got := session.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want 1 (no UnifiedRuntime → immediate Stop)", got)
	}
}

// TestReportStatusCompleteDefersStopUntilTurnEnd is the QUM-866 acceptance
// wiring for state=complete: while the agent is still in-turn, ReportStatus
// must NOT tear the runtime down (so a follow-on send_message survives). The
// teardown fires only once the turn-end event is observed.
func TestReportStatusCompleteDefersStopUntilTurnEnd(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, testAgentState("weave"))
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := &stopAfterTurnHandle{
		runtimeTestSession: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
		urt: urt,
	}
	handle.inTurn.Store(true)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: handle})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if _, err := r.ReportStatus(context.Background(), "alice", "complete", "done"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	// Deferred: still alive because the turn has not yielded yet.
	time.Sleep(150 * time.Millisecond)
	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("Stop fired before turn-end: stopCalls = %d, want 0 (QUM-866: teardown must defer)", got)
	}
	if !rt.SubprocessAlive() {
		t.Fatalf("SubprocessAlive() = false before turn-end; the follow-on send_message window was cut off")
	}

	// Turn ends → teardown fires.
	urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})
	waitFor(t, func() bool { return !rt.SubprocessAlive() }, 2*time.Second,
		"SubprocessAlive() to become false after turn-end")
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want 1", got)
	}

	st, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if st.Status != state.StatusComplete {
		t.Errorf("alice.Status = %q, want %q", st.Status, state.StatusComplete)
	}
	if got := rt.Snapshot().Liveness; got != liveness.Stopped {
		t.Errorf("Snapshot().Liveness = %q, want %q after teardown", got, liveness.Stopped)
	}
}

// TestReportStatusFailureDefersStopAndResyncs is the failure-path parity: the
// deferred Stop fires at turn-end AND syncRuntimeFromState still runs, so the
// snapshot projects DiskStatus=Faulted (QUM-606 Recover accepts the agent).
func TestReportStatusFailureDefersStopAndResyncs(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, testAgentState("weave"))
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := &stopAfterTurnHandle{
		runtimeTestSession: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
		urt: urt,
	}
	handle.inTurn.Store(true)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: handle})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if _, err := r.ReportStatus(context.Background(), "alice", "failure", "boom"); err != nil {
		t.Fatalf("ReportStatus: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("Stop fired before turn-end on failure path: stopCalls = %d, want 0", got)
	}

	urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})
	waitFor(t, func() bool { return !rt.SubprocessAlive() }, 2*time.Second,
		"SubprocessAlive() to become false after turn-end (failure path)")
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want 1", got)
	}

	st, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if st.Status != state.StatusFaulted {
		t.Errorf("disk alice.Status = %q, want %q", st.Status, state.StatusFaulted)
	}
	// syncRuntimeFromState (failure-path only) must have re-synced the snapshot
	// from disk so the projection sees Faulted, not the stale Stopped that
	// stopWithFunc leaves for a non-complete LastReportState.
	waitFor(t, func() bool { return rt.Snapshot().Status == state.StatusFaulted }, 2*time.Second,
		"Snapshot().Status to reflect DiskStatus=Faulted after syncRuntimeFromState")
}
