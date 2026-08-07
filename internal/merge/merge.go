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

// DefaultValidateTimeout is the default timeout for the validation run, which
// since QUM-1087 happens BEFORE the parent is touched — it is pre-merge, not
// post-merge. Applies when neither Config.ValidateTimeout nor a project-level
// override is set. QUM-496.
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
	SprawlRoot     string
	AgentName      string
	AgentBranch    string
	AgentWorktree  string
	ParentBranch   string
	ParentWorktree string
	NoValidate     bool
	ValidateCmd    string
	DryRun         bool
	AgentState     *state.AgentState

	// ValidateTimeout caps the duration of the validation run (pre-merge since
	// QUM-1087). Zero means use DefaultValidateTimeout. QUM-496.
	ValidateTimeout time.Duration
}

// Deps holds injectable dependencies for the merge operation.
type Deps struct {
	LockAcquire  func(lockPath string) (unlock func(), err error)
	GitMergeBase func(repoRoot, a, b string) (string, error)

	GitRevParseHead func(worktree string) (string, error)

	// GitRevParseRef resolves rev to a full SHA. The ff-merge predicate reads
	// the tip of the REF `git merge --ff-only` will resolve, not the
	// worktree's HEAD; where those diverge a HEAD-based check asserts a
	// property of a different object than the merge acts on (QUM-1088 was
	// exactly that shape).
	GitRevParseRef func(worktree, rev string) (string, error)

	// GitIsAncestor reports whether ancestor is an ancestor of descendant.
	// Exit 1 is git's FALSE answer and arrives as (false, nil) — only a real
	// git failure returns an error.
	GitIsAncestor func(worktree, ancestor, descendant string) (bool, error)

	GitRebase      func(worktree, onto string) error
	GitRebaseAbort func(worktree string) error
	GitFFMerge     func(worktree, branch string) error

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

	// GitSymbolicRefHead returns the full ref name HEAD points at (e.g.
	// "refs/heads/foo"), or an error on a detached HEAD. Used to restore the
	// branch the worktree is ACTUALLY on rather than the advertised
	// AgentBranch, which is the stale spawn-time name on the retire path
	// (QUM-1088).
	GitSymbolicRefHead func(worktree string) (string, error)

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
	// MergedTip is the parent branch's tip AFTER the fast-forward — i.e. the
	// commit the parent now points at, read back from the parent worktree.
	//
	// It replaces the old CommitHash, which reported the squash commit's hash
	// captured BEFORE the rebase. After the rebase that object existed on no
	// ref, so the field named a commit nobody could look up. Reporting the
	// post-ff parent tip cannot have that failure mode: it is read from the
	// ref, after the ref moved, and it is the same value the SHA-equality
	// predicate already compares.
	MergedTip string

	WasNoOp bool
}

// Merge rebases the agent's branch onto the parent's, validates the REBASED
// TREE IN THE AGENT WORKTREE, and only then fast-forwards the parent onto it.
// Steps: acquire lock, check for zero commits, write recovery refs, rebase,
// prove fast-forwardability, validate, ff-merge, prove the ref moved, poke,
// release lock.
//
// THE ORDER IS THE POINT (QUM-1087). The parent is mutated exactly once,
// forward-only, after the tree is already known good — so there is no rollback
// of the parent to get wrong, and this package contains no primitive that could
// perform one. Every confirmed loss mode in the previous design lived in the
// window between "the parent was mutated" and "the tree was known good":
//
//   - S5b: the agent's content was already upstream under a different SHA, so
//     the rebase dropped it and `--ff-only` exited 0 WITHOUT MOVING the parent.
//     The validate-failure rollback then rewound a pre-existing parent commit.
//   - S5c: a second merge landed during the first's validation, and the first's
//     rollback removed the second's work while leaving its own.
//
// Neither is patched here; both are structurally absent. Note also that the
// engine creates NO commit: the agent's own commits are fast-forwarded as they
// are, which additionally removes the QUM-1083 squash-then-rebase divergence
// class (a downstream branch stays a genuine ancestor).
//
// ctx drives validation; ValidateTimeout (or DefaultValidateTimeout) is layered
// on top to bound runaway validate commands (QUM-496/QUM-524).
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

	// Step 3: Record the recovery point. The first mutation is now the rebase,
	// so this is the agent branch's tip as the rebase will find it.
	preRebaseSHA := agentHead

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

	// Step 4: Rebase the agent's branch onto the parent's. THE FIRST MUTATION,
	// and it is confined to the agent's own branch.
	if err := deps.GitRebase(cfg.AgentWorktree, cfg.ParentBranch); err != nil {
		_ = deps.GitRebaseAbort(cfg.AgentWorktree)
		return nil, rebaseFailureError(cfg, deps, preRebaseSHA, agentRef, parentRef)
	}
	cpMerge(deps, "merge.rebased")

	// Step 5: Prove the rebase produced a fast-forwardable branch, BEFORE
	// validation and therefore before anything expensive.
	//
	// rebasedTip is read from the REF, and it is the value EVERY later step is
	// stated in: the ff-precondition just below, the fast-forward itself, and
	// the post-ff equality check. One object, read once, so those three cannot
	// disagree about what "the validated tree" means.
	//
	// This read used to be justified by "`git merge --ff-only <branch>`
	// resolves a name, so read the ref the merge will act on, not HEAD".
	// That justification is now FALSE: step 7 fast-forwards this SHA, and a
	// SHA resolves to itself. With the precondition below held, reading HEAD
	// here would be EQUIVALENT; the ref is preferred only because it is the
	// object the caller named.
	//
	// What is left is an UNSTATED PRECONDITION, recorded here rather than
	// checked: that cfg.AgentBranch names the branch this worktree is actually
	// on, so the tree validation STARTS ON is the tree this SHA points at.
	// agentops.Merge — the only caller — holds it, because it derives
	// AgentBranch from the worktree HEAD and refuses a detached HEAD. It is
	// NOT enforced here.
	//
	// Note the precondition is NECESSARY, NOT SUFFICIENT, and the
	// counterexample is in this package: the branch can move DURING validation
	// (a live agent, or a validate command that commits — see
	// TestMergeSafety_AgentBranchMovesDuringValidation), at which point the
	// worktree's tree and this SHA diverge with the precondition still
	// holding. What defends against that is not this read but step 7 merging
	// this SHA and step 8's exact equality check.
	rebasedTip, err := deps.GitRevParseRef(cfg.SprawlRoot, cfg.AgentBranch)
	if err != nil {
		return nil, fmt.Errorf("reading the rebased tip of %s: %w", cfg.AgentBranch, err)
	}
	// Captured HERE, before validation, so a later ff refusal can distinguish
	// "the rebase was wrong" from "the parent moved underneath us". Those have
	// different causes and different remedies, and reporting one as the other
	// sends the caller to the wrong place.
	parentTipBeforeValidate, err := deps.GitRevParseHead(cfg.ParentWorktree)
	if err != nil {
		return nil, fmt.Errorf("reading parent HEAD before validation: %w", err)
	}
	// ARGUMENT ORDER IS LOAD-BEARING. This asks "is the PARENT contained in the
	// REBASED BRANCH" — the question whose true answer means fast-forwardable.
	// Reversed it asks whether the branch is contained in the parent, a
	// different claim that is also true whenever the two are equal (CLAUDE.md's
	// "check that the question the command answers is the question you are
	// claiming").
	//
	// What a swap actually costs, measured rather than assumed: it is invisible
	// to the mock-based unit tests (their GitIsAncestor stub returns true
	// regardless of arguments) and it BREAKS four real-git scenario tests, since
	// post-rebase the parent is a strict ancestor so the reversed question
	// answers false and the merge refuses. An earlier version of this comment
	// claimed a swap would "leave the check green and inert" everywhere; the
	// negative control refuted that.
	ffOK, err := deps.GitIsAncestor(cfg.SprawlRoot, parentTipBeforeValidate, rebasedTip)
	if err != nil {
		// A git failure is NOT the same as a false answer: one is a broken
		// repository to report, the other a rebase to diagnose.
		return nil, fmt.Errorf("checking fast-forwardability of %s onto %s: %w", cfg.AgentBranch, cfg.ParentBranch, err)
	}
	if !ffOK {
		return nil, fmt.Errorf(
			"the rebase %s: parent %s is at %s, which is not contained in the rebased branch %s (%s).\n"+
				"This is a signal to surface, not something the merge should reconcile: step 1 did not rebase correctly.\n"+
				"Nothing was merged and the parent was not touched.\n%s",
			ffPredicateFailure, cfg.ParentBranch, parentTipBeforeValidate, cfg.AgentBranch, rebasedTip,
			refPairText(agentRef, parentRef))
	}
	cpMerge(deps, "merge.ff-precondition-ok", "parent_tip", parentTipBeforeValidate, "rebased_tip", rebasedTip)

	// Step 6: Validate THE REBASED TREE, IN THE AGENT WORKTREE.
	//
	// Post-rebase that tree is exactly what the parent would contain, so this
	// is the same assertion the old design made — moved to a place where a red
	// result costs nothing, because the parent has not been touched. A failure
	// here has nothing to undo.
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
		cpMerge(deps, "merge.validate-started", "cmd", cfg.ValidateCmd, "dir", cfg.AgentWorktree, "log_path", logPath)
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
		output, verr := deps.RunTestsStreaming(validateCtx, cfg.AgentWorktree, cfg.ValidateCmd, sink)
		cancel()
		if vlog != nil {
			vlog.Finish(verr)
		}
		if verr != nil {
			cpMerge(deps, "merge.validate-ended", "exit", "nonzero", "log_path", logPath, "error", verr.Error())
			truncated := truncateOutput(output, 50)
			suffix := ""
			if logPath != "" {
				suffix = fmt.Sprintf("\nFull validate log: %s", logPath)
			}
			// NOTHING TO ROLL BACK, and that is the whole of QUM-1087. The
			// parent was never touched, so this path performs no repair — it
			// reports. The agent's branch is left rebased on purpose: it is a
			// legitimate, content-complete state to fix forward from, and the
			// recovery refs cover the attempt either way.
			// verr is WRAPPED, not just described. The output is for a human;
			// the wrapped error is what lets a caller (and a test) establish
			// WHICH failure path this is rather than pattern-matching prose.
			return nil, fmt.Errorf("validation failed on the rebased tree of %s (in %s); %s was NOT modified: %w\n%s%s\n%s\nFix the failure on the agent's branch and re-run the merge, or use --no-validate to skip validation",
				cfg.AgentName, cfg.AgentWorktree, cfg.ParentBranch, verr, truncated, suffix,
				refPairText(agentRef, parentRef))
		}
		cpMerge(deps, "merge.validate-ended", "exit", "0", "log_path", logPath)
	} else if !cfg.NoValidate && cfg.ValidateCmd == "" {
		fmt.Fprintf(deps.Stderr, "WARNING: no validate command configured; skipping validation.\n  Configure with: sprawl config set validate \"<command>\"\n  See: sprawl config --help\n")
	}

	// Step 7: Fast-forward the parent. THE ONLY MUTATION OF THE PARENT.
	//
	// Fast-forward to the EXACT SHA that was validated, never to the branch
	// NAME. A name is resolved by git at ff time, and the agent's branch can
	// move between the validation and this call — the per-agent flock has no
	// second taker, so a live agent can commit during its own merge, and since
	// QUM-1087 validation runs in that agent's worktree for as long as a
	// validate takes. Merging the name would then advance the parent onto a tip
	// nothing validated, and the engine would only notice afterwards, having
	// already mutated the parent. Merging the SHA makes "the parent receives
	// exactly the tree that passed" true by construction instead of detected
	// after the fact.
	if err := deps.GitFFMerge(cfg.ParentWorktree, rebasedTip); err != nil {
		return nil, ffMergeFailureError(cfg, deps, err, parentTipBeforeValidate, rebasedTip, agentRef, parentRef)
	}
	cpMerge(deps, "merge.ff-merged")

	// Step 9: Prove it was a PURE REF MOVE. `--ff-only` exiting 0 does not
	// establish this — it exits 0 without moving the parent at all when already
	// up to date, which is exactly what made S5b possible. Only SHA equality
	// discriminates that case, so it is asserted rather than implied.
	parentTipAfter, err := deps.GitRevParseHead(cfg.ParentWorktree)
	if err != nil {
		return nil, fmt.Errorf("reading parent HEAD after the fast-forward: %w", err)
	}
	if parentTipAfter != rebasedTip {
		// Inequality has two causes with OPPOSITE severities, and reporting one
		// as the other is the same over-claiming mistake ffMergeFailureError's
		// default leg had. Distinguish them instead of guessing:
		//
		//   - the rebased tip is NOT an ancestor of the parent: nothing of ours
		//     landed. This is the S5b shape (`--ff-only` exited 0 while already
		//     up to date) and it is the serious one.
		//   - the rebased tip IS an ancestor: our work DID land and the parent
		//     then moved further, i.e. something else committed in the window
		//     between our ff and this read. Benign, and possible because
		//     mergeSem only serialises in-process merges — the separate-process
		//     CLI (QUM-1089) and a human both bypass it.
		//
		// A read failure here must not be reported as either, so it is a third
		// branch rather than a silent default.
		landed, ancErr := deps.GitIsAncestor(cfg.SprawlRoot, rebasedTip, parentTipAfter)
		switch {
		case ancErr != nil:
			return nil, fmt.Errorf(
				"the fast-forward reported success but %s is at %s, not at the rebased tip %s, and whether the\n"+
					"merge landed could not be determined (%v). Inspect %s before re-running.\n%s",
				cfg.ParentBranch, parentTipAfter, rebasedTip, ancErr, cfg.ParentBranch,
				refPairText(agentRef, parentRef))
		case landed:
			// Our work is on the parent, but the parent's tip is NOT the tree
			// that was validated. Do NOT call this benign: one of its two causes
			// means the parent now carries content validation never saw.
			//
			//   - the AGENT branch moved during validation, so the ff carried a
			//     newer tip than the one that was validated. The per-agent flock
			//     has no second taker, so a live agent can commit during its own
			//     merge, and validation now runs in its worktree for minutes.
			//   - something committed to the parent AFTER our ff. mergeSem
			//     cannot prevent that for the separate-process CLI (QUM-1089) or
			//     for a human.
			// The first is the reason this is loud rather than informational.
			return nil, fmt.Errorf(
				"the fast-forward landed %s on %s, but %s is now at %s while the tree that was VALIDATED was %s.\n"+
					"Your work is on %s and nothing was lost — but its tip is not the tree validation passed, so\n"+
					"%s is not known good. Either the agent branch moved during validation (a live agent can\n"+
					"commit during its own merge) or something committed to %s after the merge.\n"+
					"Verify %s before relying on it.\n%s",
				cfg.AgentBranch, cfg.ParentBranch, cfg.ParentBranch, parentTipAfter, rebasedTip,
				cfg.ParentBranch, cfg.ParentBranch, cfg.ParentBranch, cfg.ParentBranch,
				refPairText(agentRef, parentRef))
		default:
			return nil, fmt.Errorf(
				"the fast-forward reported success but NOTHING WAS MERGED: %s is at %s and the rebased tip %s is\n"+
					"not contained in it.\n"+
					"`git merge --ff-only` exits 0 without moving the branch when it is already up to date, so its\n"+
					"exit status cannot establish that the merge landed — this is exactly the case that made the\n"+
					"old rollback rewind a pre-existing parent commit. The parent was not modified by this merge.\n%s",
				cfg.ParentBranch, parentTipAfter, rebasedTip, refPairText(agentRef, parentRef))
		}
	}
	cpMerge(deps, "merge.ff-verified", "parent_tip", parentTipAfter)

	// A POST-REBASE NO-OP is reported, not detected-and-reclassified.
	//
	// If the rebase dropped everything (the agent's delta was already upstream
	// under other SHAs), rebasedTip equals the parent tip, the ff is a ref move
	// to where the parent already is, and the equality above holds — so this is
	// a legitimate success and QUM-1087 explicitly lists reclassifying it as
	// out of scope. Step 3's no-op check cannot see it; that runs pre-rebase.
	//
	// But "Merged agent X" with an unchanged parent tip is the kind of output
	// this whole series exists to stop people misreading, so SAY so rather than
	// let the caller infer a landing from a success. Deliberately not
	// Result.WasNoOp: that would change the callers' control flow, which is the
	// out-of-scope part.
	if parentTipAfter == parentTipBeforeValidate {
		fmt.Fprintf(deps.Stderr,
			"NOTE: %s did not move (%s). The rebase found the agent's changes already present on %s\n"+
				"under different SHAs, so there was nothing new to fast-forward. The content IS on %s;\n"+
				"this merge added no commits.\n",
			cfg.ParentBranch, parentTipAfter, cfg.ParentBranch, cfg.ParentBranch)
		cpMerge(deps, "merge.post-rebase-noop", "parent_tip", parentTipAfter)
	}

	// Step 10: Write poke BEFORE releasing lock.
	pokeMsg := fmt.Sprintf(
		"Your branch %q was just rebased onto %q and fast-forward merged into it. "+
			"Your commits were kept as they are — nothing was squashed — but they have new SHAs, "+
			"so do not reference the old ones. Your worktree is clean and your branch is up to "+
			"date with the parent.",
		cfg.AgentBranch, cfg.ParentBranch)
	_ = deps.WritePoke(cfg.SprawlRoot, cfg.AgentName, pokeMsg)
	cpMerge(deps, "merge.poke-written")

	// Step 11: Release flock (handled by defer unlock()).
	return &Result{MergedTip: parentTipAfter}, nil
}

func dryRun(cfg *Config, deps *Deps) (*Result, error) { //nolint:unparam // error return kept for interface consistency
	mergeBase, baseErr := deps.GitMergeBase(cfg.SprawlRoot, cfg.ParentBranch, cfg.AgentBranch)
	if baseErr != nil {
		mergeBase = "(unknown)"
	}

	agentHead, headErr := deps.GitRevParseHead(cfg.AgentWorktree)
	if headErr != nil {
		agentHead = "(unknown)"
	}

	// Two failed reads both yield "(unknown)" and would compare EQUAL, so
	// this cannot key on the values alone: a dry run that could read nothing
	// would otherwise report "no-op (no new commits)", which is a claim about
	// the branch rather than about the failure.
	isNoOp := baseErr == nil && headErr == nil && mergeBase == agentHead

	fmt.Fprintf(deps.Stderr, "[dry-run] Would merge agent %q (branch %s) into %s\n", cfg.AgentName, cfg.AgentBranch, cfg.ParentBranch)
	fmt.Fprintf(deps.Stderr, "  Merge base: %s\n", mergeBase)
	fmt.Fprintf(deps.Stderr, "  Agent HEAD: %s\n", agentHead)

	if isNoOp {
		fmt.Fprintf(deps.Stderr, "  Result: no-op (no new commits)\n")
		return &Result{WasNoOp: true}, nil
	}

	// The plan names no squash and no commit: the engine creates neither. It
	// also states WHERE validation runs, because that is the difference the
	// caller can observe and the thing QUM-1087 changed.
	fmt.Fprintf(deps.Stderr, "  Steps: acquire lock → rebase %s onto %s", cfg.AgentBranch, cfg.ParentBranch)
	if !cfg.NoValidate && cfg.ValidateCmd != "" {
		fmt.Fprintf(deps.Stderr, " → validate (%s) in %s", cfg.ValidateCmd, cfg.AgentWorktree)
	} else if !cfg.NoValidate && cfg.ValidateCmd == "" {
		fmt.Fprintf(deps.Stderr, " → validate (skipped - not configured)")
	}
	fmt.Fprintf(deps.Stderr, " → ff-merge into %s → poke → release lock\n", cfg.ParentBranch)
	// No commit COUNT: the engine no longer reads the agent's commit range
	// (GitLogRange went with the squash), so any number printed here would be
	// invented. The first cut printed a hardcoded 0, which every non-noop dry
	// run reported as "the agent's 0 commit(s)".
	fmt.Fprintf(deps.Stderr, "  The agent's own commits land as-is; no squash commit is created.\n")

	return &Result{}, nil
}

// restoreAgentBranch compare-and-swaps the branch HEAD is on back to
// preRebaseSHA, refusing unless the ref currently reads expectedSHA.
//
// Run in the AGENT WORKTREE (not SprawlRoot, unlike the premerge writes):
// raw `update-ref` does not honour git's "branch is checked out in another
// worktree" protection, so issuing it from the worktree whose HEAD points at
// the branch keeps HEAD and the ref moving together.
func restoreAgentBranch(cfg *Config, deps *Deps, preRebaseSHA, expectedSHA string) error {
	// Resolve the branch from HEAD, NOT from cfg.AgentBranch. On the retire
	// path AgentBranch is the stale spawn-time name (QUM-1088), and keying
	// the CAS on the merge base does not protect against a wrong NAME: once
	// an agent has merged once, merge-base(parent, staleBranch) EQUALS the
	// stale branch's tip, so the swap SUCCEEDS on a branch nobody asked
	// about, fast-forwards it, leaves the real branch rewound, and reports
	// success. Reproduced in real git; found in review of the first cut of
	// QUM-1100, which had exactly that defect.
	//
	// This is the same resolution the refused-leg message tells a human to
	// perform, and the same rule agentops.Merge already applies to the merge
	// SOURCE (QUM-511). A detached HEAD is refused rather than guessed at.
	ref, err := deps.GitSymbolicRefHead(cfg.AgentWorktree)
	if err != nil {
		return fmt.Errorf("resolving the checked-out branch of %s: %w", cfg.AgentWorktree, err)
	}
	if !strings.HasPrefix(ref, "refs/heads/") {
		return fmt.Errorf("refusing to restore: HEAD of %s is %q, not a branch", cfg.AgentWorktree, ref)
	}
	return deps.GitUpdateRefCAS(cfg.AgentWorktree, ref, preRebaseSHA, expectedSHA)
}

// rebaseFailureError restores the agent branch to its pre-rebase tip and
// builds the caller-facing error (QUM-1090 part B).
//
// `git rebase --abort` returns the branch to ORIG_HEAD, so on the
// abort-succeeded path the compare-and-swap below writes the value the ref
// already holds. THAT IS NOT A POINTLESS WRITE — it is the only thing that
// ASSERTS the abort actually worked. RealGitRebaseAbort swallows every error
// and always returns nil, so a partial abort is invisible to this caller; the
// CAS is its sole detector. On a partial abort HEAD is detached mid-rebase,
// GitSymbolicRefHead errors, and the loud leg below fires.
//
// QUM-1087 CHANGED THE ARGUMENT HERE, and the old one must not be carried
// forward. It read: "`reset --soft` preserved the index and worktree, so the
// squash commit's TREE is byte-identical to preRebaseSHA's; and `rebase
// --abort` restores the index and worktree to that squash commit." Both
// premises are gone — there is no `reset --soft` and no squash commit. What
// remains is simpler and stands on its own: the abort restores branch, index
// and worktree together to ORIG_HEAD, which IS preRebaseSHA. (An agent
// worktree with uncommitted work cannot reach here at all — agentops refuses a
// dirty worktree before calling Merge.)
//
// Run in the AGENT WORKTREE, not SprawlRoot, unlike the premerge writes:
// raw `update-ref` does NOT honour git's "branch is checked out in another
// worktree" protection, so issuing it from the worktree whose HEAD points at
// that branch is what keeps that worktree's index consistent with the move.
//
// Compare-and-swap, never a blind write. The CAS oldSHA is read from HEAD,
// while the ref being swapped is resolved from HEAD too (see
// restoreAgentBranch) — so on the retire path, where cfg.AgentBranch is the
// stale spawn-time name (QUM-1088), the safety rests on the CAS REFUSING
// rather than on the advertised name being right.
func rebaseFailureError(cfg *Config, deps *Deps, preRebaseSHA, agentRef, parentRef string) error {
	refPair := refPairText(agentRef, parentRef)

	curTip, why := deps.GitRevParseHead(cfg.AgentWorktree)
	if why == nil {
		why = restoreAgentBranch(cfg, deps, preRebaseSHA, curTip)
		if why == nil {
			return fmt.Errorf("rebase failed (conflicts likely). Aborted rebase.\nBranch %s %s %s.\n%s",
				cfg.AgentBranch, premergeRestoredClaim, preRebaseSHA, refPair)
		}
	}
	// Deliberately does NOT print a `git update-ref refs/heads/<branch>`
	// one-liner. The CAS refused, which means we do not know which branch is
	// actually damaged — and on the retire path cfg.AgentBranch is the stale
	// spawn-time name, so naming it would aim the caller's recovery at the
	// WRONG branch while leaving the damaged one mid-rebase. That is the
	// QUM-1083 failure mode with extra steps. Name the ref, make the caller
	// confirm the branch.
	return fmt.Errorf("rebase failed (conflicts likely). Aborted rebase.\n"+
		"Could NOT auto-restore the agent branch (%v); it is left wherever `git rebase --abort` got to.\n"+
		"Confirm which branch is actually checked out before recovering:\n"+
		"  git -C %s rev-parse --abbrev-ref HEAD\n"+
		"then, ONLY if that branch is not AHEAD of the recovery ref, point it there\n"+
		"with a compare-and-swap (if it has commits the ref lacks, do NOT point it there):\n"+
		"  git -C %s log --oneline <that-branch>\n"+
		"  git update-ref refs/heads/<that-branch> %s $(git -C %s rev-parse <that-branch>)\n%s",
		why, cfg.AgentWorktree, cfg.AgentWorktree, agentRef, cfg.AgentWorktree, refPair)
}

// refPairText renders the recovery-ref pair. BOTH refs, always: the /agent ref
// covers agent-branch damage, and the /parent ref is what makes a wrongly
// advanced parent a one-liner. A message naming only one of them omits exactly
// the half the reader may need (CLAUDE.md).
func refPairText(agentRef, parentRef string) string {
	return fmt.Sprintf("Recovery refs:\n  %s\n  %s", agentRef, parentRef)
}

// The two ff-failure diagnoses, as named constants because each must identify
// its path UNIQUELY. The two causes are different and their remedies are
// different — a bad rebase is fixed by re-rebasing, a moved parent by
// re-running — so a shared phrase would send half the callers to the wrong
// remedy. The tests assert each path emits its own phrase AND not the other's.
const (
	ffPredicateFailure = "did not produce a fast-forwardable branch"
	parentMovedFailure = "moved during validation"
)

// ffMergeFailureError diagnoses a refused `git merge --ff-only`.
//
// It RE-READS the parent tip rather than reusing the pre-validate value,
// because that read is what distinguishes the two causes. Without it the
// engine would have to guess, and the natural guess ("the rebase was wrong")
// is the wrong diagnosis in the common case: the expected reason for a refusal
// here is that another merge landed while validation was running, which is a
// race with a defined remedy, not an anomaly.
func ffMergeFailureError(cfg *Config, deps *Deps, cause error, parentTipBeforeValidate, rebasedTip, agentRef, parentRef string) error {
	refPair := refPairText(agentRef, parentRef)

	nowTip, readErr := deps.GitRevParseHead(cfg.ParentWorktree)
	switch {
	case readErr != nil:
		return fmt.Errorf("fast-forward merge of %s into %s was refused: %w\n"+
			"(could not re-read %s to diagnose why: %v)\nThe parent was not modified by this merge.\n%s",
			cfg.AgentBranch, cfg.ParentBranch, cause, cfg.ParentBranch, readErr, refPair)
	case nowTip != parentTipBeforeValidate:
		return fmt.Errorf("fast-forward merge of %s into %s was refused: %w\n"+
			"%s %s: it was at %s when validation started and is now at %s — another merge landed while this one was validating.\n"+
			"This is the correct outcome, not an error to reconcile: nothing of yours was merged and the parent keeps the other merge's work.\n"+
			"Re-rebase onto the new tip and re-run the merge.\n%s",
			cfg.AgentBranch, cfg.ParentBranch, cause,
			cfg.ParentBranch, parentMovedFailure, parentTipBeforeValidate, nowTip, refPair)
	default:
		// The parent did not move, so this is NOT the validation race. It does
		// NOT follow that the rebase is at fault, and this branch must not
		// claim it does: `git merge --ff-only` also refuses when the PARENT
		// WORKTREE has local changes it would overwrite, with the branch a
		// perfectly good descendant. That is reachable here — precondition 7
		// checks the caller's worktree clean at merge START, and validation now
		// runs elsewhere for minutes, during which the parent checkout (a live
		// weave or human working tree) can acquire edits. Verified directly
		// against git: parent tip unmoved, `--is-ancestor` true, `--ff-only`
		// exits 1 with "Your local changes ... would be overwritten by merge".
		//
		// So name both candidates and let git's own message (wrapped above)
		// discriminate. Asserting the rebase would send the caller to re-rebase,
		// which fixes nothing when the real problem is a dirty parent.
		return fmt.Errorf("fast-forward merge of %s into %s was refused: %w\n"+
			"%s is still at %s, so nothing moved underneath this merge and re-running will not help by itself.\n"+
			"Either the rebase %s (rebased tip %s), or the %s worktree has local changes that the\n"+
			"fast-forward would overwrite — git's message above says which.\n"+
			"The parent branch was not modified.\n%s",
			cfg.AgentBranch, cfg.ParentBranch, cause,
			cfg.ParentBranch, nowTip, ffPredicateFailure, rebasedTip, cfg.ParentBranch, refPair)
	}
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
// mandatory func seams Deps carries.
//
// Move it deliberately in EITHER DIRECTION — down on a removal as well as up on
// an addition — and say which seams moved. "Bump it when adding one" (the
// previous wording) gives the next reader no way to tell a deliberate DROP from
// a reflect walk that stopped seeing fields, and the whole purpose of a floor is
// to catch the latter.
//
// QUM-1087: removed GitLogRange, GitResetSoft, GitCommit, GitResetHard with the
// squash and the parent rollback (16 → 12); added GitRevParseRef and
// GitIsAncestor for the ref-move predicate (12 → 14).
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
