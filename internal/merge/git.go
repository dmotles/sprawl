package merge

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// RealLockAcquire acquires an exclusive flock on the given path.
// Returns an unlock function that releases the lock and closes the file.
func RealLockAcquire(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquiring flock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// RealGitMergeBase returns the merge base commit between two refs.
func RealGitMergeBase(repoRoot, a, b string) (string, error) {
	cmd := exec.Command("git", "merge-base", "--", a, b)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git merge-base %s %s: %w", a, b, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RealGitRevParseHead returns the HEAD commit SHA of the given worktree.
func RealGitRevParseHead(worktree string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RealGitResetSoft performs a soft reset to the given ref.
func RealGitResetSoft(worktree, ref string) error {
	cmd := exec.Command("git", "reset", "--soft", "--", ref)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git reset --soft %s: %w: %s", ref, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RealGitCommit creates a commit with the given message and returns the short hash.
func RealGitCommit(worktree, message string) (string, error) {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	hashCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	hashCmd.Dir = worktree
	hashOut, err := hashCmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --short HEAD: %w", err)
	}
	return strings.TrimSpace(string(hashOut)), nil
}

// RealGitRebase rebases the current branch onto the given branch.
func RealGitRebase(worktree, onto string) error {
	cmd := exec.Command("git", "rebase", "--", onto)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git rebase %s: %w: %s", onto, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RealGitRebaseAbort aborts an in-progress rebase. Best-effort: errors are
// intentionally swallowed since this is cleanup after a failed rebase.
func RealGitRebaseAbort(worktree string) error {
	cmd := exec.Command("git", "rebase", "--abort")
	cmd.Dir = worktree
	_ = cmd.Run()
	return nil
}

// RealGitFFMerge performs a fast-forward-only merge of the given branch.
func RealGitFFMerge(worktree, branch string) error {
	cmd := exec.Command("git", "merge", "--ff-only", "--", branch)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git merge --ff-only %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RealGitResetHard resets the worktree to HEAD~1.
func RealGitResetHard(worktree string) error {
	cmd := exec.Command("git", "reset", "--hard", "HEAD~1")
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git reset --hard HEAD~1: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RealRunTests runs make build && go test ./... in the given directory.
func RealRunTests(dir string) (string, error) {
	cmd := exec.Command("bash", "-c", "make build && go test ./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RealWritePoke writes a poke file for the given agent.
func RealWritePoke(dendraRoot, agentName, content string) error {
	pokePath := filepath.Join(dendraRoot, ".dendra", "agents", agentName+".poke")
	return os.WriteFile(pokePath, []byte(content), 0644)
}
