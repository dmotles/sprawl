package agentops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
)

// MergeDeps holds the injectable dependencies for Merge.
type MergeDeps struct {
	Getenv        func(string) string
	LoadAgent     func(sprawlRoot, name string) (*state.AgentState, error)
	ListAgents    func(sprawlRoot string) ([]*state.AgentState, error)
	GitStatus     func(worktree string) (string, error)
	BranchExists  func(repoRoot, branchName string) bool
	CurrentBranch func(repoRoot string) (string, error)
	LoadConfig    func(sprawlRoot string) (*config.Config, error)
	DoMerge       func(ctx context.Context, cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
	NewMergeDeps  func() *merge.Deps
	Stderr        io.Writer

	// Checkpoint, if non-nil, is propagated into the merge.Deps constructed
	// by NewMergeDeps so the underlying merge operation emits per-call
	// observability checkpoints (QUM-494). nil is allowed.
	Checkpoint func(step string, kv ...any)
}

// MergeOutcome is returned by Merge so callers (e.g. the MCP toolMerge
// handler) can distinguish a real merge from a no-op (zero new commits) and
// know which branch was actually merged. See QUM-511 / QUM-489.
type MergeOutcome struct {
	NoOp           bool
	ResolvedBranch string

	// QueuedBehind is set by the supervisor layer (not agentops.Merge) when
	// the merge was queued behind another in-flight merge on the same
	// sprawl root. Empty when the merge ran uncontended. See QUM-588.
	QueuedBehind string
	// QueueWait is the time spent blocked on the per-sprawl-root merge
	// lock before this merge began executing. Zero when uncontended.
	QueueWait time.Duration
}

// ErrMessageOverrideRetired is returned for a non-empty message override.
//
// It REFUSES rather than ignores, and the asymmetry between the two surfaces is
// what decides that. Cobra errors on an unknown flag, so deleting `--message`
// would fail loudly for CLI callers — but encoding/json SILENTLY DROPS an
// unknown property, so deleting `message:` from the MCP tool's struct would
// make it a no-op that every agent goes on passing and no one is told about.
// Since one surface cannot fail loudly by deletion, both are made to fail
// loudly by refusal.
var ErrMessageOverrideRetired = errors.New("the merge engine no longer creates a commit, so a commit message has nothing to apply to")

// Merge fast-forwards agentName's branch into the caller's current branch,
// after rebasing and validating it in the agent's own worktree.
//
// salvagingTerminalAgent relaxes precondition 4 (the agent must be active or
// have reported complete). It is named for what it PERMITS rather than for the
// check it disables, deliberately: a parameter called skipPrecondition4 or
// force invites being set to make an error go away, whereas this one can only
// be passed by someone who means it. Set true ONLY by the retire path, where
// the agent is being torn down and its branch is the entire point — an agent
// that died with commits is legitimately mergeable, while precondition 4 exists
// to stop merging an agent that never got going (QUM-625).
func Merge(ctx context.Context, deps *MergeDeps, agentName, messageOverride string, noValidate, dryRun, salvagingTerminalAgent bool) (*MergeOutcome, error) {
	if err := agent.ValidateName(agentName); err != nil {
		return nil, err
	}

	// Refused FIRST, before any precondition work: the caller asked for
	// something the engine cannot do, and doing most of a merge before saying
	// so would be worse than saying so immediately.
	if messageOverride != "" {
		return nil, fmt.Errorf("%w (QUM-1087): the agent's own commits are fast-forwarded as they are.\n"+
			"To land a single commit with a message you choose, squash on the agent's branch first, then merge:\n"+
			"  git -C <agent worktree> reset --soft $(git merge-base HEAD <parent branch>) && git -C <agent worktree> commit",
			ErrMessageOverrideRetired)
	}

	sprawlRoot := deps.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return nil, fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	sprawlCfg, err := deps.LoadConfig(sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	callerName := deps.Getenv("SPRAWL_AGENT_IDENTITY")
	if callerName == "" {
		return nil, fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set")
	}

	// Precondition 1: Agent exists
	agentState, err := deps.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}

	// Precondition 2: Not a subagent
	if agentState.Subagent {
		return nil, fmt.Errorf("agent %q is a subagent and has no branch to merge", agentName)
	}

	// Precondition 3: Caller is the parent
	if agentState.Parent != callerName {
		return nil, fmt.Errorf("cannot merge %q: you are not its parent (parent is %q)", agentName, agentState.Parent)
	}

	// Precondition 4: Agent's Status is in the mergeable allow-set.
	//
	// QUM-1186 (D4): the outcome axis (LastReportState) is deleted, so
	// mergeability is a pure Status question. This precondition's job is to
	// stop you merging a retired, killed or dead agent — NOT to require a live
	// process. A stopped agent's worktree is more quiescent than a running
	// one's, not less.
	//
	// Deliberately an allow-set rather than a deny-set: a deny-set fails OPEN
	// for any status added later. There is deliberately no quiescence gate
	// here — that is QUM-1187.
	if !salvagingTerminalAgent && !mergeableStatus(agentState.Status) {
		// QUM-1186: the hint is SPLIT by what is actually actionable for the
		// observed status. An earlier version said "wake it first if it is
		// retired, killed or dead" — but `wake` explicitly ERRORS on
		// retired/retiring, so for two of the three statuses it named the
		// advice could not work. /cli-ux-best-practices: a next-action hint has
		// to be actionable for every case it covers.
		hint := "wake it first (mcp__sprawl__wake), then retry"
		if state.IsTerminal(agentState.Status) {
			hint = "a retired/retiring agent cannot be woken; merge its branch through `retire --merge` instead"
		}
		return nil, fmt.Errorf("agent %q cannot be merged (status: %q). Merge requires status active, idle, suspended or complete — %s", agentName, agentState.Status, hint)
	}

	// Precondition 5: No active children
	allAgents, err := deps.ListAgents(sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	var childNames []string
	for _, a := range allAgents {
		// QUM-739 / QUM-787: resolved-orphan children are not blockers
		// — skip them in the active-children check. Uses
		// IsResolvedOrphan because QUM-787 narrowed IsTerminal to
		// {retired, retiring}.
		if a.Parent == agentName && !state.IsResolvedOrphan(a.Status) {
			childNames = append(childNames, a.Name)
		}
	}
	if len(childNames) > 0 {
		// QUM-1186: "active children" is now a LIE for one of the statuses this
		// check blocks on. An idle-reclaimed child has no process at all, yet it
		// correctly still blocks — reclamation is a memory optimisation and must
		// be semantically invisible in BOTH directions, so a reclaimed child
		// keeps counting as a child. Being quiet for a while is not evidence
		// that its work is done; that is exactly why `idle` is a distinct state
		// from `complete`.
		//
		// The BLOCK is right; the sentence was wrong. It says "unresolved"
		// rather than "active", and names cascade as the next action
		// (/cli-ux-best-practices).
		return nil, fmt.Errorf("agent %q has unresolved children: [%s]. They may be running, paused, or idle-reclaimed (no process, but revivable and possibly holding unmerged work) — retire or cascade-retire them first", agentName, strings.Join(childNames, ", "))
	}

	// QUM-511: resolve the agent's actual current branch from its worktree
	// HEAD. agentState.Branch records the branch the agent was SPAWNED on; an
	// agent that checks out or creates a branch inside its own worktree —
	// which any agent may do, and which multi-branch work requires — leaves
	// that field stale, so it cannot be trusted as the merge source.
	//
	// QUM-1186: this comment previously cited delegate reusing an agent on a
	// follow-up branch. That mechanism is deleted; the field still goes stale
	// for the more general reason above, so the resolution below stays.
	resolvedBranch, err := deps.CurrentBranch(agentState.Worktree)
	if err != nil {
		return nil, fmt.Errorf("resolving agent %q current branch: %w", agentName, err)
	}
	if resolvedBranch == "" || resolvedBranch == "HEAD" {
		return nil, fmt.Errorf("agent %q worktree is in detached HEAD state; refusing to merge a phantom branch", agentName)
	}

	// Precondition 6 (downgraded): if the spawn-time branch no longer exists,
	// warn but proceed — the resolved current branch is the source of truth.
	if !deps.BranchExists(sprawlRoot, agentState.Branch) {
		fmt.Fprintf(deps.Stderr, "warning: agent %q spawn-time branch %q no longer exists; using resolved current branch %q\n",
			agentName, agentState.Branch, resolvedBranch)
	}

	// Precondition 6': decisive existence check on the resolved branch.
	if !deps.BranchExists(sprawlRoot, resolvedBranch) {
		return nil, fmt.Errorf("branch %q not found", resolvedBranch)
	}

	// Load caller agent to get worktree path.
	callerWorktree := sprawlRoot
	if a, err := deps.LoadAgent(sprawlRoot, callerName); err == nil {
		callerWorktree = a.Worktree
	}

	// Precondition 7: Caller's worktree is clean
	callerStatus, err := deps.GitStatus(callerWorktree)
	if err != nil {
		return nil, fmt.Errorf("checking caller worktree status: %w", err)
	}
	if callerStatus != "" {
		return nil, fmt.Errorf("your worktree has uncommitted changes. Commit or stash before merging")
	}

	// Precondition 8: Agent's worktree is clean
	agentStatus, err := deps.GitStatus(agentState.Worktree)
	if err != nil {
		return nil, fmt.Errorf("checking agent worktree status: %w", err)
	}
	if agentStatus != "" {
		// QUM-1100: staged-with-nothing-modified is the fingerprint of a
		// previous merge that died between `git reset --soft` and its squash
		// commit. Saying "uncommitted changes" there is TRUE and misdescribes
		// the cause, and the natural response to it destroys the work.
		if StagedOnlyPorcelain(agentStatus) {
			return nil, fmt.Errorf(
				"agent %q worktree has STAGED content its branch does not contain, and no unstaged edits.\n"+
					"That is the shape a PREVIOUS FAILED MERGE leaves behind when it was run by a PRE-QUM-1087 engine — its `git reset --soft` ran and its squash commit did not (QUM-1100) — and also what an agent that staged work without committing looks like. The current engine creates no commit and cannot produce this, so on an up-to-date binary the second cause is the likely one.\n"+
					"Do NOT discard or clean this worktree before checking: if it is the former, the staged tree may be the only live copy of the agent's work.\n"+
					"Check, in this order:\n"+
					"  git -C %s rev-parse --abbrev-ref HEAD   # which branch is it really on\n"+
					"  git -C %s log --oneline -3              # does the branch still have its commits\n"+
					"  git -C %s for-each-ref %s/%s\n"+
					"If a recovery ref names a tip the branch no longer has, restore the branch first; otherwise ask the agent to commit",
				agentName, agentState.Worktree, agentState.Worktree, agentState.Worktree,
				merge.PremergeRefPrefix, agentName)
		}
		return nil, fmt.Errorf("agent %q has uncommitted changes in worktree. Ask the agent to commit first", agentName)
	}

	// Get current branch for merge target
	targetBranch, err := deps.CurrentBranch(callerWorktree)
	if err != nil {
		return nil, fmt.Errorf("determining current branch: %w", err)
	}

	// Build merge config
	cfg := &merge.Config{
		SprawlRoot:      sprawlRoot,
		AgentName:       agentName,
		AgentBranch:     resolvedBranch,
		AgentWorktree:   agentState.Worktree,
		ParentBranch:    targetBranch,
		ParentWorktree:  callerWorktree,
		NoValidate:      noValidate,
		ValidateCmd:     sprawlCfg.Validate,
		ValidateTimeout: sprawlCfg.ValidateTimeoutDuration(),
		DryRun:          dryRun,
		AgentState:      agentState,
	}

	// Execute merge
	mergeDeps := deps.NewMergeDeps()
	if mergeDeps != nil && deps.Checkpoint != nil {
		mergeDeps.Checkpoint = deps.Checkpoint
	}
	result, err := deps.DoMerge(ctx, cfg, mergeDeps)
	if err != nil {
		return nil, err
	}

	if result.WasNoOp {
		fmt.Fprintf(deps.Stderr, "Nothing to merge: %s has no new commits\n", agentName)
		return &MergeOutcome{NoOp: true, ResolvedBranch: resolvedBranch}, nil
	}

	if !dryRun {
		fmt.Fprintf(deps.Stderr, "Merged agent %q (branch %s) into %s\n", agentName, resolvedBranch, targetBranch)
		fmt.Fprintf(deps.Stderr, "  %s is now at: %s\n", targetBranch, result.MergedTip)
		fmt.Fprintf(deps.Stderr, "  Agent %s is still active (not retired)\n", agentName)
		fmt.Fprintf(deps.Stderr, "  Branch %s preserved (shows in git branch --merged)\n", resolvedBranch)
	}

	return &MergeOutcome{NoOp: false, ResolvedBranch: resolvedBranch}, nil
}

// mergeableStatus is the closed allow-set for merge precondition 4.
// QUM-1186 (D4). See the precondition for why it is an allow-set.
func mergeableStatus(s string) bool {
	switch s {
	case state.StatusActive,
		// StatusRunning is the on-disk legacy synonym for StatusActive.
		// liveness.LivenessFromStatus projects both to Running, and
		// boot-resume eligibility follows that projection — so merge was the
		// one axis treating them differently, by omission rather than by
		// decision. Deliberately NOT added to the error hint below: nobody
		// shown that error has status "running".
		state.StatusRunning,
		// An idle-reclaimed agent's branch is as merge-ready as a live
		// one's; reclamation is a runtime memory decision, not a statement
		// about the work.
		state.StatusIdle,
		// Suspended and Complete are mergeable TODAY via the deleted
		// LastReportState arm. Dropping them would be a capability
		// regression smuggled in under a deletion.
		state.StatusSuspended,
		state.StatusComplete:
		return true
	}
	return false
}
