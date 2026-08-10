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
		done <- rt.StopAfterTurn(context.Background(), 5*time.Second, stopReasonNone)
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
			go func() { done <- rt.StopAfterTurn(context.Background(), 5*time.Second, stopReasonNone) }()

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
	if err := rt.StopAfterTurn(context.Background(), 5*time.Second, stopReasonNone); err != nil {
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
	if err := rt.StopAfterTurn(context.Background(), 150*time.Millisecond, stopReasonNone); err != nil {
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

	if err := rt.StopAfterTurn(context.Background(), 5*time.Second, stopReasonNone); err != nil {
		t.Fatalf("StopAfterTurn: %v", err)
	}
	if got := session.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d, want 1 (no UnifiedRuntime → immediate Stop)", got)
	}
}

// QUM-1186: TestReportStatusCompleteDefersStopUntilTurnEnd and
// TestReportStatusFailureDefersStopAndResyncs were removed here. They were the
// QUM-866 acceptance wiring — report_status(complete|failure) must defer
// teardown until the turn actually yields, so a follow-on send_message is not
// cut off. The TOOL is deleted, so the wiring has no trigger.
//
// The PRIMITIVE they exercised survives untouched and is fully covered by the
// TestStopAfterTurn_* tests above: in-turn deferral, firing on each terminal
// event, immediate stop when idle, the runaway timeout bound, and the
// no-UnifiedRuntime fast path.
//
// NOTE for lane 3: StopAfterTurn now has NO production caller until the idle
// reaper wires one up. It is deliberately kept (with a new stopReason
// parameter) rather than deleted. scripts/e2e-tests/report-then-send.sh and
// the matrix row that pinned its only production call are flagged to the
// manager for re-homing onto idle-reclaim, not silently left green.
