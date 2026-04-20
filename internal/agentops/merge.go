package agentops

import (
	"fmt"
	"io"
	"strings"

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
	DoMerge       func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
	NewMergeDeps  func() *merge.Deps
	Stderr        io.Writer
}

// Merge squash-merges agentName's branch into the caller's current branch.
func Merge(deps *MergeDeps, agentName, messageOverride string, noValidate, dryRun bool) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	sprawlRoot := deps.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	sprawlCfg, err := deps.LoadConfig(sprawlRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	callerName := deps.Getenv("SPRAWL_AGENT_IDENTITY")
	if callerName == "" {
		return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set")
	}

	// Precondition 1: Agent exists
	agentState, err := deps.LoadAgent(sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found", agentName)
	}

	// Precondition 2: Not a subagent
	if agentState.Subagent {
		return fmt.Errorf("agent %q is a subagent and has no branch to merge", agentName)
	}

	// Precondition 3: Caller is the parent
	if agentState.Parent != callerName {
		return fmt.Errorf("cannot merge %q: you are not its parent (parent is %q)", agentName, agentState.Parent)
	}

	// Precondition 4: Agent status is "active" or "done"
	if agentState.Status != "active" && agentState.Status != "done" {
		return fmt.Errorf("agent %q cannot be merged (status: %q). Agent must be active or done", agentName, agentState.Status)
	}

	// Precondition 5: No active children
	allAgents, err := deps.ListAgents(sprawlRoot)
	if err != nil {
		return fmt.Errorf("listing agents: %w", err)
	}
	var childNames []string
	for _, a := range allAgents {
		if a.Parent == agentName {
			childNames = append(childNames, a.Name)
		}
	}
	if len(childNames) > 0 {
		return fmt.Errorf("agent %q has active children: [%s]. Retire or cascade-retire them first", agentName, strings.Join(childNames, ", "))
	}

	// Precondition 6: Source branch exists
	if !deps.BranchExists(sprawlRoot, agentState.Branch) {
		return fmt.Errorf("branch %q not found", agentState.Branch)
	}

	// Load caller agent to get worktree path.
	callerWorktree := sprawlRoot
	if a, err := deps.LoadAgent(sprawlRoot, callerName); err == nil {
		callerWorktree = a.Worktree
	}

	// Precondition 7: Caller's worktree is clean
	callerStatus, err := deps.GitStatus(callerWorktree)
	if err != nil {
		return fmt.Errorf("checking caller worktree status: %w", err)
	}
	if callerStatus != "" {
		return fmt.Errorf("your worktree has uncommitted changes. Commit or stash before merging")
	}

	// Precondition 8: Agent's worktree is clean
	agentStatus, err := deps.GitStatus(agentState.Worktree)
	if err != nil {
		return fmt.Errorf("checking agent worktree status: %w", err)
	}
	if agentStatus != "" {
		return fmt.Errorf("agent %q has uncommitted changes in worktree. Ask the agent to commit first", agentName)
	}

	// Get current branch for merge target
	targetBranch, err := deps.CurrentBranch(callerWorktree)
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	// Build merge config
	cfg := &merge.Config{
		SprawlRoot:      sprawlRoot,
		AgentName:       agentName,
		AgentBranch:     agentState.Branch,
		AgentWorktree:   agentState.Worktree,
		ParentBranch:    targetBranch,
		ParentWorktree:  callerWorktree,
		MessageOverride: messageOverride,
		NoValidate:      noValidate,
		ValidateCmd:     sprawlCfg.Validate,
		DryRun:          dryRun,
		AgentState:      agentState,
	}

	// Execute merge
	result, err := deps.DoMerge(cfg, deps.NewMergeDeps())
	if err != nil {
		return err
	}

	if result.WasNoOp {
		fmt.Fprintf(deps.Stderr, "Nothing to merge: %s has no new commits\n", agentName)
		return nil
	}

	if !dryRun {
		fmt.Fprintf(deps.Stderr, "Merged agent %q (branch %s) into %s\n", agentName, agentState.Branch, targetBranch)
		fmt.Fprintf(deps.Stderr, "  Squash commit: %s\n", result.CommitHash)
		fmt.Fprintf(deps.Stderr, "  Agent %s is still active (not retired)\n", agentName)
		fmt.Fprintf(deps.Stderr, "  Branch %s preserved (shows in git branch --merged)\n", agentState.Branch)
	}

	return nil
}
