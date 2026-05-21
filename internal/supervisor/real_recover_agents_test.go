// QUM-372: tests for Real.RecoverAgents — the startup auto-resume scan that
// walks .sprawl/agents/ and calls AgentRuntime.StartResume on every direct
// child still in a non-terminal state.
package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// recoverTestStarter is a per-test starter that records every Start call
// (spec + return) and lets the test inject per-agent errors. Reuses
// runtimeTestSession from runtime_test.go.
type recoverTestStarter struct {
	specs       []RuntimeStartSpec
	session     RuntimeHandle
	errByName   map[string]error
	callCounter int
}

func (s *recoverTestStarter) Start(spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.callCounter++
	s.specs = append(s.specs, spec)
	if err, ok := s.errByName[spec.Name]; ok && err != nil {
		return nil, err
	}
	return s.session, nil
}

// recoverTestSession returns a runtimeTestSession with default capabilities
// pre-populated for resume scenarios.
func recoverTestSession(sessionID string) *runtimeTestSession {
	return &runtimeTestSession{
		sessionID: sessionID,
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}
}

// makeWorktreeDir creates a stub worktree directory so RecoverAgents'
// os.Stat filter does not skip the agent.
func makeWorktreeDir(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "wt", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worktree %q: %v", name, err)
	}
	return dir
}

// saveRecoverAgent persists an AgentState with the supplied fields, ensuring
// the worktree dir on disk so RecoverAgents does not skip it.
func saveRecoverAgent(t *testing.T, root, name, status, parent string) *state.AgentState {
	t.Helper()
	wt := makeWorktreeDir(t, root, name)
	ag := &state.AgentState{
		Name:      name,
		Type:      "engineer",
		Family:    "engineering",
		Parent:    parent,
		Branch:    "dmotles/" + name,
		Worktree:  wt,
		Status:    status,
		CreatedAt: "2026-05-19T00:00:00Z",
		SessionID: "sess-" + name,
		TreePath:  parent + "/" + name,
	}
	saveTestAgent(t, root, ag)
	return ag
}

// installStarter swaps the runtime starter on Real with the given starter so
// every Ensure+StartResume path goes through it.
func installStarter(r *Real, st RuntimeStarter) {
	r.runtimeStarter = st
}

func TestRealRecoverAgents_EmptyDirReturnsZero(t *testing.T) {
	r, _ := newFakeReal(t)
	installStarter(r, &recoverTestStarter{session: recoverTestSession("sess-x")})

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 0 || failed != 0 || len(errs) != 0 {
		t.Errorf("empty dir = (%d,%d,%v), want (0,0,nil)", resumed, failed, errs)
	}
}

// TestRealRecoverAgents_FiltersAndCallsStartResumeWithResumeTrue persists
// agents on disk with the full status enum: the three non-terminal statuses
// {suspended, active, running} plus the five terminal/transient ones
// {killed, done, retired, retiring, resume_failed}. RecoverAgents must start
// exactly the three non-terminal ones, each with Resume=true in the spec.
func TestRealRecoverAgents_FiltersAndCallsStartResumeWithResumeTrue(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	// Non-terminal: must be started.
	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")
	saveRecoverAgent(t, tmpDir, "bob", state.StatusActive, "weave")
	saveRecoverAgent(t, tmpDir, "carol", state.StatusRunning, "weave")
	// Terminal / not-eligible-for-resume: must be skipped.
	saveRecoverAgent(t, tmpDir, "dave", state.StatusKilled, "weave")
	saveRecoverAgent(t, tmpDir, "eve", state.StatusDone, "weave")
	saveRecoverAgent(t, tmpDir, "frank", state.StatusRetired, "weave")
	saveRecoverAgent(t, tmpDir, "grace", state.StatusRetiring, "weave")
	saveRecoverAgent(t, tmpDir, "heidi", state.StatusResumeFailed, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if failed != 0 || len(errs) != 0 {
		t.Errorf("unexpected failures: failed=%d errs=%v", failed, errs)
	}
	if resumed != 3 {
		t.Errorf("resumed = %d, want 3", resumed)
	}

	if len(starter.specs) != 3 {
		t.Fatalf("starter.specs len = %d, want 3 (alice/bob/carol; killed/done/retired/retiring/resume_failed must be skipped)", len(starter.specs))
	}
	gotNames := make([]string, 0, 3)
	for _, sp := range starter.specs {
		gotNames = append(gotNames, sp.Name)
		if !sp.Resume {
			t.Errorf("spec for %q must have Resume=true; got false", sp.Name)
		}
	}
	sort.Strings(gotNames)
	wantNames := []string{"alice", "bob", "carol"}
	for i := range wantNames {
		if gotNames[i] != wantNames[i] {
			t.Errorf("started names = %v, want %v", gotNames, wantNames)
			break
		}
	}
}

// TestRealRecoverAgents_SkipsRootCaller — the supervisor's own callerName
// (weave) is itself persisted as an agent in some sessions; the scan must
// not try to resume itself.
func TestRealRecoverAgents_SkipsRootCaller(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	// callerName is "weave" by default in newFakeReal.
	saveRecoverAgent(t, tmpDir, "weave", state.StatusSuspended, "")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 0 || failed != 0 || len(errs) != 0 {
		t.Errorf("got (%d,%d,%v), want (0,0,nil) — weave is the caller, must be skipped", resumed, failed, errs)
	}
	if len(starter.specs) != 0 {
		t.Errorf("starter.specs = %d, want 0 — RecoverAgents must not start the root caller", len(starter.specs))
	}
}

// TestRealRecoverAgents_SkipsMissingWorktree — if the worktree dir was
// deleted between sessions, the agent cannot be resumed safely; skip and
// continue.
func TestRealRecoverAgents_SkipsMissingWorktree(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	ag := saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")
	// Remove the worktree we just created.
	if err := os.RemoveAll(ag.Worktree); err != nil {
		t.Fatalf("RemoveAll worktree: %v", err)
	}

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 0 || failed != 0 || len(errs) != 0 {
		t.Errorf("got (%d,%d,%v), want (0,0,nil)", resumed, failed, errs)
	}
	if len(starter.specs) != 0 {
		t.Errorf("starter.specs = %d, want 0 — agent with missing worktree must be skipped", len(starter.specs))
	}
}

// TestRealRecoverAgents_FailureIsolation — three eligible agents; starter
// errs on the middle one. The first and third must still be started, the
// failing one's status must NOT be flipped to "active" (it remains as it
// was: suspended), and the return values must reflect (2, 1, len==1).
func TestRealRecoverAgents_FailureIsolation(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{
		session:   recoverTestSession("sess-shared"),
		errByName: map[string]error{"bob": errors.New("bob start exploded")},
	}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")
	saveRecoverAgent(t, tmpDir, "bob", state.StatusSuspended, "weave")
	saveRecoverAgent(t, tmpDir, "carol", state.StatusSuspended, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 2 {
		t.Errorf("resumed = %d, want 2", resumed)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if len(errs) != 1 {
		t.Errorf("len(errs) = %d, want 1", len(errs))
	}

	// All three were attempted (failure must not abort the loop).
	if len(starter.specs) != 3 {
		t.Errorf("starter.specs len = %d, want 3 (loop must continue past failure)", len(starter.specs))
	}

	// alice + carol flipped to active, bob remains suspended (the
	// resume-failed status is only set by the OnResumeFailure marker
	// callback, not on a starter error — covered separately below).
	for _, want := range []struct{ name, status string }{
		{"alice", state.StatusActive},
		{"carol", state.StatusActive},
		{"bob", state.StatusSuspended},
	} {
		loaded, err := state.LoadAgent(tmpDir, want.name)
		if err != nil {
			t.Fatalf("LoadAgent(%q): %v", want.name, err)
		}
		if loaded.Status != want.status {
			t.Errorf("%s.Status = %q, want %q", want.name, loaded.Status, want.status)
		}
	}
}

// TestRealRecoverAgents_OnResumeFailureFlipsStatusToResumeFailed — when the
// starter records the spec and the test invokes spec.OnResumeFailure() (as
// the live claude stderr scanner would), the on-disk status must flip to
// resume_failed so the next sprawl-enter can fall back to a fresh launch.
func TestRealRecoverAgents_OnResumeFailureFlipsStatusToResumeFailed(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 1 || failed != 0 || len(errs) != 0 {
		t.Fatalf("RecoverAgents = (%d,%d,%v), want (1,0,nil)", resumed, failed, errs)
	}
	if len(starter.specs) != 1 {
		t.Fatalf("starter.specs len = %d, want 1", len(starter.specs))
	}
	if starter.specs[0].OnResumeFailure == nil {
		t.Fatalf("starter.specs[0].OnResumeFailure must be installed by RecoverAgents")
	}

	starter.specs[0].OnResumeFailure()

	loaded, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status != state.StatusResumeFailed {
		t.Errorf("Status = %q, want %q (OnResumeFailure must flip on-disk status)", loaded.Status, state.StatusResumeFailed)
	}
}

// TestRealRecoverAgents_OnSuccessSetsStatusActiveAndSavesSessionID — happy
// path: starter returns a healthy handle whose SessionID matches the
// persisted ID. After RecoverAgents, on-disk status is "active" and
// session_id is unchanged (Step 0 confirmed claude --resume does not rotate
// the ID).
func TestRealRecoverAgents_OnSuccessSetsStatusActiveAndSavesSessionID(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-alice")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")

	if _, _, errs := r.RecoverAgents(context.Background()); len(errs) > 0 {
		t.Fatalf("RecoverAgents errs: %v", errs)
	}

	loaded, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Status != state.StatusActive {
		t.Errorf("Status = %q, want %q", loaded.Status, state.StatusActive)
	}
	if loaded.SessionID != "sess-alice" {
		t.Errorf("SessionID = %q, want sess-alice (must remain stable per Step 0 finding)", loaded.SessionID)
	}
}

// TestRealRecoverAgents_WakesForDeliveryAfterSuccess — QUM-605: after a
// successful StartResume, RecoverAgents must call WakeForDelivery on the
// runtime so any maildir entries that arrived while the agent was suspended
// get drained into the resumed session's first turn. Without this, async
// send_message landings sit forever until something else wakes the agent.
func TestRealRecoverAgents_WakesForDeliveryAfterSuccess(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	session := recoverTestSession("sess-alice")
	starter := &recoverTestStarter{session: session}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 1 || failed != 0 || len(errs) != 0 {
		t.Fatalf("RecoverAgents = (%d,%d,%v), want (1,0,nil)", resumed, failed, errs)
	}
	if session.wakeForDeliveryCalls < 1 {
		t.Errorf("wakeForDeliveryCalls = %d, want >= 1 (QUM-605: resumed agents must drain pending maildir on resume)", session.wakeForDeliveryCalls)
	}
}

// TestRealRecoverAgents_NoWakeAfterFailedStart — when StartResume fails, no
// handle is attached, so WakeForDelivery must not be invoked (it would error).
// The OnResumeFailure callback is the recovery path; wake is only on success.
func TestRealRecoverAgents_NoWakeAfterFailedStart(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	session := recoverTestSession("sess-alice")
	starter := &recoverTestStarter{
		session:   session,
		errByName: map[string]error{"alice": errors.New("boom")},
	}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")

	if _, failed, _ := r.RecoverAgents(context.Background()); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	if session.wakeForDeliveryCalls != 0 {
		t.Errorf("wakeForDeliveryCalls = %d, want 0 (must not wake when StartResume failed)", session.wakeForDeliveryCalls)
	}
}

// TestRealRecoverAgents_BFSOrderParentsBeforeChildren — when the tree is
// (weave-root) -> alice -> bob, the scan must start alice BEFORE bob so
// child runtimes can find their parent registered when they begin
// initializing their MCP bridge.
func TestRealRecoverAgents_BFSOrderParentsBeforeChildren(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	// alice is a child of weave (root caller).
	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")
	// bob is a child of alice.
	saveRecoverAgent(t, tmpDir, "bob", state.StatusSuspended, "alice")

	if _, _, errs := r.RecoverAgents(context.Background()); len(errs) > 0 {
		t.Fatalf("RecoverAgents errs: %v", errs)
	}
	if len(starter.specs) != 2 {
		t.Fatalf("starter.specs len = %d, want 2", len(starter.specs))
	}

	aliceIdx, bobIdx := -1, -1
	for i, sp := range starter.specs {
		switch sp.Name {
		case "alice":
			aliceIdx = i
		case "bob":
			bobIdx = i
		}
	}
	if aliceIdx < 0 || bobIdx < 0 {
		t.Fatalf("expected both alice and bob to be started; got specs=%v", starter.specs)
	}
	if aliceIdx >= bobIdx {
		t.Errorf("BFS order violation: alice idx=%d, bob idx=%d (parent must precede child)", aliceIdx, bobIdx)
	}
}
