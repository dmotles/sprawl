package merge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
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

// CommitRecord is one commit from the agent branch's range: the pair a
// squash destroys and this package has to carry forward.
type CommitRecord struct {
	// SHA is the full 40-char hash. Recorded in full, not abbreviated:
	// abbreviation length depends on repository size, so a record that is
	// unambiguous in a fixture can be ambiguous in a real repo — and after
	// the squash this SHA is the only handle relating the merged commit to
	// what was reviewed.
	SHA string

	// Message is the raw %B body with its trailing newline trimmed.
	Message string
}

// Subject returns the commit's first line.
func (c CommitRecord) Subject() string {
	if i := strings.IndexByte(c.Message, '\n'); i >= 0 {
		return c.Message[:i]
	}
	return c.Message
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
	LockAcquire  func(lockPath string) (unlock func(), err error)
	GitMergeBase func(repoRoot, a, b string) (string, error)

	// GitLogRange returns the commits in base..head, oldest first, restricted
	// to the branch's own line of development. It is the SOURCE OF THE SQUASH
	// COMMIT MESSAGE (QUM-1105) — the agent's commit messages are the durable
	// record, and after the squash they exist only here.
	GitLogRange func(worktree, base, head string) ([]CommitRecord, error)

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

	// Step 3b (QUM-1105): derive the commit message from the agent's own
	// commits, still before the first mutation. Deriving here rather than at
	// the commit means a branch we cannot describe is refused while it is
	// intact, instead of after `reset --soft` has rewound it to the merge
	// base — the QUM-1100 window, entered for a reason that is knowable in
	// advance.
	commitMsg, err := deriveCommitMessage(cfg, deps, mergeBase, agentHead, agentRef)
	if err != nil {
		return nil, err
	}
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

	commitHash, err := deps.GitCommit(cfg.AgentWorktree, commitMsg)
	if err != nil {
		return nil, commitFailureError(cfg, deps, err, preSquashSHA, mergeBase, agentRef, parentRef)
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

	// Derivation comes AFTER the no-op return, matching Merge's ordering —
	// otherwise the two disagree about when the message is even needed. The
	// recovery ref is named as "" here: a dry run writes none, and printing
	// an invented ref name would be a claim about a ref that does not exist.
	//
	// A revision we failed to read is NOT handed to git as the literal
	// "(unknown)": that would report the wrong cause (a bogus revision)
	// instead of the real one (the read that failed).
	var commitMsg string
	msgErr := errors.Join(baseErr, headErr)
	if msgErr == nil {
		commitMsg, msgErr = deriveCommitMessage(cfg, deps, mergeBase, agentHead, "")
	}

	// A dry run mutates nothing, so a derivation failure is reported and the
	// plan still printed — but it is reported as the blocker it is, since the
	// real merge will refuse for the same reason.
	if msgErr != nil {
		fmt.Fprintf(deps.Stderr, "  Commit message: CANNOT BE DERIVED — this merge would FAIL:\n    %v\n", msgErr)
	} else {
		fmt.Fprintf(deps.Stderr, "  Commit message:\n%s\n", "    "+strings.ReplaceAll(commitMsg, "\n", "\n    "))
	}
	fmt.Fprintf(deps.Stderr, "  Steps: acquire lock → squash → rebase → ff-merge")
	if !cfg.NoValidate && cfg.ValidateCmd != "" {
		fmt.Fprintf(deps.Stderr, " → validate (%s)", cfg.ValidateCmd)
	} else if !cfg.NoValidate && cfg.ValidateCmd == "" {
		fmt.Fprintf(deps.Stderr, " → validate (skipped - not configured)")
	}
	fmt.Fprintf(deps.Stderr, " → poke → release lock\n")

	return &Result{}, nil
}

// errNoDerivableMessage is returned when the agent branch's range yields no
// commit to derive a message from. Deliberately not a fallback: see
// buildMergeCommitMessage.
var errNoDerivableMessage = errors.New("no commit message could be derived")

// buildMergeCommitMessage composes the squash commit message (QUM-1105).
//
// On a squash merge the agent's own commit message IS the durable record —
// the source commits stop being reachable from any branch, and the branch
// itself is deleted at retire. So this reads the commits.
//
// It used to read AgentState.LastReportMessage instead, and that is worth
// stating as a class rather than as a bug: LastReportMessage is a ≤160-char
// status ping written for a TUI line and updated asynchronously, under no
// obligation to be current at merge time. Substituting it was not a
// formatting problem but reading a field whose contract does not include
// being true later — and the failure was silent, because the merge reported
// success and the diff was correct. The observed case replaced a 455-line
// verified message with a one-liner naming a SHA three amends old.
//
// It is therefore NOT a fallback here, in any form. An empty derivation is an
// error, and the caller's remedy is to say what the merge is for via
// messageOverride, which remains the highest-precedence source.
//
// recoveryRef names the premerge /agent ref (QUM-1090) so a reader of the
// squash can find the originals; empty on the dry-run path, where no ref is
// written.
func buildMergeCommitMessage(agent *state.AgentState, parentBranch, messageOverride string, commits []CommitRecord, recoveryRef string) (string, error) {
	const coAuthor = "Co-Authored-By: Claude <noreply@anthropic.com>"

	if messageOverride != "" {
		return messageOverride + "\n\n" + coAuthor, nil
	}
	if len(commits) == 0 {
		return "", errNoDerivableMessage
	}

	var body strings.Builder
	if len(commits) == 1 {
		// Byte-for-byte, subject line included: the point is that a reader of
		// the parent branch sees exactly what the agent wrote. The provenance
		// trailers below are appended to this, so "verbatim" is a claim about
		// the BODY, not about the whole message — the earlier wording said
		// the latter and was false.
		body.WriteString(commits[0].Message)
	} else {
		fmt.Fprintf(&body, "%s: %s (+%d more)\n", agent.Name, commits[0].Subject(), len(commits)-1)
		for _, c := range commits {
			body.WriteString("\n" + c.Message + "\n")
		}
	}

	// Provenance is emitted as TRAILERS, not as a free-text footer, and this
	// is the whole reason the block looks like this.
	//
	// git only parses the message's LAST paragraph as trailers. A free-text
	// footer appended after the body therefore demotes every trailer the
	// agent wrote — `Refs:`, `Signed-off-by:`, its own `Co-Authored-By:` —
	// out of the trailer block. They remain visible as text and stop being
	// readable by `git interpret-trailers`, by GitHub's co-author
	// attribution, and by anything else that parses them. That is precisely
	// the QUM-1105 failure shape one level down: preserved to the eye,
	// silently degraded to every machine.
	//
	// So: all-trailer lines, and appended to the agent's own trailer block
	// (single newline) when the body already ends in one, rather than
	// starting a competing paragraph after it.
	var t strings.Builder
	fmt.Fprintf(&t, "Squash-Merge: %s -> %s\nSprawl-Agent: %s (%s, %s)\n", agent.Branch, parentBranch, agent.Name, agent.Type, agent.Family)
	for _, c := range commits {
		// Source commits stop being reachable from any branch once the squash
		// lands; this and the recovery ref are how a reader gets back to them.
		fmt.Fprintf(&t, "Source-Commit: %s %s\n", c.SHA, c.Subject())
	}
	if recoveryRef != "" {
		fmt.Fprintf(&t, "Premerge-Ref: %s\n", recoveryRef)
	}

	trimmed := strings.TrimRight(body.String(), "\n")
	sep := "\n\n"
	if endsWithTrailerBlock(trimmed) {
		sep = "\n"
	}
	msg := trimmed + sep + t.String()

	// Case-insensitively, because git's trailer matching is: an agent
	// following CLAUDE.md writes `Co-Authored-By: Claude Opus 5 <...>`, which
	// does not contain this exact line, so an exact-match guard appends a
	// SECOND, conflicting co-author to every convention-following commit.
	if !strings.Contains(strings.ToLower(msg), "co-authored-by:") {
		msg += coAuthor + "\n"
	}
	return msg, nil
}

// trailerLine matches a git trailer line: `Token: value`, token being
// alphanumerics and dashes.
var trailerLine = regexp.MustCompile(`^[A-Za-z0-9-]+:( |$)`)

// endsWithTrailerBlock reports whether body's last paragraph is entirely
// trailer lines (plus their folded continuations).
//
// Deliberately STRICTER than git's own rule, which accepts a paragraph that
// is only partly trailers. The two directions are not symmetric: judging a
// trailer paragraph to be prose costs a blank line, while judging a prose
// paragraph to be trailers glues our lines onto the end of the agent's last
// sentence. Only the conservative error is recoverable by reading.
func endsWithTrailerBlock(body string) bool {
	paras := strings.Split(body, "\n\n")
	lines := strings.Split(paras[len(paras)-1], "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			continue // folded continuation of the previous trailer
		}
		if !trailerLine.MatchString(l) {
			return false
		}
	}
	return true
}

// deriveCommitMessage reads the agent branch's commits and composes the
// squash message. Run BEFORE the first mutation: a branch this cannot
// describe must not first be rewound to its merge base.
func deriveCommitMessage(cfg *Config, deps *Deps, mergeBase, agentHead, recoveryRef string) (string, error) {
	commits, err := deps.GitLogRange(cfg.AgentWorktree, mergeBase, agentHead)
	if err != nil {
		return "", fmt.Errorf("reading the agent's commits in %s..%s: %w%s", mergeBase, agentHead, err, explicitMessageRemedy)
	}
	msg, err := buildMergeCommitMessage(cfg.AgentState, cfg.ParentBranch, cfg.MessageOverride, commits, recoveryRef)
	if err != nil {
		return "", fmt.Errorf("%w from %s..%s: the range contains no non-merge commit on the branch's own first-parent line%s", err, mergeBase, agentHead, explicitMessageRemedy)
	}
	return msg, nil
}

// explicitMessageRemedy is appended to every message-derivation failure.
// Derivation failing REFUSES the merge, so without it the caller is stuck;
// the override path is the escape hatch and has to be named.
const explicitMessageRemedy = "\nRe-run supplying the commit message explicitly: `sprawl merge --message \"...\"`, or `message:` on the merge MCP tool."

// restoreAgentBranch compare-and-swaps refs/heads/<AgentBranch> back to
// preSquashSHA, refusing unless the ref currently reads expectedSHA.
//
// Run in the AGENT WORKTREE (not SprawlRoot, unlike the premerge writes):
// raw `update-ref` does not honour git's "branch is checked out in another
// worktree" protection, so issuing it from the worktree whose HEAD points at
// the branch keeps HEAD and the ref moving together.
func restoreAgentBranch(cfg *Config, deps *Deps, preSquashSHA, expectedSHA string) error {
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
	return deps.GitUpdateRefCAS(cfg.AgentWorktree, ref, preSquashSHA, expectedSHA)
}

// commitFailureError undoes the `reset --soft` and builds the caller-facing
// error when the squash commit fails (QUM-1100).
//
// `git commit` is a SUBPROCESS: it runs the pre-commit hook, which runs
// `make validate`. A non-zero exit is therefore ROUTINE, and the window
// between the reset and a completed commit is as wide as a validate run, on
// every merge. It fired in production on 2026-08-06 when an unrelated test
// flake failed the hook, leaving 3026 insertions across 30 files reachable
// from no ref — and the retry then blamed the agent for "uncommitted
// changes" that were the engine's own orphaned squash.
//
// THE RESTORE IS REF-ONLY, AND THAT IS SUFFICIENT. This is a different
// argument from rebaseFailureError's, resting on different facts — do not
// merge the two comments. `git reset --soft` writes ONLY the ref; a
// hook-rejected `git commit` writes nothing at all: no tree, no commit, no
// ref update, and no index change. So the index still holds preSquashSHA's
// exact tree, and the "staged changes" the retry saw were the UNCHANGED
// index compared against a REWOUND HEAD. `update-ref` touches neither index
// nor worktree, so putting the ref back exactly reverses the reset and
// `git status` is clean again (verified directly, and byte-identically in
// the live incident before recovery).
//
// NEVER add a reset, checkout or read-tree here. In the crash variant of
// this window the index is the ONLY live copy of the work, and destroying it
// is precisely what the incident nearly did.
//
// The CAS oldSHA is the MERGE BASE, not a freshly-read HEAD. mergeBase is
// the value this engine itself wrote, so the swap asserts "the ref is still
// where my reset put it, therefore undoing my reset is safe" — a provable
// undo rather than a blind rewind with a CAS-shaped window. It also makes
// the RealGitCommit edge safe, where `git commit` SUCCEEDS and only the
// follow-up hash read fails: HEAD is then a real commit, and keying on
// mergeBase refuses instead of rewinding it.
func commitFailureError(cfg *Config, deps *Deps, cause error, preSquashSHA, mergeBase, agentRef, parentRef string) error {
	refPair := fmt.Sprintf("Recovery refs:\n  %s\n  %s", agentRef, parentRef)

	casErr := restoreAgentBranch(cfg, deps, preSquashSHA, mergeBase)
	if casErr == nil {
		cpMerge(deps, "merge.squash-commit-failed", "restored", "true",
			"branch", cfg.AgentBranch, "restored_to", preSquashSHA)
		return fmt.Errorf("squash commit: %w\nBranch %s %s %s: the `git reset --soft` was undone. The engine did not touch the index or worktree; if a hook staged or modified files before failing, those remain.\n%s",
			cause, cfg.AgentBranch, premergeRestoredClaim, preSquashSHA, refPair)
	}
	cpMerge(deps, "merge.squash-commit-failed", "restored", "false",
		"branch", cfg.AgentBranch, "cas_error", casErr.Error())
	// Deliberately names no branch to update: on the retire path
	// cfg.AgentBranch is the stale spawn-time name (QUM-1088), so a
	// ready-made one-liner would aim recovery at the wrong branch.
	return fmt.Errorf("squash commit: %w\n"+
		"WARNING: the agent branch is NOT at its pre-merge tip %s and could NOT be auto-restored (%v).\n"+
		"The agent's commits may be reachable ONLY from the recovery ref below.\n"+
		"Do NOT discard, clean or check out the agent worktree before recovering: the squash content may exist only in its index.\n"+
		"Confirm which branch is actually checked out:\n"+
		"  git -C %s rev-parse --abbrev-ref HEAD\n"+
		"then, ONLY if that branch is not AHEAD of the recovery ref, point it there\n"+
		"with a compare-and-swap (if it has commits the ref lacks, do NOT point it there):\n"+
		"  git -C %s log --oneline <that-branch>\n"+
		"  git update-ref refs/heads/<that-branch> %s $(git -C %s rev-parse <that-branch>)\n%s",
		cause, preSquashSHA, casErr, cfg.AgentWorktree, cfg.AgentWorktree, agentRef, cfg.AgentWorktree, refPair)
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
		why = restoreAgentBranch(cfg, deps, preSquashSHA, curTip)
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
		"then, ONLY if that branch is not AHEAD of the recovery ref, point it there\n"+
		"with a compare-and-swap (if it has commits the ref lacks, do NOT point it there):\n"+
		"  git -C %s log --oneline <that-branch>\n"+
		"  git update-ref refs/heads/<that-branch> %s $(git -C %s rev-parse <that-branch>)\n%s",
		why, cfg.AgentWorktree, cfg.AgentWorktree, agentRef, cfg.AgentWorktree, refPair)
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
const MinDepsSeams = 16

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
