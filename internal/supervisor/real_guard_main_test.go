// QUM-837 — defense-in-depth wiring: a non-root agent must never resume or be
// woken while its worktree HEAD is on the shared 'main' branch, and a stale
// advertised AgentState.Branch is self-healed from the worktree's real HEAD on
// resume/wake (warn-only, never a hard error).
package supervisor

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
)

// gitInitWorktreeOnBranch creates a real git repo at <root>/wt/<name> checked
// out on the given branch (with one commit so HEAD resolves) and returns its
// path. Used so the QUM-837 git-backed assertions run against a genuine repo.
func gitInitWorktreeOnBranch(t *testing.T, root, name, branch string) string {
	t.Helper()
	dir := filepath.Join(root, "wt", name)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	if err := exec.Command("mkdir", "-p", dir).Run(); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	run("init")
	run("symbolic-ref", "HEAD", "refs/heads/"+branch)
	run("commit", "--allow-empty", "-m", "initial")
	return dir
}

// TestRealRecoverAgents_SkipsWorktreeOnMain — a non-root agent whose worktree
// real HEAD is on 'main' must NOT be resumed: it is recorded as a failure and
// the starter is never invoked for it (QUM-837 defense-in-depth).
func TestRealRecoverAgents_SkipsWorktreeOnMain(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-x")}
	installStarter(r, starter)

	wt := gitInitWorktreeOnBranch(t, tmpDir, "drifter", "main")
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "drifter", Type: "engineer", Family: "engineering", Parent: "weave",
		Branch: "dmotles/drifter", Worktree: wt, Status: state.StatusSuspended,
		CreatedAt: "2026-05-19T00:00:00Z", SessionID: "sess-drifter", TreePath: "weave/drifter",
	})

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 0 {
		t.Errorf("resumed = %d, want 0 (worktree on main must not resume)", resumed)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "main") {
		t.Errorf("errs = %v, want one error mentioning 'main'", errs)
	}
	for _, sp := range starter.specs {
		if sp.Name == "drifter" {
			t.Errorf("starter was invoked for an agent on main; want it skipped")
		}
	}
}

// TestRealRecoverAgents_RefreshesStaleBranch — when the worktree's real branch
// differs from the persisted AgentState.Branch, RecoverAgents self-heals the
// advertised value (warn-only) and the resume still succeeds. The git seam is
// injected so the worktree dir need not be a real repo for this axis.
func TestRealRecoverAgents_RefreshesStaleBranch(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-alice")}
	installStarter(r, starter)
	r.gitCurrentBranch = func(string) (string, error) { return "dmotles/qum-839-real", nil }

	wt := makeWorktreeDir(t, tmpDir, "alice")
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering", Parent: "weave",
		Branch: "dmotles/qum-826-stale", Worktree: wt, Status: state.StatusSuspended,
		CreatedAt: "2026-05-19T00:00:00Z", SessionID: "sess-alice", TreePath: "weave/alice",
	})

	resumed, failed, errs := r.RecoverAgents(context.Background())
	if resumed != 1 || failed != 0 || len(errs) != 0 {
		t.Fatalf("RecoverAgents = (%d,%d,%v), want (1,0,nil)", resumed, failed, errs)
	}

	loaded, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Branch != "dmotles/qum-839-real" {
		t.Errorf("Branch = %q, want %q (stale advertised branch must be refreshed from real HEAD)", loaded.Branch, "dmotles/qum-839-real")
	}
}

// TestRealRecoverAgents_NoRefreshWhenBranchMatches — when the resolved branch
// matches the persisted one, no spurious rewrite occurs (the warn/refresh is a
// no-op on agreement).
func TestRealRecoverAgents_NoRefreshWhenBranchMatches(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	starter := &recoverTestStarter{session: recoverTestSession("sess-alice")}
	installStarter(r, starter)
	calls := 0
	r.gitCurrentBranch = func(string) (string, error) { calls++; return "dmotles/alice", nil }

	wt := makeWorktreeDir(t, tmpDir, "alice")
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name: "alice", Type: "engineer", Family: "engineering", Parent: "weave",
		Branch: "dmotles/alice", Worktree: wt, Status: state.StatusSuspended,
		CreatedAt: "2026-05-19T00:00:00Z", SessionID: "sess-alice", TreePath: "weave/alice",
	})

	if _, _, errs := r.RecoverAgents(context.Background()); len(errs) != 0 {
		t.Fatalf("RecoverAgents errs: %v", errs)
	}
	if calls == 0 {
		t.Errorf("gitCurrentBranch was not consulted; want it called for the refresh check")
	}
	loaded, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Branch != "dmotles/alice" {
		t.Errorf("Branch = %q, want unchanged dmotles/alice", loaded.Branch)
	}
}

// TestWake_RefusesWorktreeOnMain — a direct wake of a non-root agent whose
// worktree real HEAD is on 'main' must return an error and not start a runtime
// (QUM-837 defense-in-depth, symmetric with RecoverAgents).
func TestWake_RefusesWorktreeOnMain(t *testing.T) {
	r, tmpDir := newFakeReal(t)
	shortenWakeTimeouts(t)
	starter := &wakeCapturingStarter{}
	installStarter(r, starter)

	wt := gitInitWorktreeOnBranch(t, tmpDir, "drifter", "main")
	agentState := testAgentState("drifter")
	agentState.Worktree = wt
	agentState.Status = state.StatusComplete
	saveTestAgent(t, tmpDir, agentState)

	_, err := r.Wake(context.Background(), "drifter", agentpkg.WakeReasonBare, "")
	if err == nil {
		t.Fatalf("Wake on an agent whose worktree is on main returned nil; want a 'main' rejection")
	}
	if !strings.Contains(err.Error(), "main") {
		t.Errorf("error %q does not mention 'main'", err.Error())
	}
	if len(starter.snapshotSpecs()) != 0 {
		t.Errorf("starter received specs; want zero (must not start a runtime on main)")
	}
}
