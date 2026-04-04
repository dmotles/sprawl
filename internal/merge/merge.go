package merge

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/dmotles/dendra/internal/state"
)

// Config holds the parameters for a merge operation.
type Config struct {
	DendraRoot      string
	AgentName       string
	AgentBranch     string
	AgentWorktree   string
	ParentBranch    string
	ParentWorktree  string
	MessageOverride string
	NoValidate      bool
	DryRun          bool
	AgentState      *state.AgentState
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
	RunTests        func(dir string) (string, error)
	WritePoke       func(dendraRoot, agentName, content string) error
	Stderr          io.Writer
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
	lockPath := filepath.Join(cfg.DendraRoot, ".dendra", "locks", cfg.AgentName+".lock")
	unlock, err := deps.LockAcquire(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquiring lock for %s: %w", cfg.AgentName, err)
	}
	defer unlock()

	// Step 2: Check for zero-commit case.
	mergeBase, err := deps.GitMergeBase(cfg.DendraRoot, cfg.ParentBranch, cfg.AgentBranch)
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

	// Step 5: Rebase onto parent.
	if err := deps.GitRebase(cfg.AgentWorktree, cfg.ParentBranch); err != nil {
		_ = deps.GitRebaseAbort(cfg.AgentWorktree)
		return nil, fmt.Errorf("rebase failed (conflicts likely). Aborted rebase.\nTo restore original branch state: git reset --hard %s", preSquashSHA)
	}

	// Step 6: Fast-forward merge on parent.
	if err := deps.GitFFMerge(cfg.ParentWorktree, cfg.AgentBranch); err != nil {
		return nil, fmt.Errorf("fast-forward merge failed (this is unexpected after a clean rebase): %w", err)
	}

	// Step 7: Post-merge validation.
	if !cfg.NoValidate {
		output, err := deps.RunTests(cfg.ParentWorktree)
		if err != nil {
			if resetErr := deps.GitResetHard(cfg.ParentWorktree); resetErr != nil {
				fmt.Fprintf(deps.Stderr, "WARNING: rollback (git reset --hard HEAD~1) failed: %v\n", resetErr)
			}
			truncated := truncateOutput(output, 50)
			return nil, fmt.Errorf("post-merge validation failed: tests fail after merging %s into %s\nMerge rolled back. Your branch is back to its pre-merge state.\n%s\nUse --no-validate to skip validation", cfg.AgentName, cfg.ParentBranch, truncated)
		}
	}

	// Step 8: Write poke BEFORE releasing lock.
	pokeMsg := fmt.Sprintf(
		"Your branch %q was just rebased and fast-forward merged into %q. "+
			"Your commit history has changed — any previous commits have been squashed. "+
			"Your worktree is clean and your branch is up to date with the parent.",
		cfg.AgentBranch, cfg.ParentBranch)
	_ = deps.WritePoke(cfg.DendraRoot, cfg.AgentName, pokeMsg)

	// Step 9: Release flock (handled by defer unlock()).
	return &Result{
		CommitHash:   commitHash,
		PreSquashSHA: preSquashSHA,
	}, nil
}

func dryRun(cfg *Config, deps *Deps) (*Result, error) {
	mergeBase, err := deps.GitMergeBase(cfg.DendraRoot, cfg.ParentBranch, cfg.AgentBranch)
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
	if !cfg.NoValidate {
		fmt.Fprintf(deps.Stderr, " → validate")
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
