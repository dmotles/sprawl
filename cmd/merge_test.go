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
	deps.gitCommit = func(worktree, message string) (string, error) {
		capturedMsg = message
		return "abc1234", nil
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
	deps.gitCommit = func(worktree, message string) (string, error) {
		capturedMsg = message
		return "abc1234", nil
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
	deps.gitCommit = func(worktree, message string) (string, error) {
		capturedMsg = message
		return "abc1234", nil
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

	err := runMerge(deps, "target-agent", "")
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
	err := runMerge(deps, "target-agent", "")
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

	err := runMerge(deps, "target-agent", "")
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
	err := runMerge(deps, "target-agent", "")
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

	err := runMerge(deps, "ash", "")
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

	err := runMerge(deps, "target-agent", "")
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
