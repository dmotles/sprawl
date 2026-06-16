package supervisor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-722: Real.Pause(ctx, name, PauseOptions) (*PauseResult, error).
// Outcome ∈ {"paused", "escalated_to_kill"}; cascade walks children in
// parallel.

// TestRealPause_CleanReturnsPausedOutcome — single agent, clean pause flow.
func TestRealPause_CleanReturnsPausedOutcome(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusActive,
	})

	// Register an idle runtime so Pause short-circuits to clean Stop.
	handle := &pauseRecordingHandle{}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "ratz", Status: state.StatusActive},
	})
	rt.AttachHandle(handle)

	res, err := r.Pause(context.Background(), "ratz", PauseOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if res == nil {
		t.Fatal("PauseResult nil")
	}
	if res.Outcome != "paused" {
		t.Errorf("Outcome = %q, want %q", res.Outcome, "paused")
	}
	cur, _ := state.LoadAgent(tmpDir, "ratz")
	if cur.Status != state.StatusPaused {
		t.Errorf("disk Status = %q, want %q", cur.Status, state.StatusPaused)
	}
}

// TestRealPause_TimeoutReturnsEscalatedToKill — InTurn agent never publishes
// a turn-completed event; the wait times out and Pause escalates.
func TestRealPause_TimeoutReturnsEscalatedToKill(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusActive,
	})

	handle := &pauseRecordingHandle{inTurn: true}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "ratz", Status: state.StatusActive},
	})
	rt.AttachHandle(handle)

	res, err := r.Pause(context.Background(), "ratz", PauseOptions{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if res.Outcome != "escalated_to_kill" {
		t.Errorf("Outcome = %q, want %q", res.Outcome, "escalated_to_kill")
	}
	cur, _ := state.LoadAgent(tmpDir, "ratz")
	if cur.Status != state.StatusKilled {
		t.Errorf("disk Status = %q, want %q (escalation lands on killed)", cur.Status, state.StatusKilled)
	}
}

// TestRealPause_CancelsParkedAskUserQuestion — a parked AUQ for the agent
// must be cancelled BEFORE the runtime Pause call, so a pending modal
// doesn't force the timeout escalation. We can observe this by checking
// the questions queue is empty for that agent after a clean pause.
func TestRealPause_CancelsParkedAskUserQuestion(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusActive,
	})

	// Register a QuestionConsumer so AskUserQuestion actually enqueues the
	// question (without a registered consumer the queue short-circuits with
	// OutcomeTUIUnavailable and nothing is parked).
	consumer := newFakeConsumer("tui")
	if err := r.RegisterQuestionConsumer(consumer); err != nil {
		t.Fatalf("RegisterQuestionConsumer: %v", err)
	}
	t.Cleanup(func() { r.UnregisterQuestionConsumer("tui") })

	// stopBarrier blocks Stop indefinitely until the test releases it. This
	// gives the test a moment AFTER Pause has started but BEFORE the runtime
	// Stop call has completed to observe that PeekQuestions has already
	// dropped to 0 — proving cancelByAgent runs at pause ENTRY (before the
	// runtime Pause), not at exit.
	stopBarrier := make(chan struct{})
	handle := &pauseRecordingHandle{stopBarrier: stopBarrier}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "ratz", Status: state.StatusActive},
	})
	rt.AttachHandle(handle)

	// Park a question for ratz.
	go func() {
		_, _ = r.AskUserQuestion(context.Background(), QuestionRequest{
			RequestID: "q-pause-test",
			From:      "ratz",
			Questions: []Question{{ID: "q1", Prompt: "may I proceed?"}},
		})
	}()
	// Allow the AUQ goroutine to enqueue.
	time.Sleep(50 * time.Millisecond)

	if n, _ := r.PeekQuestions(); n == 0 {
		t.Fatal("test setup: expected one parked question before Pause")
	}

	// Drive Pause in a goroutine so the test can observe state mid-call.
	pauseDone := make(chan error, 1)
	go func() {
		_, err := r.Pause(context.Background(), "ratz", PauseOptions{Timeout: 2 * time.Second})
		pauseDone <- err
	}()

	// Poll for PeekQuestions=0 BEFORE releasing the Stop barrier. This is the
	// ordering proof: if cancelByAgent ran AT EXIT (after Stop), the question
	// would still be parked while Stop is blocked. We bound the poll loop to
	// 500ms.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if n, _ := r.PeekQuestions(); n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n, _ := r.PeekQuestions(); n != 0 {
		close(stopBarrier) // unblock so we don't leak the goroutine
		<-pauseDone
		t.Fatalf("PeekQuestions during blocked Stop = %d, want 0 (cancelByAgent must run BEFORE runtime Pause/Stop, QUM-722)", n)
	}

	// Release Stop and let Pause finish.
	close(stopBarrier)
	if err := <-pauseDone; err != nil {
		t.Fatalf("Pause: %v", err)
	}

	if n, _ := r.PeekQuestions(); n != 0 {
		t.Errorf("PeekQuestions after Pause = %d live questions, want 0", n)
	}
}

// TestRealPause_CascadeWalksChildrenInParallel — parent + 2 children, all
// pause clean. Cascade slice contains both child names. Children pause in
// parallel — proven via:
//  1. a sync.WaitGroup barrier inside each child's Stop that requires N=2
//     arrivals before any child can complete (a serial walk would deadlock
//     since the second child never enters Stop), and
//  2. a per-child Stop delay of 300ms with an elapsed-time upper bound of
//     1.5× the delay — a serial walk would take ≥600ms, parallel ≤450ms.
func TestRealPause_CascadeWalksChildrenInParallel(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "parent", Parent: "weave", Status: state.StatusActive})
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "kidA", Parent: "parent", Status: state.StatusActive})
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "kidB", Parent: "parent", Status: state.StatusActive})

	const perChildDelay = 300 * time.Millisecond

	// Both children share a WaitGroup barrier with Add(2): each child's
	// Stop must call Done() and then wait for the other sibling to arrive
	// before proceeding. If the cascade were sequential, child #2 would
	// never enter Stop and the test would deadlock/timeout.
	childBarrier := make(chan struct{})
	var childArrivals sync.WaitGroup
	childArrivals.Add(2)
	go func() {
		childArrivals.Wait()
		close(childBarrier) // both kids have entered Stop concurrently
	}()

	for _, name := range []string{"parent", "kidA", "kidB"} {
		h := &pauseRecordingHandle{}
		if name == "kidA" || name == "kidB" {
			h.stopArrived = &childArrivals
			h.stopBarrier = childBarrier
			h.stopDelay = perChildDelay
		}
		rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: tmpDir,
			Agent:      &state.AgentState{Name: name, Status: state.StatusActive},
		})
		rt.AttachHandle(h)
	}

	start := time.Now()
	res, err := r.Pause(context.Background(), "parent", PauseOptions{Timeout: 5 * time.Second, Cascade: true})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Pause cascade: %v", err)
	}

	// Cascade slice carries both kids.
	have := map[string]bool{}
	for _, n := range res.Cascade {
		have[n] = true
	}
	if !have["kidA"] || !have["kidB"] {
		t.Errorf("Cascade = %v, want both kidA and kidB", res.Cascade)
	}

	// Parallel ⇒ elapsed ≈ 1× perChildDelay (+ a little overhead).
	// Serial   ⇒ elapsed ≥ 2× perChildDelay.
	if elapsed >= 2*perChildDelay {
		t.Errorf("cascade pause elapsed = %v, want < %v (children must pause in parallel)",
			elapsed, 2*perChildDelay)
	}
}

// TestRealPause_CascadeFalseErrorsIfChildrenExist — Cascade=false plus
// children present → error.
func TestRealPause_CascadeFalseErrorsIfChildrenExist(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "parent", Parent: "weave", Status: state.StatusActive})
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "kidA", Parent: "parent", Status: state.StatusActive})

	h := &pauseRecordingHandle{}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "parent", Status: state.StatusActive},
	})
	rt.AttachHandle(h)

	_, err := r.Pause(context.Background(), "parent", PauseOptions{Timeout: time.Second, Cascade: false})
	if err == nil {
		t.Errorf("Pause(cascade=false) with children should error, got nil")
	}
}

// --- Real.Kill hard-stop (QUM-722) ---

// TestRealKill_UsesStopAbandonNotStop — Real.Kill flips from polite Stop to
// hard StopAbandon. The handle must observe StopAbandon, NOT Stop nor
// Session.Interrupt.
func TestRealKill_UsesStopAbandonNotStop(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusActive,
	})

	handle := &pauseRecordingHandle{}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "ratz", Status: state.StatusActive},
	})
	rt.AttachHandle(handle)

	if err := r.Kill(context.Background(), "ratz"); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	if got := atomic.LoadInt64(&handle.stopAbandonCalls); got != 1 {
		t.Errorf("handle.StopAbandon calls = %d, want 1 (Kill must use hard StopAbandon, QUM-722)", got)
	}
	if got := atomic.LoadInt64(&handle.stopCalls); got != 0 {
		t.Errorf("handle.Stop calls = %d, want 0 (Kill must NOT issue polite Stop)", got)
	}
	if got := atomic.LoadInt64(&handle.interruptCalls); got != 0 {
		t.Errorf("handle.Interrupt calls = %d, want 0 (Kill must NOT issue Session.Interrupt)", got)
	}
}

// --- AgentInfo / PeekResult Liveness surfacing (QUM-722) ---

// TestRealStatus_PopulatesLivenessForRegisteredAgents — every returned
// AgentInfo carries Liveness derived from the runtime snapshot's projection.
func TestRealStatus_PopulatesLivenessForRegisteredAgents(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusActive,
	})
	handle := &pauseRecordingHandle{}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "ratz", Status: state.StatusActive},
	})
	rt.AttachHandle(handle)

	infos, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(infos) == 0 {
		t.Fatal("no agents returned")
	}
	for _, info := range infos {
		if info.Name != "ratz" {
			continue
		}
		// Running state projection — should populate as "running".
		if info.Liveness == "" {
			t.Errorf("AgentInfo.Liveness = empty, want non-empty for registered runtime")
		}
		if info.Liveness != liveness.Running.String() {
			t.Errorf("AgentInfo.Liveness = %q, want %q", info.Liveness, liveness.Running.String())
		}
	}
}

// TestRealStatus_PopulatesLivenessFromDiskForUnregisteredAgents — write a
// paused-state agent with no runtime; Status should still project Liveness
// from the disk Status field.
func TestRealStatus_PopulatesLivenessFromDiskForUnregisteredAgents(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "snoozy", Type: "engineer", Parent: "weave", Status: state.StatusPaused,
	})

	infos, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	found := false
	for _, info := range infos {
		if info.Name == "snoozy" {
			found = true
			if info.Liveness != liveness.Paused.String() {
				t.Errorf("snoozy.Liveness = %q, want %q (must project Paused from disk Status)",
					info.Liveness, liveness.Paused.String())
			}
		}
	}
	if !found {
		t.Fatal("snoozy missing from Status result")
	}
}

// TestRealPeek_PopulatesLiveness — Peek's PeekResult.Liveness must be set.
func TestRealPeek_PopulatesLiveness(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusPaused,
	})

	got, err := r.Peek(context.Background(), "ratz", 5)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.Liveness != liveness.Paused.String() {
		t.Errorf("PeekResult.Liveness = %q, want %q", got.Liveness, liveness.Paused.String())
	}
}

// --- Shutdown loop pause-then-kill (QUM-722) ---

// TestRealShutdown_AllIdleCompletesQuickly — when every registered runtime
// is idle, the graceful shutdown loop pauses them all and finishes within
// 1s wall-clock. Each agent ends with disk Status=paused (not killed).
func TestRealShutdown_AllIdleCompletesQuickly(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	for _, n := range []string{"a", "b", "c"} {
		saveTestAgent(t, tmpDir, &state.AgentState{Name: n, Parent: "weave", Status: state.StatusActive})
		h := &pauseRecordingHandle{}
		rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: tmpDir,
			Agent:      &state.AgentState{Name: n, Status: state.StatusActive},
		})
		rt.AttachHandle(h)
	}

	start := time.Now()
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Errorf("Shutdown(all idle) elapsed = %v, want < 1s", elapsed)
	}
	for _, n := range []string{"a", "b", "c"} {
		cur, _ := state.LoadAgent(tmpDir, n)
		if cur == nil {
			continue
		}
		if cur.Status == state.StatusKilled {
			t.Errorf("agent %s ended killed, want paused (idle agents should pause cleanly)", n)
		}
	}
}

// TestRealShutdown_InFlightTurnsEscalateToKillAfterTimeout — an in-turn
// agent never finishes; Shutdown still completes within bounded time and
// the agent ends up killed (timeout escalation).
func TestRealShutdown_InFlightTurnsEscalateToKillAfterTimeout(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "stuck", Parent: "weave", Status: state.StatusActive})

	h := &pauseRecordingHandle{inTurn: true}
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "stuck", Status: state.StatusActive},
	})
	rt.AttachHandle(h)

	// Cap test wall-time defensively.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := r.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown: %v", err)
	}
	cur, err := state.LoadAgent(tmpDir, "stuck")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cur == nil {
		t.Fatal("expected agent state after escalation")
	}
	if cur.Status != state.StatusKilled {
		t.Errorf("stuck.Status = %q, want %q (in-turn must escalate on shutdown)", cur.Status, state.StatusKilled)
	}
}

// pauseRecordingHandle is a small RuntimeHandle that records call counts
// for Pause/Kill/Shutdown assertions. InTurn is configurable.
//
// Counters use sync/atomic so concurrent cascade-children tests can race
// freely without -race complaints.
//
// stopBarrier (optional): if non-nil, Stop blocks on it before recording a
// call — used by TestRealPause_CancelsParkedAskUserQuestion to assert that
// the AUQ cancel-by-agent happens BEFORE the runtime Pause call returns.
//
// stopDelay (optional): per-call sleep inside Stop. Used by
// TestRealPause_CascadeWalksChildrenInParallel to make any serial walk
// observable in wall-clock time.
//
// stopArrived (optional): if non-nil, each Stop arrival adds 1 to the
// WaitGroup-style counter and waits for stopWG to clear, forcing N-way
// parallel arrival (Done is called after the wait).
type pauseRecordingHandle struct {
	inTurn           bool
	stopCalls        int64
	stopAbandonCalls int64
	interruptCalls   int64
	doneCh           chan struct{}
	stopBarrier      chan struct{}
	stopDelay        time.Duration
	stopArrived      *sync.WaitGroup
}

func (h *pauseRecordingHandle) Interrupt(context.Context) error {
	atomic.AddInt64(&h.interruptCalls, 1)
	return nil
}
func (h *pauseRecordingHandle) Wake() error            { return nil }
func (h *pauseRecordingHandle) WakeForDelivery() error { return nil }
func (h *pauseRecordingHandle) Stop(context.Context) error {
	if h.stopArrived != nil {
		h.stopArrived.Done()
		// Block until all expected siblings also arrive — proves N-way
		// parallel arrival. The waiter is the test goroutine via Wait().
	}
	if h.stopBarrier != nil {
		<-h.stopBarrier
	}
	if h.stopDelay > 0 {
		time.Sleep(h.stopDelay)
	}
	atomic.AddInt64(&h.stopCalls, 1)
	return nil
}

func (h *pauseRecordingHandle) StopAbandon(context.Context) error {
	atomic.AddInt64(&h.stopAbandonCalls, 1)
	return nil
}
func (h *pauseRecordingHandle) SessionID() string { return "sess-x" }
func (h *pauseRecordingHandle) Capabilities() backendpkg.Capabilities {
	return backendpkg.Capabilities{SupportsInterrupt: true}
}

func (h *pauseRecordingHandle) Done() <-chan struct{} {
	if h.doneCh == nil {
		h.doneCh = make(chan struct{})
	}
	return h.doneCh
}
func (h *pauseRecordingHandle) InTurn() bool              { return h.inTurn }
func (h *pauseRecordingHandle) IsTerminallyFaulted() bool { return false }

// TestRealPause_RefusesWhenRecovering asserts the QUM-722 review-fix guard:
// pausing an agent whose runtime is currently in liveness.Recovering must
// return an actionable error, not race the recover path.
func TestRealPause_RefusesWhenRecovering(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "ratz", Type: "engineer", Parent: "weave", Status: state.StatusActive,
	})
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "ratz", Status: state.StatusActive},
	})
	rt.AttachHandle(&pauseRecordingHandle{})
	// Force the snapshot into the transient Recovering token. Tests live
	// in package supervisor so we can touch the private fields directly.
	rt.mu.Lock()
	rt.snapshot.Liveness = liveness.Recovering
	rt.mu.Unlock()

	res, err := r.Pause(context.Background(), "ratz", PauseOptions{Timeout: time.Second})
	if err == nil {
		t.Fatalf("Pause returned nil error; want recovering-refused")
	}
	if res != nil {
		t.Errorf("PauseResult = %+v, want nil on error path", res)
	}
	if !strings.Contains(err.Error(), "waking") {
		t.Errorf("error = %q, want it to mention 'waking' (QUM-724 rename)", err.Error())
	}
}

// TestRealPause_DiskOnlyDoesNotClobberTerminalStatus asserts the QUM-722
// review-fix guard for the no-runtime branch: an agent whose disk Status
// is already terminal (Killed/Retired/Died/Faulted) must NOT be downgraded
// to Paused on a best-effort pause.
func TestRealPause_DiskOnlyDoesNotClobberTerminalStatus(t *testing.T) {
	cases := []string{
		state.StatusKilled,
		state.StatusRetired,
		state.StatusDied,
		state.StatusFaulted,
	}
	for _, st := range cases {
		st := st
		t.Run(st, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name: "ratz", Type: "engineer", Parent: "weave", Status: st,
			})
			// No runtime registered — exercises the disk-only branch.

			res, err := r.Pause(context.Background(), "ratz", PauseOptions{Timeout: time.Second})
			if err != nil {
				t.Fatalf("Pause: %v", err)
			}
			if res == nil {
				t.Fatal("PauseResult nil")
			}
			cur, _ := state.LoadAgent(tmpDir, "ratz")
			if cur.Status != st {
				t.Errorf("disk Status = %q, want %q preserved (no-clobber on terminal)", cur.Status, st)
			}
		})
	}
}
