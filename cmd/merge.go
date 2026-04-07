package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/spf13/cobra"
)

func realBranchExists(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName) //nolint:gosec // arguments are not user-controlled
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}

type mergeDeps struct {
	getenv        func(string) string
	loadAgent     func(sprawlRoot, name string) (*state.AgentState, error)
	listAgents    func(sprawlRoot string) ([]*state.AgentState, error)
	gitStatus     func(worktree string) (string, error)
	branchExists  func(repoRoot, branchName string) bool
	currentBranch func(repoRoot string) (string, error)
	loadConfig    func(sprawlRoot string) (*config.Config, error)
	doMerge       func(cfg *merge.Config, deps *merge.Deps) (*merge.Result, error)
	newMergeDeps  func() *merge.Deps
	stderr        io.Writer
}

var defaultMergeDeps *mergeDeps

var (
	mergeMessage    string
	mergeNoValidate bool
	mergeDryRun     bool
)

func init() {
	mergeCmd.Flags().StringVarP(&mergeMessage, "message", "m", "", "Override the squash commit message")
	mergeCmd.Flags().BoolVar(&mergeNoValidate, "no-validate", false, "Skip post-merge test validation")
	mergeCmd.Flags().BoolVar(&mergeDryRun, "dry-run", false, "Show what would happen without making changes")
	rootCmd.AddCommand(mergeCmd)
}

var mergeCmd = &cobra.Command{
	Use:   "merge <agent-name>",
	Short: "Squash-merge an agent's branch into the current worktree",
	Long: `Squash the agent's commits, rebase onto your branch, and fast-forward merge.

The agent is NOT retired and the branch is NOT deleted. This is a pure
"pull in your work" operation. The agent stays alive and can continue
to receive work.

The merge acquires a file lock on the agent to prevent concurrent Claude
invocations during the branch rebase.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMergeDeps()
		return runMerge(deps, args[0], mergeMessage, mergeNoValidate, mergeDryRun)
	},
}

func resolveMergeDeps() *mergeDeps {
	if defaultMergeDeps != nil {
		return defaultMergeDeps
	}
	return &mergeDeps{
		getenv:        os.Getenv,
		loadAgent:     state.LoadAgent,
		listAgents:    state.ListAgents,
		gitStatus:     realGitStatus,
		branchExists:  realBranchExists,
		currentBranch: gitCurrentBranch,
		loadConfig:    config.Load,
		doMerge:       merge.Merge,
		newMergeDeps: func() *merge.Deps {
			return &merge.Deps{
				LockAcquire:     merge.RealLockAcquire,
				GitMergeBase:    merge.RealGitMergeBase,
				GitRevParseHead: merge.RealGitRevParseHead,
				GitResetSoft:    merge.RealGitResetSoft,
				GitCommit:       merge.RealGitCommit,
				GitRebase:       merge.RealGitRebase,
				GitRebaseAbort:  merge.RealGitRebaseAbort,
				GitFFMerge:      merge.RealGitFFMerge,
				GitResetHard:    merge.RealGitResetHard,
				RunTests:        merge.RealRunTests,
				WritePoke:       merge.RealWritePoke,
				Stderr:          os.Stderr,
			}
		},
		stderr: os.Stderr,
	}
}

func runMerge(deps *mergeDeps, agentName, messageOverride string, noValidate bool, dryRun bool) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	sprawlRoot := deps.getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	sprawlCfg, err := deps.loadConfig(sprawlRoot)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	callerName := deps.getenv("SPRAWL_AGENT_IDENTITY")
	if callerName == "" {
		return fmt.Errorf("SPRAWL_AGENT_IDENTITY environment variable is not set")
	}

	// Precondition 1: Agent exists
	agent, err := deps.loadAgent(sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found", agentName)
	}

	// Precondition 2: Not a subagent
	if agent.Subagent {
		return fmt.Errorf("agent %q is a subagent and has no branch to merge", agentName)
	}

	// Precondition 3: Caller is the parent
	if agent.Parent != callerName {
		return fmt.Errorf("cannot merge %q: you are not its parent (parent is %q)", agentName, agent.Parent)
	}

	// Precondition 4: Agent status is "active" or "done"
	if agent.Status != "active" && agent.Status != "done" {
		return fmt.Errorf("agent %q cannot be merged (status: %q). Agent must be active or done", agentName, agent.Status)
	}

	// Precondition 5: No active children
	allAgents, err := deps.listAgents(sprawlRoot)
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
	if !deps.branchExists(sprawlRoot, agent.Branch) {
		return fmt.Errorf("branch %q not found", agent.Branch)
	}

	// Load caller agent to get worktree path.
	callerWorktree := sprawlRoot
	if a, err := deps.loadAgent(sprawlRoot, callerName); err == nil {
		callerWorktree = a.Worktree
	}

	// Precondition 7: Caller's worktree is clean
	callerStatus, err := deps.gitStatus(callerWorktree)
	if err != nil {
		return fmt.Errorf("checking caller worktree status: %w", err)
	}
	if callerStatus != "" {
		return fmt.Errorf("your worktree has uncommitted changes. Commit or stash before merging")
	}

	// Precondition 8: Agent's worktree is clean
	agentStatus, err := deps.gitStatus(agent.Worktree)
	if err != nil {
		return fmt.Errorf("checking agent worktree status: %w", err)
	}
	if agentStatus != "" {
		return fmt.Errorf("agent %q has uncommitted changes in worktree. Ask the agent to commit first", agentName)
	}

	// Get current branch for merge target
	targetBranch, err := deps.currentBranch(callerWorktree)
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	// Build merge config
	cfg := &merge.Config{
		SprawlRoot:      sprawlRoot,
		AgentName:       agentName,
		AgentBranch:     agent.Branch,
		AgentWorktree:   agent.Worktree,
		ParentBranch:    targetBranch,
		ParentWorktree:  callerWorktree,
		MessageOverride: messageOverride,
		NoValidate:      noValidate,
		ValidateCmd:     sprawlCfg.Validate,
		DryRun:          dryRun,
		AgentState:      agent,
	}

	// Execute merge
	result, err := deps.doMerge(cfg, deps.newMergeDeps())
	if err != nil {
		return err
	}

	if result.WasNoOp {
		fmt.Fprintf(deps.stderr, "Nothing to merge: %s has no new commits\n", agentName)
		return nil
	}

	if !dryRun {
		fmt.Fprintf(deps.stderr, "Merged agent %q (branch %s) into %s\n", agentName, agent.Branch, targetBranch)
		fmt.Fprintf(deps.stderr, "  Squash commit: %s\n", result.CommitHash)
		fmt.Fprintf(deps.stderr, "  Agent %s is still active (not retired)\n", agentName)
		fmt.Fprintf(deps.stderr, "  Branch %s preserved (shows in git branch --merged)\n", agent.Branch)
	}

	return nil
}
