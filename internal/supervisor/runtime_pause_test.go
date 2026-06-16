package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-722: three-way watchHandleExit classifier. Three outcomes when handle's
// Done() fires: Died (unexpected), Stopped (expecting exit after Stop), or
// Faulted (terminalErr trumps). A pre-existing disk Status=paused must NOT be
// clobbered by the watcher (the disk-status guard at runtime.go:1027-1035
// must be extended to include `paused`).

// TestWatchHandleExit_DiedWhenUnexpectedExit drives the handle's Done close
// without any prior Stop/StopAbandon call and without terminalErr. The
// expected post-state: Liveness=Died and disk Status=died.
func TestWatchHandleExit_DiedWhenUnexpectedExit(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		doneCh:    make(chan struct{}),
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	close(session.doneCh)

	deadline := time.After(2 * time.Second)
	for {
		snap := rt.Snapshot()
		if snap.Liveness == liveness.Died {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Liveness = %v, want Died after unexpected handle exit", snap.Liveness)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cur, err := state.LoadAgent(tmp, "alice")
	if err != nil {
		t.Fatalf("LoadAgent after Died: %v", err)
	}
	if cur.Status != state.StatusDied {
		t.Errorf("disk Status = %q, want %q (durable died marker)", cur.Status, state.StatusDied)
	}
}

// TestWatchHandleExit_StoppedWhenExpectingExit verifies that calling Stop
// (which must set expectingExit=true before invoking the stop fn) and then
// closing the handle's Done channel yields Liveness=Stopped — NOT Died.
func TestWatchHandleExit_StoppedWhenExpectingExit(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		doneCh:    make(chan struct{}),
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	// Closing done after Stop must classify as Stopped (expectingExit=true).
	// In production handle.Stop closes done; here we just simulate.
	select {
	case <-session.doneCh:
		// already closed by some path
	default:
		close(session.doneCh)
	}

	deadline := time.After(2 * time.Second)
	for {
		snap := rt.Snapshot()
		if snap.Liveness == liveness.Stopped {
			break
		}
		if snap.Liveness == liveness.Died {
			t.Fatalf("Liveness = Died, want Stopped (Stop was called → expectingExit=true)")
		}
		select {
		case <-deadline:
			t.Fatalf("Liveness = %v, want Stopped", snap.Liveness)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestWatchHandleExit_FaultedBeatsDied verifies that when the handle reports
// terminallyFaulted=true and Done closes (no Stop call), the classifier
// surfaces Faulted — not Died.
func TestWatchHandleExit_FaultedBeatsDied(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	session := &runtimeTestSession{
		sessionID:         "sess-alice",
		caps:              backendpkg.Capabilities{SupportsInterrupt: true},
		doneCh:            make(chan struct{}),
		terminallyFaulted: true,
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	close(session.doneCh)

	deadline := time.After(2 * time.Second)
	for {
		snap := rt.Snapshot()
		if snap.Liveness == liveness.Faulted {
			break
		}
		if snap.Liveness == liveness.Died {
			t.Fatalf("Liveness = Died, want Faulted (terminalErr must beat Died)")
		}
		select {
		case <-deadline:
			t.Fatalf("Liveness = %v, want Faulted", snap.Liveness)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cur, err := state.LoadAgent(tmp, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cur.Status != state.StatusFaulted {
		t.Errorf("disk Status = %q, want %q (Faulted beats Died)", cur.Status, state.StatusFaulted)
	}
}

// TestWatchHandleExit_PausedDiskStatusNotClobbered verifies that when disk
// Status is already "paused" (set by a successful Pause-clean flow), a
// subsequent handle Done close does NOT overwrite it with "stopped" or
// "died". The guard at runtime.go:1027-1035 must include StatusPaused.
func TestWatchHandleExit_PausedDiskStatusNotClobbered(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	// Pre-set disk Status=paused (simulating successful Pause clean path).
	cur, err := state.LoadAgent(tmp, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	cur.Status = state.StatusPaused
	if err := state.SaveAgent(tmp, cur); err != nil {
		t.Fatalf("SaveAgent (paused): %v", err)
	}

	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		doneCh:    make(chan struct{}),
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Re-stamp paused on disk (Start may have rewritten Status).
	cur, _ = state.LoadAgent(tmp, "alice")
	cur.Status = state.StatusPaused
	_ = state.SaveAgent(tmp, cur)

	close(session.doneCh)

	// Poll for the watcher goroutine to react (mirrors Died/Stopped sibling
	// tests). If the guard works, Status stays paused forever; if it's
	// broken, Status flips to stopped/died within a few ms.
	deadline := time.After(2 * time.Second)
	for {
		snap := rt.Snapshot()
		// Wait until liveness reaches a terminal value so we know the
		// watcher fully observed the Done close.
		if snap.Liveness == liveness.Stopped || snap.Liveness == liveness.Died ||
			snap.Liveness == liveness.Faulted || snap.Liveness == liveness.Paused {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("watcher did not finalize Liveness after Done close; got %v", snap.Liveness)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	got, err := state.LoadAgent(tmp, "alice")
	if err != nil {
		t.Fatalf("LoadAgent after exit: %v", err)
	}
	if got.Status != state.StatusPaused {
		t.Errorf("disk Status = %q, want %q (Paused must be preserved by watchHandleExit guard, QUM-722)",
			got.Status, state.StatusPaused)
	}
}

// saveTestAgentForRuntime writes a minimal AgentState for the named agent
// under tmp so the disk-status guard / Pause helpers can load it.
func saveTestAgentForRuntime(t *testing.T, tmp, name string) {
	t.Helper()
	a := &state.AgentState{Name: name, Type: "engineer", Status: state.StatusActive}
	if err := state.SaveAgent(tmp, a); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
}

// --- Pause machinery ---
//
// QUM-722: AgentRuntime.Pause(ctx, timeout) (clean bool, err error)
//   1. Subscribe to EventBus("pause-wait", 8) if InTurn.
//   2. Select on EventTurnCompleted | EventInterrupted | EventTurnFailed |
//      EventBackendFaulted | timeout.
//   3. Clean: r.Stop, write disk paused, emit RuntimeEventPaused. Not clean:
//      r.StopAbandon, write disk killed, emit RuntimeEventStopped.

// TestPause_CleanOnTurnCompleted drives the happy path: the runtime reports
// InTurn=true, an EventTurnCompleted publishes on the bus before the timeout,
// Pause returns (clean=true, err=nil), disk Status=paused, and the runtime
// invokes Stop (not StopAbandon) on the handle.
func TestPause_CleanOnTurnCompleted(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")

	// Use a real UnifiedRuntime so EventBus exists and InTurn() can be probed.
	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := newPauseFakeHandle(urt, true /* inTurn */)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(handle)

	events, cancel := rt.Subscribe(8)
	defer cancel()

	done := make(chan struct {
		clean bool
		err   error
	}, 1)
	go func() {
		clean, err := rt.Pause(context.Background(), 2*time.Second)
		done <- struct {
			clean bool
			err   error
		}{clean, err}
	}()

	// Let the goroutine subscribe before publishing.
	time.Sleep(50 * time.Millisecond)
	urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})

	select {
	case res := <-done:
		if !res.clean {
			t.Errorf("Pause clean = false, want true")
		}
		if res.err != nil {
			t.Errorf("Pause err = %v, want nil", res.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Pause never returned")
	}

	cur, _ := state.LoadAgent(tmp, "alice")
	if cur.Status != state.StatusPaused {
		t.Errorf("disk Status = %q, want %q", cur.Status, state.StatusPaused)
	}
	if handle.stopCalls != 1 {
		t.Errorf("handle.Stop calls = %d, want 1 (clean pause uses polite Stop)", handle.stopCalls)
	}
	if handle.stopAbandonCalls != 0 {
		t.Errorf("handle.StopAbandon calls = %d, want 0", handle.stopAbandonCalls)
	}

	// At least one RuntimeEventPaused must be emitted.
	sawPaused := false
	drain := time.After(500 * time.Millisecond)
DRAIN:
	for {
		select {
		case ev := <-events:
			if ev.Kind == RuntimeEventPaused {
				sawPaused = true
				break DRAIN
			}
		case <-drain:
			break DRAIN
		}
	}
	if !sawPaused {
		t.Errorf("expected RuntimeEventPaused emission on clean pause")
	}
}

// TestPause_EscalatesOnTimeout: agent InTurn, no event fires before the
// timeout; Pause returns (clean=false, err=nil), disk Status=killed, and
// StopAbandon was called.
func TestPause_EscalatesOnTimeout(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")

	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := newPauseFakeHandle(urt, true)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(handle)

	clean, err := rt.Pause(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Errorf("Pause err = %v, want nil even on escalation", err)
	}
	if clean {
		t.Errorf("Pause clean = true, want false (timeout must escalate)")
	}

	cur, _ := state.LoadAgent(tmp, "alice")
	if cur.Status != state.StatusKilled {
		t.Errorf("disk Status = %q, want %q (timeout → killed)", cur.Status, state.StatusKilled)
	}
	if handle.stopAbandonCalls != 1 {
		t.Errorf("handle.StopAbandon calls = %d, want 1 (escalation uses hard stop)", handle.stopAbandonCalls)
	}
}

// TestPause_OnBackendFaultedDuringWait: an EventBackendFaulted observed
// during the wait is a terminating natural event — treated as clean enough
// that no timeout escalation occurs. The handle is told to Stop (not
// StopAbandon).
func TestPause_OnBackendFaultedDuringWait(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")

	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := newPauseFakeHandle(urt, true)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(handle)

	done := make(chan error, 1)
	go func() {
		_, err := rt.Pause(context.Background(), 2*time.Second)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventBackendFaulted})

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Pause err = %v, want nil on BackendFaulted", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Pause did not return after EventBackendFaulted")
	}

	if handle.stopAbandonCalls != 0 {
		t.Errorf("StopAbandon must NOT be called after natural fault termination; got %d", handle.stopAbandonCalls)
	}
}

// TestPause_IdleSkipsWait: when InTurn()==false, Pause skips the subscribe/
// select and goes straight to Stop. Disk Status=paused, clean=true.
func TestPause_IdleSkipsWait(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")

	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := newPauseFakeHandle(urt, false /* inTurn */)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(handle)

	start := time.Now()
	clean, err := rt.Pause(context.Background(), 5*time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !clean {
		t.Errorf("Pause clean = false, want true (idle short-circuits)")
	}
	// Should be near-instant — never block on the timeout.
	if elapsed > 1*time.Second {
		t.Errorf("Pause(idle) elapsed = %v, want near-instant", elapsed)
	}
	if handle.stopCalls != 1 {
		t.Errorf("handle.Stop calls = %d, want 1", handle.stopCalls)
	}
	cur, _ := state.LoadAgent(tmp, "alice")
	if cur.Status != state.StatusPaused {
		t.Errorf("disk Status = %q, want %q", cur.Status, state.StatusPaused)
	}
	// No-subscription proof: when idle, Pause must skip the EventBus
	// subscribe path entirely. Since the only access to the bus is via
	// handle.UnifiedRuntime().EventBus(), a zero call-count on
	// UnifiedRuntime() is a tight upper bound on "did we subscribe?".
	if got := atomic.LoadInt64(&handle.unifiedRuntimeCalls); got != 0 {
		t.Errorf("handle.UnifiedRuntime() calls during idle Pause = %d, want 0 (no subscribe needed)", got)
	}
}

// TestPause_DoesNotCallSessionInterrupt: during the wait phase, Pause must
// not call Session.Interrupt — the in-flight turn drains naturally.
func TestPause_DoesNotCallSessionInterrupt(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")

	urt := runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"})
	handle := newPauseFakeHandle(urt, true)
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
	})
	rt.AttachHandle(handle)

	done := make(chan struct{})
	go func() {
		_, _ = rt.Pause(context.Background(), 2*time.Second)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})
	<-done

	if handle.interruptCalls != 0 {
		t.Errorf("handle.Interrupt calls = %d, want 0 (Pause must not interrupt during wait, QUM-722)",
			handle.interruptCalls)
	}
}

// --- pause fake handle ---

// pauseFakeHandle is a RuntimeHandle that exposes a UnifiedRuntime so
// AgentRuntime.Pause's EventBus().SubscribeNamed call can be wired through
// the unifiedRuntimeProvider interface. Records Stop / StopAbandon / Interrupt
// call counts for assertions.
//
// unifiedRuntimeCalls counts UnifiedRuntime() invocations. The idle-skips-wait
// test asserts this is 0: Pause's subscribe path must NOT touch the bus when
// the runtime is already idle.
type pauseFakeHandle struct {
	urt                 *runtimepkg.UnifiedRuntime
	inTurn              bool
	stopCalls           int
	stopAbandonCalls    int
	interruptCalls      int
	unifiedRuntimeCalls int64
	doneCh              chan struct{}
}

func newPauseFakeHandle(urt *runtimepkg.UnifiedRuntime, inTurn bool) *pauseFakeHandle {
	return &pauseFakeHandle{urt: urt, inTurn: inTurn, doneCh: make(chan struct{})}
}

func (h *pauseFakeHandle) Interrupt(context.Context) error {
	h.interruptCalls++
	return nil
}
func (h *pauseFakeHandle) Wake() error                       { return nil }
func (h *pauseFakeHandle) WakeForDelivery() error            { return nil }
func (h *pauseFakeHandle) Stop(context.Context) error        { h.stopCalls++; return nil }
func (h *pauseFakeHandle) StopAbandon(context.Context) error { h.stopAbandonCalls++; return nil }
func (h *pauseFakeHandle) SessionID() string                 { return "sess-fake" }
func (h *pauseFakeHandle) Capabilities() backendpkg.Capabilities {
	return backendpkg.Capabilities{SupportsInterrupt: true}
}
func (h *pauseFakeHandle) Done() <-chan struct{} { return h.doneCh }
func (h *pauseFakeHandle) InTurn() bool          { return h.inTurn }
func (h *pauseFakeHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime {
	atomic.AddInt64(&h.unifiedRuntimeCalls, 1)
	return h.urt
}
func (h *pauseFakeHandle) IsTerminallyFaulted() bool { return false }

// TestWatchHandleExit_DiedBeatsStoppedWithoutExpectingExit is the symmetric
// counterpart to TestWatchHandleExit_StoppedWhenExpectingExit. It asserts
// the classifier steers correctly in the OTHER direction: when the handle's
// Done closes WITHOUT a prior Stop call, the atomic-load of expectingExit
// reads false and the watcher must classify as Died (not Stopped).
//
// Together with the StoppedWhenExpectingExit arm, this proves the atomic
// load is the steering signal — not e.g. an order-of-channel-events
// heuristic.
func TestWatchHandleExit_DiedBeatsStoppedWithoutExpectingExit(t *testing.T) {
	tmp := t.TempDir()
	saveTestAgentForRuntime(t, tmp, "alice")
	session := &runtimeTestSession{
		sessionID: "sess-alice",
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		doneCh:    make(chan struct{}),
	}
	starter := &runtimeTestStarter{session: session}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: tmp,
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// No Stop() — close Done directly. expectingExit must read false.
	close(session.doneCh)

	deadline := time.After(2 * time.Second)
	for {
		snap := rt.Snapshot()
		if snap.Liveness == liveness.Died {
			break
		}
		if snap.Liveness == liveness.Stopped {
			t.Fatalf("Liveness = Stopped, want Died (expectingExit was never set → must classify Died)")
		}
		select {
		case <-deadline:
			t.Fatalf("Liveness = %v, want Died", snap.Liveness)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestWatchHandleExit_ExpectingExitAtomic_RaceFree exercises the atomic
// ordering between Stop (which sets expectingExit=true) and the watcher
// goroutine reading expectingExit on Done close. We spawn many iterations,
// each launching two concurrent goroutines:
//   - goroutine A: rt.Stop(ctx) — sets expectingExit true, then closes Done
//     via the handle's normal path.
//   - goroutine B: close(session.doneCh) — races to fire Done first.
//
// The point of the test is NOT to assert a specific outcome (both Stopped
// and Died are valid since the race winner determines classification). The
// point is:
//  1. `go test -race` must report no data race. The expectingExit field
//     must be guarded by atomic load/store (not a plain bool).
//  2. No panics from double-close of doneCh — the test serializes the
//     close via sync.Once.
//
// If the implementation reads expectingExit with a plain field load while
// another goroutine writes it, the race detector flags this immediately.
func TestWatchHandleExit_ExpectingExitAtomic_RaceFree(t *testing.T) {
	if testing.Short() {
		t.Skip("race iteration test; skipped in -short mode")
	}
	const iterations = 50

	for i := 0; i < iterations; i++ {
		tmp := t.TempDir()
		name := "alice"
		saveTestAgentForRuntime(t, tmp, name)
		session := &runtimeTestSession{
			sessionID: "sess-" + name,
			caps:      backendpkg.Capabilities{SupportsInterrupt: true},
			doneCh:    make(chan struct{}),
		}
		starter := &runtimeTestStarter{session: session}
		rt := NewAgentRuntime(AgentRuntimeConfig{
			SprawlRoot: tmp,
			Agent:      testAgentState(name),
			Starter:    starter,
		})
		if err := rt.Start(); err != nil {
			t.Fatalf("iter %d Start: %v", i, err)
		}

		var closeOnce sync.Once
		safeClose := func() { closeOnce.Do(func() { close(session.doneCh) }) }

		var wg sync.WaitGroup
		wg.Add(2)
		// Goroutine A: politely Stop. The implementation should set
		// expectingExit atomically before any handle-stop work.
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = rt.Stop(ctx)
			safeClose()
		}()
		// Goroutine B: race the Done close to fire first.
		go func() {
			defer wg.Done()
			safeClose()
		}()
		wg.Wait()

		// Drain to a terminal liveness before next iteration — guards
		// against goroutine leak across iterations.
		deadline := time.After(2 * time.Second)
	drain:
		for {
			snap := rt.Snapshot()
			switch snap.Liveness {
			case liveness.Stopped, liveness.Died, liveness.Faulted:
				break drain
			}
			select {
			case <-deadline:
				t.Fatalf("iter %d: never reached terminal liveness; got %v", i, snap.Liveness)
			default:
				time.Sleep(2 * time.Millisecond)
			}
		}
	}
}
