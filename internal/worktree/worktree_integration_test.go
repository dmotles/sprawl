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

	wtPath, branch, err := creator.Create(repo, "alice", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if branch != "dendra/alice" {
		t.Errorf("branch = %q, want %q", branch, "dendra/alice")
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
	if !branchExistsInRepo(t, repo, "dendra/alice") {
		t.Error("branch dendra/alice was not created")
	}

	// Clean up worktree so the temp dir can be removed
	cleanup := exec.Command("git", "worktree", "remove", "--force", wtPath)
	cleanup.Dir = repo
	cleanup.Run()
}

func TestRealCreator_Create_RecycledName(t *testing.T) {
	// This test simulates the bug: an agent was retired (worktree removed but
	// branch kept), then a new agent with the same name is spawned.
	repo := initTestRepo(t)
	creator := &RealCreator{}

	// First spawn
	wtPath, _, err := creator.Create(repo, "bob", "main")
	if err != nil {
		t.Fatalf("first Create failed: %v", err)
	}

	// Simulate retirement: remove worktree but keep branch
	rmCmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
	rmCmd.Dir = repo
	if out, err := rmCmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree remove failed: %s: %v", out, err)
	}

	// Verify branch still exists (this is the pre-condition for the bug)
	if !branchExistsInRepo(t, repo, "dendra/bob") {
		t.Fatal("branch dendra/bob should still exist after worktree removal")
	}

	// Second spawn with same name — this used to fail before the fix
	wtPath2, branch2, err := creator.Create(repo, "bob", "main")
	if err != nil {
		t.Fatalf("second Create (recycled name) failed: %v", err)
	}

	if branch2 != "dendra/bob" {
		t.Errorf("branch = %q, want %q", branch2, "dendra/bob")
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
