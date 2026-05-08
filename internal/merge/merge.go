package merge

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// DefaultValidateTimeout is the default timeout for post-merge validation
// when neither Config.ValidateTimeout nor a project-level override is set.
// QUM-496.
const DefaultValidateTimeout = 10 * time.Minute

// Config holds the parameters for a merge operation.
type Config struct {
	SprawlRoot      string
	AgentName       string
	AgentBranch     string
	AgentWorktree   string
	ParentBranch    string
	ParentWorktree  string
	MessageOverride string
	NoValidate      bool
	ValidateCmd     string
	DryRun          bool
	AgentState      *state.AgentState

	// Ctx is the context used to drive post-merge validation. If nil,
	// context.Background() is used. ValidateTimeout (or DefaultValidateTimeout)
	// is layered on top to bound runaway validate commands. QUM-496.
	Ctx context.Context //nolint:containedctx // Threading ctx through the public surface here is deliberate so callers can cancel validate.

	// ValidateTimeout caps the duration of post-merge validation. Zero means
	// use DefaultValidateTimeout. QUM-496.
	ValidateTimeout time.Duration
}

// Deps holds injectable dependencies for the merge operation.
type Deps struct {
	LockAcquire     func(lockPath string) (unlock func(), err error)
	GitMergeBase    func(repoRoot, a, b string) (string, error)
	GitRevParseHead func(worktree string) (string, error)
	GitResetSoft    func(worktree, ref string) error
	GitCommit       func(worktree, message string) (string, error)
	GitRebase       func(worktree, onto string) error
	GitRebaseAbort  func(worktree string) error
	GitFFMerge      func(worktree, branch string) error
	GitResetHard    func(worktree string) error

	// RunTestsStreaming runs the validate command, streaming each output
	// line into sink as it is produced and honoring ctx for cancellation.
	// Returns the full combined output and the wait error. QUM-496.
	RunTestsStreaming func(ctx context.Context, dir, command string, sink func(line string)) (string, error)

	WritePoke func(sprawlRoot, agentName, content string) error
	Stderr    io.Writer

	// Checkpoint, if non-nil, is invoked at notable points during the
	// merge for per-call observability (QUM-494). It is safe to leave
	// nil; callers that don't care can ignore it.
	Checkpoint func(step string, kv ...any)
}

// Result holds the outcome of a merge operation.
type Result struct {
	CommitHash   string
	WasNoOp      bool
	PreSquashSHA string
}

// Merge performs the squash+rebase+fast-forward merge sequence.
// Steps: acquire lock, check for zero commits, squash, rebase, ff-merge,
// validate, write poke, release lock.
func Merge(cfg *Config, deps *Deps) (*Result, error) {
	// Dry-run: show plan without making changes or acquiring lock.
	if cfg.DryRun {
		return dryRun(cfg, deps)
	}

	// Step 1: Acquire flock.
	lockPath := filepath.Join(cfg.SprawlRoot, ".sprawl", "locks", cfg.AgentName+".lock")
	unlock, err := deps.LockAcquire(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquiring lock for %s: %w", cfg.AgentName, err)
	}
	defer unlock()
	cpMerge(deps, "merge.lock-acquired", "agent", cfg.AgentName)

	// Step 2: Check for zero-commit case.
	mergeBase, err := deps.GitMergeBase(cfg.SprawlRoot, cfg.ParentBranch, cfg.AgentBranch)
	if err != nil {
		return nil, fmt.Errorf("finding merge base: %w", err)
	}

	agentHead, err := deps.GitRevParseHead(cfg.AgentWorktree)
	if err != nil {
		return nil, fmt.Errorf("reading agent HEAD: %w", err)
	}

	if mergeBase == agentHead {
		return &Result{WasNoOp: true}, nil
	}

	// Step 3: Record recovery point.
	preSquashSHA := agentHead

	// Step 4: Squash agent's branch.
	if err := deps.GitResetSoft(cfg.AgentWorktree, mergeBase); err != nil {
		return nil, fmt.Errorf("squash reset: %w", err)
	}

	commitMsg := buildMergeCommitMessage(cfg.AgentState, cfg.ParentBranch, cfg.MessageOverride)
	commitHash, err := deps.GitCommit(cfg.AgentWorktree, commitMsg)
	if err != nil {
		return nil, fmt.Errorf("squash commit: %w", err)
	}
	cpMerge(deps, "merge.squash-committed", "commit", commitHash)

	// Step 5: Rebase onto parent.
	if err := deps.GitRebase(cfg.AgentWorktree, cfg.ParentBranch); err != nil {
		_ = deps.GitRebaseAbort(cfg.AgentWorktree)
		return nil, fmt.Errorf("rebase failed (conflicts likely). Aborted rebase.\nTo restore original branch state: git reset --hard %s", preSquashSHA)
	}
	cpMerge(deps, "merge.rebased")

	// Step 6: Fast-forward merge on parent.
	if err := deps.GitFFMerge(cfg.ParentWorktree, cfg.AgentBranch); err != nil {
		return nil, fmt.Errorf("fast-forward merge failed (this is unexpected after a clean rebase): %w", err)
	}
	cpMerge(deps, "merge.ff-merged")

	// Step 7: Post-merge validation.
	if !cfg.NoValidate && cfg.ValidateCmd != "" {
		cpMerge(deps, "merge.validate-started", "cmd", cfg.ValidateCmd)
		ctx := cfg.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		timeout := cfg.ValidateTimeout
		if timeout <= 0 {
			timeout = DefaultValidateTimeout
		}
		validateCtx, cancel := context.WithTimeout(ctx, timeout)
		sink := func(line string) {
			cpMerge(deps, "merge.validate-line", "line", line)
		}
		output, err := deps.RunTestsStreaming(validateCtx, cfg.ParentWorktree, cfg.ValidateCmd, sink)
		cancel()
		if err != nil {
			if resetErr := deps.GitResetHard(cfg.ParentWorktree); resetErr != nil {
				fmt.Fprintf(deps.Stderr, "WARNING: rollback (git reset --hard HEAD~1) failed: %v\n", resetErr)
			}
			truncated := truncateOutput(output, 50)
			return nil, fmt.Errorf("post-merge validation failed: tests fail after merging %s into %s\nMerge rolled back. Your branch is back to its pre-merge state.\n%s\nUse --no-validate to skip validation", cfg.AgentName, cfg.ParentBranch, truncated)
		}
		cpMerge(deps, "merge.validate-ended")
	} else if !cfg.NoValidate && cfg.ValidateCmd == "" {
		fmt.Fprintf(deps.Stderr, "WARNING: no validate command configured; skipping post-merge validation.\n  Configure with: sprawl config set validate \"<command>\"\n  See: sprawl config --help\n")
	}

	// Step 8: Write poke BEFORE releasing lock.
	pokeMsg := fmt.Sprintf(
		"Your branch %q was just rebased and fast-forward merged into %q. "+
			"Your commit history has changed — any previous commits have been squashed. "+
			"Your worktree is clean and your branch is up to date with the parent.",
		cfg.AgentBranch, cfg.ParentBranch)
	_ = deps.WritePoke(cfg.SprawlRoot, cfg.AgentName, pokeMsg)
	cpMerge(deps, "merge.poke-written")

	// Step 9: Release flock (handled by defer unlock()).
	return &Result{
		CommitHash:   commitHash,
		PreSquashSHA: preSquashSHA,
	}, nil
}

func dryRun(cfg *Config, deps *Deps) (*Result, error) { //nolint:unparam // error return kept for interface consistency
	mergeBase, err := deps.GitMergeBase(cfg.SprawlRoot, cfg.ParentBranch, cfg.AgentBranch)
	if err != nil {
		mergeBase = "(unknown)"
	}

	agentHead, err := deps.GitRevParseHead(cfg.AgentWorktree)
	if err != nil {
		agentHead = "(unknown)"
	}

	isNoOp := mergeBase == agentHead
	commitMsg := buildMergeCommitMessage(cfg.AgentState, cfg.ParentBranch, cfg.MessageOverride)
	indentedMsg := "    " + strings.ReplaceAll(commitMsg, "\n", "\n    ")

	fmt.Fprintf(deps.Stderr, "[dry-run] Would merge agent %q (branch %s) into %s\n", cfg.AgentName, cfg.AgentBranch, cfg.ParentBranch)
	fmt.Fprintf(deps.Stderr, "  Merge base: %s\n", mergeBase)
	fmt.Fprintf(deps.Stderr, "  Agent HEAD: %s\n", agentHead)

	if isNoOp {
		fmt.Fprintf(deps.Stderr, "  Result: no-op (no new commits)\n")
		return &Result{WasNoOp: true}, nil
	}

	fmt.Fprintf(deps.Stderr, "  Commit message:\n%s\n", indentedMsg)
	fmt.Fprintf(deps.Stderr, "  Steps: acquire lock → squash → rebase → ff-merge")
	if !cfg.NoValidate && cfg.ValidateCmd != "" {
		fmt.Fprintf(deps.Stderr, " → validate (%s)", cfg.ValidateCmd)
	} else if !cfg.NoValidate && cfg.ValidateCmd == "" {
		fmt.Fprintf(deps.Stderr, " → validate (skipped - not configured)")
	}
	fmt.Fprintf(deps.Stderr, " → poke → release lock\n")

	return &Result{}, nil
}

func buildMergeCommitMessage(agent *state.AgentState, parentBranch, messageOverride string) string {
	coAuthor := "Co-Authored-By: Claude <noreply@anthropic.com>"

	if messageOverride != "" {
		return messageOverride + "\n\n" + coAuthor
	}

	var firstLine string
	if agent.LastReportMessage != "" {
		firstLine = agent.Name + ": " + agent.LastReportMessage
	} else {
		firstLine = fmt.Sprintf("%s: merge branch '%s'", agent.Name, agent.Branch)
	}

	return fmt.Sprintf("%s\n\nSquash merge of branch '%s' into '%s'.\nAgent: %s (%s, %s)\n\n%s",
		firstLine, agent.Branch, parentBranch, agent.Name, agent.Type, agent.Family, coAuthor)
}

// cpMerge calls deps.Checkpoint if non-nil. Safe to call with nil dep.
func cpMerge(d *Deps, step string, kv ...any) {
	if d != nil && d.Checkpoint != nil {
		d.Checkpoint(step, kv...)
	}
}

func truncateOutput(output string, maxLines int) string {
	if output == "" {
		return ""
	}
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	last := lines[len(lines)-maxLines:]
	return fmt.Sprintf("... (showing last %d of %d lines)\n%s", maxLines, len(lines), strings.Join(last, "\n"))
}
