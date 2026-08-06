package merge

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// RealLockAcquire acquires an exclusive flock on the given path.
// Returns an unlock function that releases the lock and closes the file.
func RealLockAcquire(lockPath string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil { //nolint:gosec // G301: world-readable lock dir is intentional
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644) //nolint:gosec // G302: world-readable lock file is intentional
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("acquiring flock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
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
	cmd := exec.Command("git", "reset", "--soft", ref)
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
//
// stdio is explicitly redirected to io.Discard so that any output (e.g.
// "fatal: No rebase in progress?" on stderr when there's nothing to abort,
// or rebase progress chatter on stdout) cannot inherit the parent's FD 1/2
// in TUI mode (Bubble Tea alt-screen). See QUM-342 (extends QUM-330).
func RealGitRebaseAbort(worktree string) error {
	cmd := exec.Command("git", "rebase", "--abort")
	cmd.Dir = worktree
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
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

// RealGitUpdateRef creates or moves ref to newSHA.
func RealGitUpdateRef(worktree, ref, newSHA string) error {
	cmd := exec.Command("git", "update-ref", "--", ref, newSHA)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git update-ref %s %s: %w: %s", ref, newSHA, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RealGitUpdateRefCAS moves ref to newSHA only if it currently points at
// oldSHA. git's third positional argument makes the update atomic
// compare-and-swap; a mismatch exits non-zero and is reported as an error
// rather than forcing the ref.
func RealGitUpdateRefCAS(worktree, ref, newSHA, oldSHA string) error {
	cmd := exec.Command("git", "update-ref", "--", ref, newSHA, oldSHA)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git update-ref (compare-and-swap) %s %s (expected %s): %w: %s",
			ref, newSHA, oldSHA, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RealWritePoke writes a poke file for the given agent.
func RealWritePoke(sprawlRoot, agentName, content string) error {
	pokePath := filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".poke")
	return os.WriteFile(pokePath, []byte(content), 0o644) //nolint:gosec // G306: world-readable poke file is intentional
}

// RealDeps returns a Deps with every seam bound to its production
// implementation. Both merge.Deps construction sites (cmd/merge.go and
// internal/supervisor/real.go, the latter feeding both the merge and retire
// paths) use it, so a newly added seam cannot be bound in one and forgotten
// in the other — a class of silent failure where MCP-initiated merges lose a
// safety net while the CLI keeps it (QUM-1090).
//
// Checkpoint is deliberately left nil: it is per-caller observability and
// cpMerge nil-guards it.
func RealDeps(stderr io.Writer) *Deps {
	return &Deps{
		LockAcquire:       RealLockAcquire,
		GitMergeBase:      RealGitMergeBase,
		GitRevParseHead:   RealGitRevParseHead,
		GitResetSoft:      RealGitResetSoft,
		GitCommit:         RealGitCommit,
		GitRebase:         RealGitRebase,
		GitRebaseAbort:    RealGitRebaseAbort,
		GitFFMerge:        RealGitFFMerge,
		GitResetHard:      RealGitResetHard,
		RunTestsStreaming: RealRunTestsStreaming,
		WritePoke:         RealWritePoke,
		GitUpdateRef:      RealGitUpdateRef,
		GitUpdateRefCAS:   RealGitUpdateRefCAS,
		Now:               time.Now,
		Stderr:            stderr,
	}
}
