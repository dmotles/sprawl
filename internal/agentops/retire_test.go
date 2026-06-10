package agentops

import (
	"context"
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
		DoMerge: func(_ context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) {
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

	if err := Retire(context.Background(), deps, agentName, false, false, false, false, false, false); err != nil {
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
	if err := Retire(context.Background(), deps, agentName, false, false, false, false, false, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}
}

// TestRetire_Subagent_SkipsWorktreeAndBranchDelete pins QUM-709: retiring a
// sub-agent must NOT remove the shared worktree (already gated by
// agent.RetireAgent) and must NOT delete the shared branch via
// GitBranchDelete or GitBranchSafeDelete. The state file IS removed.
func TestRetire_Subagent_SkipsWorktreeAndBranchDelete(t *testing.T) {
	sprawlRoot := t.TempDir()
	agentName := "sub-x"

	// Shared worktree dir (lives outside .sprawl/worktrees to make it obvious
	// it isn't owned by the sub-agent).
	sharedWT := filepath.Join(sprawlRoot, "shared-wt")
	if err := os.MkdirAll(sharedWT, 0o755); err != nil {
		t.Fatalf("mkdir shared worktree: %v", err)
	}

	agentState := &state.AgentState{
		Name:     agentName,
		Type:     "engineer",
		Family:   "engineering",
		Parent:   "manager-x",
		Branch:   "shared-br",
		Worktree: sharedWT,
		Status:   "active",
		Subagent: true,
	}
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	var removedWorktree, deletedBranch bool
	deps := &RetireDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return sprawlRoot
			case "SPRAWL_AGENT_IDENTITY":
				return "manager-x"
			}
			return ""
		},
		WorktreeRemove: func(_, _ string, _ bool) error {
			removedWorktree = true
			return nil
		},
		GitStatus:       func(string) (string, error) { return "", nil },
		RemoveAll:       os.RemoveAll,
		GitBranchDelete: func(string, string) error { deletedBranch = true; return nil },
		GitBranchIsMerged: func(string, string) (bool, error) {
			return false, nil
		},
		GitBranchSafeDelete: func(string, string) error { deletedBranch = true; return nil },
		LoadAgent:           state.LoadAgent,
		CurrentBranch:       func(string) (string, error) { return "main", nil },
		GitUnmergedCommits:  func(string, string) ([]string, error) { return nil, nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}

	// abandon=true, yes=true: typical sub-agent retire path.
	if err := Retire(context.Background(), deps, agentName, false, false, true, false, true, false); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	if removedWorktree {
		t.Errorf("WorktreeRemove was called for sub-agent retire; must be skipped (shared worktree)")
	}
	if deletedBranch {
		t.Errorf("GitBranchDelete/GitBranchSafeDelete was called for sub-agent retire; must be skipped (shared branch)")
	}

	// Shared worktree dir must still exist.
	if _, err := os.Stat(sharedWT); err != nil {
		t.Errorf("shared worktree dir was removed: %v", err)
	}

	// State file must be gone.
	if _, err := state.LoadAgent(sprawlRoot, agentName); err == nil {
		t.Errorf("state file for retired sub-agent still loads; want not-found")
	}
}

// TestRetire_TerminalStatuses_Succeed pins QUM-739 Bug 1: retire (with or
// without --abandon) must succeed when the agent is in any terminal status
// and no live runtime is registered. The previous TerminalAgentError gate
// refused these cases, trapping zombie agents.
func TestRetire_TerminalStatuses_Succeed(t *testing.T) {
	terminalStatuses := []string{
		state.StatusStopped,
		state.StatusFaulted,
		state.StatusRetired,
		state.StatusKilled,
		state.StatusDied,
		state.StatusResumeFailed,
	}
	for _, status := range terminalStatuses {
		for _, abandon := range []bool{false, true} {
			name := status
			if abandon {
				name = status + "+abandon"
			}
			t.Run(name, func(t *testing.T) {
				sprawlRoot := t.TempDir()
				agentName := "zombie"
				worktreePath := filepath.Join(sprawlRoot, ".sprawl", "worktrees", agentName)
				if err := os.MkdirAll(worktreePath, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				agentState := &state.AgentState{
					Name:            agentName,
					Type:            "engineer",
					Family:          "engineering",
					Branch:          "dmotles/zombie",
					Worktree:        worktreePath,
					Parent:          "weave",
					Status:          status,
					LastReportState: "complete",
					LastReportAt:    "2026-06-09T20:00:00Z",
				}
				if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
					t.Fatalf("SaveAgent: %v", err)
				}
				deps := terminalRetireDeps(sprawlRoot)
				err := Retire(context.Background(), deps, agentName,
					false /* cascade */, false /* force */, abandon,
					false /* mergeFirst */, true /* yes */, false /* noValidate */)
				if err != nil {
					t.Fatalf("Retire(status=%q, abandon=%v): unexpected error: %v", status, abandon, err)
				}
				if _, err := state.LoadAgent(sprawlRoot, agentName); err == nil {
					t.Errorf("state file still loads after Retire; expected removal")
				}
			})
		}
	}
}

// TestRetire_TerminalChildren_DoNotRequireCascade pins QUM-739 Bug 2 on the
// retire path: a parent whose only children are in terminal status must be
// retire-able without --cascade.
func TestRetire_TerminalChildren_DoNotRequireCascade(t *testing.T) {
	sprawlRoot := t.TempDir()
	parentName := "parent"
	parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", parentName)
	if err := os.MkdirAll(parentWT, 0o755); err != nil {
		t.Fatalf("mkdir parent wt: %v", err)
	}
	parentState := &state.AgentState{
		Name: parentName, Type: "manager", Family: "engineering",
		Branch: "dmotles/parent", Worktree: parentWT, Parent: "weave",
		Status: state.StatusActive,
	}
	if err := state.SaveAgent(sprawlRoot, parentState); err != nil {
		t.Fatalf("SaveAgent parent: %v", err)
	}

	terminalStatuses := []string{
		state.StatusStopped,
		state.StatusFaulted,
		state.StatusRetired,
		state.StatusKilled,
		state.StatusDied,
		state.StatusResumeFailed,
	}
	for i, s := range terminalStatuses {
		childName := "child" + s
		childWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", childName)
		if err := os.MkdirAll(childWT, 0o755); err != nil {
			t.Fatalf("mkdir child wt %d: %v", i, err)
		}
		child := &state.AgentState{
			Name: childName, Type: "engineer", Family: "engineering",
			Branch: "dmotles/" + childName, Worktree: childWT, Parent: parentName,
			Status: s,
		}
		if err := state.SaveAgent(sprawlRoot, child); err != nil {
			t.Fatalf("SaveAgent child: %v", err)
		}
	}

	deps := terminalRetireDeps(sprawlRoot)
	// cascade=false: must still succeed because all children are terminal.
	if err := Retire(context.Background(), deps, parentName,
		false /* cascade */, false /* force */, false, /* abandon */
		false /* mergeFirst */, false /* yes */, false /* noValidate */); err != nil {
		t.Fatalf("Retire parent with terminal-only children: %v", err)
	}
}

// TestRetire_ActiveChildBlocks pins the negative case: a live child still
// blocks parent retire without --cascade. Guards against the filter being too
// permissive.
func TestRetire_ActiveChildBlocks(t *testing.T) {
	sprawlRoot := t.TempDir()
	parentName := "parent"
	parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", parentName)
	if err := os.MkdirAll(parentWT, 0o755); err != nil {
		t.Fatalf("mkdir parent wt: %v", err)
	}
	parentState := &state.AgentState{
		Name: parentName, Type: "manager", Family: "engineering",
		Branch: "dmotles/parent", Worktree: parentWT, Parent: "weave",
		Status: state.StatusActive,
	}
	if err := state.SaveAgent(sprawlRoot, parentState); err != nil {
		t.Fatalf("SaveAgent parent: %v", err)
	}
	childWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", "kid")
	if err := os.MkdirAll(childWT, 0o755); err != nil {
		t.Fatalf("mkdir child wt: %v", err)
	}
	child := &state.AgentState{
		Name: "kid", Type: "engineer", Family: "engineering",
		Branch: "dmotles/kid", Worktree: childWT, Parent: parentName,
		Status: state.StatusActive,
	}
	if err := state.SaveAgent(sprawlRoot, child); err != nil {
		t.Fatalf("SaveAgent child: %v", err)
	}

	deps := terminalRetireDeps(sprawlRoot)
	err := Retire(context.Background(), deps, parentName,
		false, false, false, false, false, false)
	if err == nil {
		t.Fatalf("Retire of parent with active child must fail without --cascade")
	}
}

// terminalRetireDeps builds RetireDeps suitable for the QUM-739 tests above.
// No live runtime is wired in (the agentops.Retire path treats absence of a
// runtime as offline cleanup); all git operations are stubbed.
func terminalRetireDeps(sprawlRoot string) *RetireDeps {
	return &RetireDeps{
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
		RunScript: func(string, string, map[string]string) ([]byte, error) { return nil, nil },
	}
}
