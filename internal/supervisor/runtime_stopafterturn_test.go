// QUM-866: AgentRuntime.StopAfterTurn is the reusable "defer teardown to the
// genuine turn-end" primitive. Its caller wires it so a follow-on send_message
// emitted in the same turn is not silently cut off by an immediate
// drainInflight teardown. The mechanism is generic (a later issue
// reuses it for handoff), so these tests pin the state machine directly on
// AgentRuntime. QUM-1186 deleted the Real.ReportStatus wiring that was its
// only production caller; the idle reaper ((*Real).maybeReclaimIdle) is now
// its sole production caller, and the reclaim-path wiring is pinned in
// idlereap_race_test.go.

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
		done <- func() error {
			_, e := rt.StopAfterTurnIf(context.Background(), 5*time.Second, stopReasonNone, nil)
			return e
		}()
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
			go func() {
				done <- func() error {
					_, e := rt.StopAfterTurnIf(context.Background(), 5*time.Second, stopReasonNone, nil)
					return e
				}()
			}()

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
	if err := func() error {
		_, e := rt.StopAfterTurnIf(context.Background(), 5*time.Second, stopReasonNone, nil)
		return e
	}(); err != nil {
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
	if err := func() error {
		_, e := rt.StopAfterTurnIf(context.Background(), 150*time.Millisecond, stopReasonNone, nil)
		return e
	}(); err != nil {
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

	if err := func() error {
		_, e := rt.StopAfterTurnIf(context.Background(), 5*time.Second, stopReasonNone, nil)
		return e
	}(); err != nil {
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
// RESOLVED by lane 3: StopAfterTurn's production caller is now
// (*Real).maybeReclaimIdle, which passes stopReasonIdleReclaim. The
// reclaim-path wiring — StopAfterTurn rather than Stop, deferral when a turn
// begins after the idle decision, and the StatusIdle resting stamp — is pinned
// in idlereap_race_test.go. The e2e coverage that report-then-send.sh used to
// carry re-homes onto scripts/e2e-tests/idle-reclaim.sh and its matrix row.

// --- QUM-1186: the abandonable stop -----------------------------------------
//
// StopAfterTurn stops on EVERY arm, including the runaway timer. That was
// correct when its only caller was report_status(complete) — the agent had
// consented. The idle reaper stops an agent that has said nothing, so the
// timer arm became a mid-turn kill by construction, and the reaper's decision
// is taken before the wait even begins.

// TestStopAfterTurnIf_TimerArm_HonoursAGuardThatDeclines is the load-bearing
// one: the runaway timer fires while the agent is STILL in a turn, and the
// guard says the teardown is no longer wanted. Under StopAfterTurn this is an
// unconditional kill.
func TestStopAfterTurnIf_TimerArm_HonoursAGuardThatDeclines(t *testing.T) {
	tmp := t.TempDir()
	rt, handle := newStopAfterTurnRuntime(t, tmp, true /* inTurn */)

	var consulted atomic.Int64
	guard := func() (bool, func()) {
		consulted.Add(1)
		return false, nil
	}

	stopped, err := rt.StopAfterTurnIf(context.Background(), 100*time.Millisecond, stopReasonIdleReclaim, guard)
	if err != nil {
		t.Fatalf("StopAfterTurnIf: %v", err)
	}
	if stopped {
		t.Error("stopped = true although the guard declined")
	}
	if got := handle.stopCalls.Load(); got != 0 {
		t.Errorf("stopCalls = %d after the runaway timer fired on an agent whose guard declined, want 0. "+
			"The timer arm must be abandonable, or a reaper whose decision has gone stale kills an agent mid-work", got)
	}
	if consulted.Load() == 0 {
		t.Error("the guard was never consulted; the timer arm bypassed it entirely")
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false after an abandoned teardown")
	}
}

// TestStopAfterTurnIf_GuardHoldsItsLockAcrossTheStop pins the reason the guard
// returns a release func rather than a bool. A guard that released before
// returning would only RE-CHECK; whatever it is excluding could then land
// between the check and the stop. Holding across the stop is what makes
// "still idle" and "stopped" one atomic step.
func TestStopAfterTurnIf_GuardHoldsItsLockAcrossTheStop(t *testing.T) {
	tmp := t.TempDir()
	rt, handle := newStopAfterTurnRuntime(t, tmp, false /* not in turn */)

	var stopCallsAtRelease int64 = -1
	guard := func() (bool, func()) {
		return true, func() { stopCallsAtRelease = handle.stopCalls.Load() }
	}

	stopped, err := rt.StopAfterTurnIf(context.Background(), time.Second, stopReasonIdleReclaim, guard)
	if err != nil {
		t.Fatalf("StopAfterTurnIf: %v", err)
	}
	if !stopped {
		t.Fatal("stopped = false although the guard allowed the teardown")
	}
	if stopCallsAtRelease != 1 {
		t.Errorf("stopCalls at release time = %d, want 1 — release must run AFTER the stop, or the guard's lock does not span the stop",
			stopCallsAtRelease)
	}
}

// TestStopAfterTurnIf_NilGuard_IsUnconditional keeps the original contract
// available and pins that adding the guard did not change the no-guard path.
func TestStopAfterTurnIf_NilGuard_IsUnconditional(t *testing.T) {
	tmp := t.TempDir()
	rt, handle := newStopAfterTurnRuntime(t, tmp, true /* inTurn */)

	stopped, err := rt.StopAfterTurnIf(context.Background(), 100*time.Millisecond, stopReasonNone, nil)
	if err != nil {
		t.Fatalf("StopAfterTurnIf: %v", err)
	}
	if !stopped {
		t.Error("stopped = false with a nil guard; the timer arm must still stop unconditionally")
	}
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d with a nil guard, want 1", got)
	}
}
