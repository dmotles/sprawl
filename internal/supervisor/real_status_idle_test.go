package supervisor

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-1186 (D2): StatusIdle is the resting state for an agent whose runtime
// was reclaimed for inactivity by the idle reaper (lane 3).
//
// TestRecoverAgents_StatusIdleIsNotAutoResumedAtBoot pins the REVERSAL of an
// earlier decision, and the reasoning matters more than the assertion.
//
// The first version of this file asserted the OPPOSITE — that an idle agent
// auto-resumes at boot — on the reasoning that a reaped agent must come back
// after a `sprawl enter` restart or the memory win costs us the agent. That
// reasoning was right about the GOAL and wrong about the MECHANISM: this loop
// is the BOOT resume loop, so satisfying it means relaunching every reclaimed
// agent on every restart. Ten reclaimed agents is ~2.8GB of RSS handed
// straight back — the reaper would free memory only until the next restart.
//
// The agent does come back; it comes back ON DEMAND, when someone messages it
// (TestSendMessage_StatusIdleTarget_AutoWakesWithoutFlag below), which is the
// same guarantee without the eager cost.
//
// This is asserted directly because idle projects onto liveness.Suspended and
// would therefore fall INSIDE the {Suspended, Running} accept-set by default.
// The exclusion is a deliberate extra guard, and a guard nothing tests is a
// guard that gets tidied away.
func TestRecoverAgents_StatusIdleIsNotAutoResumedAtBoot(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "reaped", state.StatusIdle, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if failed != 0 || len(errs) != 0 {
		t.Fatalf("unexpected failures: failed=%d errs=%v", failed, errs)
	}
	if resumed != 0 || len(starter.specs) != 0 {
		t.Fatalf("idle-reclaimed agent must NOT relaunch at boot: resumed=%d specs=%+v", resumed, starter.specs)
	}
}

// TestRecoverAgents_BootResumeAcceptSet is the decision-site sweep tower asked
// for, turned into an assertion.
//
// Deleting the LastReport* fields removed an `if` that excluded completed and
// failed agents from this loop. That kind of deletion fails in the WORST
// direction: the condition silently becomes never-true, the guard evaporates,
// and every completed and faulted agent relaunches at boot — no error, no log,
// no compile failure. The exclusion is supposed to have moved onto the Status
// axis; this enumerates every status and proves it actually did.
func TestRecoverAgents_BootResumeAcceptSet(t *testing.T) {
	for _, tc := range []struct {
		status string
		resume bool
	}{
		{state.StatusActive, true},    // crash survivor
		{state.StatusRunning, true},   // crash survivor, legacy token
		{state.StatusSuspended, true}, // clean shutdown
		{state.StatusIdle, false},     // reclaimed: revivable on demand, not at boot
		{state.StatusComplete, false}, // finished
		{state.StatusFaulted, false},  // genuine fault
		{state.StatusPaused, false},   // operator rest state
		{state.StatusKilled, false},
		{state.StatusDied, false},
		{state.StatusRetired, false},
		{state.StatusRetiring, false},
		{state.StatusResumeFailed, false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
			installStarter(r, starter)

			saveRecoverAgent(t, tmpDir, "subject", tc.status, "weave")

			resumed, _, _ := r.RecoverAgents(context.Background())
			got := resumed > 0
			if got != tc.resume {
				t.Errorf("status %q: auto-resumed at boot = %v, want %v", tc.status, got, tc.resume)
			}
		})
	}
}

// TestStatusIdle_ProjectsSuspendedSoTheBootExclusionIsDeliberate documents WHY
// the boot exclusion above has to be an explicit extra guard rather than a
// consequence of the projection.
//
// idle projects onto liveness.Suspended — that is load-bearing for the TUI,
// for merge, and for the wake path — and Suspended IS in the boot accept-set.
// So without the explicit `a.Status == StatusIdle` skip, idle would auto-resume
// by default. Anyone who deletes that skip as redundant needs this test to
// tell them it is not.
func TestStatusIdle_ProjectsSuspendedSoTheBootExclusionIsDeliberate(t *testing.T) {
	lv, ok := liveness.LivenessFromStatus(state.StatusIdle)
	if !ok {
		t.Fatalf("LivenessFromStatus(%q) not recognized", state.StatusIdle)
	}
	if lv != liveness.Suspended {
		t.Fatalf("LivenessFromStatus(%q) = %v, want Suspended", state.StatusIdle, lv)
	}
	// Suspended is inside the accept-set, which is exactly why the explicit
	// exclusion in RecoverAgents is required and must not be tidied away.
	if lv != liveness.Suspended && lv != liveness.Running {
		t.Fatal("unreachable")
	}
}

// TestRecoverAgents_PausedStillNotResumed is the NEGATIVE control, direction:
// it must stay quiet. Widening the accept-set to admit idle must not admit
// paused, which is an explicit user-initiated rest state that must only
// revive via the `wake` verb (QUM-723).
func TestRecoverAgents_PausedStillNotResumed(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "parked", state.StatusPaused, "weave")

	resumed, _, _ := r.RecoverAgents(context.Background())
	if resumed != 0 || len(starter.specs) != 0 {
		t.Fatalf("paused agent must NOT auto-resume: resumed=%d specs=%+v", resumed, starter.specs)
	}
}

// TestSendMessage_StatusIdleTarget_AutoWakesWithoutFlag pins the SendMessage
// auto-wake arm for StatusIdle (real.go).
//
// Added after code review: the arm shipped with ZERO coverage. Mutation-proven
// — dropping `|| agentState.Status == state.StatusIdle` from the arm left the
// entire ./internal/supervisor package passing.
//
// This is the branch that keeps the reaper INVISIBLE. Without it a reclaimed
// agent rejects delivery unless the sender passes wake_if_offline, which would
// make an internal memory decision into something every caller has to know
// about.
func TestSendMessage_StatusIdleTarget_AutoWakesWithoutFlag(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	shortenWakeTimeouts(t)
	agentState := testAgentState("reaped")
	agentState.Status = state.StatusIdle
	agentState.SessionID = "sess-reaped"
	saveTestAgent(t, tmpDir, agentState)

	starter := &wakeCapturingStarter{}
	installStarter(r, starter)

	// wake_if_offline is deliberately FALSE: that is the whole assertion.
	if _, err := r.SendMessage(context.Background(), "reaped", "pick this up", false, false); err != nil {
		t.Fatalf("SendMessage to an idle-reclaimed agent returned %v; want nil (idle must auto-wake with no flag)", err)
	}
	if len(starter.snapshotSpecs()) == 0 {
		t.Fatal("no start spec captured — the idle target was not woken")
	}
}

// TestSendMessage_PausedTarget_StillRequiresFlag is the NEGATIVE control,
// direction: must stay quiet. Admitting idle to the auto-wake arm must not
// admit paused, which is an explicit user-initiated rest state and must keep
// returning the canonical byte-pinned offline error.
func TestSendMessage_PausedTarget_StillRequiresFlag(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	agentState := testAgentState("parked")
	agentState.Status = state.StatusPaused
	saveTestAgent(t, tmpDir, agentState)

	if _, err := r.SendMessage(context.Background(), "parked", "hi", false, false); err == nil {
		t.Fatal("SendMessage to a paused agent with no wake_if_offline returned nil; want the canonical offline error")
	}
}

// TestSyncAgentState_StatusIdleProjectsToUnstarted pins the SyncAgentState arm
// for StatusIdle (runtime.go).
//
// Added after code review: the arm shipped with ZERO coverage — deleting the
// `updated.Status == state.StatusIdle ||` line left the whole package passing.
//
// THE SETUP IS THE TEST. A first attempt at this test seeded a fresh runtime
// and was ITSELF vacuous: with no prior liveness the switch falls to its
// `default:` arm, which sets Unstarted anyway, so the assertion passed with the
// branch deleted. The arm only does work when a STALE Running liveness is
// already on the snapshot and the handle is gone — which is precisely the
// post-reclaim shape its comment describes, and the only shape in which
// omitting it leaves the TUI rendering a live agent that no longer exists.
//
// So the snapshot is forced to Running with handle == nil before syncing.
func TestSyncAgentState_StatusIdleProjectsToUnstarted(t *testing.T) {
	root := t.TempDir()
	agent := testAgentState("reaped")
	saveTestAgent(t, root, agent)

	rt := NewAgentRuntime(AgentRuntimeConfig{SprawlRoot: root, Agent: agent})

	// Stale pre-reclaim liveness, no handle: the reaper tore the process down
	// but the snapshot has not caught up yet.
	rt.mu.Lock()
	rt.snapshot.Liveness = liveness.Running
	rt.handle = nil
	rt.mu.Unlock()

	reclaimed := testAgentState("reaped")
	reclaimed.Status = state.StatusIdle
	rt.SyncAgentState(reclaimed)

	snap := rt.Snapshot()
	if snap.Liveness != liveness.Unstarted {
		t.Errorf("post-sync Liveness = %v, want %v (an idle-reclaimed agent has no handle and must collapse to Unstarted so liveness.From can decode DiskStatus)",
			snap.Liveness, liveness.Unstarted)
	}
	if snap.Status != state.StatusIdle {
		t.Errorf("post-sync Status = %q, want %q", snap.Status, state.StatusIdle)
	}
}
