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
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRealRecoverAgents_SettlePassFlipsZombieStatus (QUM-668) — at the top of
// RecoverAgents, before the resume filter loop, a "settle pass" reconciles
// the on-disk Status with the persisted outcome. Any agent whose
// LastReportState is terminal (complete/failure) but whose Status is still
// "active" (e.g. an earlier session crashed before Report's atomic flip ran,
// or migrated from before QUM-668) must have its Status flipped to the
// matching terminal liveness and persisted. Agents whose Status is not
// "active" are left untouched.
func TestRealRecoverAgents_SettlePassFlipsZombieStatus(t *testing.T) {
	cases := []struct {
		name            string
		initialStatus   string
		lastReportState string
		wantStatus      string
		wantResumed     bool
	}{
		{
			name:            "complete+active settles to stopped, skipped from resume",
			initialStatus:   state.StatusActive,
			lastReportState: "complete",
			wantStatus:      state.StatusStopped,
			wantResumed:     false,
		},
		{
			name:            "failure+active settles to faulted, skipped from resume",
			initialStatus:   state.StatusActive,
			lastReportState: "failure",
			wantStatus:      state.StatusFaulted,
			wantResumed:     false,
		},
		{
			name:            "working+active is a crash survivor — Status unchanged, resumed",
			initialStatus:   state.StatusActive,
			lastReportState: "working",
			wantStatus:      state.StatusActive,
			wantResumed:     true,
		},
		{
			name:            "complete+suspended: settle pass does not touch Status (already non-active)",
			initialStatus:   state.StatusSuspended,
			lastReportState: "complete",
			wantStatus:      state.StatusSuspended,
			wantResumed:     false,
		},
		{
			name:            "failure+suspended: settle pass does not touch Status (already non-active)",
			initialStatus:   state.StatusSuspended,
			lastReportState: "failure",
			wantStatus:      state.StatusSuspended,
			wantResumed:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, tmpDir := newFakeReal(t)
			starter := &recoverTestStarter{session: recoverTestSession("sess-shared")}
			installStarter(r, starter)

			wt := makeWorktreeDir(t, tmpDir, "alice")
			saveTestAgent(t, tmpDir, &state.AgentState{
				Name: "alice", Type: "engineer", Family: "engineering", Parent: "weave",
				Branch: "dmotles/alice", Worktree: wt, Status: tc.initialStatus,
				CreatedAt: "2026-05-19T00:00:00Z", SessionID: "sess-alice", TreePath: "weave/alice",
				LastReportState: tc.lastReportState,
			})

			resumed, _, _ := r.RecoverAgents(context.Background())

			gotResumed := resumed == 1
			if gotResumed != tc.wantResumed {
				t.Errorf("resumed=%d want resumed=%v", resumed, tc.wantResumed)
			}

			loaded, err := state.LoadAgent(tmpDir, "alice")
			if err != nil {
				t.Fatalf("LoadAgent: %v", err)
			}
			if loaded.Status != tc.wantStatus {
				t.Errorf("post-settle Status = %q, want %q", loaded.Status, tc.wantStatus)
			}

			// Resume-skip evidence: when a terminal-outcome settle path runs
			// from Status=active, the agent must NOT appear in starter.specs.
			// (Compare to working+active which DOES get resumed.)
			if !tc.wantResumed {
				for _, sp := range starter.specs {
					if sp.Name == "alice" {
						t.Errorf("starter.specs contains alice for non-resume case; settle pass should have excluded it before resume scan")
					}
				}
			}
		})
	}
}

// TestRealRecoverAgents_QuarantinesOrphanDir (QUM-668) — any directory under
// .sprawl/agents/<name>/ without a matching <name>.json sibling is an orphan.
// RecoverAgents quarantines orphans by moving them into
// .sprawl/agents/_orphaned/<UTC-timestamp>/<name>/, preserving the contained
// files. Legit agents (with sibling JSON) are untouched.
func TestRealRecoverAgents_QuarantinesOrphanDir(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	// Legit agent: alice has sibling JSON via saveRecoverAgent.
	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")
	aliceDir := filepath.Join(state.AgentsDir(tmpDir), "alice")
	if err := os.MkdirAll(aliceDir, 0o755); err != nil {
		t.Fatalf("mkdir alice dir: %v", err)
	}

	// Orphan: ghost has a dir + file but no ghost.json sibling.
	ghostDir := filepath.Join(state.AgentsDir(tmpDir), "ghost")
	ghostFindings := filepath.Join(ghostDir, "findings")
	if err := os.MkdirAll(ghostFindings, 0o755); err != nil {
		t.Fatalf("mkdir ghost: %v", err)
	}
	notePath := filepath.Join(ghostFindings, "note.txt")
	if err := os.WriteFile(notePath, []byte("ghost note"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	if _, _, errs := r.RecoverAgents(context.Background()); len(errs) > 0 {
		t.Fatalf("RecoverAgents errs: %v", errs)
	}

	// Ghost dir must be gone from its original location.
	if _, err := os.Stat(ghostDir); !os.IsNotExist(err) {
		t.Errorf("ghost dir still exists at %q (err=%v); want quarantined", ghostDir, err)
	}

	// The note must have been preserved under _orphaned/<ts>/ghost/findings/note.txt.
	matches, err := filepath.Glob(filepath.Join(state.AgentsDir(tmpDir), "_orphaned", "*", "ghost", "findings", "note.txt"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("quarantined note matches = %v, want exactly 1", matches)
	} else {
		body, rerr := os.ReadFile(matches[0])
		if rerr != nil {
			t.Errorf("read quarantined note: %v", rerr)
		} else if string(body) != "ghost note" {
			t.Errorf("quarantined note body = %q, want %q", string(body), "ghost note")
		}

		// Pin the timestamp directory format: must be UTC compact form
		// "20060102T150405Z" (time.Now().UTC().Format(...)). The timestamp
		// segment is the parent of "ghost" in the matched path:
		//   <agents>/_orphaned/<ts>/ghost/findings/note.txt
		// Climb up 3 levels from note.txt → findings → ghost → <ts>.
		tsDir := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(matches[0]))))
		if _, perr := time.Parse("20060102T150405Z", tsDir); perr != nil {
			t.Errorf("quarantine timestamp dir %q does not parse as UTC \"20060102T150405Z\": %v", tsDir, perr)
		}
	}

	// Legit alice dir must be untouched.
	if _, err := os.Stat(aliceDir); err != nil {
		t.Errorf("alice dir vanished: %v", err)
	}
}

// TestRealRecoverAgents_QuarantinesMultipleOrphans (QUM-668) — when there are
// multiple orphan dirs in a single RecoverAgents call, all of them must be
// quarantined, AND they must all land under the same single <ts> parent
// directory (proving the timestamp is captured once per RecoverAgents call,
// not re-sampled per orphan).
func TestRealRecoverAgents_QuarantinesMultipleOrphans(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	orphanNames := []string{"ghost", "phantom", "wraith"}
	for _, name := range orphanNames {
		dir := filepath.Join(state.AgentsDir(tmpDir), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir orphan %q: %v", name, err)
		}
		marker := filepath.Join(dir, "marker.txt")
		if err := os.WriteFile(marker, []byte(name), 0o644); err != nil {
			t.Fatalf("write marker %q: %v", name, err)
		}
	}

	if _, _, errs := r.RecoverAgents(context.Background()); len(errs) > 0 {
		t.Fatalf("RecoverAgents errs: %v", errs)
	}

	// Collect the <ts> parent dirs across all orphans; they must all be equal.
	tsDirs := make(map[string]struct{})
	for _, name := range orphanNames {
		// Original dir must be gone.
		if _, err := os.Stat(filepath.Join(state.AgentsDir(tmpDir), name)); !os.IsNotExist(err) {
			t.Errorf("orphan %q still at original location (err=%v)", name, err)
		}

		matches, err := filepath.Glob(filepath.Join(state.AgentsDir(tmpDir), "_orphaned", "*", name, "marker.txt"))
		if err != nil {
			t.Fatalf("glob %q: %v", name, err)
		}
		if len(matches) != 1 {
			t.Errorf("quarantined %q matches = %v, want exactly 1", name, matches)
			continue
		}
		// <agents>/_orphaned/<ts>/<name>/marker.txt → climb 2 dirs to get <ts>.
		tsDir := filepath.Dir(filepath.Dir(matches[0]))
		tsDirs[tsDir] = struct{}{}
	}

	if len(tsDirs) != 1 {
		t.Errorf("orphans were spread across %d timestamp dirs; want 1 single timestamp per RecoverAgents call. tsDirs=%v", len(tsDirs), tsDirs)
	}
}

// TestRealRecoverAgents_NoOrphansNoQuarantineDir (QUM-668) — if there are no
// orphans, RecoverAgents must NOT create an empty _orphaned/ directory. The
// quarantine path is opt-in by discovery.
func TestRealRecoverAgents_NoOrphansNoQuarantineDir(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	saveRecoverAgent(t, tmpDir, "alice", state.StatusSuspended, "weave")

	if _, _, errs := r.RecoverAgents(context.Background()); len(errs) > 0 {
		t.Fatalf("RecoverAgents errs: %v", errs)
	}

	quarantineDir := filepath.Join(state.AgentsDir(tmpDir), "_orphaned")
	if _, err := os.Stat(quarantineDir); !os.IsNotExist(err) {
		t.Errorf("_orphaned dir exists (err=%v); want absent when there are no orphans", err)
	}
}

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
