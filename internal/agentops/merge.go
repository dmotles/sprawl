package agentops

import (
	"context"
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

// Merge squash-merges agentName's branch into the caller's current branch.
func Merge(ctx context.Context, deps *MergeDeps, agentName, messageOverride string, noValidate, dryRun bool) (*MergeOutcome, error) {
	if err := agent.ValidateName(agentName); err != nil {
		return nil, err
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

	// Precondition 4: Agent is mergeable — alive (Status=="active") OR has
	// reported completion (LastReportState=="complete"). QUM-625 Q1: the old
	// allow-set {active, done} is reproduced under the new axes — "done" is no
	// longer a Status value; completion lives on the outcome axis.
	if agentState.Status != state.StatusActive && agentState.LastReportState != ReportStateComplete {
		return nil, fmt.Errorf("agent %q cannot be merged (status: %q). Agent must be active or have reported complete", agentName, agentState.Status)
	}

	// Precondition 5: No active children
	allAgents, err := deps.ListAgents(sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	var childNames []string
	for _, a := range allAgents {
		if a.Parent == agentName {
			childNames = append(childNames, a.Name)
		}
	}
	if len(childNames) > 0 {
		return nil, fmt.Errorf("agent %q has active children: [%s]. Retire or cascade-retire them first", agentName, strings.Join(childNames, ", "))
	}

	// QUM-511: resolve the agent's actual current branch from its worktree
	// HEAD. The spawn-time agentState.Branch field goes stale once delegate
	// reuses the agent on a follow-up branch, so we cannot trust it as the
	// merge source.
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
		MessageOverride: messageOverride,
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
		fmt.Fprintf(deps.Stderr, "  Squash commit: %s\n", result.CommitHash)
		fmt.Fprintf(deps.Stderr, "  Agent %s is still active (not retired)\n", agentName)
		fmt.Fprintf(deps.Stderr, "  Branch %s preserved (shows in git branch --merged)\n", resolvedBranch)
	}

	return &MergeOutcome{NoOp: false, ResolvedBranch: resolvedBranch}, nil
}
