package supervisor

import (
	"context"
	"sync"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-1260, second defect found by the same e2e row. Real.Shutdown uses
// AgentRuntime.Pause as the TEARDOWN MECHANISM for an in-turn agent — it drains
// the open turn instead of cutting it off, which is right. But Pause also
// stamps the OPERATOR resting status, state.StatusPaused, and the
// preserve-switch further down Shutdown then protects that value from being
// rewritten to suspended. Nobody asked for that pause. The consequence:
//
//   - `paused` is excluded from RecoverAgents' boot accept-set by design
//     (QUM-723: a paused agent revives only via the explicit `wake` verb), so
//   - any agent that happened to be mid-turn when sprawl exited is silently
//     parked and NEVER auto-resumes, on this boot or any later one.
//
// Measured in `paused-persistence` P2: the child is spawned, produces output,
// the fleet goes down, and the post-restart state file reads
// `"status": "paused"` with no `[enter] resume error:` line anywhere — because
// RecoverAgents never even tried. Zero `pause` MCP calls named that agent in
// the sandbox's mcp-calls.jsonl; the only one was `pause(agent=finn)` for a
// different agent 30 seconds earlier.
//
// The fix is to keep the mechanism and drop the operator semantics: a
// shutdown-initiated drain must rest at a status the boot accept-set includes.

// shutdownTurnHandle is in-turn and exposes a real UnifiedRuntime, so
// AgentRuntime.Pause takes its subscribe-and-wait path (with no UnifiedRuntime
// the wait collapses to a pure timeout and escalates to killed, which is a
// DIFFERENT arm — the one TestRealShutdown_InFlightTurnsEscalateToKillAfterTimeout
// already pins).
type shutdownTurnHandle struct {
	*runtimeTestSession
	urt *runtimepkg.UnifiedRuntime
}

func (h *shutdownTurnHandle) InTurn() bool                               { return true }
func (h *shutdownTurnHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.urt }

// newShutdownTurnHandle wires a handle whose turn can be completed on demand.
func newShutdownTurnHandle(name string) *shutdownTurnHandle {
	sess := &runtimeTestSession{
		sessionID: "sess-" + name,
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
	return &shutdownTurnHandle{
		runtimeTestSession: sess,
		urt:                runtimepkg.New(runtimepkg.RuntimeConfig{Name: name, Session: sess}),
	}
}

// completeTurnUntil publishes EventTurnCompleted on the handle's bus until
// done closes. Pause subscribes AFTER it decides the agent is in turn and the
// bus does not replay, so a single pre-published event would be missed; the
// loop is what makes the clean-drain arm deterministic rather than a race.
func completeTurnUntil(h *shutdownTurnHandle, done <-chan struct{}) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			h.urt.EventBus().Publish(runtimepkg.RuntimeEvent{Type: runtimepkg.EventTurnCompleted})
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return &wg
}

// TestRealShutdown_InTurnCleanDrainRestsAtSuspendedNotPaused is the defect.
// The agent's turn drains cleanly inside the budget, so this is the SUCCESS
// path of the shutdown drain — and its durable status must be one the boot
// resume accept-set includes.
func TestRealShutdown_InTurnCleanDrainRestsAtSuspendedNotPaused(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "busy", Parent: "weave", Status: state.StatusActive})

	h := newShutdownTurnHandle("busy")
	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "busy", Status: state.StatusActive},
	})
	rt.AttachHandle(h)

	done := make(chan struct{})
	wg := completeTurnUntil(h, done)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	shutdownErr := r.Shutdown(ctx)
	close(done)
	wg.Wait()
	if shutdownErr != nil {
		t.Fatalf("Shutdown: %v", shutdownErr)
	}

	cur, err := state.LoadAgent(tmpDir, "busy")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cur.Status == state.StatusKilled {
		t.Fatalf("busy.Status = killed — the turn did not drain, so this run exercised the escalation arm and says nothing about the arm under test")
	}
	if cur.Status != state.StatusSuspended {
		t.Errorf("busy.Status = %q, want %q — a shutdown-initiated drain is not an operator pause, and %q is excluded from RecoverAgents' boot accept-set so the agent would never auto-resume", cur.Status, state.StatusSuspended, cur.Status)
	}
	// The property the status is a proxy for, asserted directly so a future
	// status rename cannot quietly re-break it.
	lv, ok := liveness.LivenessFromStatus(cur.Status)
	if !ok {
		t.Fatalf("LivenessFromStatus(%q) not recognised", cur.Status)
	}
	if lv != liveness.Suspended && lv != liveness.Running {
		t.Errorf("liveness for %q = %v, which is outside RecoverAgents' boot accept-set {Suspended, Running}: this agent cannot auto-resume", cur.Status, lv)
	}
}

// TestRealShutdown_OperatorPausedAgentStaysPaused guards Shutdown's
// preserve-switch: an agent the operator paused BEFORE shutdown must still read
// `paused` afterwards, so the fix above cannot flatten it.
//
// It is NOT a negative control for pauseDrain, and calling it one would be a
// lie: with no handle attached the runtime's liveness is not Running, so
// Real.Shutdown's loop `continue`s and never reaches PauseForShutdown at all —
// no mutation of pauseDrain can make this test fire. The real negative control
// for pauseDrain is TestAgentRuntimePause_OperatorVerbStillRestsAtPaused below,
// which does reach it.
//
// No handle is attached on purpose. A paused agent's runtime has already been
// torn down, so its liveness is not Running and Shutdown's loop skips it; an
// attached handle would force Liveness=Running, which is not a state a paused
// agent can be in, and the test would then be asserting about an unreachable
// subject. (Measured while writing this: with a handle forced on, the agent is
// flattened to `suspended` by AgentRuntime.stopWithFunc's durable write, whose
// preserve switch — runtime.go, the StatusKilled/Retired/Retiring/Faulted/
// Complete/Died arm — does NOT list StatusPaused, unlike the one in
// Real.Shutdown. That asymmetry is real but only reachable from a state the
// product cannot produce, so it is noted rather than fixed here.)
func TestRealShutdown_OperatorPausedAgentStaysPaused(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "parked", Parent: "weave", Status: state.StatusPaused})

	r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "parked", Status: state.StatusPaused},
	})

	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	cur, err := state.LoadAgent(tmpDir, "parked")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cur.Status != state.StatusPaused {
		t.Errorf("parked.Status = %q, want %q (an operator pause must survive shutdown — QUM-722)", cur.Status, state.StatusPaused)
	}
}

// TestAgentRuntimePause_OperatorVerbStillRestsAtPaused is the other half of the
// negative control, at the runtime seam: the OPERATOR pause verb must keep
// stamping `paused`. The clean-drain stamping code is shared between the
// operator verb and the shutdown drain, so without this a fix could satisfy the
// test above by making nothing ever rest at paused.
func TestAgentRuntimePause_OperatorVerbStillRestsAtPaused(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	saveTestAgent(t, tmpDir, &state.AgentState{Name: "alice", Parent: "weave", Status: state.StatusActive})

	rt := r.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "alice", Status: state.StatusActive},
	})
	rt.AttachHandle(&pauseRecordingHandle{})

	clean, err := rt.Pause(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !clean {
		t.Fatalf("Pause reported clean=false for an idle handle; this run did not exercise the clean-stamp path")
	}
	cur, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cur.Status != state.StatusPaused {
		t.Errorf("alice.Status = %q, want %q (the operator pause verb owns this token)", cur.Status, state.StatusPaused)
	}
}
