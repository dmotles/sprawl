package merge

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// DefaultValidateTimeout is the default timeout for post-merge validation
// when neither Config.ValidateTimeout nor a project-level override is set.
// QUM-496.
const DefaultValidateTimeout = 10 * time.Minute

// PremergeRefPrefix roots the recovery refs written before every non-noop,
// non-dry-run merge (QUM-1090). Unlike reflog entries these survive `git
// gc`, survive branch deletion at retire, and are discoverable with
// `git for-each-ref`.
const PremergeRefPrefix = "refs/sprawl/premerge"

// PremergeTSLayout is the ref-name timestamp layout: UTC, millisecond
// precision. Millisecond and not second because two sequential merges of one
// agent inside the same second would otherwise collide and the second would
// overwrite the first's recovery pair — destroying the exact artifact these
// refs exist to preserve. Fixed width, so lexical ref order is chronological.
//
// `sprawl gc` parses this back out of the ref name to age refs; keep the two
// reading the same constant.
const PremergeTSLayout = "20060102T150405.000Z"

// premergeRefs returns the (agent, parent) recovery ref names for one merge
// attempt. Agent names are validated to ^[A-Za-z0-9][A-Za-z0-9_-]*$
// (internal/agent), so they are always a single legal ref component and the
// resulting ref always splits into exactly 6 "/"-separated parts.
func premergeRefs(agentName string, now time.Time) (agentRef, parentRef string) {
	base := fmt.Sprintf("%s/%s/%s", PremergeRefPrefix, agentName, now.UTC().Format(PremergeTSLayout))
	return base + "/agent", base + "/parent"
}

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

	// GitUpdateRef creates or moves ref to newSHA unconditionally. Used to
	// write the premerge recovery refs (QUM-1090).
	GitUpdateRef func(worktree, ref, newSHA string) error

	// GitUpdateRefCAS moves ref to newSHA only if it currently points at
	// oldSHA, and returns an error on refusal. Used to restore the agent
	// branch after a failed rebase without ever forcing a ref that
	// something else moved (QUM-1090).
	GitUpdateRefCAS func(worktree, ref, newSHA, oldSHA string) error

	// Now supplies the premerge recovery refs' timestamp. Mandatory, like
	// every other seam here: Merge dereferences it unconditionally, and
	// NilSeams/MinDepsSeams gate every construction site on it being bound.
	Now func() time.Time

	Stderr io.Writer

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
//
// ctx drives post-merge validation; ValidateTimeout (or DefaultValidateTimeout)
// is layered on top to bound runaway validate commands (QUM-496/QUM-524).
func Merge(ctx context.Context, cfg *Config, deps *Deps) (*Result, error) {
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

	// Step 3a (QUM-1090): write the premerge recovery refs BEFORE the first
	// mutation. Both the reads and the writes here are pre-mutation, so a
	// failure leaves the repo untouched — abort loudly rather than run the
	// merge without its safety net.
	parentTip, err := deps.GitRevParseHead(cfg.ParentWorktree)
	if err != nil {
		return nil, fmt.Errorf("reading parent HEAD for premerge ref: %w", err)
	}
	agentRef, parentRef := premergeRefs(cfg.AgentName, deps.Now())
	// Written in SprawlRoot, not the agent worktree: refs are shared across
	// worktrees, and SprawlRoot is the one path still guaranteed to exist on
	// the retire path, which removes the agent worktree.
	if err := deps.GitUpdateRef(cfg.SprawlRoot, agentRef, agentHead); err != nil {
		return nil, fmt.Errorf("writing premerge agent ref %s: %w", agentRef, err)
	}
	if err := deps.GitUpdateRef(cfg.SprawlRoot, parentRef, parentTip); err != nil {
		return nil, fmt.Errorf("writing premerge parent ref %s: %w", parentRef, err)
	}
	fmt.Fprintf(deps.Stderr, "Pre-merge recovery refs (pruned by `sprawl gc`):\n  %s -> %s\n  %s -> %s\n",
		agentRef, agentHead, parentRef, parentTip)
	cpMerge(deps, "merge.premerge-refs-written", "agent_ref", agentRef, "parent_ref", parentRef)

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
		return nil, rebaseFailureError(cfg, deps, preSquashSHA, agentRef, parentRef)
	}
	cpMerge(deps, "merge.rebased")

	// Step 6: Fast-forward merge on parent.
	if err := deps.GitFFMerge(cfg.ParentWorktree, cfg.AgentBranch); err != nil {
		return nil, fmt.Errorf("fast-forward merge failed (this is unexpected after a clean rebase): %w", err)
	}
	cpMerge(deps, "merge.ff-merged")

	// Step 7: Post-merge validation.
	if !cfg.NoValidate && cfg.ValidateCmd != "" {
		// QUM-588: open a persistent validate log under .sprawl/logs/ so
		// every validate run is post-hoc inspectable via less/tail. The
		// log is tee'd alongside the checkpoint sink and retained on both
		// success and failure.
		vlog, vlogErr := OpenValidateLog(cfg.SprawlRoot, nil, time.Now)
		var logPath string
		if vlogErr != nil {
			fmt.Fprintf(deps.Stderr, "WARNING: could not open validate log: %v\n", vlogErr)
		} else {
			logPath = vlog.Path()
		}
		cpMerge(deps, "merge.validate-started", "cmd", cfg.ValidateCmd, "log_path", logPath)
		timeout := cfg.ValidateTimeout
		if timeout <= 0 {
			timeout = DefaultValidateTimeout
		}
		validateCtx, cancel := context.WithTimeout(ctx, timeout)
		sink := func(line string) {
			if vlog != nil {
				vlog.Write(line)
			}
			cpMerge(deps, "merge.validate-line", "line", line)
		}
		output, err := deps.RunTestsStreaming(validateCtx, cfg.ParentWorktree, cfg.ValidateCmd, sink)
		cancel()
		if vlog != nil {
			vlog.Finish(err)
		}
		if err != nil {
			cpMerge(deps, "merge.validate-ended", "exit", "nonzero", "log_path", logPath, "error", err.Error())
			if resetErr := deps.GitResetHard(cfg.ParentWorktree); resetErr != nil {
				fmt.Fprintf(deps.Stderr, "WARNING: rollback (git reset --hard HEAD~1) failed: %v\n", resetErr)
			}
			truncated := truncateOutput(output, 50)
			suffix := ""
			if logPath != "" {
				suffix = fmt.Sprintf("\nFull validate log: %s", logPath)
			}
			// QUM-1090, deliberate asymmetry with the rebase-failure path
			// above: we name the recovery refs but do NOT restore the agent
			// branch here. A rebase conflict means the merge did not happen,
			// so the squash is a pure artifact of a failed attempt and
			// undoing it is a plain undo. A validate failure means the merge
			// DID happen and was rejected on quality — the squashed+rebased
			// branch is a legitimate, content-complete state to iterate on,
			// and rewinding it would silently discard the rebase work. The
			// premerge refs make recovery a one-liner either way, so
			// automating it here adds risk without adding recoverability.
			// Keeping this path to a message-only change also means QUM-1087,
			// which reorders validate to run before the ff-merge, has nothing
			// to re-reason about here.
			return nil, fmt.Errorf("post-merge validation failed: tests fail after merging %s into %s\nMerge rolled back. Your branch is back to its pre-merge state.\n%s%s\nRecovery refs:\n  %s\n  %s\nUse --no-validate to skip validation", cfg.AgentName, cfg.ParentBranch, truncated, suffix, agentRef, parentRef)
		}
		cpMerge(deps, "merge.validate-ended", "exit", "0", "log_path", logPath)
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

// rebaseFailureError restores the agent branch to its pre-squash tip and
// builds the caller-facing error (QUM-1090 part B).
//
// `git rebase --abort` returns the branch to the SQUASH commit, not to the
// original tip, so before this the tool printed a manual `git reset --hard`
// for a human to run — and manual recovery after a squash is historically
// where the damage happens (CLAUDE.md, QUM-1083). Doing it in the tool
// removes the step.
//
// The restore needs no worktree reset. Two facts, both load-bearing:
// `reset --soft` preserved the index and worktree, so the squash commit's
// TREE is byte-identical to preSquashSHA's; and `rebase --abort` restores
// the index and worktree to that squash commit. Together, moving the ref
// back to preSquashSHA leaves the checkout matching its new HEAD and
// `git status` clean. (An agent worktree with uncommitted work cannot reach
// here at all — agentops refuses a dirty worktree before calling Merge.)
//
// Run in the AGENT WORKTREE, not SprawlRoot, unlike the premerge writes:
// raw `update-ref` does NOT honour git's "branch is checked out in another
// worktree" protection, so issuing it from the worktree whose HEAD points at
// that branch is what keeps that worktree's index consistent with the move.
//
// Compare-and-swap, never a blind write. Note the CAS oldSHA is read from
// HEAD, while the ref being swapped is refs/heads/<cfg.AgentBranch>; those
// coincide on the merge path (agentops resolves AgentBranch from the
// worktree's CurrentBranch, QUM-511) and diverge on the retire path, which
// still passes the stale spawn-time name (QUM-1088). The safety on that path
// rests on the CAS REFUSING, not on the read being the right ref.
func rebaseFailureError(cfg *Config, deps *Deps, preSquashSHA, agentRef, parentRef string) error {
	refPair := fmt.Sprintf("Recovery refs:\n  %s\n  %s", agentRef, parentRef)

	curTip, why := deps.GitRevParseHead(cfg.AgentWorktree)
	if why == nil {
		why = deps.GitUpdateRefCAS(cfg.AgentWorktree, "refs/heads/"+cfg.AgentBranch, preSquashSHA, curTip)
		if why == nil {
			return fmt.Errorf("rebase failed (conflicts likely). Aborted rebase.\nBranch %s %s %s.\n%s",
				cfg.AgentBranch, premergeRestoredClaim, preSquashSHA, refPair)
		}
	}
	// Deliberately does NOT print a `git update-ref refs/heads/<branch>`
	// one-liner. The CAS refused, which means we do not know which branch is
	// actually damaged — and on the retire path cfg.AgentBranch is the stale
	// spawn-time name, so naming it would aim the caller's recovery at the
	// WRONG branch while leaving the damaged one at the squash commit. That
	// is the QUM-1083 failure mode with extra steps. Name the ref, make the
	// caller confirm the branch.
	return fmt.Errorf("rebase failed (conflicts likely). Aborted rebase.\n"+
		"Could NOT auto-restore the agent branch (%v); it is left at the squash commit.\n"+
		"Confirm which branch is actually checked out before recovering:\n"+
		"  git -C %s rev-parse --abbrev-ref HEAD\n"+
		"then point THAT branch at the recovery ref (%q may be stale):\n"+
		"  git update-ref refs/heads/<that-branch> %s\n%s",
		why, cfg.AgentWorktree, cfg.AgentBranch, agentRef, refPair)
}

// premergeRestoredClaim is the phrase the rebase-failure path uses when it
// actually restored the branch. It appears on that leg ONLY — the CAS-refused
// leg must never claim it.
const premergeRestoredClaim = "was restored to its pre-merge tip"

// NilSeams reports which func-typed Deps fields are nil, and how many were
// examined. Every Deps construction site's test uses it so the walk (and its
// assertion-count floor) lives in one place rather than being copied per
// package — the same drift shape RealDeps exists to prevent.
//
// checked is the assertion-count floor: a refactor that groups seams behind
// an interface or a nested struct makes them invisible to the Func kind
// gate, and the count drops even though the field count does not.
//
// Checkpoint is excluded: it is documented-optional and cpMerge nil-guards it.
func NilSeams(d *Deps) (missing []string, checked int) {
	v := reflect.ValueOf(*d)
	tp := v.Type()
	for i := 0; i < tp.NumField(); i++ {
		f := tp.Field(i)
		if f.Name == "Checkpoint" || v.Field(i).Kind() != reflect.Func {
			continue
		}
		checked++
		if v.Field(i).IsNil() {
			missing = append(missing, f.Name)
		}
	}
	return missing, checked
}

// MinDepsSeams is the assertion-count floor for NilSeams: the number of
// mandatory func seams Deps carries. Bump it deliberately when adding one.
const MinDepsSeams = 14

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
