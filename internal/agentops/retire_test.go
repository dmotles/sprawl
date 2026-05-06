package agentops

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRetire_EmitsCheckpoints verifies that the retire path threads
// per-call observability checkpoints (QUM-494). The exact set of seams
// is bounded — at minimum a preflight checkpoint (before state mutation)
// and a worktree-removed checkpoint (after agent.RetireAgent succeeds)
// must be emitted on the happy path.
func TestRetire_EmitsCheckpoints(t *testing.T) {
	sprawlRoot := t.TempDir()

	// Set up a fake worktree directory for the agent.
	agentName := "finn"
	worktreePath := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	// Persist agent state (real state package, no mocking).
	agentState := &state.AgentState{
		Name:     agentName,
		Type:     "engineer",
		Family:   "engineering",
		Branch:   "dmotles/finn-feature",
		Worktree: worktreePath,
		Parent:   "weave",
		Status:   "active",
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	var steps []string
	deps := &RetireDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return sprawlRoot
			case "SPRAWL_AGENT_IDENTITY":
				return "weave"
			}
			return ""
		},
		WorktreeRemove: func(repoRoot, worktreePath string, force bool) error {
			return os.RemoveAll(worktreePath)
		},
		GitStatus:           func(string) (string, error) { return "", nil },
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     func(string, string) error { return nil },
		GitBranchIsMerged:   func(string, string) (bool, error) { return false, nil },
		GitBranchSafeDelete: func(string, string) error { return nil },
		DoMerge: func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) {
			return &merge.Result{WasNoOp: true}, nil
		},
		NewMergeDeps:       func() *merge.Deps { return &merge.Deps{} },
		LoadAgent:          state.LoadAgent,
		CurrentBranch:      func(string) (string, error) { return "main", nil },
		GitUnmergedCommits: func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript: func(script, workDir string, env map[string]string) ([]byte, error) {
			return nil, nil
		},
		Checkpoint: func(step string, _ ...any) {
			steps = append(steps, step)
		},
	}

	if err := Retire(deps, agentName, false, false, false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	// Required checkpoints. The implementer chooses the exact seam names but
	// these prefixes guard against silently dropping observability.
	required := []string{
		"retire.preflight",
		"retire.checkpoint-saved",
		"retire.worktree-removed",
	}

	have := make(map[string]bool, len(steps))
	for _, s := range steps {
		have[s] = true
	}
	for _, want := range required {
		if !have[want] {
			t.Errorf("missing required checkpoint %q (got: %v)", want, steps)
		}
	}
}

// TestRetire_NilCheckpointSafe pins that nil Checkpoint is allowed.
func TestRetire_NilCheckpointSafe(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentName := "ghost"
	worktreePath := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	agentState := &state.AgentState{
		Name: agentName, Type: "researcher", Family: "engineering",
		Branch: "dmotles/ghost", Worktree: worktreePath, Parent: "weave",
		Status: "active",
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	deps := &RetireDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return sprawlRoot
			}
			return ""
		},
		WorktreeRemove:      func(_, p string, _ bool) error { return os.RemoveAll(p) },
		GitStatus:           func(string) (string, error) { return "", nil },
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     func(string, string) error { return nil },
		GitBranchIsMerged:   func(string, string) (bool, error) { return false, nil },
		GitBranchSafeDelete: func(string, string) error { return nil },
		LoadAgent:           state.LoadAgent,
		CurrentBranch:       func(string) (string, error) { return "main", nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript:  func(string, string, map[string]string) ([]byte, error) { return nil, nil },
		Checkpoint: nil,
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil Checkpoint panicked: %v", r)
		}
	}()
	if err := Retire(deps, agentName, false, false, false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
}
