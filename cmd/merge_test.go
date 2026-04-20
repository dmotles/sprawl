package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

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
		LoadAgent:     state.LoadAgent,
		ListAgents:    state.ListAgents,
		GitStatus:     func(worktree string) (string, error) { return "", nil },
		BranchExists:  func(repoRoot, branchName string) bool { return true },
		CurrentBranch: func(repoRoot string) (string, error) { return "main", nil },
		LoadConfig: func(sprawlRoot string) (*config.Config, error) {
			return &config.Config{Validate: "make validate"}, nil
		},
		DoMerge: func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error) {
			return &merge.Result{CommitHash: "abc1234"}, nil
		},
		NewMergeDeps: func() *merge.Deps { return &merge.Deps{} },
		Stderr:       io.Discard,
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)

	return deps, tmpDir
}

func TestMerge_InvalidAgentNameReturnsError(t *testing.T) {
	deps, _ := newTestMergeDeps(t)
	err := runMerge(deps, "../evil", "", false, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Completed the task",
	})

	var mergeCalled bool
	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		mergeCalled = true
		return &merge.Result{CommitHash: "abc1234"}, nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	err := runMerge(deps, "target-agent", "", true, false)
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

	err := runMerge(deps, "nonexistent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Subagent: true,
	})

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
	})

	err := runMerge(deps, "target-agent", "", true, false)
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

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	err := runMerge(deps, "target-agent", "", true, false)
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

	err := runMerge(deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for non-active/non-done agent")
	}
	if !strings.Contains(err.Error(), "cannot be merged") {
		t.Errorf("error should mention cannot be merged, got: %v", err)
	}
}

func TestMerge_ActiveChildren(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "child-one", Status: "active", Branch: "child-branch-1",
		Worktree: "/worktree/child1", Parent: "target-agent",
	})

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.BranchExists = func(repoRoot, branchName string) bool { return false }

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.GitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/parent" {
			return "M  some-file.go", nil
		}
		return "", nil
	}

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.GitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/target" {
			return "M  dirty-file.go", nil
		}
		return "", nil
	}

	err := runMerge(deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error for dirty agent worktree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should mention dirty worktree, got: %v", err)
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

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
	})

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return &merge.Result{WasNoOp: true}, nil
	}

	var stderr bytes.Buffer
	deps.Stderr = &stderr

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return nil, fmt.Errorf("rebase conflict in main.go")
	}

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "did stuff",
	})

	var capturedCfg *merge.Config
	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{CommitHash: "abc"}, nil
	}

	err := runMerge(deps, "target-agent", "custom msg", false, false)
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
	if capturedCfg.MessageOverride != "custom msg" {
		t.Errorf("MessageOverride = %q, want 'custom msg'", capturedCfg.MessageOverride)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	var capturedCfg *merge.Config
	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{}, nil
	}

	err := runMerge(deps, "target-agent", "", true, true)
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
		Name: "finn", Status: "done", Branch: "sprawl/finn",
		Worktree: "/worktree/finn", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "implement QUM-42 broadcast fix",
	})

	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		return &merge.Result{CommitHash: "a1b2c3d"}, nil
	}

	err := runMerge(deps, "finn", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	var configLoadedFrom string
	deps.LoadConfig = func(sprawlRoot string) (*config.Config, error) {
		configLoadedFrom = sprawlRoot
		return &config.Config{Validate: "go test ./..."}, nil
	}

	var capturedCfg *merge.Config
	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{CommitHash: "abc1234"}, nil
	}

	err := runMerge(deps, "target-agent", "", true, false)
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
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.LoadConfig = func(sprawlRoot string) (*config.Config, error) {
		return &config.Config{}, nil
	}

	var capturedCfg *merge.Config
	deps.DoMerge = func(cfg *merge.Config, d *merge.Deps) (*merge.Result, error) {
		capturedCfg = cfg
		return &merge.Result{CommitHash: "abc1234"}, nil
	}

	err := runMerge(deps, "target-agent", "", true, false)
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

func TestMerge_ConfigLoadError(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
	})

	deps.LoadConfig = func(sprawlRoot string) (*config.Config, error) {
		return nil, fmt.Errorf("permission denied reading config.yaml")
	}

	err := runMerge(deps, "target-agent", "", true, false)
	if err == nil {
		t.Fatal("expected error from config load failure")
	}
	if !strings.Contains(err.Error(), "loading config") {
		t.Errorf("error should mention loading config, got: %v", err)
	}
}
