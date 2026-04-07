package worktree

import (
	"os"
	"path/filepath"
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

func TestSetupBeadsRedirect_CreatesRedirect(t *testing.T) {
	repoRoot := t.TempDir()
	worktreePath := filepath.Join(repoRoot, ".sprawl", "worktrees", "alice")

	// Create .beads/ in repo root
	if err := os.MkdirAll(filepath.Join(repoRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create worktree .beads/ dir (simulating git checkout of tracked files)
	if err := os.MkdirAll(filepath.Join(worktreePath, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SetupBeadsRedirect(repoRoot, worktreePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	redirectPath := filepath.Join(worktreePath, ".beads", "redirect")
	data, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("failed to read redirect file: %v", err)
	}

	// From <worktree>/.beads/ to <repoRoot>/.beads/ is ../../../../.beads
	want := filepath.Join("..", "..", "..", "..", ".beads") + "\n"
	if string(data) != want {
		t.Errorf("redirect content = %q, want %q", string(data), want)
	}
}

func TestSetupBeadsRedirect_NoBeadsInRepoRoot(t *testing.T) {
	repoRoot := t.TempDir()
	worktreePath := filepath.Join(repoRoot, ".sprawl", "worktrees", "alice")

	// No .beads/ in repo root — should be a no-op
	if err := SetupBeadsRedirect(repoRoot, worktreePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	redirectPath := filepath.Join(worktreePath, ".beads", "redirect")
	if _, err := os.Stat(redirectPath); !os.IsNotExist(err) {
		t.Errorf("expected redirect file to not exist, but got err: %v", err)
	}
}

func TestSetupBeadsRedirect_ExistingRedirectNotOverwritten(t *testing.T) {
	repoRoot := t.TempDir()
	worktreePath := filepath.Join(repoRoot, ".sprawl", "worktrees", "alice")

	// Create .beads/ in both locations
	if err := os.MkdirAll(filepath.Join(repoRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktreePath, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Pre-create a redirect file with custom content
	redirectPath := filepath.Join(worktreePath, ".beads", "redirect")
	existing := "custom/path\n"
	if err := os.WriteFile(redirectPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetupBeadsRedirect(repoRoot, worktreePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("failed to read redirect file: %v", err)
	}
	if string(data) != existing {
		t.Errorf("redirect was overwritten: got %q, want %q", string(data), existing)
	}
}

func TestSetupBeadsRedirect_CreatesBeadsDirInWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	worktreePath := filepath.Join(repoRoot, ".sprawl", "worktrees", "alice")

	// Create .beads/ in repo root only — not in worktree
	if err := os.MkdirAll(filepath.Join(repoRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the worktree dir itself but NOT .beads/ inside it
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := SetupBeadsRedirect(repoRoot, worktreePath); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	redirectPath := filepath.Join(worktreePath, ".beads", "redirect")
	if _, err := os.Stat(redirectPath); err != nil {
		t.Fatalf("redirect file should exist: %v", err)
	}
}
