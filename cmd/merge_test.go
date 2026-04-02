package cmd

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/dmotles/dendra/internal/state"
)

func newTestMergeDeps(t *testing.T) (*mergeDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()

	deps := &mergeDeps{
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "parent-agent"
			}
			return ""
		},
		loadAgent:      state.LoadAgent,
		listAgents:     state.ListAgents,
		gitMergeSquash: func(worktree, branch string) error { return nil },
		gitCommit:      func(worktree, message string) error { return nil },
		gitMergeAbort:  func(worktree string) error { return nil },
		gitStatus:      func(worktree string) (string, error) { return "", nil },
		branchExists:   func(repoRoot, branchName string) bool { return true },
		currentBranch:  func(repoRoot string) (string, error) { return "caller-branch", nil },
	}

	os.MkdirAll(state.AgentsDir(tmpDir), 0755)

	return deps, tmpDir
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

	var squashWorktree, squashBranch string
	deps.gitMergeSquash = func(worktree, branch string) error {
		squashWorktree = worktree
		squashBranch = branch
		return nil
	}

	var commitCalled bool
	deps.gitCommit = func(worktree, message string) error {
		commitCalled = true
		return nil
	}

	err := runMerge(deps, "target-agent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if squashWorktree != "/worktree/parent" {
		t.Errorf("gitMergeSquash worktree = %q, want %q", squashWorktree, "/worktree/parent")
	}
	if squashBranch != "feature-branch" {
		t.Errorf("gitMergeSquash branch = %q, want %q", squashBranch, "feature-branch")
	}
	if !commitCalled {
		t.Error("expected gitCommit to be called")
	}
}

func TestMerge_AgentNotFound(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})

	err := runMerge(deps, "nonexistent", "")
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

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for subagent")
	}
	if !strings.Contains(err.Error(), `agent "target-agent" is a subagent and has no branch to merge`) {
		t.Errorf("error should mention subagent, got: %v", err)
	}
}

func TestMerge_NotParent(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	deps.getenv = func(key string) string {
		switch key {
		case "DENDRA_ROOT":
			return tmpDir
		case "DENDRA_AGENT_IDENTITY":
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

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for non-parent caller")
	}
	if !strings.Contains(err.Error(), `cannot merge "target-agent": you are not its parent (parent is "parent-agent")`) {
		t.Errorf("error should mention parent mismatch, got: %v", err)
	}
}

func TestMerge_NotDone(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
	})

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for non-done agent")
	}
	if !strings.Contains(err.Error(), `agent "target-agent" has not reported done (status: "active"). Use --force to merge anyway`) {
		t.Errorf("error should mention not done, got: %v", err)
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
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "child-two", Status: "active", Branch: "child-branch-2",
		Worktree: "/worktree/child2", Parent: "target-agent",
	})

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for active children")
	}
	if !strings.Contains(err.Error(), `agent "target-agent" has active children:`) {
		t.Errorf("error should mention active children, got: %v", err)
	}
	if !strings.Contains(err.Error(), "child-one") {
		t.Errorf("error should list child-one, got: %v", err)
	}
	if !strings.Contains(err.Error(), "child-two") {
		t.Errorf("error should list child-two, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Retire or cascade-retire them first") {
		t.Errorf("error should suggest retiring children, got: %v", err)
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

	deps.branchExists = func(repoRoot, branchName string) bool { return false }

	err := runMerge(deps, "target-agent", "")
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

	deps.gitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/parent" {
			return "M  some-file.go", nil
		}
		return "", nil
	}

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for dirty caller worktree")
	}
	if !strings.Contains(err.Error(), "your worktree has uncommitted changes. Commit or stash before merging") {
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

	deps.gitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/parent" {
			return "", nil
		}
		if worktree == "/worktree/target" {
			return "M  dirty-file.go", nil
		}
		return "", nil
	}

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for dirty agent worktree")
	}
	if !strings.Contains(err.Error(), `Agent "target-agent" has uncommitted changes in worktree. Ask the agent to commit, or use --force to discard.`) {
		t.Errorf("error should mention agent dirty worktree with agent name, got: %v", err)
	}
}

func TestMerge_ConflictAbortsAndErrors(t *testing.T) {
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

	mergeErr := errors.New("merge conflict in main.go")
	deps.gitMergeSquash = func(worktree, branch string) error {
		return mergeErr
	}

	var abortCalled bool
	deps.gitMergeAbort = func(worktree string) error {
		abortCalled = true
		return nil
	}

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error from merge conflict")
	}
	if !abortCalled {
		t.Error("expected gitMergeAbort to be called after merge conflict")
	}
}

func TestMerge_DefaultCommitMessage(t *testing.T) {
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

	var capturedMsg string
	deps.gitCommit = func(worktree, message string) error {
		capturedMsg = message
		return nil
	}

	err := runMerge(deps, "target-agent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := strings.Split(capturedMsg, "\n")
	if len(lines) == 0 {
		t.Fatal("commit message is empty")
	}

	if lines[0] != "target-agent: Completed the task" {
		t.Errorf("first line = %q, want %q", lines[0], "target-agent: Completed the task")
	}
	if !strings.Contains(capturedMsg, "Squash merge of branch 'feature-branch' into 'caller-branch'") {
		t.Errorf("message should contain squash merge line, got:\n%s", capturedMsg)
	}
	if !strings.Contains(capturedMsg, "Agent: target-agent (engineer, engineering)") {
		t.Errorf("message should contain agent info, got:\n%s", capturedMsg)
	}
	if !strings.Contains(capturedMsg, "Co-Authored-By: Claude <noreply@anthropic.com>") {
		t.Errorf("message should contain co-authored-by, got:\n%s", capturedMsg)
	}
}

func TestMerge_DefaultCommitMessage_EmptyReport(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "",
	})

	var capturedMsg string
	deps.gitCommit = func(worktree, message string) error {
		capturedMsg = message
		return nil
	}

	err := runMerge(deps, "target-agent", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := strings.Split(capturedMsg, "\n")
	if len(lines) == 0 {
		t.Fatal("commit message is empty")
	}

	if lines[0] != "target-agent: merge branch 'feature-branch'" {
		t.Errorf("first line = %q, want %q", lines[0], "target-agent: merge branch 'feature-branch'")
	}
}

func TestMerge_CustomMessage(t *testing.T) {
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

	var capturedMsg string
	deps.gitCommit = func(worktree, message string) error {
		capturedMsg = message
		return nil
	}

	err := runMerge(deps, "target-agent", "Custom merge message")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(capturedMsg, "Custom merge message") {
		t.Errorf("message should contain custom message, got:\n%s", capturedMsg)
	}
	if !strings.Contains(capturedMsg, "Co-Authored-By: Claude <noreply@anthropic.com>") {
		t.Errorf("message should contain co-authored-by, got:\n%s", capturedMsg)
	}
}

func TestMerge_MissingDendraRoot(t *testing.T) {
	deps, _ := newTestMergeDeps(t)

	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "parent-agent"
		}
		return ""
	}

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestMerge_MissingCallerIdentity(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return tmpDir
		}
		return ""
	}

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
	})

	err := runMerge(deps, "target-agent", "")
	if err == nil {
		t.Fatal("expected error for missing caller identity")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}
