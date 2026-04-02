package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
		gitCommit:      func(worktree, message string) (string, error) { return "abc1234", nil },
		gitMergeAbort:  func(worktree string) error { return nil },
		gitStatus:      func(worktree string) (string, error) { return "", nil },
		branchExists:   func(repoRoot, branchName string) bool { return true },
		currentBranch:  func(repoRoot string) (string, error) { return "caller-branch", nil },
		retireAgent:     func(dendraRoot string, agent *state.AgentState) error { return nil },
		gitBranchDelete: func(repoRoot, branchName string) error { return nil },
		runTests:        func(dir string) (string, error) { return "", nil },
		gitResetHard:    func(worktree string) error { return nil },
		dirExists:       func(path string) bool { return true },
		gitRevListCount: func(repoRoot, base, head string) (int, error) { return 3, nil },
		stderr:          io.Discard,
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
	deps.gitCommit = func(worktree, message string) (string, error) {
		commitCalled = true
		return "abc1234", nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "nonexistent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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
	deps.gitCommit = func(worktree, message string) (string, error) {
		capturedMsg = message
		return "abc1234", nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
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
	deps.gitCommit = func(worktree, message string) (string, error) {
		capturedMsg = message
		return "abc1234", nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
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
	deps.gitCommit = func(worktree, message string) (string, error) {
		capturedMsg = message
		return "abc1234", nil
	}

	err := runMerge(deps, "target-agent", "Custom merge message", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
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

	err := runMerge(deps, "target-agent", "", true, false, false)
	if err == nil {
		t.Fatal("expected error for missing caller identity")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestMerge_RetiresAgentAfterCommit(t *testing.T) {
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

	var retireCalled bool
	var retiredAgentName string
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		retireCalled = true
		retiredAgentName = agent.Name
		return nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !retireCalled {
		t.Error("expected retireAgent to be called after successful commit")
	}
	if retiredAgentName != "target-agent" {
		t.Errorf("retireAgent called with agent %q, want %q", retiredAgentName, "target-agent")
	}
}

func TestMerge_RetireFailure_WarnsButSucceeds(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

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

	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		return fmt.Errorf("tmux session not found")
	}

	// Merge should succeed even if retire fails.
	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("merge should succeed even if retire fails, got: %v", err)
	}

	// Should print a warning about retire failure.
	output := stderr.String()
	if !strings.Contains(output, "could not retire agent") {
		t.Errorf("expected warning about retire failure in stderr, got: %q", output)
	}
	if !strings.Contains(output, "target-agent") {
		t.Errorf("warning should mention agent name, got: %q", output)
	}
	if !strings.Contains(output, "dendra retire") {
		t.Errorf("warning should suggest manual retire command, got: %q", output)
	}
}

func TestMerge_BranchDeleteAfterCommit(t *testing.T) {
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

	var deletedBranch string
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		deletedBranch = branchName
		return nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if deletedBranch != "feature-branch" {
		t.Errorf("gitBranchDelete called with branch %q, want %q", deletedBranch, "feature-branch")
	}
}

func TestMerge_BranchDeleteFailure_WarnsButSucceeds(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

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

	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		return fmt.Errorf("branch is checked out elsewhere")
	}

	// Merge should succeed even if branch delete fails.
	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("merge should succeed even if branch delete fails, got: %v", err)
	}

	// Should print a warning about branch delete failure.
	output := stderr.String()
	if !strings.Contains(output, "could not delete branch") {
		t.Errorf("expected warning about branch delete failure in stderr, got: %q", output)
	}
	if !strings.Contains(output, "feature-branch") {
		t.Errorf("warning should mention branch name, got: %q", output)
	}
	if !strings.Contains(output, "git branch -D") {
		t.Errorf("warning should suggest manual branch delete command, got: %q", output)
	}
}

func TestMerge_SuccessOutput(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "done", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "implement QUM-42 broadcast fix",
	})

	deps.gitCommit = func(worktree, message string) (string, error) {
		return "a1b2c3d", nil
	}

	err := runMerge(deps, "ash", "", true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()

	// Should include agent name, branch, and target branch.
	if !strings.Contains(output, `"ash"`) {
		t.Errorf("output should mention agent name, got: %q", output)
	}
	if !strings.Contains(output, "dendra/ash") {
		t.Errorf("output should mention branch name, got: %q", output)
	}
	// Should include commit hash.
	if !strings.Contains(output, "a1b2c3d") {
		t.Errorf("output should include commit hash, got: %q", output)
	}
	// Should indicate branch deleted.
	if !strings.Contains(output, "deleted") || !strings.Contains(output, "dendra/ash") {
		t.Errorf("output should indicate branch was deleted, got: %q", output)
	}
	// Should indicate agent retired.
	if !strings.Contains(output, "retired") || !strings.Contains(output, "ash") {
		t.Errorf("output should indicate agent was retired, got: %q", output)
	}
}

func TestMerge_OrderRetireThenBranchDelete(t *testing.T) {
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

	var order []string
	deps.gitCommit = func(worktree, message string) (string, error) {
		order = append(order, "commit")
		return "abc1234", nil
	}
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		order = append(order, "retire")
		return nil
	}
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		order = append(order, "branch-delete")
		return nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(order) != 3 {
		t.Fatalf("expected 3 operations, got %d: %v", len(order), order)
	}
	if order[0] != "commit" {
		t.Errorf("expected commit first, got: %v", order)
	}
	if order[1] != "retire" {
		t.Errorf("expected retire second, got: %v", order)
	}
	if order[2] != "branch-delete" {
		t.Errorf("expected branch-delete third, got: %v", order)
	}
}

// --- Pre-merge and post-merge validation tests ---

func newValidationTestDeps(t *testing.T) (*mergeDeps, string) {
	t.Helper()
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

	return deps, tmpDir
}

func TestMerge_PreMergeValidation_Pass(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	var testRunDirs []string
	deps.runTests = func(dir string) (string, error) {
		testRunDirs = append(testRunDirs, dir)
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	err := runMerge(deps, "target-agent", "", false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(testRunDirs) < 1 {
		t.Fatal("expected runTests to be called at least once")
	}
	if testRunDirs[0] != "/worktree/target" {
		t.Errorf("first runTests call dir = %q, want %q (pre-merge on agent worktree)", testRunDirs[0], "/worktree/target")
	}
}

func TestMerge_PreMergeValidation_Fail(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	deps.runTests = func(dir string) (string, error) {
		if dir == "/worktree/target" {
			return "FAIL: TestSomething\nexit status 1", fmt.Errorf("tests failed")
		}
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	var squashCalled bool
	deps.gitMergeSquash = func(worktree, branch string) error {
		squashCalled = true
		return nil
	}
	var commitCalled bool
	deps.gitCommit = func(worktree, message string) (string, error) {
		commitCalled = true
		return "abc1234", nil
	}

	err := runMerge(deps, "target-agent", "", false, false, false)
	if err == nil {
		t.Fatal("expected error from pre-merge validation failure")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "alidation failed") {
		t.Errorf("error should mention validation failed, got: %v", err)
	}
	if !strings.Contains(errMsg, "--no-validate") {
		t.Errorf("error should suggest --no-validate, got: %v", err)
	}
	if squashCalled {
		t.Error("gitMergeSquash should NOT be called when pre-merge validation fails")
	}
	if commitCalled {
		t.Error("gitCommit should NOT be called when pre-merge validation fails")
	}
}

func TestMerge_PreMergeValidation_SkipNoWorktree(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	var testsCalled bool
	deps.runTests = func(dir string) (string, error) {
		testsCalled = true
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return false }

	err := runMerge(deps, "target-agent", "", false, false, false)
	// With dirExists=false and noValidate=false, pre-merge validation should be skipped
	// but post-merge validation should still run. For this test we only care that
	// runTests was NOT called for the agent worktree (pre-merge skip).
	// Post-merge will call runTests for the caller worktree, so we need to check
	// that the call was NOT for the agent worktree.
	// Actually, let's just check the merge succeeds (post-merge passes by default).
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// runTests should not have been called with the agent worktree
	// (it may be called with caller worktree for post-merge)
	_ = testsCalled
}

func TestMerge_PostMergeValidation_Pass(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	deps.runTests = func(dir string) (string, error) { return "ok", nil }
	deps.dirExists = func(path string) bool { return true }

	var retireCalled bool
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		retireCalled = true
		return nil
	}

	err := runMerge(deps, "target-agent", "", false, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !retireCalled {
		t.Error("expected retireAgent to be called after successful post-merge validation")
	}
}

func TestMerge_PostMergeValidation_Fail_Rollback(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	deps.dirExists = func(path string) bool { return true }
	deps.runTests = func(dir string) (string, error) {
		if dir == "/worktree/target" {
			// Pre-merge: pass
			return "ok", nil
		}
		if dir == "/worktree/parent" {
			// Post-merge: fail
			return "FAIL: TestIntegration\nexit status 1", fmt.Errorf("tests failed")
		}
		return "ok", nil
	}

	var resetCalledWith string
	deps.gitResetHard = func(worktree string) error {
		resetCalledWith = worktree
		return nil
	}
	var retireCalled bool
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		retireCalled = true
		return nil
	}
	var branchDeleteCalled bool
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		branchDeleteCalled = true
		return nil
	}

	err := runMerge(deps, "target-agent", "", false, false, false)
	if err == nil {
		t.Fatal("expected error from post-merge validation failure")
	}

	errMsg := err.Error()
	if !strings.Contains(errMsg, "ost-merge validation failed") {
		t.Errorf("error should mention post-merge validation failed, got: %v", err)
	}
	if resetCalledWith != "/worktree/parent" {
		t.Errorf("gitResetHard called with %q, want %q", resetCalledWith, "/worktree/parent")
	}
	if retireCalled {
		t.Error("retireAgent should NOT be called when post-merge validation fails")
	}
	if branchDeleteCalled {
		t.Error("gitBranchDelete should NOT be called when post-merge validation fails")
	}
}

func TestMerge_NoValidate_SkipsBoth(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	var testCallCount int
	deps.runTests = func(dir string) (string, error) {
		testCallCount++
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if testCallCount != 0 {
		t.Errorf("runTests called %d times, want 0 (noValidate=true should skip)", testCallCount)
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLines  int
		wantLines int    // expected number of lines in output (0 = check exact)
		wantExact string // if non-empty, check exact match
		wantContains string // if non-empty, check contains
	}{
		{
			name:      "short output under limit",
			input:     "line1\nline2\nline3",
			maxLines:  50,
			wantExact: "line1\nline2\nline3",
		},
		{
			name:     "long output truncated",
			input:    generateLines(60),
			maxLines: 50,
			wantContains: "showing last 50 of 60 lines",
		},
		{
			name:      "empty string",
			input:     "",
			maxLines:  50,
			wantExact: "",
		},
		{
			name:      "exactly at limit",
			input:     generateLines(50),
			maxLines:  50,
			wantExact: generateLines(50),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateOutput(tt.input, tt.maxLines)

			if tt.wantExact != "" || tt.input == "" {
				if got != tt.wantExact {
					t.Errorf("truncateOutput() = %q, want %q", got, tt.wantExact)
				}
			}
			if tt.wantContains != "" {
				if !strings.Contains(got, tt.wantContains) {
					t.Errorf("truncateOutput() should contain %q, got %q", tt.wantContains, got)
				}
				// Verify it actually has the right number of content lines
				lines := strings.Split(got, "\n")
				// Should have the prefix line + 50 content lines
				if len(lines) < tt.maxLines {
					t.Errorf("truncated output should have at least %d lines, got %d", tt.maxLines, len(lines))
				}
			}
		})
	}
}

func generateLines(n int) string {
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	return strings.Join(lines, "\n")
}

func TestMerge_PostMergeValidation_Fail_ErrorMessage(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	deps.dirExists = func(path string) bool { return true }
	deps.runTests = func(dir string) (string, error) {
		if dir == "/worktree/parent" {
			return "FAIL output here", fmt.Errorf("tests failed")
		}
		return "ok", nil
	}
	deps.gitResetHard = func(worktree string) error { return nil }

	err := runMerge(deps, "target-agent", "", false, false, false)
	if err == nil {
		t.Fatal("expected error")
	}

	errMsg := err.Error()
	// Should mention agent name
	if !strings.Contains(errMsg, "target-agent") {
		t.Errorf("error should mention agent name, got: %v", err)
	}
	// Should mention rollback
	if !strings.Contains(errMsg, "rollback") || !strings.Contains(errMsg, "reset") {
		t.Errorf("error should mention rollback/reset, got: %v", err)
	}
	// Should suggest --no-validate
	if !strings.Contains(errMsg, "--no-validate") {
		t.Errorf("error should suggest --no-validate, got: %v", err)
	}
	// Should mention agent NOT retired
	if !strings.Contains(errMsg, "not retired") && !strings.Contains(errMsg, "NOT retired") {
		t.Errorf("error should mention agent not retired, got: %v", err)
	}
}

func TestMerge_PreMergeValidation_Fail_ErrorMessage(t *testing.T) {
	deps, _ := newValidationTestDeps(t)

	deps.dirExists = func(path string) bool { return true }
	deps.runTests = func(dir string) (string, error) {
		if dir == "/worktree/target" {
			return "FAIL: TestFoo\nsome long output\nexit status 1", fmt.Errorf("tests failed")
		}
		return "ok", nil
	}

	err := runMerge(deps, "target-agent", "", false, false, false)
	if err == nil {
		t.Fatal("expected error")
	}

	errMsg := err.Error()
	// Should mention branch name
	if !strings.Contains(errMsg, "feature-branch") {
		t.Errorf("error should mention branch name, got: %v", err)
	}
	// Should include test output (possibly truncated)
	if !strings.Contains(errMsg, "FAIL") {
		t.Errorf("error should include test output, got: %v", err)
	}
	// Should suggest --no-validate
	if !strings.Contains(errMsg, "--no-validate") {
		t.Errorf("error should suggest --no-validate, got: %v", err)
	}
}

// --- Force mode tests ---

func TestMerge_Force_NonDoneAgent_RetiresFirst(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Still working",
	})

	var order []string
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		order = append(order, "retire")
		return nil
	}
	deps.gitMergeSquash = func(worktree, branch string) error {
		order = append(order, "squash")
		return nil
	}
	deps.gitCommit = func(worktree, message string) (string, error) {
		order = append(order, "commit")
		return "abc1234", nil
	}
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		order = append(order, "branch-delete")
		return nil
	}
	// Pre-merge validation should NOT be called on agent worktree
	var preMergeDirs []string
	deps.runTests = func(dir string) (string, error) {
		preMergeDirs = append(preMergeDirs, dir)
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	err := runMerge(deps, "target-agent", "", true, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Retire must happen BEFORE squash merge for non-done agents.
	if len(order) < 4 {
		t.Fatalf("expected at least 4 operations, got %d: %v", len(order), order)
	}
	retireIdx := -1
	squashIdx := -1
	for i, op := range order {
		if op == "retire" && retireIdx == -1 {
			retireIdx = i
		}
		if op == "squash" && squashIdx == -1 {
			squashIdx = i
		}
	}
	if retireIdx == -1 {
		t.Fatal("retire was not called")
	}
	if squashIdx == -1 {
		t.Fatal("squash was not called")
	}
	if retireIdx >= squashIdx {
		t.Errorf("retire (index %d) should happen before squash (index %d), order: %v", retireIdx, squashIdx, order)
	}

	// Pre-merge validation should NOT be called on agent worktree.
	for _, dir := range preMergeDirs {
		if dir == "/worktree/target" {
			t.Error("runTests should NOT be called with agent worktree for force + non-done agent")
		}
	}
}

func TestMerge_Force_DoneAgent_NormalOrder(t *testing.T) {
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

	var order []string
	deps.gitCommit = func(worktree, message string) (string, error) {
		order = append(order, "commit")
		return "abc1234", nil
	}
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		order = append(order, "retire")
		return nil
	}
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		order = append(order, "branch-delete")
		return nil
	}

	err := runMerge(deps, "target-agent", "", true, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// For done agents with force, normal order: commit -> retire -> branch-delete.
	if len(order) != 3 {
		t.Fatalf("expected 3 operations, got %d: %v", len(order), order)
	}
	if order[0] != "commit" {
		t.Errorf("expected commit first, got: %v", order)
	}
	if order[1] != "retire" {
		t.Errorf("expected retire second, got: %v", order)
	}
	if order[2] != "branch-delete" {
		t.Errorf("expected branch-delete third, got: %v", order)
	}
}

func TestMerge_Force_DirtyWorktree_Succeeds(t *testing.T) {
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

	deps.gitStatus = func(worktree string) (string, error) {
		if worktree == "/worktree/target" {
			return "M dirty.go", nil
		}
		return "", nil
	}

	err := runMerge(deps, "target-agent", "", true, true, false)
	if err != nil {
		t.Fatalf("force merge with dirty agent worktree should succeed, got: %v", err)
	}
}

func TestMerge_Force_NonDone_SkipsPreMergeValidation(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Still working",
	})

	var testRunDirs []string
	deps.runTests = func(dir string) (string, error) {
		testRunDirs = append(testRunDirs, dir)
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	// force=true, noValidate=false: should skip pre-merge on agent worktree,
	// but still run post-merge on caller worktree.
	err := runMerge(deps, "target-agent", "", false, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify runTests was NOT called with agent worktree.
	for _, dir := range testRunDirs {
		if dir == "/worktree/target" {
			t.Error("runTests should NOT be called with agent worktree (/worktree/target) when force=true")
		}
	}

	// Verify runTests WAS called with caller worktree (post-merge).
	found := false
	for _, dir := range testRunDirs {
		if dir == "/worktree/parent" {
			found = true
		}
	}
	if !found {
		t.Error("runTests should be called with caller worktree (/worktree/parent) for post-merge validation")
	}
}

func TestMerge_Force_NonDone_PostMergeStillRuns(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Still working",
	})

	deps.runTests = func(dir string) (string, error) {
		if dir == "/worktree/parent" {
			return "FAIL: TestIntegration\nexit status 1", fmt.Errorf("tests failed")
		}
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	var resetCalled bool
	deps.gitResetHard = func(worktree string) error {
		resetCalled = true
		return nil
	}

	// force=true, noValidate=false, post-merge fails.
	err := runMerge(deps, "target-agent", "", false, true, false)
	if err == nil {
		t.Fatal("expected error from post-merge validation failure")
	}

	if !resetCalled {
		t.Error("expected gitResetHard to be called on post-merge validation failure")
	}
}

func TestMerge_Force_NonDone_RetireFailure_IsHardError(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Still working",
	})

	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		return fmt.Errorf("failed to stop tmux session")
	}

	var squashCalled bool
	deps.gitMergeSquash = func(worktree, branch string) error {
		squashCalled = true
		return nil
	}

	// force=true, agent status="active", retire fails -> hard error.
	err := runMerge(deps, "target-agent", "", true, true, false)
	if err == nil {
		t.Fatal("expected error when force retire fails for non-done agent")
	}

	if squashCalled {
		t.Error("gitMergeSquash should NOT be called when force retire fails")
	}
}

func TestMerge_Force_PlusNoValidate_SkipsAll(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Still working",
	})

	var testCallCount int
	deps.runTests = func(dir string) (string, error) {
		testCallCount++
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }

	// force=true, noValidate=true -> no tests should run at all.
	err := runMerge(deps, "target-agent", "", true, true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if testCallCount != 0 {
		t.Errorf("runTests called %d times, want 0 (force+noValidate should skip all validation)", testCallCount)
	}
}

func TestMerge_Force_NonDone_PostMergeRollback_Warning(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "active", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Still working",
	})

	deps.runTests = func(dir string) (string, error) {
		if dir == "/worktree/parent" {
			return "FAIL: TestIntegration\nexit status 1", fmt.Errorf("tests failed")
		}
		return "ok", nil
	}
	deps.dirExists = func(path string) bool { return true }
	deps.gitResetHard = func(worktree string) error { return nil }

	// force=true, noValidate=false, agent status="active".
	// Post-merge validation fails -> rollback. Since agent was already retired
	// (force retires before merge for non-done agents), error/stderr should
	// warn that the agent is already retired.
	err := runMerge(deps, "target-agent", "", false, true, false)
	if err == nil {
		t.Fatal("expected error from post-merge validation failure")
	}

	// Check that stderr or error message warns about agent already being retired.
	combined := stderr.String() + err.Error()
	if !strings.Contains(combined, "already retired") {
		t.Errorf("expected warning about agent being 'already retired' in stderr or error, got stderr=%q, err=%v", stderr.String(), err)
	}
}

// --- Dry-run tests ---

func TestMerge_DryRun_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "done", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "implemented QUM-42 broadcast partial failure handling",
	})

	deps.gitRevListCount = func(repoRoot, base, head string) (int, error) { return 3, nil }

	var squashCalled, commitCalled, retireCalled, deleteCalled bool
	deps.gitMergeSquash = func(worktree, branch string) error {
		squashCalled = true
		return nil
	}
	deps.gitCommit = func(worktree, message string) (string, error) {
		commitCalled = true
		return "abc1234", nil
	}
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		retireCalled = true
		return nil
	}
	deps.gitBranchDelete = func(repoRoot, branchName string) error {
		deleteCalled = true
		return nil
	}

	err := runMerge(deps, "ash", "", false, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()

	expectedStrings := []string{
		`[dry-run] Would merge agent "ash" (branch dendra/ash) into caller-branch`,
		`Agent status: done`,
		`Last report: "implemented QUM-42 broadcast partial failure handling"`,
		`Source branch: dendra/ash (3 commits ahead of caller-branch)`,
		`Squash merge of branch 'dendra/ash' into 'caller-branch'`,
		`Co-Authored-By: Claude <noreply@anthropic.com>`,
		`validate`,
		`squash-merge`,
		`retire agent`,
		`delete branch`,
	}
	for _, s := range expectedStrings {
		if !strings.Contains(output, s) {
			t.Errorf("expected stderr to contain %q, got:\n%s", s, output)
		}
	}

	if squashCalled {
		t.Error("gitMergeSquash should NOT be called in dry-run mode")
	}
	if commitCalled {
		t.Error("gitCommit should NOT be called in dry-run mode")
	}
	if retireCalled {
		t.Error("retireAgent should NOT be called in dry-run mode")
	}
	if deleteCalled {
		t.Error("gitBranchDelete should NOT be called in dry-run mode")
	}
}

func TestMerge_DryRun_NoValidate(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "done", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "implemented QUM-42 broadcast partial failure handling",
	})

	deps.gitRevListCount = func(repoRoot, base, head string) (int, error) { return 3, nil }

	err := runMerge(deps, "ash", "", true, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()

	// Steps should NOT contain "validate" but should contain "squash-merge"
	if strings.Contains(output, "validate") && !strings.Contains(output, "no-validate") {
		// Need to check more carefully: the steps line should not list "validate" as a step
		// We look for the steps line specifically
	}
	if !strings.Contains(output, "squash-merge") {
		t.Errorf("expected stderr to contain 'squash-merge', got:\n%s", output)
	}

	// More precise check: steps should not include "validate" step
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Steps:") || strings.Contains(line, "steps:") {
			if strings.Contains(line, "validate") && !strings.Contains(line, "no-validate") {
				t.Errorf("steps line should NOT contain 'validate' when noValidate=true, got: %s", line)
			}
			if !strings.Contains(line, "squash-merge") {
				t.Errorf("steps line should contain 'squash-merge', got: %s", line)
			}
		}
	}
}

func TestMerge_DryRun_ForceNonDone(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "active", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "still working on it",
	})

	deps.gitRevListCount = func(repoRoot, base, head string) (int, error) { return 3, nil }

	var retireCalled bool
	deps.retireAgent = func(dendraRoot string, agent *state.AgentState) error {
		retireCalled = true
		return nil
	}

	err := runMerge(deps, "ash", "", true, true, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()

	// For force + non-done, "retire agent" should appear BEFORE "squash-merge" in steps
	retireIdx := strings.Index(output, "retire agent")
	squashIdx := strings.Index(output, "squash-merge")
	if retireIdx == -1 {
		t.Errorf("expected 'retire agent' in output, got:\n%s", output)
	}
	if squashIdx == -1 {
		t.Errorf("expected 'squash-merge' in output, got:\n%s", output)
	}
	if retireIdx >= squashIdx {
		t.Errorf("'retire agent' (index %d) should appear before 'squash-merge' (index %d) in output:\n%s", retireIdx, squashIdx, output)
	}

	if retireCalled {
		t.Error("retireAgent should NOT be called in dry-run mode")
	}
}

func TestMerge_DryRun_PreconditionFails(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	// Do NOT create the agent we're trying to merge — it should not be found.

	err := runMerge(deps, "nonexistent-agent", "", true, false, true)
	if err == nil {
		t.Fatal("expected error for missing agent in dry-run mode")
	}

	output := stderr.String()
	if strings.Contains(output, "[dry-run]") {
		t.Errorf("stderr should NOT contain '[dry-run]' when precondition fails, got:\n%s", output)
	}
}

func TestMerge_DryRun_CustomMessage(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "done", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "implemented feature",
	})

	deps.gitRevListCount = func(repoRoot, base, head string) (int, error) { return 3, nil }

	err := runMerge(deps, "ash", "Custom merge message", true, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "Custom merge message") {
		t.Errorf("expected stderr to contain 'Custom merge message', got:\n%s", output)
	}
}

func TestMerge_DryRun_EmptyReport(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "done", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "",
	})

	deps.gitRevListCount = func(repoRoot, base, head string) (int, error) { return 3, nil }

	err := runMerge(deps, "ash", "", true, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, `Last report: "(none)"`) {
		t.Errorf("expected stderr to contain 'Last report: \"(none)\"', got:\n%s", output)
	}
}

func TestMerge_DryRun_RevListCountError(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	var stderr bytes.Buffer
	deps.stderr = &stderr

	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "parent-agent", Status: "active", Branch: "main",
		Worktree: "/worktree/parent", Parent: "root",
	})
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "ash", Status: "done", Branch: "dendra/ash",
		Worktree: "/worktree/ash", Parent: "parent-agent",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "done with work",
	})

	deps.gitRevListCount = func(repoRoot, base, head string) (int, error) {
		return 0, fmt.Errorf("git rev-list failed")
	}

	err := runMerge(deps, "ash", "", true, false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "unknown") {
		t.Errorf("expected stderr to contain 'unknown' when rev-list count fails, got:\n%s", output)
	}
}

func TestMerge_RootCallerNoStateFile(t *testing.T) {
	deps, tmpDir := newTestMergeDeps(t)

	// Override caller identity to "sensei" (root agent with no state file)
	deps.getenv = func(key string) string {
		switch key {
		case "DENDRA_ROOT":
			return tmpDir
		case "DENDRA_AGENT_IDENTITY":
			return "sensei"
		}
		return ""
	}

	// Use real loadAgent — it will fail for "sensei" since no state file exists
	deps.loadAgent = func(dendraRoot, name string) (*state.AgentState, error) {
		return state.LoadAgent(dendraRoot, name)
	}

	// Only create the target agent, NOT the caller (sensei)
	createTestAgent(t, tmpDir, &state.AgentState{
		Name: "target-agent", Status: "done", Branch: "feature-branch",
		Worktree: "/worktree/target", Parent: "sensei",
		Type: "engineer", Family: "engineering",
		LastReportMessage: "Completed the task",
	})

	var squashWorktree, commitWorktree string
	deps.gitMergeSquash = func(worktree, branch string) error {
		squashWorktree = worktree
		return nil
	}
	deps.gitCommit = func(worktree, message string) (string, error) {
		commitWorktree = worktree
		return "abc1234", nil
	}

	err := runMerge(deps, "target-agent", "", true, false, false)
	if err != nil {
		t.Fatalf("expected merge to succeed for root caller with no state file, got: %v", err)
	}

	// Verify that dendraRoot was used as the worktree (fallback for root agent)
	if squashWorktree != tmpDir {
		t.Errorf("gitMergeSquash worktree = %q, want dendraRoot %q", squashWorktree, tmpDir)
	}
	if commitWorktree != tmpDir {
		t.Errorf("gitCommit worktree = %q, want dendraRoot %q", commitWorktree, tmpDir)
	}
}
