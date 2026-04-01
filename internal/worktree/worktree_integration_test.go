package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTestRepo creates a temporary git repo with an initial commit and returns
// the path. The caller should defer os.RemoveAll on the returned path.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v failed: %s: %v", args, out, err)
		}
	}
	return dir
}

// branchExistsInRepo checks if a branch exists in the given repo.
func branchExistsInRepo(t *testing.T, repoDir, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branch)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func TestRealCreator_Create(t *testing.T) {
	repo := initTestRepo(t)
	creator := &RealCreator{}

	wtPath, branch, err := creator.Create(repo, "alice", "feature/alice-work", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if branch != "feature/alice-work" {
		t.Errorf("branch = %q, want %q", branch, "feature/alice-work")
	}

	wantPath := filepath.Join(repo, ".dendra", "worktrees", "alice")
	if wtPath != wantPath {
		t.Errorf("worktreePath = %q, want %q", wtPath, wantPath)
	}

	// Verify the worktree directory exists
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		t.Errorf("worktree directory does not exist at %s", wtPath)
	}

	// Verify the branch was created
	if !branchExistsInRepo(t, repo, "feature/alice-work") {
		t.Error("branch feature/alice-work was not created")
	}

	// Clean up worktree so the temp dir can be removed
	cleanup := exec.Command("git", "worktree", "remove", "--force", wtPath)
	cleanup.Dir = repo
	cleanup.Run()
}

func TestRealCreator_Create_RecycledName_PreservesBranch(t *testing.T) {
	// When an agent name is recycled, the old branch is preserved (not force-deleted).
	// The new spawn uses a different branch name.
	repo := initTestRepo(t)
	creator := &RealCreator{}

	// First spawn
	wtPath, _, err := creator.Create(repo, "bob", "feature/first-task", "main")
	if err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	// Simulate retirement: remove worktree but keep branch
	rmCmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
	rmCmd.Dir = repo
	if out, err := rmCmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree remove failed: %s: %v", out, err)
	}

	// Verify old branch still exists
	if !branchExistsInRepo(t, repo, "feature/first-task") {
		t.Fatal("branch feature/first-task should still exist after worktree removal")
	}

	// Second spawn with same agent name but different branch
	wtPath2, branch2, err := creator.Create(repo, "bob", "feature/second-task", "main")
	if err != nil {
		t.Fatalf("second Create (recycled name) failed: %v", err)
	}

	if branch2 != "feature/second-task" {
		t.Errorf("branch = %q, want %q", branch2, "feature/second-task")
	}

	// Old branch should still exist (not force-deleted)
	if !branchExistsInRepo(t, repo, "feature/first-task") {
		t.Error("old branch feature/first-task should be preserved after name recycling")
	}

	// Verify the worktree directory exists
	if _, err := os.Stat(wtPath2); os.IsNotExist(err) {
		t.Errorf("worktree directory does not exist at %s", wtPath2)
	}

	// Clean up
	cleanup := exec.Command("git", "worktree", "remove", "--force", wtPath2)
	cleanup.Dir = repo
	cleanup.Run()
}

func TestBranchExists(t *testing.T) {
	repo := initTestRepo(t)

	// "main" should exist after init
	if !branchExists(repo, "main") {
		t.Error("branchExists(main) = false, want true")
	}

	// A non-existent branch should return false
	if branchExists(repo, "dendra/nonexistent") {
		t.Error("branchExists(dendra/nonexistent) = true, want false")
	}
}
