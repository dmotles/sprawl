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
	return h.runtimeTestSession.Stop(ctx)
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
	// An hour of quiet: comfortably past the 15m threshold.
	handle.lastAct.Store(time.Now().Add(-time.Hour).UnixNano())

	rt := ensureRuntimeWithStarter(t, r, tmpDir, agentState, &runtimeTestStarter{session: handle})
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

	time.Sleep(200 * time.Millisecond)
	if got := handle.stopCalls.Load(); got != 0 {
		t.Fatalf("stopCalls = %d while a turn is running, want 0; teardown must defer to turn-end (StopAfterTurn), not fire immediately", got)
	}

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
		t.Error("agent rests idle after an UNREADABLE queue check; unknown is not empty")
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

	time.Sleep(200 * time.Millisecond)
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

// TestReclaim_ConcurrentSends_NeverStrandAMessage is the invariant test, run
// under -race by make validate: after any interleaving of sends and reaps, an
// agent must never be resting idle with mail still in its queue.
func TestReclaim_ConcurrentSends_NeverStrandAMessage(t *testing.T) {
	r, tmpDir, rt, _ := newReclaimFixture(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = r.SendMessage(context.Background(), "alice", "ping", false, false)
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); r.maybeReclaimIdle(context.Background(), rt) }()
	wg.Wait()

	got, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if got.Status != state.StatusIdle {
		return // never reaped, or re-woken: both fine
	}
	pending, err := agentloop.ListPending(tmpDir, "alice")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) > 0 {
		t.Fatalf("agent rests idle with %d entries still pending; a send was stranded by a concurrent reap", len(pending))
	}
}
