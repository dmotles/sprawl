package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestMergeDeps(t *testing.T) (*mergeDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()

	deps := &mergeDeps{
		Getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return tmpDir
			case "SPRAWL_AGENT_IDENTITY":
				return "parent-agent"
			}
			return ""
		},
		LoadAgent:    state.LoadAgent,
		ListAgents:   state.ListAgents,
		GitStatus:    func(worktree string) (string, error) { return "", nil },
		BranchExists: func(repoRoot, branchName string) bool { return true },
		// Default path-aware mock: resolve any worktree path back to its
		// agent's spawn-time Branch (so existing tests that fixture
		// agentState.Branch see that value flow into AgentBranch). The
		// SPRAWL_ROOT itself maps to "main". Tests that need to simulate
		// delegate-style branch swaps override this directly.
		CurrentBranch: func(repoRoot string) (string, error) {
			if repoRoot == tmpDir {
				return "main", nil
			}
			agents, err := state.ListAgents(tmpDir)
			if err == nil {
				for _, a := range agents {
					if a.Worktree == repoRoot {
						return a.Branch, nil
					}
				}
			}
			return "main", nil
		},
		LoadConfig: func(sprawlRoot string) (*config.Config, error) {
			return &config.Config{Validate: "make validate"}, nil
		},
		DoMerge: func(_ context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) {
			return &merge.Result{MergedTip: "abc1234"}, nil
		},
		NewMergeDeps: func() *merge.Deps { return &merge.Deps{} },
		Stderr:       io.Discard,
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	return deps, tmpDir
}

func TestMerge_InvalidAgentNameReturnsError(t *testing.T) {
	deps, _ := newTestMergeDeps(t)
	err := runMerge(context.Background(), deps, "../evil", "", false, false)
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error should mention 'invalid agent name', got: %v", err)
	}
}

func TestMerge_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Completed the task",
	})

	var mergeCalled bool
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		mergeCalled = true
		return &merge.Result{MergedTip: "abc1234"}, nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !mergeCalled {
		t.Error("expected doMerge to be called")
	}

	output := stderr.String()
	if !strings.Contains(output, `"target-agent"`) {
		t.Errorf("output should mention agent name, got: %q", output)
	}
	if !strings.Contains(output, "abc1234") {
		t.Errorf("output should include commit hash, got: %q", output)
	}
	if !strings.Contains(output, "not retired") {
		t.Errorf("output should indicate agent not retired, got: %q", output)
	}
	if !strings.Contains(output, "preserved") {
		t.Errorf("output should indicate branch preserved, got: %q", output)
	}
}

func TestMerge_AgentNotFound(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})

	err := runMerge(context.Background(), deps, "nonexistent", "", true, false)
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), `agent "nonexistent" not found`) {
		t.Errorf("error should mention agent not found, got: %v", err)
	}
}

func TestMerge_SubagentRejected(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Subagent: true,
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for subagent")
	}
	if !strings.Contains(err.Error(), "subagent") {
		t.Errorf("error should mention subagent, got: %v", err)
	}
}

func TestMerge_NotParent(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	deps.Getenv = func(key string) string {
		switch key {
		case "SPRAWL_ROOT":
			return tmpDir
		case "SPRAWL_AGENT_IDENTITY":
			return "other-agent"
		}
		return ""
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "other-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/other", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for non-parent caller")
	}
	if !strings.Contains(err.Error(), "you are not its parent") {
		t.Errorf("error should mention parent mismatch, got: %v", err)
	}
}

func TestMerge_StatusActive_Accepted(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("active agents should be mergeable, got: %v", err)
	}
}

func TestMerge_StatusDone_Accepted(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("done agents should be mergeable, got: %v", err)
	}
}

func TestMerge_StatusOther_Rejected(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "problem", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for non-active/non-done agent")
	}
	if !strings.Contains(err.Error(), "cannot be merged") {
		t.Errorf("error should mention cannot be merged, got: %v", err)
	}
}

// TestMerge_SuspendedAgentMerges pins the capability that a suspended agent
// with a ready branch is mergeable.
//
// QUM-1186 (D4): this test was called TestMerge_CompleteViaLastReportState and
// claimed to prove that LastReportState=="complete" satisfied precondition 4.
// Precondition 4 is now a Status allow-set, so the case passes because
// Status=="suspended" is in that set — a DIFFERENT reason than the old name
// asserted. Left unrenamed it would have been a vacuous green: still passing,
// no longer testing what it said.
//
// The underlying capability is the one worth keeping, and it is the specific
// thing the allow-set was widened to preserve.
func TestMerge_SuspendedAgentMerges(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	var mergeCalled bool
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		mergeCalled = true
		return &merge.Result{MergedTip: "abc1234"}, nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("merge of suspended agent: err = %v, want nil (suspended is in the precondition-4 allow-set)", err)
	}
	if !mergeCalled {
		t.Error("expected doMerge to be called for a suspended agent")
	}
}

func TestMerge_ActiveChildren(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "child-one", Status: "active", Branch: "child-branch-1",
		Worktree: "/worktree/child1", Parent: "target-agent",
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for active children")
	}
	if !strings.Contains(err.Error(), "active children") {
		t.Errorf("error should mention active children, got: %v", err)
	}
	if !strings.Contains(err.Error(), "child-one") {
		t.Errorf("error should list child-one, got: %v", err)
	}
}

func TestMerge_BranchNotFound(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.BranchExists = func(repoRoot, branchName string) bool { return false }

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for missing branch")
	}
	if !strings.Contains(err.Error(), `branch "feature-branch" not found`) {
		t.Errorf("error should mention branch not found, got: %v", err)
	}
}

func TestMerge_CallerDirtyWorktree(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.GitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/parent" {
			return "M  some-file.go", nil
		}
		return "", nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for dirty caller worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should mention dirty worktree, got: %v", err)
	}
}

func TestMerge_AgentDirtyWorktree(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	// QUM-1100: " M" (worktree-modified) is what an agent's own edits look
	// like. The previous fixture was "M  " — staged-with-nothing-modified —
	// which is the fingerprint of a FAILED MERGE, not of agent edits, and now
	// selects the other message. Corrected rather than papered over: keeping
	// the old fixture passing would have required both causes to share one
	// message, which is exactly what this issue separates.
	deps.GitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/target" {
			return " M dirty-file.go", nil
		}
		return "", nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for dirty agent worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should mention dirty worktree, got: %v", err)
	}
}

// TestMerge_AgentStagedOnlyWorktree — the QUM-1100 wording must reach the
// CLI, not just internal/agentops. In the live incident the operator saw this
// message through the tool, and acting on it would have destroyed the work.
func TestMerge_AgentStagedOnlyWorktree(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.GitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/target" {
			return "A  orphaned.go", nil
		}
		return "", nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for a staged-only agent worktree")
	}
	if !strings.Contains(err.Error(), "PREVIOUS FAILED MERGE") {
		t.Errorf("CLI must surface the failed-merge explanation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Do NOT discard") {
		t.Errorf("CLI must surface the do-not-discard warning, got: %v", err)
	}
}

func TestMerge_MissingSprawlRoot(t *testing.T) {
	deps, _ := newTestMergeDeps(t)

	deps.Getenv = func(key string) string {
		if key == "SPRAWL_AGENT_IDENTITY" {
			return "parent-agent"
		}
		return ""
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestMerge_MissingCallerIdentity(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	deps.Getenv = func(key string) string {
		if key == "SPRAWL_ROOT" {
			return tmpDir
		}
		return ""
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
	})

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for missing caller identity")
	}
	if !strings.Contains(err.Error(), "SPRAWL_AGENT_IDENTITY") {
		t.Errorf("error should mention SPRAWL_AGENT_IDENTITY, got: %v", err)
	}
}

func TestMerge_NoOp(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return &merge.Result{WasNoOp: true}, nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "Nothing to merge") {
		t.Errorf("output should mention nothing to merge, got: %q", output)
	}
}

func TestMerge_MergeError_Propagated(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return nil, fmt.Errorf("rebase conflict in main.go")
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error to propagate from doMerge")
	}
	if !strings.Contains(err.Error(), "rebase conflict") {
		t.Errorf("error should propagate merge error, got: %v", err)
	}
}

func TestMerge_ConfigWiring(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "did stuff",
	})

	var capturedCfg *merge.Config
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{MergedTip: "abc"}, nil
	}

	// No message override: it is refused outright now (QUM-1087 — the engine
	// creates no commit), and TestMerge_MessageOverrideIsRefused covers that.
	err := runMerge(context.Background(), deps, "target-agent", "", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if capturedCfg.SprawlRoot != tmpDir {
		t.Errorf("SprawlRoot = %q, want %q", capturedCfg.SprawlRoot, tmpDir)
	}
	if capturedCfg.AgentName != "target-agent" {
		t.Errorf("AgentName = %q, want target-agent", capturedCfg.AgentName)
	}
	if capturedCfg.AgentBranch != "feature-branch" {
		t.Errorf("AgentBranch = %q, want feature-branch", capturedCfg.AgentBranch)
	}
	if capturedCfg.AgentWorktree != "/worktree/target" {
		t.Errorf("AgentWorktree = %q, want /worktree/target", capturedCfg.AgentWorktree)
	}
	if capturedCfg.ParentBranch != "main" {
		t.Errorf("ParentBranch = %q, want main", capturedCfg.ParentBranch)
	}
	if capturedCfg.ParentWorktree != "/worktree/parent" {
		t.Errorf("ParentWorktree = %q, want /worktree/parent", capturedCfg.ParentWorktree)
	}
	if capturedCfg.NoValidate != false {
		t.Error("NoValidate should be false")
	}
	if capturedCfg.AgentState.LastReportMessage != "did stuff" {
		t.Errorf("AgentState.LastReportMessage = %q, want 'did stuff'", capturedCfg.AgentState.LastReportMessage)
	}
}

func TestMerge_DryRun_PassedToConfig(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	var capturedCfg *merge.Config
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{}, nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if !capturedCfg.DryRun {
		t.Error("DryRun should be true in config")
	}
}

func TestMerge_SuccessOutput(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "finn", Status: "suspended", LastReportState: "complete", Branch: "sprawl/finn",
		Worktree: "/worktree/finn", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "implement QUM-42 broadcast fix",
	})

	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return &merge.Result{MergedTip: "a1b2c3d"}, nil
	}

	err := runMerge(context.Background(), deps, "finn", "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, `"finn"`) {
		t.Errorf("output should mention agent name, got: %q", output)
	}
	if !strings.Contains(output, "sprawl/finn") {
		t.Errorf("output should mention branch name, got: %q", output)
	}
	if !strings.Contains(output, "a1b2c3d") {
		t.Errorf("output should include commit hash, got: %q", output)
	}
	// Should NOT mention retired or deleted
	if strings.Contains(output, "retired") && !strings.Contains(output, "not retired") {
		t.Errorf("output should not indicate agent was retired, got: %q", output)
	}
	if strings.Contains(output, "deleted") {
		t.Errorf("output should not indicate branch was deleted, got: %q", output)
	}
}

func TestMerge_ConfigValidateCmd_PassedThrough(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	var configLoadedFrom string
	deps.LoadConfig = func(sprawlRoot string) (*config.Config, error) {
		configLoadedFrom = sprawlRoot
		return &config.Config{Validate: "go test ./..."}, nil
	}

	var capturedCfg *merge.Config
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{MergedTip: "abc1234"}, nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if capturedCfg.ValidateCmd != "go test ./..." {
		t.Errorf("ValidateCmd = %q, want %q", capturedCfg.ValidateCmd, "go test ./...")
	}
	if configLoadedFrom != tmpDir {
		t.Errorf("loadConfig called with %q, want %q", configLoadedFrom, tmpDir)
	}
}

func TestMerge_NoConfig_SkipsValidation(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.LoadConfig = func(sprawlRoot string) (*config.Config, error) {
		return &config.Config{}, nil
	}

	var capturedCfg *merge.Config
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{MergedTip: "abc1234"}, nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if capturedCfg.ValidateCmd != "" {
		t.Errorf("ValidateCmd = %q, want empty string", capturedCfg.ValidateCmd)
	}
}

// TestMerge_UsesAgentWorktreeCurrentBranch — QUM-511: merge must resolve the
// agent's branch from its worktree's HEAD (so post-delegate branch swaps are
// honored), NOT from the spawn-time agentState.Branch field which goes stale
// after delegate reuses the agent on a follow-up branch.
//
// Red phase: today merge.go:129 sets cfg.AgentBranch = agentState.Branch
// unconditionally, so the captured AgentBranch will be "spawn-branch", not the
// resolved "follow-up-branch".
func TestMerge_UsesAgentWorktreeCurrentBranch(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "spawn-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	// Path-aware CurrentBranch: parent worktree on "main", agent worktree
	// on "follow-up-branch" (simulating delegate reuse).
	deps.CurrentBranch = func(repoRoot string) (string, error) {
		switch repoRoot {
		case "/worktree/target":
			return "follow-up-branch", nil
		case "/worktree/parent":
			return "main", nil
		default:
			return "main", nil
		}
	}

	var capturedCfg *merge.Config
	deps.DoMerge = func(_ context.Context, cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{MergedTip: "abc1234"}, nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedCfg == nil {
		t.Fatal("doMerge was not called")
	}
	if capturedCfg.AgentBranch != "follow-up-branch" {
		t.Errorf("AgentBranch = %q, want %q (resolved from agent worktree HEAD, not stale agentState.Branch)",
			capturedCfg.AgentBranch, "follow-up-branch")
	}

	output := stderr.String()
	if !strings.Contains(output, "follow-up-branch") {
		t.Errorf("stderr summary should mention resolved branch %q, got: %q", "follow-up-branch", output)
	}
}

// TestMerge_DetachedHEADErrors — QUM-511: if the agent worktree is in detached
// HEAD state (CurrentBranch returns "HEAD"), merge must refuse rather than
// attempt to merge a phantom branch.
func TestMerge_DetachedHEADErrors(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "spawn-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.CurrentBranch = func(repoRoot string) (string, error) {
		if repoRoot == "/worktree/target" {
			return "HEAD", nil
		}
		return "main", nil
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for detached HEAD on agent worktree")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "detached head") {
		t.Errorf("error should mention detached HEAD, got: %v", err)
	}
}

// TestMerge_StaleSpawnBranchAbsentDoesNotFail — QUM-511: when the agent's
// spawn-time branch no longer exists (e.g. because delegate moved them onto
// a fresh branch and the original was cleaned up), merge must NOT fail the
// "branch not found" precondition. The decisive existence check is on the
// resolved current branch. A warning about the stale spawn-time branch is
// expected on stderr.
func TestMerge_StaleSpawnBranchAbsentDoesNotFail(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "spawn-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	// spawn-branch absent, follow-up-branch present.
	deps.BranchExists = func(repoRoot, branchName string) bool {
		return branchName == "follow-up-branch"
	}
	deps.CurrentBranch = func(repoRoot string) (string, error) {
		if repoRoot == "/worktree/target" {
			return "follow-up-branch", nil
		}
		return "main", nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err != nil {
		t.Fatalf("merge should proceed when stale spawn branch is absent but resolved branch exists; got: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "spawn-branch") {
		t.Errorf("stderr should warn about stale spawn-time branch %q, got: %q", "spawn-branch", output)
	}
}

func TestMerge_ConfigLoadError(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "suspended", LastReportState: "complete", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.LoadConfig = func(sprawlRoot string) (*config.Config, error) {
		return nil, fmt.Errorf("permission denied reading config.yaml")
	}

	err := runMerge(context.Background(), deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error from config load failure")
	}
	if !strings.Contains(err.Error(), "loading config") {
		t.Errorf("error should mention loading config, got: %v", err)
	}
}

// TestResolveMergeDeps_MergeDepsBindEveryGitSeam — QUM-1090 wiring gate for
// the CLI path. Twin of internal/supervisor's; see that test for why both
// construction sites need one. Negative control: drop any binding from
// merge.RealDeps and this names it.
func TestResolveMergeDeps_MergeDepsBindEveryGitSeam(t *testing.T) {
	saved := defaultMergeDeps
	defaultMergeDeps = nil
	t.Cleanup(func() { defaultMergeDeps = saved })

	d := resolveMergeDeps().NewMergeDeps()
	missing, checked := merge.NilSeams(d)
	if len(missing) != 0 {
		t.Errorf("resolveMergeDeps left merge.Deps seams nil: %v", missing)
	}
	if checked < merge.MinDepsSeams {
		t.Errorf("NilSeams examined %d seams, want >= %d; the walk looks broken", checked, merge.MinDepsSeams)
	}
	// Not a func, so skipped by the walk; Merge writes to it unconditionally.
	if d.Stderr == nil {
		t.Error("resolveMergeDeps left merge.Deps.Stderr nil")
	}
}

// TestMerge_MessageOverrideIsRefused pins that --message FAILS rather than being
// silently ignored (QUM-1087).
//
// Refusal rather than removal, because the two surfaces fail differently:
// cobra errors on an unknown flag, but encoding/json silently DROPS an unknown
// property, so deleting `message:` from the MCP tool would make it a no-op that
// agents keep passing and nobody is told about. One surface cannot fail loudly
// by deletion, so both are made to fail loudly by refusal.
func TestMerge_MessageOverrideIsRefused(t *testing.T) {
	var doMergeCalled bool
	deps := &mergeDeps{
		Getenv: func(k string) string {
			switch k {
			case "SPRAWL_ROOT":
				return t.TempDir()
			case "SPRAWL_AGENT_IDENTITY":
				return "weave"
			}
			return ""
		},
		DoMerge: func(_ context.Context, _ *merge.Config, _ *merge.Deps) (*merge.Result, error) {
			doMergeCalled = true
			return &merge.Result{}, nil
		},
		Stderr: io.Discard,
	}

	err := runMerge(context.Background(), deps, "target-agent", "custom msg", false, false)
	if err == nil {
		t.Fatal("expected --message to be refused")
	}
	if !errors.Is(err, agentops.ErrMessageOverrideRetired) {
		t.Errorf("want ErrMessageOverrideRetired, got: %v", err)
	}
	// Refused BEFORE doing any work: a caller who asked for something the
	// engine cannot do should not have most of a merge performed first.
	if doMergeCalled {
		t.Error("the merge ran despite the message override being invalid")
	}
	// The remedy must be actionable (/cli-ux-best-practices: every command
	// tells the caller what to do next).
	if !strings.Contains(err.Error(), "squash") {
		t.Errorf("the error must name the remedy (squash on the agent's branch first), got: %v", err)
	}
}
