// Tests for QUM-739 Bug 2 in the merge path: a parent whose only children are
// in terminal status must be mergeable. Active-status children still block.
package agentops

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
)

func TestMerge_TerminalChildrenIgnored(t *testing.T) {
	terminalStatuses := []string{
		state.StatusStopped,
		state.StatusFaulted,
		state.StatusRetired,
		state.StatusKilled,
		state.StatusDied,
		state.StatusResumeFailed,
		// QUM-787: StatusComplete is a resolved-orphan resting state
		// and must NOT block a parent merge.
		state.StatusComplete,
	}
	for _, s := range terminalStatuses {
		t.Run(s, func(t *testing.T) {
			sprawlRoot := t.TempDir()
			parentName := "parent"
			parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", parentName)
			if err := os.MkdirAll(parentWT, 0o755); err != nil {
				t.Fatalf("mkdir parent wt: %v", err)
			}
			parentState := &state.AgentState{
				Name: parentName, Type: "engineer", Family: "engineering",
				Branch: "dmotles/parent", Worktree: parentWT, Parent: "weave",
				Status: state.StatusActive,
			}
			if err := state.SaveAgent(sprawlRoot, parentState); err != nil {
				t.Fatalf("SaveAgent parent: %v", err)
			}
			// Add caller (weave) so it can be loaded.
			weaveWT := filepath.Join(sprawlRoot, "weave-wt")
			if err := os.MkdirAll(weaveWT, 0o755); err != nil {
				t.Fatalf("mkdir weave wt: %v", err)
			}
			if err := state.SaveAgent(sprawlRoot, &state.AgentState{
				Name: "weave", Type: "manager", Family: "engineering",
				Worktree: weaveWT, Status: state.StatusActive,
			}); err != nil {
				t.Fatalf("SaveAgent weave: %v", err)
			}
			// Terminal-status child of parent.
			child := &state.AgentState{
				Name: "kid", Type: "engineer", Family: "engineering",
				Parent: parentName, Status: s,
			}
			if err := state.SaveAgent(sprawlRoot, child); err != nil {
				t.Fatalf("SaveAgent child: %v", err)
			}

			deps := mergeTestDeps(sprawlRoot)
			out, err := Merge(context.Background(), deps, parentName, "", true, false)
			if err != nil {
				t.Fatalf("Merge with terminal child status=%q: %v", s, err)
			}
			if out == nil {
				t.Fatalf("Merge returned nil outcome")
			}
		})
	}
}

func TestMerge_ActiveChildBlocks(t *testing.T) {
	sprawlRoot := t.TempDir()
	parentName := "parent"
	parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", parentName)
	if err := os.MkdirAll(parentWT, 0o755); err != nil {
		t.Fatalf("mkdir parent wt: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: parentName, Type: "engineer", Family: "engineering",
		Branch: "dmotles/parent", Worktree: parentWT, Parent: "weave",
		Status: state.StatusActive,
	}); err != nil {
		t.Fatalf("SaveAgent parent: %v", err)
	}
	weaveWT := filepath.Join(sprawlRoot, "weave-wt")
	if err := os.MkdirAll(weaveWT, 0o755); err != nil {
		t.Fatalf("mkdir weave wt: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: "weave", Type: "manager", Family: "engineering",
		Worktree: weaveWT, Status: state.StatusActive,
	}); err != nil {
		t.Fatalf("SaveAgent weave: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: "kid", Type: "engineer", Family: "engineering",
		Parent: parentName, Status: state.StatusActive,
	}); err != nil {
		t.Fatalf("SaveAgent child: %v", err)
	}

	deps := mergeTestDeps(sprawlRoot)
	_, err := Merge(context.Background(), deps, parentName, "", true, false)
	if err == nil {
		t.Fatalf("Merge of parent with active child must fail")
	}
	if !strings.Contains(err.Error(), "active children") {
		t.Errorf("unexpected error: %v", err)
	}
}

func mergeTestDeps(sprawlRoot string) *MergeDeps {
	return &MergeDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return sprawlRoot
			case "SPRAWL_AGENT_IDENTITY":
				return "weave"
			}
			return ""
		},
		LoadAgent:     state.LoadAgent,
		ListAgents:    state.ListAgents,
		GitStatus:     func(string) (string, error) { return "", nil },
		BranchExists:  func(string, string) bool { return true },
		CurrentBranch: func(string) (string, error) { return "main", nil },
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{}, nil
		},
		DoMerge: func(_ context.Context, cfg *merge.Config, _ *merge.Deps) (*merge.Result, error) {
			return &merge.Result{WasNoOp: true}, nil
		},
		NewMergeDeps: func() *merge.Deps { return &merge.Deps{} },
		Stderr:       io.Discard,
	}
}
