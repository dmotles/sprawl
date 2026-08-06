package merge

import (
	"fmt"
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

// TestRealGitLogRange_ParsesEveryMessageShape gates the -z record split
// (QUM-1105). Parsing is where this seam can fail silently: a wrong boundary
// does not error, it produces plausible-looking records with bodies truncated
// at the first blank line, and the squash message would still look fine.
//
// Not red-first — the function is new — so it was demonstrated by mutation:
// dropping -z from the git invocation makes every multi-paragraph case fail
// here with the body split across records.
func TestRealGitLogRange_ParsesEveryMessageShape(t *testing.T) {
	r := newScenarioRepo(t)
	base := r.sha("main")

	shapes := []struct {
		name string
		msg  string
		want string
	}{
		{"subject only", "SUBJECT-ONLY", "SUBJECT-ONLY"},
		{"multi paragraph", "subj\n\npara1\n\npara2", "subj\n\npara1\n\npara2"},
		{"trailing blank lines", "subj\n\nbody\n\n\n", "subj\n\nbody"},
		{"hash-leading line", "subj\n\n# not a comment here\nbody", "subj\n\n# not a comment here\nbody"},
	}
	var shas []string
	for i, s := range shapes {
		shas = append(shas, r.commitFileMsg(r.root, s.name, fmt.Sprintf("shape%d.txt", i), "x\n", s.msg))
	}

	got, err := RealGitLogRange(r.root, base, "main")
	if err != nil {
		t.Fatalf("RealGitLogRange: %v", err)
	}
	if len(got) != len(shapes) {
		t.Fatalf("got %d records, want %d: %#v", len(got), len(shapes), got)
	}
	for i, s := range shapes {
		// Oldest first: the composed message must read in commit order.
		if got[i].SHA != shas[i] {
			t.Errorf("%s: SHA = %q, want %q (records out of order?)", s.name, got[i].SHA, shas[i])
		}
		if got[i].Message != s.want {
			t.Errorf("%s: message = %q, want %q", s.name, got[i].Message, s.want)
		}
	}
}
