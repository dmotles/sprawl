package worktree

import (
	"testing"
)

// mockCreator implements Creator for testing.
type mockCreator struct {
	worktreePath string
	branchName   string
	err          error
	calledWith   struct {
		repoRoot   string
		agentName  string
		baseBranch string
	}
}

func (m *mockCreator) Create(repoRoot, agentName, baseBranch string) (string, string, error) {
	m.calledWith.repoRoot = repoRoot
	m.calledWith.agentName = agentName
	m.calledWith.baseBranch = baseBranch
	return m.worktreePath, m.branchName, m.err
}

func TestMockCreator_ReturnsConfiguredValues(t *testing.T) {
	mock := &mockCreator{
		worktreePath: "/repo/.dendra/worktrees/frank",
		branchName:   "dendra/frank",
	}

	path, branch, err := mock.Create("/repo", "frank", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "/repo/.dendra/worktrees/frank" {
		t.Errorf("path = %q, want %q", path, "/repo/.dendra/worktrees/frank")
	}
	if branch != "dendra/frank" {
		t.Errorf("branch = %q, want %q", branch, "dendra/frank")
	}
	if mock.calledWith.repoRoot != "/repo" {
		t.Errorf("repoRoot = %q, want %q", mock.calledWith.repoRoot, "/repo")
	}
	if mock.calledWith.agentName != "frank" {
		t.Errorf("agentName = %q, want %q", mock.calledWith.agentName, "frank")
	}
	if mock.calledWith.baseBranch != "main" {
		t.Errorf("baseBranch = %q, want %q", mock.calledWith.baseBranch, "main")
	}
}
