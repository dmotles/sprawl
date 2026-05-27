// QUM-372: tests for Real.RecoverAgents — the startup auto-resume scan that
// walks .sprawl/agents/ and calls AgentRuntime.StartResume on every direct
// child still in a non-terminal state.
package supervisor

import (
	"context"
	"encoding/json"
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

// writeRawV0RecoverAgent writes a genuine schema_version-0 agent fixture
// (no schema_version key — SchemaVersion=0 is omitempty) directly to disk,
// bypassing SaveAgent (which would stamp v1). It also creates the worktree so
// the resume-eligibility worktree-exists check is not the reason an agent is
// skipped. Used to exercise migrate-on-load through RecoverAgents.
func writeRawV0RecoverAgent(t *testing.T, root, name, status, parent string) {
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
		// SchemaVersion intentionally 0 (omitempty) → genuine v0 fixture.
	}
	data, err := json.MarshalIndent(ag, "", "  ")
	if err != nil {
		t.Fatalf("marshal v0 fixture: %v", err)
	}
	if err := os.MkdirAll(state.AgentsDir(root), 0o755); err != nil {
		t.Fatalf("mkdir agents: %v", err)
	}
	path := filepath.Join(state.AgentsDir(root), name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write v0 fixture: %v", err)
	}
}

// TestRealRecoverAgents_CrashSurvivorActiveResumes (QUM-625 Q2 AC #1): an agent
// left as disk Status="active" by an UNCLEAN exit (no Shutdown→suspend) — a
// crash survivor — must STILL auto-resume. The liveness filter maps "active"→
// Running, which is in the resume accept-set {Suspended, Running}.
func TestRealRecoverAgents_CrashSurvivorActiveResumes(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "crashy", state.StatusActive, "weave")

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if failed != 0 || len(errs) != 0 {
		t.Fatalf("unexpected failures: failed=%d errs=%v", failed, errs)
	}
	if resumed != 1 || len(starter.specs) != 1 || starter.specs[0].Name != "crashy" {
		t.Fatalf("crash survivor (Status=active) not resumed: resumed=%d specs=%+v", resumed, starter.specs)
	}
}

// TestRealRecoverAgents_CompletedAgentNotResumed (QUM-625 Q2 AC #2): an agent
// that reported complete (LastReportState="complete") must NOT be auto-resumed,
// even though its liveness Status ("active"/"suspended") is otherwise eligible.
// The done-exclusion lives on the OUTCOME axis now, not the Status string.
func TestRealRecoverAgents_CompletedAgentNotResumed(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	wt := makeWorktreeDir(t, tmpDir, "donezo")
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "donezo", Type: "engineer", Family: "engineering", Parent: "weave",
		Branch: "dmotles/donezo", Worktree: wt, Status: state.StatusActive,
		CreatedAt: "2026-05-19T00:00:00Z", SessionID: "sess-donezo", TreePath: "weave/donezo",
		LastReportState: "complete",
	})

	resumed, _, _ := r.RecoverAgents(context.Background())
	if resumed != 0 || len(starter.specs) != 0 {
		t.Fatalf("completed agent (LastReportState=complete) must NOT resume: resumed=%d specs=%+v", resumed, starter.specs)
	}
}

// TestRealRecoverAgents_LegacyV0DoneNotResumedAndIdempotent (QUM-625, weave's
// requested pair): a legacy v0 agent with Status="done" must, after
// migrate-on-load (done→Suspended + LastReportState=complete because SessionID
// is set), (1) NOT be auto-resumed (outcome-axis exclusion), and (2) the
// migration must be idempotent (re-load is a no-op).
func TestRealRecoverAgents_LegacyV0DoneNotResumedAndIdempotent(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
	installStarter(r, starter)

	writeRawV0RecoverAgent(t, tmpDir, "legacydone", state.StatusDone, "weave")

	// (1) not auto-resumed — RecoverAgents loads (migrates in mem) and skips it.
	resumed, _, _ := r.RecoverAgents(context.Background())
	if resumed != 0 || len(starter.specs) != 0 {
		t.Fatalf("legacy v0 done agent must NOT resume: resumed=%d specs=%+v", resumed, starter.specs)
	}

	// Verify the migration mapping + idempotence directly.
	first, err := state.LoadAgent(tmpDir, "legacydone")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if first.Status != state.StatusSuspended || first.LastReportState != "complete" || first.SchemaVersion != state.CurrentSchemaVersion {
		t.Fatalf("v0 done migration: Status=%q LastReportState=%q SchemaVersion=%d; want suspended/complete/%d",
			first.Status, first.LastReportState, first.SchemaVersion, state.CurrentSchemaVersion)
	}
	// Idempotent: a second load yields an identical result (no further drift).
	second, err := state.LoadAgent(tmpDir, "legacydone")
	if err != nil {
		t.Fatalf("LoadAgent (2nd): %v", err)
	}
	if second.Status != first.Status || second.LastReportState != first.LastReportState || second.SchemaVersion != first.SchemaVersion {
		t.Fatalf("migration not idempotent: first={%q,%q,%d} second={%q,%q,%d}",
			first.Status, first.LastReportState, first.SchemaVersion,
			second.Status, second.LastReportState, second.SchemaVersion)
	}
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
