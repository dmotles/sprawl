package worktree

import (
	"os/exec"
	"strings"
	"testing"
)

// TestVerifyWorktreeHEAD_Match — on a worktree correctly checked out on the
// intended branch, verifyWorktreeHEAD returns nil.
func TestVerifyWorktreeHEAD_Match(t *testing.T) {
	repo := initTestRepo(t)
	creator := &RealCreator{}
	wtPath, _, err := creator.Create(repo, "alice", "feature/alice", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
		cmd.Dir = repo
		_ = cmd.Run()
	})

	if err := verifyWorktreeHEAD(wtPath, "feature/alice"); err != nil {
		t.Errorf("verifyWorktreeHEAD on a correct worktree = %v, want nil", err)
	}
}

// TestVerifyWorktreeHEAD_DetachedHEAD — the bad state `-b` never produces is
// synthesized by detaching the worktree's HEAD; the verifier must hard-fail and
// name the mismatch (QUM-837 cheap invariant).
func TestVerifyWorktreeHEAD_DetachedHEAD(t *testing.T) {
	repo := initTestRepo(t)
	creator := &RealCreator{}
	wtPath, _, err := creator.Create(repo, "bob", "feature/bob", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
		cmd.Dir = repo
		_ = cmd.Run()
	})

	// Synthesize the bad HEAD: detach it (no longer on the named branch).
	detach := exec.Command("git", "checkout", "--detach")
	detach.Dir = wtPath
	if out, err := detach.CombinedOutput(); err != nil {
		t.Fatalf("detach HEAD: %s: %v", out, err)
	}

	err = verifyWorktreeHEAD(wtPath, "feature/bob")
	if err == nil {
		t.Fatalf("verifyWorktreeHEAD on a detached HEAD = nil, want a mismatch error")
	}
	if !strings.Contains(err.Error(), "feature/bob") {
		t.Errorf("error %q should name the intended branch feature/bob", err.Error())
	}
}

// TestVerifyWorktreeHEAD_WrongBranch — a worktree switched to a different
// branch than intended must also fail the invariant.
func TestVerifyWorktreeHEAD_WrongBranch(t *testing.T) {
	repo := initTestRepo(t)
	creator := &RealCreator{}
	wtPath, _, err := creator.Create(repo, "carol", "feature/carol", "main")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command("git", "worktree", "remove", "--force", wtPath)
		cmd.Dir = repo
		_ = cmd.Run()
	})

	sw := exec.Command("git", "checkout", "-b", "other-branch")
	sw.Dir = wtPath
	if out, err := sw.CombinedOutput(); err != nil {
		t.Fatalf("switch branch: %s: %v", out, err)
	}

	if err := verifyWorktreeHEAD(wtPath, "feature/carol"); err == nil {
		t.Fatalf("verifyWorktreeHEAD on a wrong-branch worktree = nil, want error")
	}
}
