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
		// QUM-1186: state.StatusStopped was here. It is no longer reachable
		// as a child Status — LoadAgent now migrates the legacy "stopped"
		// sentinel to StatusSuspended (see internal/state migrate), and
		// suspended is deliberately NOT a resolved orphan. A legacy stopped
		// child therefore BLOCKS a parent merge where it previously did not.
		// That is a real behaviour change on legacy state files, flagged to
		// the manager rather than absorbed silently; it follows from mapping
		// an unlabelled clean stop to "parked" instead of "faulted".
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
			out, err := Merge(context.Background(), deps, parentName, "", true, false, false)
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
	_, err := Merge(context.Background(), deps, parentName, "", true, false, false)
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

// --- QUM-1100: distinguish a previous failed merge from agent edits ------

// mergeDirtyEnv seeds a parent agent + weave and returns deps whose
// GitStatus reports agentStatus for the AGENT worktree and clean elsewhere.
func mergeDirtyEnv(t *testing.T, agentStatus string) (*MergeDeps, string) {
	t.Helper()
	sprawlRoot := t.TempDir()
	parentWT := filepath.Join(sprawlRoot, ".sprawl", "worktrees", "parent")
	if err := os.MkdirAll(parentWT, 0o755); err != nil {
		t.Fatalf("mkdir parent wt: %v", err)
	}
	if err := state.SaveAgent(sprawlRoot, &state.AgentState{
		Name: "parent", Type: "engineer", Family: "engineering",
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
	deps := mergeTestDeps(sprawlRoot)
	deps.GitStatus = func(wt string) (string, error) {
		if wt == parentWT {
			return agentStatus, nil
		}
		return "", nil
	}
	return deps, parentWT
}

// TestMerge_AgentWorktree_StagedOnly_NamesThePreviousFailedMerge — the
// QUM-1100 misdirection. In the live incident the retry said `agent "chip"
// has uncommitted changes in worktree`: TRUE, and it entirely misdescribed
// the cause — the content was the engine's own orphaned squash. The natural
// response (`reset --hard`) would have destroyed 3026 lines.
func TestMerge_AgentWorktree_StagedOnly_NamesThePreviousFailedMerge(t *testing.T) {
	deps, _ := mergeDirtyEnv(t, "A  k.txt")

	_, err := Merge(context.Background(), deps, "parent", "", true, false, false)
	if err == nil {
		t.Fatal("a dirty agent worktree must still block the merge")
	}
	msg := err.Error()
	if !strings.Contains(msg, "PREVIOUS FAILED MERGE") {
		t.Errorf("must raise a previous failed merge as a possible cause, got: %v", err)
	}
	if !strings.Contains(msg, "refs/sprawl/premerge") {
		t.Errorf("must point at the recovery refs, got: %v", err)
	}
	if !strings.Contains(msg, "Do NOT discard") {
		t.Errorf("must warn against discarding before checking, got: %v", err)
	}
	if strings.Contains(msg, "Ask the agent to commit first") {
		t.Errorf("must not blame the agent for the engine's orphaned squash, got: %v", err)
	}
}

// TestMerge_AgentWorktree_ModifiedFiles_StillBlamesTheAgent — the other
// direction. Real agent edits must keep the original, correct message; a fix
// that widened both cases into one would re-merge exactly what QUM-1100 asks
// to separate.
func TestMerge_AgentWorktree_ModifiedFiles_StillBlamesTheAgent(t *testing.T) {
	deps, _ := mergeDirtyEnv(t, " M a.go")

	_, err := Merge(context.Background(), deps, "parent", "", true, false, false)
	if err == nil {
		t.Fatal("a dirty agent worktree must block the merge")
	}
	msg := err.Error()
	if !strings.Contains(msg, "uncommitted changes") || !strings.Contains(msg, "Ask the agent to commit first") {
		t.Errorf("agent edits must keep the original message, got: %v", err)
	}
	if strings.Contains(msg, "PREVIOUS FAILED MERGE") || strings.Contains(msg, "premerge") {
		t.Errorf("must not blame a failed merge for the agent's own edits, got: %v", err)
	}
}
