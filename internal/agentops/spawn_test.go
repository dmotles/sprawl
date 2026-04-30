package agentops_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeWorktreeCreator records the requested worktree and creates the directory
// without invoking real git.
type fakeWorktreeCreator struct {
	root string
}

func (c *fakeWorktreeCreator) Create(_, agentName, branchName, _ string) (string, string, error) {
	path := filepath.Join(c.root, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", "", err
	}
	return path, branchName, nil
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
