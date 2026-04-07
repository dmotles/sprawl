package worktree

import (
	"testing"
)

// Compile-time interface check.
var _ Creator = (*mockCreator)(nil)

// mockCreator implements Creator for testing.
type mockCreator struct {
	worktreePath string
	branchName   string
	err          error
	calledWith   struct {
		repoRoot   string
		agentName  string
		branchName string
		baseBranch string
	}
}

func (m *mockCreator) Create(repoRoot, agentName, branchName, baseBranch string) (string, string, error) {
	m.calledWith.repoRoot = repoRoot
	m.calledWith.agentName = agentName
	m.calledWith.branchName = branchName
	m.calledWith.baseBranch = baseBranch
	return m.worktreePath, m.branchName, m.err
}

func TestMockCreator_ReturnsConfiguredValues(t *testing.T) {
	mock := &mockCreator{
		worktreePath: "/repo/.sprawl/worktrees/frank",
		branchName:   "sprawl/frank",
	}

	path, branch, err := mock.Create("/repo", "frank", "feature/frank-work", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/repo/.sprawl/worktrees/frank" {
		t.Errorf("path = %q, want %q", path, "/repo/.sprawl/worktrees/frank")
	}
	if branch != "sprawl/frank" {
		t.Errorf("branch = %q, want %q", branch, "sprawl/frank")
	}
	if mock.calledWith.repoRoot != "/repo" {
		t.Errorf("repoRoot = %q, want %q", mock.calledWith.repoRoot, "/repo")
	}
	if mock.calledWith.agentName != "frank" {
		t.Errorf("agentName = %q, want %q", mock.calledWith.agentName, "frank")
	}
	if mock.calledWith.branchName != "feature/frank-work" {
		t.Errorf("branchName = %q, want %q", mock.calledWith.branchName, "feature/frank-work")
	}
	if mock.calledWith.baseBranch != "main" {
		t.Errorf("baseBranch = %q, want %q", mock.calledWith.baseBranch, "main")
	}
}
