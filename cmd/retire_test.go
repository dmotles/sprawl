package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestRetireDeps(t *testing.T) (*retireDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()
	deps := &retireDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		WorktreeRemove: func(repoRoot, worktreePath string, force bool) error {
			return os.RemoveAll(worktreePath)
		},
		GitStatus: func(worktreePath string) (string, error) { return "", nil },
		RemoveAll: os.RemoveAll,
		GitBranchDelete: func(repoRoot, branchName string) error {
			return nil
		},
		GitBranchIsMerged: func(repoRoot, branchName string) (bool, error) {
			return false, nil
		},
		GitBranchSafeDelete: func(repoRoot, branchName string) error { return nil },
		DoMerge: func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) {
			return &merge.Result{}, nil
		},
		NewMergeDeps: func() *merge.Deps { return &merge.Deps{} },
		LoadAgent:    state.LoadAgent,
		CurrentBranch: func(repoRoot string) (string, error) {
			return "main", nil
		},
		GitUnmergedCommits: func(repoRoot, branchName string) ([]string, error) {
			return nil, nil
		},
		LoadConfig: func(sprawlRoot string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}
	return deps, tmpDir
}

func saveRetireCmdAgent(t *testing.T, sprawlRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
}

func TestRetire_InvalidAgentNameReturnsError(t *testing.T) {
	deps, _ := newTestRetireDeps(t)
	err := runRetire(deps, "../evil", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected invalid agent name error")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Fatalf("error = %q, want invalid agent name", err)
	}
}

func TestRetire_FailsClosedWhenLiveWeaveSessionOwnsRuntimes(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	saveRetireCmdAgent(t, tmpDir, &state.AgentState{Name: "alice", Status: "active", Branch: "feature/alice"})

	lock, err := rootinit.AcquireWeaveLock(tmpDir)
	if err != nil {
		t.Fatalf("AcquireWeaveLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	err = runRetire(deps, "alice", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected standalone retire rejection")
	}
	if !strings.Contains(err.Error(), "sprawl enter") || !strings.Contains(err.Error(), "sprawl_retire") {
		t.Fatalf("error = %q, want sprawl enter + sprawl_retire guidance", err)
	}
}

func TestRetire_HappyPathDeletesState(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	worktree := filepath.Join(tmpDir, ".sprawl", "worktrees", "alice")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	saveRetireCmdAgent(t, tmpDir, &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: worktree,
		Parent:   "weave",
	})

	if err := runRetire(deps, "alice", false, false, false, false, false); err != nil {
		t.Fatalf("runRetire() error: %v", err)
	}
	if _, err := state.LoadAgent(tmpDir, "alice"); err == nil {
		t.Fatal("expected agent state to be deleted")
	}
}

func TestRetire_DirtyWorktree_Refuses(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	deps.GitStatus = func(string) (string, error) { return "M file.go", nil }
	saveRetireCmdAgent(t, tmpDir, &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: filepath.Join(tmpDir, "worktree"),
		Parent:   "weave",
	})

	err := runRetire(deps, "alice", false, false, false, false, false)
	if err == nil {
		t.Fatal("expected dirty worktree error")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %q, want uncommitted changes", err)
	}

	agentState, loadErr := state.LoadAgent(tmpDir, "alice")
	if loadErr != nil {
		t.Fatalf("expected state file to remain: %v", loadErr)
	}
	if agentState.Status != "retiring" {
		t.Fatalf("status = %q, want retiring", agentState.Status)
	}
}

func TestRetire_DirtyWorktree_ForceOverrides(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	deps.GitStatus = func(string) (string, error) { return "M file.go", nil }
	var gotForce bool
	deps.WorktreeRemove = func(repoRoot, worktreePath string, force bool) error {
		gotForce = force
		return nil
	}
	saveRetireCmdAgent(t, tmpDir, &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: filepath.Join(tmpDir, "worktree"),
		Parent:   "weave",
	})

	if err := runRetire(deps, "alice", false, true, false, false, false); err != nil {
		t.Fatalf("runRetire() error: %v", err)
	}
	if !gotForce {
		t.Fatal("expected force removal")
	}
}

func TestRetire_AbandonWithUnmergedCommitsRequiresYes(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	deps.GitUnmergedCommits = func(repoRoot, branchName string) ([]string, error) {
		return []string{"abc123 test commit"}, nil
	}
	saveRetireCmdAgent(t, tmpDir, &state.AgentState{
		Name:     "alice",
		Status:   "active",
		Branch:   "sprawl/alice",
		Worktree: filepath.Join(tmpDir, "worktree"),
		Parent:   "weave",
	})

	err := runRetire(deps, "alice", false, false, true, false, false)
	if err == nil {
		t.Fatal("expected abandon confirmation error")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("error = %q, want --yes guidance", err)
	}
}

func TestRetire_MergeAndAbandonAreMutuallyExclusive(t *testing.T) {
	deps, tmpDir := newTestRetireDeps(t)
	saveRetireCmdAgent(t, tmpDir, &state.AgentState{Name: "alice", Status: "active", Branch: "sprawl/alice"})

	err := runRetire(deps, "alice", false, false, true, true, false)
	if err == nil {
		t.Fatal("expected mutually exclusive flag error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %q, want mutually exclusive", err)
	}
}
