package merge

import (
	"errors"
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

// RealGitRevParseRef resolves rev to a full SHA in worktree.
//
// Distinct from RealGitRevParseHead, and the distinction is load-bearing for
// the QUM-1087 ref-move predicate: `git merge --ff-only <branch>` resolves a
// NAME, so the predicate must read the tip of that same ref. A HEAD-based read
// asserts a property of a potentially different object than the one the merge
// acts on — and this engine has already shipped one defect of exactly that
// shape (QUM-1088, the stale advertised branch).
func RealGitRevParseRef(worktree, rev string) (string, error) {
	// `rev-list -n 1` rather than `rev-parse`, and that choice carries two
	// properties this seam needs.
	//
	// First, a bare `git rev-parse <unknown>` ECHOES ITS ARGUMENT BACK and exits
	// 0, so it would return the input string dressed up as a SHA; the ff-merge
	// predicate would then compare a branch NAME against a real SHA and refuse a
	// good merge with an incomprehensible message. rev-list errors instead.
	//
	// Second, it resolves to a COMMIT (peeling an annotated tag), which
	// `rev-parse --verify` would not. The obvious spelling for that is
	// `--verify <rev>^{commit}`, but that requires string-concatenating the
	// caller's rev into one argv element — which is both a gosec G204 finding and
	// the thing the rest of this file deliberately avoids (see RealGitMergeBase
	// and RealGitLogRange: revisions stay their own argv elements, and the
	// trailing `--` keeps a rev from being read as a path).
	cmd := exec.Command("git", "rev-list", "-n", "1", "--end-of-options", rev, "--")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-list -n 1 %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// RealGitIsAncestor reports whether ancestor is an ancestor of descendant.
//
// Exit 1 is git's FALSE answer, not a failure, and it is returned as
// (false, nil). Folding it into the error would make a legitimate "not an
// ancestor" indistinguishable from a broken repository — and the caller's two
// responses are opposite: one is a diagnosis to surface, the other is a bug to
// report. Any other non-zero exit is a real error.
func RealGitIsAncestor(worktree, ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", "--end-of-options", ancestor, descendant)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %w: %s",
		ancestor, descendant, err, strings.TrimSpace(string(out)))
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

// RealGitFFMerge performs a fast-forward-only merge of the given revision.
//
// Callers pass a SHA, not a branch name — see the call site in Merge. `git merge
// --ff-only <sha>` is as valid as with a name, and it removes the window where
// the name resolves to something newer than what was validated.
func RealGitFFMerge(worktree, branch string) error {
	cmd := exec.Command("git", "merge", "--ff-only", "--", branch)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git merge --ff-only %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
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

// RealGitSymbolicRefHead returns the full ref HEAD points at, erroring on a
// detached HEAD.
func RealGitSymbolicRefHead(worktree string) (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "--quiet", "HEAD")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git symbolic-ref HEAD (detached HEAD?): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
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
		LockAcquire:        RealLockAcquire,
		GitMergeBase:       RealGitMergeBase,
		GitRevParseHead:    RealGitRevParseHead,
		GitRevParseRef:     RealGitRevParseRef,
		GitIsAncestor:      RealGitIsAncestor,
		GitRebase:          RealGitRebase,
		GitRebaseAbort:     RealGitRebaseAbort,
		GitFFMerge:         RealGitFFMerge,
		RunTestsStreaming:  RealRunTestsStreaming,
		WritePoke:          RealWritePoke,
		GitUpdateRef:       RealGitUpdateRef,
		GitUpdateRefCAS:    RealGitUpdateRefCAS,
		GitSymbolicRefHead: RealGitSymbolicRefHead,
		Now:                time.Now,
		Stderr:             stderr,
	}
}
