package agentops_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeWorktreeCreator records the requested worktree and creates the directory
// without invoking real git.
type fakeWorktreeCreator struct {
	root         string
	capturedBase string
}

func (c *fakeWorktreeCreator) Create(_, agentName, branchName, baseBranch string) (string, string, error) {
	c.capturedBase = baseBranch
	path := filepath.Join(c.root, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", "", err
	}
	return path, branchName, nil
}

// newBaseRefSpawnDeps builds a minimal valid SpawnDeps for the baseBranch
// resolution tests. Callers should set CurrentBranch and (optionally)
// ResolveBase on the returned struct.
func newBaseRefSpawnDeps(t *testing.T, tmpDir string) (*agentops.SpawnDeps, *fakeWorktreeCreator) {
	t.Helper()
	creator := &fakeWorktreeCreator{root: tmpDir}
	deps := &agentops.SpawnDeps{
		WorktreeCreator: creator,
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "manager-x"
			case "SPRAWL_ROOT":
				return tmpDir
			}
			return ""
		},
		CurrentBranch: func(string) (string, error) { return "main", nil },
		NewSpawnLock: func(string) (func() error, func() error) {
			return func() error { return nil }, func() error { return nil }
		},
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript:       agentops.RunBashScript,
		WorktreeRemove:  agentops.RealWorktreeRemove,
		GitBranchDelete: func(string, string) error { return nil },
	}
	return deps, creator
}

// TestPrepareSpawn_UsesResolveBaseWhenProvided pins QUM-572: when the
// optional ResolveBase dep returns a non-empty ref, the worktree must be
// created from THAT ref (the caller manager's worktree HEAD), not the
// main repo's current branch. Without this fix, a manager's spawned
// engineers silently lose the manager's integration commits.
func TestPrepareSpawn_UsesResolveBaseWhenProvided(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	deps.ResolveBase = func(caller, root string) (string, error) {
		if caller != "manager-x" {
			t.Errorf("ResolveBase caller = %q, want %q", caller, "manager-x")
		}
		if root != tmpDir {
			t.Errorf("ResolveBase root = %q, want %q", root, tmpDir)
		}
		return "deadbeefcafebabe1234567890abcdef12345678", nil
	}

	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch"); err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	if creator.capturedBase != "deadbeefcafebabe1234567890abcdef12345678" {
		t.Errorf("worktree baseBranch = %q, want %q (must use ResolveBase output — QUM-572)", creator.capturedBase, "deadbeefcafebabe1234567890abcdef12345678")
	}
}

// TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseReturnsEmpty
// models the root-weave case: weave has no agent state, so ResolveBase
// returns ("", nil) → fall through to CurrentBranch.
func TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseReturnsEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	deps.ResolveBase = func(string, string) (string, error) { return "", nil }

	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch"); err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	if creator.capturedBase != "main" {
		t.Errorf("worktree baseBranch = %q, want %q (empty ResolveBase must fall back to CurrentBranch)", creator.capturedBase, "main")
	}
}

// TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseIsNil pins
// backwards-compat: callers that haven't been updated to provide
// ResolveBase still get the old behavior (CurrentBranch of main repo).
func TestPrepareSpawn_FallsBackToCurrentBranchWhenResolveBaseIsNil(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	// ResolveBase intentionally omitted.

	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch"); err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}

	if creator.capturedBase != "main" {
		t.Errorf("worktree baseBranch = %q, want %q (nil ResolveBase must fall back to CurrentBranch)", creator.capturedBase, "main")
	}
}

// TestPrepareSpawn_PropagatesResolveBaseError pins the documented contract:
// when ResolveBase returns a non-nil error, PrepareSpawn must propagate it
// (wrap is fine) rather than swallowing it and falling back to
// CurrentBranch. A silent fallback would hide a real fault (e.g. the caller's
// worktree is corrupt / non-existent / not a git repo) and silently strip
// integration commits from the spawned child — the exact regression class
// QUM-572 is guarding against.
func TestPrepareSpawn_PropagatesResolveBaseError(t *testing.T) {
	tmpDir := t.TempDir()
	deps, _ := newBaseRefSpawnDeps(t, tmpDir)
	deps.CurrentBranch = func(string) (string, error) { return "main", nil }
	resolveErr := errors.New("boom")
	deps.ResolveBase = func(string, string) (string, error) { return "", resolveErr }

	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch")
	if err == nil {
		t.Fatal("PrepareSpawn returned nil error; expected ResolveBase error to propagate")
	}
	if !errors.Is(err, resolveErr) && !strings.Contains(err.Error(), "boom") {
		t.Errorf("PrepareSpawn err = %v, expected to wrap/contain ResolveBase error %q", err, resolveErr)
	}
}

// TestSpawn_WritesStateFile_GrandchildCase pins the regression-guard claim
// from QUM-404: when a researcher (e.g. "ghost") spawns a manager child, the
// child's state JSON must be persisted in <root>/.sprawl/agents/<name>.json.
//
// This is the grandchild scenario (root → researcher → manager), distinct
// from the engineer-spawned tests in real_runtime_test.go. Production code
// already writes the JSON — this test pins that behavior so a future
// refactor can't silently regress it.
func TestSpawn_WritesStateFile_GrandchildCase(t *testing.T) {
	tmpDir := t.TempDir()

	deps := &agentops.SpawnDeps{
		WorktreeCreator: &fakeWorktreeCreator{root: tmpDir},
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_AGENT_IDENTITY":
				return "ghost" // researcher (the grandchild's parent)
			case "SPRAWL_ROOT":
				return tmpDir
			}
			return ""
		},
		CurrentBranch: func(string) (string, error) { return "main", nil },
		NewSpawnLock: func(string) (func() error, func() error) {
			return func() error { return nil }, func() error { return nil }
		},
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript:       agentops.RunBashScript,
		WorktreeRemove:  agentops.RealWorktreeRemove,
		GitBranchDelete: func(string, string) error { return nil },
	}

	got, err := agentops.PrepareSpawn(deps, "engineering", "manager", "task body", "dmotles/test-branch")
	if err != nil {
		t.Fatalf("PrepareSpawn: %v", err)
	}
	if got == nil {
		t.Fatal("PrepareSpawn returned nil agent state")
	}

	if got.Type != "manager" {
		t.Errorf("Type = %q, want %q", got.Type, "manager")
	}
	if got.Parent != "ghost" {
		t.Errorf("Parent = %q, want %q", got.Parent, "ghost")
	}

	jsonPath := filepath.Join(state.AgentsDir(tmpDir), got.Name+".json")
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("expected state JSON at %s, stat err = %v", jsonPath, err)
	}

	loaded, err := state.LoadAgent(tmpDir, got.Name)
	if err != nil {
		t.Fatalf("LoadAgent(%s): %v", got.Name, err)
	}
	if loaded.Type != "manager" {
		t.Errorf("loaded Type = %q, want %q", loaded.Type, "manager")
	}
	if loaded.Parent != "ghost" {
		t.Errorf("loaded Parent = %q, want %q", loaded.Parent, "ghost")
	}
	if loaded.Name != got.Name {
		t.Errorf("loaded Name = %q, want %q", loaded.Name, got.Name)
	}
}
