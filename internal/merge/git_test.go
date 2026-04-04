package merge

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRealGitResetSoft_NoPathSeparator verifies that RealGitResetSoft does not
// pass "--" between "--soft" and the ref, which would cause git to interpret
// the ref as a path and fail with "Cannot do soft reset with paths."
func TestRealGitResetSoft_NoPathSeparator(t *testing.T) {
	// Create a temporary git repo with two commits so we can soft-reset to the first.
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
	}

	run("git", "init", "--initial-branch=main", dir)
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "test")

	// First commit
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "a.txt")
	run("git", "commit", "-m", "first")

	// Capture first commit SHA
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	firstSHA, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	firstRef := string(firstSHA[:len(firstSHA)-1]) // trim newline

	// Second commit
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "b.txt")
	run("git", "commit", "-m", "second")

	// Now soft-reset to the first commit. This is the operation that was broken
	// when "--" was present: git would fail with "Cannot do soft reset with paths."
	if err := RealGitResetSoft(dir, firstRef); err != nil {
		t.Fatalf("RealGitResetSoft failed: %v", err)
	}

	// Verify HEAD now points to the first commit
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	headSHA, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse after reset: %v", err)
	}
	headRef := string(headSHA[:len(headSHA)-1])

	if headRef != firstRef {
		t.Errorf("HEAD after reset = %s, want %s", headRef, firstRef)
	}

	// Verify b.txt is staged (soft reset keeps changes in index)
	cmd = exec.Command("git", "diff", "--cached", "--name-only")
	cmd.Dir = dir
	diffOut, err := cmd.Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if string(diffOut) != "b.txt\n" {
		t.Errorf("staged files after soft reset = %q, want \"b.txt\\n\"", string(diffOut))
	}
}
