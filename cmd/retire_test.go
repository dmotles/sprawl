package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// retireMockRunner extends killMockRunner for retire tests.
type retireMockRunner = killMockRunner

func newTestRetireDeps(t *testing.T) (*retireDeps, *retireMockRunner, string) {
	t.Helper()
	tmpDir := t.TempDir()
	runner := &retireMockRunner{}

	deps := &retireDeps{
		tmuxRunner: runner,
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		writeFile:  func(path string, data []byte, perm os.FileMode) error { return nil },
		removeFile: func(path string) error { return nil },
		sleepFunc:  func(d time.Duration) {},
		worktreeRemove: func(repoRoot, worktreePath string, force bool) error {
			return nil
		},
		gitStatus: func(worktreePath string) (string, error) {
			return "", nil // clean
		},
		removeAll:       func(path string) error { return nil },
		gitBranchDelete: func(repoRoot, branchName string) error { return nil },
		gitBranchIsMerged: func(repoRoot, branchName string) (bool, error) {
			return false, nil
		},
		gitBranchSafeDelete: func(repoRoot, branchName string) error {
			return nil
		},
		doMerge: func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) {
			return &merge.Result{}, nil
		},
		newMergeDeps: func() *merge.Deps {
			return &merge.Deps{}
		},
		loadAgent: state.LoadAgent,
		currentBranch: func(repoRoot string) (string, error) {
			return "main", nil
		},
		gitUnmergedCommits: func(repoRoot, branchName string) ([]string, error) {
			return nil, nil
		},
		loadConfig: func(sprawlRoot string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		runScript: func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	return deps, runner, tmpDir
}

func TestRetire_InvalidAgentNameReturnsError(t *testing.T) {
	deps, _, _ := newTestRetireDeps(t)
	err := runRetire(deps, "../evil", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error should mention 'invalid agent name', got: %v", err)
	}
}

func TestRetire_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	// Window disappears immediately during graceful poll.
	runner.pidsErr = os.ErrNotExist

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// State file should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_AgentNotFound(t *testing.T) {
	deps, _, _ := newTestRetireDeps(t)

	err := runRetire(deps, "nonexistent", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestRetire_MissingSprawlRoot(t *testing.T) {
	deps, _, _ := newTestRetireDeps(t)
	deps.getenv = func(key string) string { return "" }

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestRetire_DirtyWorktree_Refuses(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.gitStatus = func(worktreePath string) (string, error) {
		return "M some/file.go", nil // dirty
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected error for dirty worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should mention uncommitted changes, got: %v", err)
	}

	// State file should still exist (in "retiring" status for crash safety).
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("state file should still exist: %v", err)
	}
	if agentState.Status != "retiring" {
		t.Errorf("status = %q, want %q", agentState.Status, "retiring")
	}
}

func TestRetire_DirtyWorktree_ForceOverrides(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.gitStatus = func(worktreePath string) (string, error) {
		return "M some/file.go", nil // dirty
	}

	var removedForce bool
	deps.worktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		removedForce = force
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, true, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error with --force: %v", err)
	}

	// Should have force-removed the worktree.
	if !removedForce {
		t.Error("expected force removal of worktree")
	}

	// State should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_WithChildren_Refuses(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	// Create parent.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create children.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "bob",
		Status: "active",
		Parent: "alice",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "charlie",
		Status: "active",
		Parent: "alice",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected error for agent with children")
	}
	if !strings.Contains(err.Error(), "active children") {
		t.Errorf("error should mention active children, got: %v", err)
	}
	if !strings.Contains(err.Error(), "bob") || !strings.Contains(err.Error(), "charlie") {
		t.Errorf("error should list children names, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--cascade") {
		t.Errorf("error should suggest --cascade, got: %v", err)
	}
}

func TestRetire_WithChildren_ForceOrphans(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "bob",
		Status: "active",
		Parent: "alice",
	})

	err := runRetire(deps, "alice", false, true, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error with --force: %v", err)
	}

	// Alice should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected alice state to be deleted")
	}

	// Bob should still exist (orphaned).
	bob, err := state.LoadAgent(tmpDir, "bob")
	if err != nil {
		t.Fatalf("bob should still exist: %v", err)
	}
	if bob.Parent != "alice" {
		t.Errorf("bob parent = %q, want %q", bob.Parent, "alice")
	}
}

func TestRetire_Cascade(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	// Create parent.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree/alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create child.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "sprawl/bob",
		Worktree:    "/some/worktree/bob",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	// Create grandchild.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "charlie",
		Status:      "active",
		Branch:      "sprawl/charlie",
		Worktree:    "/some/worktree/charlie",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"+tmux.BranchSeparator+"bob"),
		TmuxWindow:  "charlie",
		Parent:      "bob",
	})

	err := runRetire(deps, "alice", true, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error with --cascade: %v", err)
	}

	// All agents should be deleted.
	for _, name := range []string{"alice", "bob", "charlie"} {
		_, err := state.LoadAgent(tmpDir, name)
		if err == nil {
			t.Errorf("expected %s state to be deleted", name)
		}
	}
}

func TestRetire_CrashRecovery_RetiringState(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	// Agent is in "retiring" state (simulating crash mid-retire).
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "retiring",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error during crash recovery: %v", err)
	}

	// State should be deleted (recovery completed).
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted after recovery")
	}
}

func TestRetire_EmptyWorktree_SkipsRemoval(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	worktreeRemoveCalled := false
	deps.worktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		worktreeRemoveCalled = true
		return nil
	}

	// Agent with no worktree (like a code merger).
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "", // no worktree
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if worktreeRemoveCalled {
		t.Error("worktree remove should not be called when worktree is empty")
	}
}

func TestRetire_WorktreeRemoveFailure_WarnsButContinues(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)
	deps.worktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		return os.ErrNotExist
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should still have deleted the state.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted despite worktree removal failure")
	}
}

func TestRetire_ForceKillsProcess(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)
	runner.pids = []int{12345}

	// Track writeFile calls: no sentinel should be written with force.
	var writtenPaths []string
	deps.writeFile = func(path string, data []byte, perm os.FileMode) error {
		writtenPaths = append(writtenPaths, path)
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, true, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With force=true, KillWindow should be called immediately (no sentinel).
	if !runner.killWindowCalled {
		t.Error("expected KillWindow to be called with --force")
	}

	// No sentinel should be written with force.
	if len(writtenPaths) > 0 {
		t.Errorf("expected no sentinel writes with force, got: %v", writtenPaths)
	}

	// State should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_CascadeDirtyChild_Aborts(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	statusCalls := 0
	deps.gitStatus = func(worktreePath string) (string, error) {
		statusCalls++
		if strings.Contains(worktreePath, "bob") {
			return "M dirty-file.go", nil
		}
		return "", nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    "/worktree/alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "sprawl/bob",
		Worktree:    "/worktree/bob",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	err := runRetire(deps, "alice", true, false, false, false, false)
	if err == nil {
		t.Fatal("expected error for dirty child worktree in cascade")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should mention uncommitted changes, got: %v", err)
	}
}

func TestRetire_Subagent_SkipsWorktreeCleanup(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	worktreeRemoveCalled := false
	deps.worktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		worktreeRemoveCalled = true
		return nil
	}

	// Create a subagent with a non-empty Worktree.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "sub-alice",
		Status:      "active",
		Branch:      "sprawl/sub-alice",
		Worktree:    "/some/worktree/sub-alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "sub-alice",
		Parent:      "alice",
		Subagent:    true,
	})

	err := runRetire(deps, "sub-alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Subagent worktree should NOT be removed (it belongs to the parent).
	if worktreeRemoveCalled {
		t.Error("worktreeRemove should NOT be called for subagent, even with non-empty worktree")
	}

	// State file should be deleted.
	_, err = state.LoadAgent(tmpDir, "sub-alice")
	if err == nil {
		t.Error("expected subagent state to be deleted")
	}
}

func TestRetire_CleansUpLogsDirectory(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create a logs directory with a fake log file.
	logsDir := filepath.Join(tmpDir, ".sprawl", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("failed to create logs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "agent.log"), []byte("log data"), 0o644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	// Track what path removeAll is called with.
	var removedPath string
	deps.removeAll = func(path string) error {
		removedPath = path
		return os.RemoveAll(path)
	}

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify removeAll was called with the correct logs directory.
	if removedPath != logsDir {
		t.Errorf("removeAll called with %q, want %q", removedPath, logsDir)
	}

	// Verify the logs directory was actually removed.
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Errorf("logs directory should have been removed, but still exists")
	}
}

func TestRetire_Abandon_DeletesBranch(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	var deletedBranch string
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		deletedBranch = branchName
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if deletedBranch != "sprawl/alice" {
		t.Errorf("gitBranchDelete called with branch %q, want %q", deletedBranch, "sprawl/alice")
	}

	// State file should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_Abandon_BranchDeleteFails_WarnsButSucceeds(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		return fmt.Errorf("branch not found")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, false)
	if err != nil {
		t.Fatalf("expected no error when branch deletion fails, got: %v", err)
	}

	// State file should still be deleted (branch failure is non-fatal).
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_NoAbandon_PreservesBranch(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	branchDeleteCalled := false
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		branchDeleteCalled = true
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if branchDeleteCalled {
		t.Error("gitBranchDelete should NOT be called without --abandon")
	}
}

func TestRealGitBranchDelete(t *testing.T) {
	// Create a temporary git repo with a branch to delete
	repoDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s: %v", args, out, err)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")

	// Create an initial commit so we can create branches
	run("commit", "--allow-empty", "-m", "initial")
	run("branch", "feature-to-delete")

	// Verify the branch exists
	cmd := exec.Command("git", "branch", "--list", "feature-to-delete")
	cmd.Dir = repoDir
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "feature-to-delete") {
		t.Fatal("expected branch feature-to-delete to exist before deletion")
	}

	// Delete it using our function
	err := realGitBranchDelete(repoDir, "feature-to-delete")
	if err != nil {
		t.Fatalf("realGitBranchDelete returned error: %v", err)
	}

	// Verify the branch is gone
	cmd = exec.Command("git", "branch", "--list", "feature-to-delete")
	cmd.Dir = repoDir
	out, _ = cmd.Output()
	if strings.Contains(string(out), "feature-to-delete") {
		t.Error("expected branch feature-to-delete to be deleted")
	}
}

func TestRealGitBranchDelete_NonexistentBranch(t *testing.T) {
	repoDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %s: %v", args, out, err)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "initial")

	err := realGitBranchDelete(repoDir, "nonexistent-branch")
	if err == nil {
		t.Fatal("expected error when deleting nonexistent branch")
	}
	if !strings.Contains(err.Error(), "git branch -D") {
		t.Errorf("error should mention 'git branch -D', got: %v", err)
	}
}

// --- New tests for QUM-129 retire overhaul ---

func TestRetire_Default_MergedBranch_DeletesBranch(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitBranchIsMerged = func(repoRoot, branchName string) (bool, error) {
		return true, nil
	}

	var safeDeletedBranch string
	deps.gitBranchSafeDelete = func(repoRoot, branchName string) error {
		safeDeletedBranch = branchName
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if safeDeletedBranch != "sprawl/alice" {
		t.Errorf("gitBranchSafeDelete called with branch %q, want %q", safeDeletedBranch, "sprawl/alice")
	}
}

func TestRetire_Default_UnmergedBranch_PreservesBranch(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitBranchIsMerged = func(repoRoot, branchName string) (bool, error) {
		return false, nil
	}

	safeDeleteCalled := false
	deps.gitBranchSafeDelete = func(repoRoot, branchName string) error {
		safeDeleteCalled = true
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if safeDeleteCalled {
		t.Error("gitBranchSafeDelete should NOT be called when branch is not merged")
	}

	// Agent should still be retired (state deleted).
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_MergeFlag_HappyPath(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.currentBranch = func(repoRoot string) (string, error) {
		return "main", nil
	}

	var doMergeCalled bool
	deps.doMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		doMergeCalled = true
		return &merge.Result{CommitHash: "abc123"}, nil
	}

	var safeDeletedBranch string
	deps.gitBranchSafeDelete = func(repoRoot, branchName string) error {
		safeDeletedBranch = branchName
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !doMergeCalled {
		t.Error("expected doMerge to be called with mergeFirst=true")
	}

	// Agent state should be deleted (retired).
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}

	if safeDeletedBranch != "sprawl/alice" {
		t.Errorf("gitBranchSafeDelete called with branch %q, want %q", safeDeletedBranch, "sprawl/alice")
	}
}

func TestRetire_MergeAndAbandon_MutualExclusion(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, true, false)
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should mention 'mutually exclusive', got: %v", err)
	}
}

func TestRetire_MergeFlag_MergeFails_AbortsRetire(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.doMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return nil, fmt.Errorf("merge conflict: cannot rebase")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, true, false)
	if err == nil {
		t.Fatal("expected error when merge fails")
	}
	if !strings.Contains(err.Error(), "merge") {
		t.Errorf("error should mention merge failure, got: %v", err)
	}

	// Agent state should still exist (retire did NOT proceed).
	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("agent state should still exist after failed merge: %v", err)
	}
	if agentState.Status != "active" {
		t.Errorf("agent status = %q, want %q (should not have changed)", agentState.Status, "active")
	}
}

func TestRetire_ForcePlusMerge(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.currentBranch = func(repoRoot string) (string, error) {
		return "main", nil
	}

	var doMergeCalled bool
	deps.doMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		doMergeCalled = true
		return &merge.Result{CommitHash: "def456"}, nil
	}

	var safeDeletedBranch string
	deps.gitBranchSafeDelete = func(repoRoot, branchName string) error {
		safeDeletedBranch = branchName
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, true, false, true, false)
	if err != nil {
		t.Fatalf("unexpected error with force+merge: %v", err)
	}

	if !doMergeCalled {
		t.Error("expected doMerge to be called with force+merge")
	}

	// Agent state should be deleted.
	_, err = state.LoadAgent(tmpDir, "alice")
	if err == nil {
		t.Error("expected agent state to be deleted")
	}

	if safeDeletedBranch != "sprawl/alice" {
		t.Errorf("gitBranchSafeDelete called with branch %q, want %q", safeDeletedBranch, "sprawl/alice")
	}
}

func TestRetire_CascadePlusMerge_ChildrenNotMerged(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.currentBranch = func(repoRoot string) (string, error) {
		return "main", nil
	}

	var mergedAgents []string
	deps.doMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		mergedAgents = append(mergedAgents, cfg.AgentName)
		return &merge.Result{CommitHash: "abc123"}, nil
	}

	// Create parent alice with Parent: "root".
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create child bob with Parent: "alice".
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "sprawl/bob",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "bob"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	err := runRetire(deps, "alice", true, false, false, true, false)
	if err != nil {
		t.Fatalf("unexpected error with cascade+merge: %v", err)
	}

	// doMerge should be called exactly once, for alice only (not bob).
	if len(mergedAgents) != 1 {
		t.Fatalf("expected doMerge to be called 1 time, got %d: %v", len(mergedAgents), mergedAgents)
	}
	if mergedAgents[0] != "alice" {
		t.Errorf("doMerge called for agent %q, want %q", mergedAgents[0], "alice")
	}

	// Both agents should be retired.
	for _, name := range []string{"alice", "bob"} {
		_, err := state.LoadAgent(tmpDir, name)
		if err == nil {
			t.Errorf("expected %s state to be deleted", name)
		}
	}
}

func TestRetire_CascadePlusAbandon_PropagatesAbandon(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	var deletedBranches []string
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		deletedBranches = append(deletedBranches, branchName)
		return nil
	}

	// Create parent alice.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create child bob.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "sprawl/bob",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "bob"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	err := runRetire(deps, "alice", true, false, true, false, false)
	if err != nil {
		t.Fatalf("unexpected error with cascade+abandon: %v", err)
	}

	// gitBranchDelete should be called for BOTH alice and bob branches.
	if len(deletedBranches) != 2 {
		t.Fatalf("expected gitBranchDelete to be called 2 times, got %d: %v", len(deletedBranches), deletedBranches)
	}

	branchSet := make(map[string]bool)
	for _, b := range deletedBranches {
		branchSet[b] = true
	}
	if !branchSet["sprawl/alice"] {
		t.Error("expected gitBranchDelete to be called for sprawl/alice")
	}
	if !branchSet["sprawl/bob"] {
		t.Error("expected gitBranchDelete to be called for sprawl/bob")
	}
}

func TestRetire_CleansUpLockAndPokeFiles(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	var removedFiles []string
	deps.removeFile = func(path string) error {
		removedFiles = append(removedFiles, path)
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedLockPath := filepath.Join(tmpDir, ".sprawl", "locks", "alice.lock")
	expectedPokePath := filepath.Join(tmpDir, ".sprawl", "agents", "alice.poke")

	var foundLock, foundPoke bool
	for _, path := range removedFiles {
		if path == expectedLockPath {
			foundLock = true
		}
		if path == expectedPokePath {
			foundPoke = true
		}
	}

	if !foundLock {
		t.Errorf("removeFile not called with lock path %q; called with: %v", expectedLockPath, removedFiles)
	}
	if !foundPoke {
		t.Errorf("removeFile not called with poke path %q; called with: %v", expectedPokePath, removedFiles)
	}
}

// --- Tests for QUM-159: abandon safety guards ---

func TestRetire_Abandon_UnmergedCommits_BlocksWithoutYes(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitUnmergedCommits = func(repoRoot, branchName string) ([]string, error) {
		return []string{"abc1234 Add initial implementation", "def5678 Fix edge case"}, nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, false) // abandon=true, yes=false
	if err == nil {
		t.Fatal("expected error for unmerged commits without --yes")
	}
	if !strings.Contains(err.Error(), "abandon blocked") {
		t.Errorf("error should mention 'abandon blocked', got: %v", err)
	}
	if !strings.Contains(err.Error(), "unmerged commits") {
		t.Errorf("error should mention 'unmerged commits', got: %v", err)
	}

	// Agent state should still exist (not retired).
	agentState, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr != nil {
		t.Fatalf("agent state should still exist: %v", loadErr)
	}
	if agentState.Status != "active" {
		t.Errorf("status = %q, want %q", agentState.Status, "active")
	}
}

func TestRetire_Abandon_UnmergedCommits_ProceedsWithYes(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitUnmergedCommits = func(repoRoot, branchName string) ([]string, error) {
		return []string{"abc1234 Add initial implementation"}, nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, true) // abandon=true, yes=true
	if err != nil {
		t.Fatalf("unexpected error with --yes: %v", err)
	}

	// Agent should be retired.
	_, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_Abandon_LiveProcess_BlocksWithoutYes(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	runner.pids = []int{12345} // live process

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, false) // abandon=true, yes=false
	if err == nil {
		t.Fatal("expected error for live process without --yes")
	}
	if !strings.Contains(err.Error(), "abandon blocked") {
		t.Errorf("error should mention 'abandon blocked', got: %v", err)
	}
	if !strings.Contains(err.Error(), "live process") {
		t.Errorf("error should mention 'live process', got: %v", err)
	}
}

func TestRetire_Abandon_LiveProcess_ProceedsWithYes(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	runner.pids = []int{12345}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, true) // abandon=true, yes=true
	if err != nil {
		t.Fatalf("unexpected error with --yes: %v", err)
	}

	_, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_Abandon_BothGuards_BlocksWithoutYes(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	deps.gitUnmergedCommits = func(repoRoot, branchName string) ([]string, error) {
		return []string{"abc1234 Some commit"}, nil
	}
	runner.pids = []int{12345}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, false)
	if err == nil {
		t.Fatal("expected error for both guards without --yes")
	}
	if !strings.Contains(err.Error(), "unmerged commits") {
		t.Errorf("error should mention 'unmerged commits', got: %v", err)
	}
	if !strings.Contains(err.Error(), "live process") {
		t.Errorf("error should mention 'live process', got: %v", err)
	}
}

func TestRetire_Abandon_NoGuards_ProceedsWithoutYes(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	// No unmerged commits (default mock returns nil).
	// No live process.
	runner.pidsErr = os.ErrNotExist

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, true, false, false) // abandon=true, yes=false
	if err != nil {
		t.Fatalf("unexpected error: --yes should not be required when no guards fire: %v", err)
	}

	_, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr == nil {
		t.Error("expected agent state to be deleted")
	}
}

func TestRetire_Abandon_Subagent_SkipsGuards(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitUnmergedCommits = func(repoRoot, branchName string) ([]string, error) {
		return []string{"abc1234 Some commit"}, nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "sub-alice",
		Status:      "active",
		Branch:      "sprawl/sub-alice",
		Worktree:    "/some/worktree/sub-alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "sub-alice",
		Parent:      "alice",
		Subagent:    true,
	})

	// Subagents should skip guards even with unmerged commits.
	err := runRetire(deps, "sub-alice", false, false, true, false, false) // abandon=true, yes=false
	if err != nil {
		t.Fatalf("unexpected error: subagent should skip guards: %v", err)
	}

	_, loadErr := state.LoadAgent(tmpDir, "sub-alice")
	if loadErr == nil {
		t.Error("expected subagent state to be deleted")
	}
}

func TestRetire_Abandon_Cascade_PropagatesYes(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.gitUnmergedCommits = func(repoRoot, branchName string) ([]string, error) {
		return []string{"abc1234 Some commit"}, nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "sprawl/bob",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "bob"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	// cascade=true, abandon=true, yes=true — should propagate yes to children.
	err := runRetire(deps, "alice", true, false, true, false, true)
	if err != nil {
		t.Fatalf("unexpected error with cascade+abandon+yes: %v", err)
	}

	for _, name := range []string{"alice", "bob"} {
		_, loadErr := state.LoadAgent(tmpDir, name)
		if loadErr == nil {
			t.Errorf("expected %s state to be deleted", name)
		}
	}
}

func TestRetire_MergeFlag_PassesValidateCmdFromConfig(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.currentBranch = func(repoRoot string) (string, error) {
		return "main", nil
	}

	deps.loadConfig = func(sprawlRoot string) (*config.Config, error) {
		return &config.Config{Validate: "make validate"}, nil
	}

	var capturedCfg *merge.Config
	deps.doMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{CommitHash: "abc123"}, nil
	}

	deps.gitBranchSafeDelete = func(repoRoot, branchName string) error {
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if capturedCfg.ValidateCmd != "make validate" {
		t.Errorf("ValidateCmd = %q, want %q", capturedCfg.ValidateCmd, "make validate")
	}
}

func TestRetire_MergeFlag_NoValidate_SkipsValidation(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.currentBranch = func(repoRoot string) (string, error) {
		return "main", nil
	}

	deps.loadConfig = func(sprawlRoot string) (*config.Config, error) {
		return &config.Config{Validate: "make validate"}, nil
	}

	var capturedCfg *merge.Config
	deps.doMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{CommitHash: "abc123"}, nil
	}

	deps.gitBranchSafeDelete = func(repoRoot, branchName string) error {
		return nil
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	retireNoValidate = true
	defer func() { retireNoValidate = false }()

	err := runRetire(deps, "alice", false, false, false, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if !capturedCfg.NoValidate {
		t.Error("NoValidate should be true when --no-validate is passed")
	}
}

func TestRetire_MergeFlag_ConfigLoadError(t *testing.T) {
	deps, _, tmpDir := newTestRetireDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "root"
		}
		return ""
	}

	deps.currentBranch = func(repoRoot string) (string, error) {
		return "main", nil
	}

	deps.loadConfig = func(sprawlRoot string) (*config.Config, error) {
		return nil, fmt.Errorf("permission denied reading config.yaml")
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false, false, true, false)
	if err == nil {
		t.Fatal("expected error when config load fails")
	}
	if !strings.Contains(err.Error(), "loading config") {
		t.Errorf("error should mention config loading, got: %v", err)
	}
}

func TestRetire_TeardownScript_Runs(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)
	runner.pidsErr = os.ErrNotExist

	worktreePath := filepath.Join(tmpDir, ".sprawl", "worktrees", "alice")
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    worktreePath,
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	teardownScript := "rm -rf node_modules"
	cfg := &config.Config{}
	cfg.Set("worktree.teardown", teardownScript)
	deps.loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}

	var scriptCalled bool
	var gotScript, gotWorkDir string
	var gotEnv map[string]string
	deps.runScript = func(script, workDir string, env map[string]string) ([]byte, error) {
		scriptCalled = true
		gotScript = script
		gotWorkDir = workDir
		gotEnv = env
		return []byte("ok"), nil
	}

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !scriptCalled {
		t.Fatal("expected runScript to be called when worktree.teardown is configured")
	}
	if gotScript != teardownScript {
		t.Errorf("script = %q, want %q", gotScript, teardownScript)
	}
	if gotWorkDir != worktreePath {
		t.Errorf("workDir = %q, want %q", gotWorkDir, worktreePath)
	}
	if gotEnv == nil {
		t.Fatal("expected env to be non-nil")
	}
	if gotEnv["SPRAWL_AGENT_IDENTITY"] != "alice" {
		t.Errorf("env SPRAWL_AGENT_IDENTITY = %q, want %q", gotEnv["SPRAWL_AGENT_IDENTITY"], "alice")
	}
	if gotEnv["SPRAWL_ROOT"] != tmpDir {
		t.Errorf("env SPRAWL_ROOT = %q, want %q", gotEnv["SPRAWL_ROOT"], tmpDir)
	}
}

func TestRetire_TeardownScript_Failure_ContinuesRetirement(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)
	runner.pidsErr = os.ErrNotExist

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	cfg := &config.Config{}
	cfg.Set("worktree.teardown", "exit 1")
	deps.loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	deps.runScript = func(string, string, map[string]string) ([]byte, error) {
		return []byte("ERR"), errors.New("teardown failed")
	}

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("retirement should succeed even when teardown script fails, got: %v", err)
	}

	// Agent state should be deleted (retirement completed)
	_, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr == nil {
		t.Error("agent state should be deleted after successful retirement")
	}
}

func TestRetire_TeardownScript_NotConfigured_Skipped(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)
	runner.pidsErr = os.ErrNotExist

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "sprawl/alice",
		Worktree:    filepath.Join(tmpDir, ".sprawl", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	var scriptCalled bool
	deps.runScript = func(string, string, map[string]string) ([]byte, error) {
		scriptCalled = true
		return nil, nil
	}

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if scriptCalled {
		t.Error("runScript should NOT be called when worktree.teardown is not configured")
	}
}

func TestRetire_TeardownScript_SubagentSkipped(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)
	runner.pidsErr = os.ErrNotExist

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "",
		Worktree:    "",
		Subagent:    true,
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	cfg := &config.Config{}
	cfg.Set("worktree.teardown", "cleanup")
	deps.loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}

	var scriptCalled bool
	deps.runScript = func(string, string, map[string]string) ([]byte, error) {
		scriptCalled = true
		return nil, nil
	}

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if scriptCalled {
		t.Error("runScript should NOT be called for subagents")
	}
}
