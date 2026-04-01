package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
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
			if key == "DENDRA_ROOT" {
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
		removeAll: func(path string) error { return nil },
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0755)

	return deps, runner, tmpDir
}

func TestRetire_HappyPath(t *testing.T) {
	deps, runner, tmpDir := newTestRetireDeps(t)

	// Window disappears immediately during graceful poll.
	runner.pidsErr = os.ErrNotExist

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "alice",
		Status:      "active",
		Branch:      "dendra/alice",
		Worktree:    filepath.Join(tmpDir, ".dendra", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false)
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

	err := runRetire(deps, "nonexistent", false, false)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

func TestRetire_MissingDendraRoot(t *testing.T) {
	deps, _, _ := newTestRetireDeps(t)
	deps.getenv = func(key string) string { return "" }

	err := runRetire(deps, "alice", false, false)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
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
		Branch:      "dendra/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false)
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
		Branch:      "dendra/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, true)
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
		Branch:      "dendra/alice",
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

	err := runRetire(deps, "alice", false, false)
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
		Branch:      "dendra/alice",
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

	err := runRetire(deps, "alice", false, true)
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
		Branch:      "dendra/alice",
		Worktree:    "/some/worktree/alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create child.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "dendra/bob",
		Worktree:    "/some/worktree/bob",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	// Create grandchild.
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "charlie",
		Status:      "active",
		Branch:      "dendra/charlie",
		Worktree:    "/some/worktree/charlie",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"+tmux.BranchSeparator+"bob"),
		TmuxWindow:  "charlie",
		Parent:      "bob",
	})

	err := runRetire(deps, "alice", true, false)
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
		Branch:      "dendra/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false)
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
		Branch:      "dendra/alice",
		Worktree:    "", // no worktree
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false)
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
		Branch:      "dendra/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, false)
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
		Branch:      "dendra/alice",
		Worktree:    "/some/worktree",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	err := runRetire(deps, "alice", false, true)
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
		Branch:      "dendra/alice",
		Worktree:    "/worktree/alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	createTestAgent(t, tmpDir, &state.AgentState{
		Name:        "bob",
		Status:      "active",
		Branch:      "dendra/bob",
		Worktree:    "/worktree/bob",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName+tmux.BranchSeparator+"alice"),
		TmuxWindow:  "bob",
		Parent:      "alice",
	})

	err := runRetire(deps, "alice", true, false)
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
		Branch:      "dendra/sub-alice",
		Worktree:    "/some/worktree/sub-alice",
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "sub-alice",
		Parent:      "alice",
		Subagent:    true,
	})

	err := runRetire(deps, "sub-alice", false, false)
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
		Branch:      "dendra/alice",
		Worktree:    filepath.Join(tmpDir, ".dendra", "worktrees", "alice"),
		TmuxSession: tmux.ChildrenSessionName(tmux.DefaultNamespace, tmux.DefaultRootName),
		TmuxWindow:  "alice",
		Parent:      "root",
	})

	// Create a logs directory with a fake log file.
	logsDir := filepath.Join(tmpDir, ".dendra", "agents", "alice", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		t.Fatalf("failed to create logs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logsDir, "agent.log"), []byte("log data"), 0644); err != nil {
		t.Fatalf("failed to create log file: %v", err)
	}

	// Track what path removeAll is called with.
	var removedPath string
	deps.removeAll = func(path string) error {
		removedPath = path
		return os.RemoveAll(path)
	}

	err := runRetire(deps, "alice", false, false)
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
