package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// QUM-1260. The boot resume path (Real.RecoverAgents → AgentRuntime.StartResume)
// had no fresh-session fallback, while the on-demand path (AgentRuntime.Wake)
// has had one since QUM-744. Measured consequence, from the paused-persistence
// P2 row on an idle host at loadavg 0.0-0.7: the child's claude session is torn
// down before it has written a transcript (verified — no
// ~/.claude/projects/<cwd>/<sid>.jsonl was ever created for it), `claude
// --resume <sid>` is therefore rejected, the resume start fails with "backend:
// session reader exited before initialize handshake", and RecoverAgents'
// OnResumeFailure closure stamps state.StatusResumeFailed — a status OUTSIDE
// the boot accept-set ({Suspended, Running}), so the agent can never
// auto-resume again on any later `sprawl enter`. A rejected cookie should cost
// transcript continuity, not the agent.
//
// CONTROL DIRECTIONS, stated explicitly because a control's name never tells
// you which way it is aimed:
//
//   - POSITIVE: the subject is StartResume as it stood before the fix, which
//     contains the defect. Every test below whose name says "FallsBack" or
//     "ReportsBoth" was run against it and fired; the recorded red output is in
//     the commit message, per-assertion, along with the mutations used for the
//     assertions the leading t.Fatalf shadowed.
//   - NEGATIVE: TestStartResume_NoFallbackWhenResumeSucceeds — a subject known
//     clean (a resume that is NOT rejected). These probes must stay silent
//     there, otherwise a fix that always started a second session would satisfy
//     every other assertion in the file.
//   - Cross-path regression guard (NOT a control):
//     TestWake_FallbackOnResumeRejected in runtime_wake_new_test.go pins the
//     already-correct wake-path behaviour and must stay green.

var (
	errResumeRejected = errors.New("backend: session reader exited before initialize handshake")
	errFreshRejected  = errors.New("backend: fresh start also failed")
)

// mirrorSpecSession builds a handle whose SessionID is the one the spec asked
// to launch with, so a caller persisting Snapshot().SessionID records the id
// the attempt actually used rather than a fixture constant.
func mirrorSpecSession(_ int, spec RuntimeStartSpec) *runtimeTestSession {
	return &runtimeTestSession{
		sessionID: spec.SessionID,
		caps:      recoverTestSession("").caps,
	}
}

// TestStartResume_FallsBackToFreshWhenResumeStartFails — a resume whose Start
// fails must be retried ONCE as a fresh session, carrying the same
// restart-injection prompt and a NEWLY MINTED session id (never the rejected
// one, and never empty: QUM-744 established that letting claude self-generate
// loses the id host-side, so the next restart would resume a defunct
// transcript).
func TestStartResume_FallsBackToFreshWhenResumeStartFails(t *testing.T) {
	starter := &wakeCapturingStarter{
		startErrByCall: map[int]error{1: errResumeRejected},
		sessionMaker:   mirrorSpecSession,
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	if err := rt.StartResume("RESTART-INJECTION"); err != nil {
		t.Fatalf("StartResume = %v, want nil (the fresh fallback must rescue a rejected resume)", err)
	}

	specs := starter.snapshotSpecs()
	if len(specs) != 2 {
		t.Fatalf("starter called %d time(s), want 2 (one --resume attempt, then one fresh fallback)", len(specs))
	}
	if !specs[0].Resume {
		t.Errorf("first attempt Resume = false, want true")
	}
	if specs[0].SessionID != "sess-alice" {
		t.Errorf("first attempt SessionID = %q, want the persisted %q", specs[0].SessionID, "sess-alice")
	}
	if specs[0].OnResumeFailure == nil {
		t.Errorf("first attempt has no OnResumeFailure installed; the stderr marker would then have no way to report a rejected cookie at all")
	}
	if specs[1].Resume {
		t.Errorf("fallback attempt Resume = true, want false (that is what makes it a FRESH session)")
	}
	if specs[1].SessionID == "" {
		t.Errorf("fallback SessionID is empty; want a freshly minted id so the host tracks the new transcript (QUM-744)")
	}
	if specs[1].SessionID == specs[0].SessionID {
		t.Errorf("fallback SessionID = %q, must differ from the rejected id", specs[1].SessionID)
	}
	if specs[1].RestartInjection != "RESTART-INJECTION" {
		t.Errorf("fallback RestartInjection = %q, want it carried through", specs[1].RestartInjection)
	}
	if got := rt.Snapshot().SessionID; got != specs[1].SessionID {
		t.Errorf("Snapshot().SessionID = %q, want the fresh id %q so RecoverAgents persists it", got, specs[1].SessionID)
	}
}

// TestStartResume_FallsBackWhenCookieRejectedInBand — the other half of the
// defect: the stderr marker can fire while Start still returns a handle (the
// claude process comes up, prints "No conversation found", and only then
// exits). AgentRuntime.Wake already treats that as needing the fresh fallback;
// the boot path must agree, or the two resume paths disagree about the same
// backend signal.
func TestStartResume_FallsBackWhenCookieRejectedInBand(t *testing.T) {
	starter := &wakeCapturingStarter{
		fireResumeFailOn: 1,
		sessionMaker:     mirrorSpecSession,
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	var stamped atomic.Int64
	if err := rt.StartResume("RESTART-INJECTION", func() { stamped.Add(1) }); err != nil {
		t.Fatalf("StartResume = %v, want nil", err)
	}
	specs := starter.snapshotSpecs()
	if len(specs) != 2 {
		t.Fatalf("starter called %d time(s), want 2 (an in-band cookie rejection must trigger the fallback)", len(specs))
	}
	if specs[1].Resume {
		t.Errorf("fallback attempt Resume = true, want false")
	}
	if got := stamped.Load(); got != 0 {
		t.Errorf("caller's OnResumeFailure fired %d time(s), want 0 — the fallback rescued the agent, so resume_failed would be a false durable status", got)
	}
	// The abandoned resume attempt's handle must not still be the live one.
	if got := rt.Snapshot().SessionID; got != specs[1].SessionID {
		t.Errorf("Snapshot().SessionID = %q, want the fresh id %q (the rejected attempt must have been abandoned)", got, specs[1].SessionID)
	}
}

// TestStartResume_DiscardsMarkerFromAbandonedAttempt — once the fallback has
// been taken, a marker arriving late from the ABANDONED resume attempt must not
// reach the caller: stamping resume_failed then would brick an agent that is
// running fine on its fresh session. This is the assertion that distinguishes a
// correct fix from one that simply forwards every marker.
func TestStartResume_DiscardsMarkerFromAbandonedAttempt(t *testing.T) {
	starter := &wakeCapturingStarter{
		startErrByCall: map[int]error{1: errResumeRejected},
		sessionMaker:   mirrorSpecSession,
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	var stamped atomic.Int64
	if err := rt.StartResume("RESTART-INJECTION", func() { stamped.Add(1) }); err != nil {
		t.Fatalf("StartResume = %v, want nil", err)
	}
	specs := starter.snapshotSpecs()
	if len(specs) < 1 || specs[0].OnResumeFailure == nil {
		t.Fatalf("no OnResumeFailure captured from the resume attempt")
	}
	specs[0].OnResumeFailure()
	if got := stamped.Load(); got != 0 {
		t.Errorf("caller's OnResumeFailure fired %d time(s) for a marker from the abandoned attempt, want 0", got)
	}
}

// TestStartResume_ForwardsLateMarkerOnSuccessfulResume — the converse of the
// test above, and the reason the caller's callback is intercepted rather than
// dropped. When the resume SUCCEEDED there is no fallback in flight, so a
// marker that lands after StartResume returned is a genuine late rejection and
// must reach the caller so the durable status becomes resume_failed. Without
// this, the fix would silently delete the behaviour
// TestRealRecoverAgents_OnResumeFailureFlipsStatusToResumeFailed depends on.
func TestStartResume_ForwardsLateMarkerOnSuccessfulResume(t *testing.T) {
	starter := &wakeCapturingStarter{sessionMaker: mirrorSpecSession}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	var stamped atomic.Int64
	if err := rt.StartResume("RESTART-INJECTION", func() { stamped.Add(1) }); err != nil {
		t.Fatalf("StartResume = %v, want nil", err)
	}
	specs := starter.snapshotSpecs()
	if len(specs) != 1 {
		t.Fatalf("starter called %d time(s), want 1", len(specs))
	}
	if specs[0].OnResumeFailure == nil {
		t.Fatalf("no OnResumeFailure captured")
	}
	specs[0].OnResumeFailure()
	if got := stamped.Load(); got != 1 {
		t.Errorf("caller's OnResumeFailure fired %d time(s), want 1 (a late marker on a session with no fallback in flight is a real failure)", got)
	}
}

// TestStartResume_BothAttemptsFailReportsBoth — when the fresh fallback fails
// too there is nothing left to try. The error must name BOTH causes (a message
// mentioning only the fallback hides why a resume was attempted at all) and the
// caller's callback MUST fire, because resume_failed is now the truthful
// durable status.
func TestStartResume_BothAttemptsFailReportsBoth(t *testing.T) {
	starter := &wakeCapturingStarter{
		startErrByCall: map[int]error{1: errResumeRejected, 2: errFreshRejected},
		sessionMaker:   mirrorSpecSession,
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	var stamped atomic.Int64
	err := rt.StartResume("RESTART-INJECTION", func() { stamped.Add(1) })
	if err == nil {
		t.Fatalf("StartResume = nil, want an error when both the resume and the fresh fallback fail")
	}
	if !errors.Is(err, errResumeRejected) {
		t.Errorf("error %q does not wrap the resume-leg cause %q", err, errResumeRejected)
	}
	if !errors.Is(err, errFreshRejected) {
		t.Errorf("error %q does not wrap the fresh-leg cause %q", err, errFreshRejected)
	}
	if got := stamped.Load(); got != 1 {
		t.Errorf("OnResumeFailure fired %d time(s), want 1 (both attempts failed, so resume_failed is the truthful status)", got)
	}
	if n := starter.callCount(); n != 2 {
		t.Errorf("starter called %d time(s), want exactly 2 — the fallback must be tried once, not retried in a loop", n)
	}
}

// TestStartResume_NoFallbackWhenResumeSucceeds is the NEGATIVE control for this
// file: a subject known clean (resume accepted), where every fallback probe
// must stay silent.
func TestStartResume_NoFallbackWhenResumeSucceeds(t *testing.T) {
	starter := &wakeCapturingStarter{sessionMaker: mirrorSpecSession}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})

	var stamped atomic.Int64
	if err := rt.StartResume("RESTART-INJECTION", func() { stamped.Add(1) }); err != nil {
		t.Fatalf("StartResume = %v, want nil", err)
	}
	specs := starter.snapshotSpecs()
	if len(specs) != 1 {
		t.Fatalf("starter called %d time(s), want 1 (a successful resume must not start a second session)", len(specs))
	}
	if !specs[0].Resume {
		t.Errorf("Resume = false, want true")
	}
	if specs[0].SessionID != "sess-alice" {
		t.Errorf("SessionID = %q, want the persisted id unchanged", specs[0].SessionID)
	}
	if got := stamped.Load(); got != 0 {
		t.Errorf("OnResumeFailure fired %d time(s), want 0", got)
	}
}

// TestRealRecoverAgents_ResumeRejectionEndsActiveNotResumeFailed is the
// end-to-end shape of the paused-persistence P2 failure, at the supervisor
// seam: an eligible crash-survivor whose --resume leg is rejected must come
// back as a live agent at Status=active with the fresh session id persisted,
// and must be counted as resumed rather than failed. Before the fix this agent
// ended at resume_failed with failed=1 — permanently unresumable, because
// resume_failed is outside the boot accept-set.
func TestRealRecoverAgents_ResumeRejectionEndsActiveNotResumeFailed(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &wakeCapturingStarter{
		startErrByCall: map[int]error{1: errResumeRejected},
		sessionMaker:   mirrorSpecSession,
	}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusActive, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 1 || failed != 0 || len(errs) != 0 {
		t.Fatalf("RecoverAgents = (%d,%d,%v), want (1,0,nil)", resumed, failed, errs)
	}

	loaded, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status != state.StatusActive {
		t.Errorf("Status = %q, want %q — a rescued resume must not leave the agent in a status the boot accept-set excludes", loaded.Status, state.StatusActive)
	}
	specs := starter.snapshotSpecs()
	if len(specs) != 2 {
		t.Fatalf("starter called %d time(s), want 2", len(specs))
	}
	if loaded.SessionID != specs[1].SessionID {
		t.Errorf("persisted SessionID = %q, want the fresh fallback id %q (otherwise the NEXT restart resumes a defunct transcript)", loaded.SessionID, specs[1].SessionID)
	}
}
