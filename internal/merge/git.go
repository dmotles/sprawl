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

// RealGitLogRange returns the commits in base..head, oldest first.
//
// --first-parent --no-merges, and both are load-bearing. An agent that merges
// another branch into its own would otherwise pull that branch's commit
// messages into its squash: --no-merges drops the merge commit's own
// boilerplate subject, and --first-parent drops the side branch it brought in.
// Neither flag subsumes the other (pinned by S12 in
// commit_message_scenario_test.go, which fails if either is removed).
//
// -z rather than newline separation: commit bodies contain blank lines, so a
// newline-delimited stream has no unambiguous record boundary. With --format,
// -z terminates each record with a NUL.
func RealGitLogRange(worktree, base, head string) ([]CommitRecord, error) {
	// `head --not base` rather than `base..head`: identical to git, but it
	// keeps both revisions as their own argv elements instead of a
	// concatenated string, which is what the rest of this file does and what
	// keeps the gosec G204 rule satisfied honestly rather than by nolint.
	cmd := exec.Command("git", "log", "--reverse", "--first-parent", "--no-merges",
		"-z", "--format=%H%n%B", head, "--not", base, "--")
	cmd.Dir = worktree
	out, err := cmd.Output()
	if err != nil {
		// git's own diagnosis, not just "exit status 128": a failure here
		// REFUSES the merge, so this is the one read in this file where the
		// caller most needs to know why.
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git log %s..%s: %w: %s", base, head, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git log %s..%s: %w", base, head, err)
	}
	var records []CommitRecord
	// Records are `<sha>\n<message>\0`, so the only empty element is the tail
	// after the final NUL. Keyed on exactly that rather than on a whitespace
	// test: a commit created with --allow-empty-message is `<sha>\n`, which a
	// TrimSpace guard would still keep, but the precise test cannot ever drop
	// a real commit from the SHA index.
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		sha, msg, _ := strings.Cut(rec, "\n")
		records = append(records, CommitRecord{
			SHA:     strings.TrimSpace(sha),
			Message: strings.TrimRight(msg, "\n"),
		})
	}
	return records, nil
}

// RealGitCommit creates a commit with the given message and returns the short hash.
//
// The message goes in on stdin (-F -), never as `-m <message>`. Linux caps a
// SINGLE argument at MAX_ARG_STRLEN (128 KiB) regardless of ARG_MAX, and a
// derived message carrying an agent's real commit bodies passes that easily —
// `-m` fails at fork/exec with "argument list too long" before git runs.
// Stdin is set explicitly: a nil Stdin would let git inherit the parent's,
// which in TUI mode is the session's own input (cf. git_stdio_leak_test.go).
//
// --cleanup=verbatim, and NOT the `whitespace` default. Being explicit is
// what defeats a user's `commit.cleanup=strip`, which would silently delete
// every '#'-leading line of an agent's message — and a code block is where
// those live. But `whitespace` is itself lossy in ways that matter for the
// messages we now carry: measured, it strips trailing whitespace from every
// line and collapses runs of blank lines. A blank context line in an
// embedded diff is a single space; a markdown hard break is two trailing
// spaces. `verbatim` is byte-faithful and defeats `strip` just as well, so
// it strictly dominates.
func RealGitCommit(worktree, message string) (string, error) {
	cmd := exec.Command("git", "commit", "--cleanup=verbatim", "-F", "-")
	cmd.Dir = worktree
	cmd.Stdin = strings.NewReader(message)
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
		GitLogRange:        RealGitLogRange,
		GitRevParseHead:    RealGitRevParseHead,
		GitResetSoft:       RealGitResetSoft,
		GitCommit:          RealGitCommit,
		GitRebase:          RealGitRebase,
		GitRebaseAbort:     RealGitRebaseAbort,
		GitFFMerge:         RealGitFFMerge,
		GitResetHard:       RealGitResetHard,
		RunTestsStreaming:  RealRunTestsStreaming,
		WritePoke:          RealWritePoke,
		GitUpdateRef:       RealGitUpdateRef,
		GitUpdateRefCAS:    RealGitUpdateRefCAS,
		GitSymbolicRefHead: RealGitSymbolicRefHead,
		Now:                time.Now,
		Stderr:             stderr,
	}
}
