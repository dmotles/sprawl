package supervisor

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// QUM-1186 (D2): StatusIdle is the resting state for an agent whose runtime
// was reclaimed for inactivity by the idle reaper (lane 3, QUM-1186).
//
// TestRecoverAgents_StatusIdleIsAutoResumeEligible is the load-bearing pin.
// The ENTIRE reaper design depends on a reaped agent coming back after a
// `sprawl enter` restart. If it silently drops out of the auto-resume
// accept-set, the memory the reaper reclaimed costs us the agent instead —
// and nothing else in the suite would notice, because a non-resumed agent
// produces no error, just an absence.
//
// This is exactly the class of thing that is correct on the day it lands and
// rots silently afterwards, so it is asserted directly rather than being left
// implicit in the liveness projection.
func TestRecoverAgents_StatusIdleIsAutoResumeEligible(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "reaped", state.StatusIdle, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if failed != 0 || len(errs) != 0 {
		t.Fatalf("unexpected failures: failed=%d errs=%v", failed, errs)
	}
	if resumed != 1 || len(starter.specs) != 1 || starter.specs[0].Name != "reaped" {
		t.Fatalf("idle-reclaimed agent must auto-resume on restart: resumed=%d specs=%+v", resumed, starter.specs)
	}
}

// TestStatusIdle_IsInAutoResumeAcceptSetByProjection pins the MECHANISM, not
// just the outcome. RecoverAgents keys resume-eligibility off
// liveness.LivenessFromStatus and the accept-set {Suspended, Running}; the
// test above would still pass if someone special-cased "idle" with a literal
// string compare in RecoverAgents. This asserts that idle earns its place the
// intended way — by projecting onto Suspended — so the two-axis separation
// QUM-625 established is not quietly reopened.
func TestStatusIdle_IsInAutoResumeAcceptSetByProjection(t *testing.T) {
	lv, ok := liveness.LivenessFromStatus(state.StatusIdle)
	if !ok {
		t.Fatalf("LivenessFromStatus(%q) not recognized; idle would be skipped by the resume filter", state.StatusIdle)
	}
	if lv != liveness.Suspended && lv != liveness.Running {
		t.Errorf("LivenessFromStatus(%q) = %v, which is outside the RecoverAgents accept-set {Suspended, Running}", state.StatusIdle, lv)
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
