// QUM-1186 lane 3: the reclaim action, and the reap-vs-send race it opens.
//
// Real.SendMessage decides liveness, THEN enqueues, THEN pokes only if the
// runtime is live. A reap landing inside that window leaves the entry durably
// queued with the poke dropped, and nothing picks it up — child handles have no
// redrain ticker. Ordering alone cannot close it, so the design is a per-agent
// reclaim gate (linearising the decision against the reap) PLUS a post-stop
// backstop that re-checks the queue and re-wakes. These tests pin both halves
// independently, because either one alone still loses messages.
package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/protocol"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// reclaimTestHandle is a RuntimeHandle that is observable on every predicate
// term (turnProbe + UnifiedRuntime + lastActivityProbe) and lets a test run a
// hook inside Stop — the only way to inject an event into the window between
// the reaper's final check and the teardown completing.
type reclaimTestHandle struct {
	*runtimeTestSession
	urt *runtimepkg.UnifiedRuntime

	inTurn   atomic.Bool
	lastAct  atomic.Int64
	onStop   func()
	stopOnce sync.Once
	// stopErr, when set, is what Stop returns — so a test can drive the
	// reaper's failed-teardown arm without faulting the whole session.
	stopErr error
	starter *runtimeTestStarter

	// inTurnReads counts InTurn() probes. flipInTurnAfter, when > 0, makes the
	// handle report in-turn from that read onward — the only way to place a
	// turn-start strictly BETWEEN the reaper's decision and StopAfterTurn's own
	// re-check, which is the window the subscribe-before-check ordering exists
	// to cover.
	inTurnReads     atomic.Int64
	flipInTurnAfter atomic.Int64
}

func (h *reclaimTestHandle) InTurn() bool {
	n := h.inTurnReads.Add(1)
	if after := h.flipInTurnAfter.Load(); after > 0 && n > after {
		return true
	}
	return h.inTurn.Load()
}

func (h *reclaimTestHandle) LastActivityAt() time.Time {
	return time.Unix(0, h.lastAct.Load())
}

func (h *reclaimTestHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.urt }

func (h *reclaimTestHandle) Stop(ctx context.Context) error {
	if h.onStop != nil {
		h.stopOnce.Do(h.onStop)
	}
	if err := h.runtimeTestSession.Stop(ctx); err != nil {
		return err
	}
	return h.stopErr
}

// newReclaimFixture builds a Real with "alice" registered, started, and idle on
// every term of the predicate — the state in which a reap SHOULD happen. Every
// test below either asserts the reap or spoils exactly one thing.
func newReclaimFixture(t *testing.T) (*Real, string, *AgentRuntime, *reclaimTestHandle) {
	t.Helper()
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("alice")
	saveTestAgent(t, tmpDir, agentState)

	handle := &reclaimTestHandle{
		runtimeTestSession: &runtimeTestSession{
			sessionID: "sess-alice",
			caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
		},
		urt: runtimepkg.New(runtimepkg.RuntimeConfig{Name: "alice"}),
	}
	// The production DEFAULT is now 0 (disabled, QUM-1197), so these tests must
	// enable the reaper explicitly — which is honest: they are testing the
	// enabled behaviour. Both the threshold and the fixture's idle age derive
	// from SuggestedIdleReclaimAfter so they cannot drift apart.
	handle.lastAct.Store(time.Now().Add(-2 * config.SuggestedIdleReclaimAfter).UnixNano())

	// NewReal starts a LIVE reaper on this same registry. Left running it would
	// (a) sweep and tear down these fixtures' handles on its own schedule —
	// a destructive action against a t.TempDir() that is about to be removed —
	// and (b) add uncontrolled InTurn() reads, which the flipInTurnAfter counter
	// below is sensitive to. Quiesce it: these tests drive maybeReclaimIdle
	// directly. TestNewReal_StartsAndStopsIdleReaper is what covers the live one.
	if r.idleReaper != nil {
		r.idleReaper.Stop()
	}
	r.idleReclaimAfter.set(config.SuggestedIdleReclaimAfter)
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	handle.starter = &runtimeTestStarter{session: handle}
	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, handle.starter)
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}
	return r, tmpDir, rt, handle
}

// serveHealthFrames publishes assistant frames on the handle's bus until the
// test ends, so the post-Start health probe in Wake (probeNewHandleHealth)
// succeeds. Without it a re-wake takes the full 5s probe timeout and then
// fails, and a backstop assertion would be measuring the fake's silence rather
// than the reaper's behaviour.
func serveHealthFrames(t *testing.T, handle *reclaimTestHandle) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				handle.urt.EventBus().Publish(runtimepkg.RuntimeEvent{
					Type:    runtimepkg.EventProtocolMessage,
					Message: &protocol.Message{Type: "assistant"},
				})
			}
		}
	}()
}

// TestReclaim_IdleAgent_IsTornDownAndRestsIdle is the positive control for
// every other test in this file: without it, "did not reap" assertions could
// all be passing because the fixture never reaps at all.
func TestReclaim_IdleAgent_IsTornDownAndRestsIdle(t *testing.T) {
	r, tmpDir, rt, handle := newReclaimFixture(t)

	r.maybeReclaimIdle(context.Background(), rt)

	if got := handle.stopCalls.Load(); got != 1 {
		t.Fatalf("stopCalls = %d after reclaiming an idle agent, want 1", got)
	}
	got, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != state.StatusIdle {
		t.Errorf("resting Status = %q after an idle reclaim, want %q; the reason must be wired through StopAfterTurn or the agent looks suspended (or worse, faulted)",
			got.Status, state.StatusIdle)
	}
}

// TestReclaim_BusyAgent_IsNotTornDown is the negative control: an agent that
// fails the predicate must be left alone, and the probe must stay quiet.
func TestReclaim_BusyAgent_IsNotTornDown(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)
	handle.inTurn.Store(true)

	r.maybeReclaimIdle(context.Background(), rt)

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for an in-turn agent, want 0", got)
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false for an in-turn agent; it was torn down anyway")
	}
}

// TestReclaim_CancelledSweepCtx_DoesNotStartATeardown: Shutdown stops the
// reaper before tearing down runtimes, and Stop blocks on the loop goroutine.
// Without this check a sweep that had just decided to reap would make Shutdown
// wait out the whole stop budget for a teardown Shutdown was about to perform
// itself. The agent here is fully reapable — only the cancelled ctx stops it,
// which is what makes this distinguishable from the busy-agent case.
func TestReclaim_CancelledSweepCtx_DoesNotStartATeardown(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.maybeReclaimIdle(ctx, rt)

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for a cancelled sweep, want 0", got)
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false after a cancelled sweep; the teardown started anyway")
	}
}

// TestReclaim_TurnStartsAfterAssessment_DefersTeardownToTurnEnd is what
// distinguishes StopAfterTurn from a plain Stop, and nothing else in the tree
// pins it on the reclaim path. The handle reports "not in turn" for the
// predicate's read, then flips to in-turn — mimicking a turn that begins in the
// window between the decision and the teardown. StopAfterTurn subscribes before
// re-checking, so it must WAIT for a genuine turn-end. A plain
// Stop/StopWithReason would cut the turn off; that is the mutation this test
// catches.
func TestReclaim_TurnStartsAfterAssessment_DefersTeardownToTurnEnd(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)

	// The predicate reads InTurn once and sees false; every read after that —
	// i.e. StopAfterTurn's own re-check — sees a turn in progress.
	handle.flipInTurnAfter.Store(1)

	done := make(chan struct{})
	go func() { defer close(done); r.maybeReclaimIdle(context.Background(), rt) }()

	// select, not a bare sleep: a bare sleep turns "the reap was slow" into a
	// silent pass on a loaded host, which is the failure mode a negative
	// assertion is most prone to.
	select {
	case <-done:
		t.Fatalf("reclaim COMPLETED while a turn is running (stopCalls=%d); teardown must defer to turn-end (StopAfterTurn), not fire immediately", handle.stopCalls.Load())
	case <-time.After(200 * time.Millisecond):
	}
	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d while a turn is running, want 0", got)
	}

	// The turn genuinely ends: subsequent probes report idle again, so the
	// guard's re-read agrees the teardown is still wanted and it proceeds.
	// Without this reset the handle would keep reporting in-turn forever and
	// the guard would (correctly) abandon — which would make this test measure
	// abandonment rather than deferral.
	handle.flipInTurnAfter.Store(1 << 30)
	handle.urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reclaim did not complete after EventTurnCompleted")
	}
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d after turn-end, want 1", got)
	}
}

// TestReclaim_MessageEnqueuedDuringStop_IsBackstoppedByRewake is the race the
// spec calls out. The send lands while StopAfterTurn is running — after the
// gate was released, so the gate alone cannot see it. Only the post-stop
// ListPending re-check catches it.
//
// Mutation that proves this fires: delete the backstop's re-wake and this test
// goes red while every other test here stays green.
func TestReclaim_MessageEnqueuedDuringStop_IsBackstoppedByRewake(t *testing.T) {
	r, tmpDir, rt, handle := newReclaimFixture(t)
	serveHealthFrames(t, handle)

	handle.onStop = func() {
		if _, err := agentloop.Enqueue(tmpDir, "alice", agentloop.Entry{
			ShortID: "m-race", Class: agentloop.ClassAsync, From: "weave", Body: "landed mid-teardown",
		}); err != nil {
			t.Errorf("Enqueue during stop: %v", err)
		}
	}

	r.maybeReclaimIdle(context.Background(), rt)

	got, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status == state.StatusIdle {
		t.Fatalf("agent rests at %q with a message still pending in the queue; the backstop must re-wake it or the message is stranded forever (child handles have no redrain ticker)", got.Status)
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false after the backstop re-wake; the agent must be back up to drain its queue")
	}
}

// TestReclaim_ListPendingErrorAfterStop_AlsoRewakes: D1a applies to the
// backstop too. If the queue cannot be read after teardown, "unknown" must be
// treated as "there might be mail", not as "empty".
func TestReclaim_ListPendingErrorAfterStop_AlsoRewakes(t *testing.T) {
	r, tmpDir, rt, handle := newReclaimFixture(t)
	serveHealthFrames(t, handle)

	handle.onStop = func() { breakPendingDir(t, tmpDir, "alice") }

	r.maybeReclaimIdle(context.Background(), rt)

	got, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status == state.StatusIdle {
		t.Fatal("agent rests idle after an UNREADABLE queue check; unknown is not empty")
	}
	// "Not idle" alone would also be satisfied by a wake that started and
	// faulted. serveHealthFrames exists precisely so the wake can SUCCEED, so
	// assert that it did.
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false after the backstop re-wake; the agent must be back up")
	}
}

// breakPendingDir replaces the agent's pending/ queue directory with a regular
// file, so agentloop.ListPending fails with ENOTDIR rather than reporting an
// empty queue.
func breakPendingDir(t *testing.T, sprawlRoot, name string) {
	t.Helper()
	pending := agentloop.PendingDir(sprawlRoot, name)
	if err := os.RemoveAll(pending); err != nil {
		t.Fatalf("RemoveAll(%s): %v", pending, err)
	}
	if err := os.MkdirAll(filepath.Dir(pending), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(pending, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// --- the gate: mutual exclusion between a send decision and a reap ----------

// TestReclaim_WaitsForTheSendGate proves the reaper takes the per-agent gate:
// with the gate held by the test, the final idle check and teardown must not
// proceed. Without the gate, the reap fires immediately and this test fails.
func TestReclaim_WaitsForTheSendGate(t *testing.T) {
	r, _, rt, handle := newReclaimFixture(t)

	gate := r.reclaimGate("alice")
	gate.Lock()

	done := make(chan struct{})
	go func() { defer close(done); r.maybeReclaimIdle(context.Background(), rt) }()

	select {
	case <-done:
		gate.Unlock()
		t.Fatalf("reclaim COMPLETED while the reclaim gate was held (stopCalls=%d)", handle.stopCalls.Load())
	case <-time.After(200 * time.Millisecond):
	}
	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d while the reclaim gate is held, want 0", got)
	}

	gate.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("reclaim did not complete after the gate was released")
	}
	if got := handle.stopCalls.Load(); got != 1 {
		t.Errorf("stopCalls = %d after the gate was released, want 1", got)
	}
}

// TestSendMessage_WaitsForTheSendGate is the other half: SendMessage's liveness
// decision → enqueue → poke sequence must run under the same gate. Held by the
// test, SendMessage must block.
func TestSendMessage_WaitsForTheSendGate(t *testing.T) {
	r, _, _, _ := newReclaimFixture(t)

	gate := r.reclaimGate("alice")
	gate.Lock()

	done := make(chan error, 1)
	go func() { _, err := r.SendMessage(context.Background(), "alice", "hi", false, false); done <- err }()

	select {
	case err := <-done:
		gate.Unlock()
		t.Fatalf("SendMessage returned (err=%v) while the reclaim gate was held; its liveness decision and enqueue must be serialised against a reap", err)
	case <-time.After(200 * time.Millisecond):
	}

	gate.Unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendMessage: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SendMessage did not complete after the gate was released")
	}
}

// TestReclaim_ConcurrentSends_IsARaceExerciser is deliberately NOT named for an
// invariant, because it cannot establish one. Measured, not assumed: over 30
// runs the 8 senders always win the reclaim gate before phase A, so Pending is
// obsBusy, no reap ever happens, and a "did the reap strand mail" assertion is
// unreachable by construction. The deterministic version of that claim is
// TestReclaim_MessageEnqueuedDuringStop_IsBackstoppedByRewake, which forces the
// interleaving with handle.onStop.
//
// What this one is for: driving SendMessage and maybeReclaimIdle concurrently
// under -race. Its assertions are that every send SUCCEEDED and that the end
// state is self-consistent — neither is a silent skip.
func TestReclaim_ConcurrentSends_IsARaceExerciser(t *testing.T) {
	r, tmpDir, rt, handle := newReclaimFixture(t)
	// Required, and the reason is the point of the test: when the reaper DOES
	// win the race the agent rests idle, and the next send auto-wakes it. With
	// no frames on the bus that wake fails its health probe, the agent lands in
	// resume_failed, and every later send returns "Delivery failed". Measured
	// at -count=5: without this the test fails ~1 run in 3, and it fails with a
	// message that reads like a product defect rather than a fixture that
	// cannot revive anything.
	serveHealthFrames(t, handle)

	const senders = 8
	errs := make(chan error, senders)
	var wg sync.WaitGroup
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.SendMessage(context.Background(), "alice", "ping", false, false)
			errs <- err
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); r.maybeReclaimIdle(context.Background(), rt) }()
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("SendMessage raced with a reclaim and failed: %v", err)
		}
	}

	got, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != state.StatusIdle {
		return // not reaped: the overwhelmingly common outcome, and fine
	}
	pending, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) > 0 {
		t.Fatalf("agent rests idle with %d entries still pending; a send was stranded by a concurrent reap", len(pending))
	}
}

// --- the four mutations the test critic measured as surviving ---------------

// TestReclaim_RootIsNeverReaped_AtTheReclaimCallSite is the one that matters
// most. TestAssessIdle_RootIsNeverReaped passes RootName in by hand, so it pins
// the PREDICATE and nothing else: mutating idleInputsFor's `RootName:
// r.callerName` to `""` left the whole package green while the production
// reaper would tear down the root agent and take the operator's console with
// it. This test is the wiring half.
func TestReclaim_RootIsNeverReaped_AtTheReclaimCallSite(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	if r.idleReaper != nil {
		r.idleReaper.Stop()
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	// "weave" is newFakeReal's CallerName — i.e. the root.
	rootState := testAgentState("weave")
	saveTestAgent(t, tmpDir, rootState)
	handle := &reclaimTestHandle{
		runtimeTestSession: &runtimeTestSession{sessionID: "sess-weave"},
		urt:                runtimepkg.New(runtimepkg.RuntimeConfig{Name: "weave"}),
	}
	handle.lastAct.Store(time.Now().Add(-2 * config.SuggestedIdleReclaimAfter).UnixNano())
	r.idleReclaimAfter.set(config.SuggestedIdleReclaimAfter)
	rt := ensureRuntimeWithStarter(t, r, tmpDir, rootState, &runtimeTestStarter{session: handle})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	r.maybeReclaimIdle(context.Background(), rt)

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d for the ROOT agent, want 0. The reaper just took the operator's console down", got)
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false for the root agent")
	}
}

// TestReclaim_AgentBecomesBusyDuringTheWait_IsAbandoned is the window
// StopAfterTurn cannot close on its own: the reaper's decision is taken BEFORE
// the wait, and every StopAfterTurn arm — including the runaway timer — stops
// unconditionally. So a message that lands while the reaper is waiting reaches
// an agent that is then torn down anyway, one turn later, mid-work.
//
// Here the agent is NOT in a turn by the time the teardown arm fires, so the
// only thing that can save it is the guard re-reading the predicate and finding
// the queued mail. Mutation that proves it fires: pass a nil guard to
// StopAfterTurnIf in maybeReclaimIdle.
func TestReclaim_AgentBecomesBusyDuringTheWait_IsAbandoned(t *testing.T) {
	r, tmpDir, rt, handle := newReclaimFixture(t)

	// Idle for the predicate's read, in-turn for StopAfterTurn's re-check, so
	// the reclaim parks in the turn-wait rather than stopping immediately.
	handle.flipInTurnAfter.Store(1)

	done := make(chan struct{})
	go func() { defer close(done); r.maybeReclaimIdle(context.Background(), rt) }()

	select {
	case <-done:
		t.Fatalf("precondition: reclaim completed before the turn ended (stopCalls=%d)", handle.stopCalls.Load())
	case <-time.After(150 * time.Millisecond):
	}

	// Mail lands inside the window. This is the whole point: the decision to
	// reap is already made and cannot be revised by ordering alone.
	if _, err := agentloop.Enqueue(tmpDir, "alice", agentloop.Entry{
		ShortID: "m-window", Class: agentloop.ClassAsync, From: "weave", Body: "new work",
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// The turn ends and the agent is genuinely idle again — so "still in turn"
	// is NOT what declines this teardown. Only the re-read predicate can.
	handle.flipInTurnAfter.Store(1 << 30)
	handle.urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("maybeReclaimIdle did not return after the turn ended")
	}

	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d — the agent was torn down even though work arrived while the reaper was waiting. "+
			"A stop that can only be DEFERRED is not enough; it has to be ABANDONABLE", got)
	}
	got, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status == state.StatusIdle {
		t.Errorf("agent rests idle after an abandoned reclaim, want it left active")
	}
	if !rt.SubprocessAlive() {
		t.Error("SubprocessAlive() = false after an abandoned reclaim")
	}
}

// TestNewReal_IdleReaperUsesTheConfiguredSweepInterval is the wiring half of the
// sweep knob. TestIdleReaper_Loop_SweepsOnEveryTick asserts the interval against
// a hand-built deps struct, so it proves loop() reads ITS seam — not that
// NewReal supplies one. Both `Interval: nil` and a changed fallback constant
// survived the whole package before this.
func TestNewReal_IdleReaperUsesTheConfiguredSweepInterval(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("idle_reclaim.after: \"20m\"\nidle_reclaim.sweep: \"17s\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	r, err := NewReal(Config{SprawlRoot: root, CallerName: "weave"})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	if r.idleReaper == nil {
		t.Fatal("idleReaper = nil with a positive threshold")
	}
	if r.idleReaper.deps.Interval == nil {
		t.Fatal("NewReal wired no Interval seam, so the ticker falls back to a constant and idle_reclaim.sweep is a knob that does nothing")
	}
	if got := r.idleReaper.deps.Interval(); got != 17*time.Second {
		t.Errorf("wired sweep interval = %v, want the configured 17s", got)
	}
	if got := r.idleReclaimAfter.get(); got != 20*time.Minute {
		t.Errorf("wired idle threshold = %v, want the configured 20m", got)
	}
}
