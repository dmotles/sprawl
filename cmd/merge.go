package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

type mergeDeps struct {
	getenv         func(string) string
	loadAgent      func(dendraRoot, name string) (*state.AgentState, error)
	listAgents     func(dendraRoot string) ([]*state.AgentState, error)
	gitMergeSquash func(worktree, branch string) error
	gitCommit      func(worktree, message string) error
	gitMergeAbort  func(worktree string) error
	gitStatus      func(worktree string) (string, error)
	branchExists   func(repoRoot, branchName string) bool
	currentBranch  func(repoRoot string) (string, error)
}

var defaultMergeDeps *mergeDeps

var (
	mergeMessage    string
	mergeNoValidate bool
)

func init() {
	mergeCmd.Flags().StringVarP(&mergeMessage, "message", "m", "", "Override the squash commit message")
	mergeCmd.Flags().BoolVar(&mergeNoValidate, "no-validate", false, "Skip test validation (no-op until Issue 2)")
	rootCmd.AddCommand(mergeCmd)
}

var mergeCmd = &cobra.Command{
	Use:   "merge <agent-name>",
	Short: "Squash-merge an agent's branch into the current worktree",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMergeDeps()
		return runMerge(deps, args[0], mergeMessage)
	},
}

func resolveMergeDeps() *mergeDeps {
	if defaultMergeDeps != nil {
		return defaultMergeDeps
	}
	return &mergeDeps{
		getenv:         os.Getenv,
		loadAgent:      state.LoadAgent,
		listAgents:     state.ListAgents,
		gitMergeSquash: realGitMergeSquash,
		gitCommit:      realGitCommit,
		gitMergeAbort:  realGitMergeAbort,
		gitStatus:      realGitStatus,
		branchExists:   realBranchExists,
		currentBranch:  gitCurrentBranch,
	}
}

func runMerge(deps *mergeDeps, agentName, messageOverride string) error {
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	callerName := deps.getenv("DENDRA_AGENT_IDENTITY")
	if callerName == "" {
		return fmt.Errorf("DENDRA_AGENT_IDENTITY environment variable is not set")
	}

	// Precondition 1: Agent exists
	agent, err := deps.loadAgent(dendraRoot, agentName)
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

	// Precondition 4: Agent status is "done"
	if agent.Status != "done" {
		return fmt.Errorf("agent %q has not reported done (status: %q). Use --force to merge anyway", agentName, agent.Status)
	}

	// Precondition 5: No active children
	allAgents, err := deps.listAgents(dendraRoot)
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
	if !deps.branchExists(dendraRoot, agent.Branch) {
		return fmt.Errorf("branch %q not found", agent.Branch)
	}

	// Load caller agent to get worktree path
	callerAgent, err := deps.loadAgent(dendraRoot, callerName)
	if err != nil {
		return fmt.Errorf("loading caller agent %q: %w", callerName, err)
	}

	// Precondition 7: Caller's worktree is clean
	callerStatus, err := deps.gitStatus(callerAgent.Worktree)
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
		return fmt.Errorf("Agent %q has uncommitted changes in worktree. Ask the agent to commit, or use --force to discard.", agentName)
	}

	// Get current branch for commit message
	targetBranch, err := deps.currentBranch(callerAgent.Worktree)
	if err != nil {
		return fmt.Errorf("determining current branch: %w", err)
	}

	// Squash merge
	if err := deps.gitMergeSquash(callerAgent.Worktree, agent.Branch); err != nil {
		// Merge conflict — abort and return error
		_ = deps.gitMergeAbort(callerAgent.Worktree)
		return fmt.Errorf("merge failed: %w", err)
	}

	// Build commit message
	commitMsg := buildMergeCommitMessage(agent, targetBranch, messageOverride)

	// Commit
	if err := deps.gitCommit(callerAgent.Worktree, commitMsg); err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Merged %q into %s\n", agentName, targetBranch)
	return nil
}

func buildMergeCommitMessage(agent *state.AgentState, targetBranch, messageOverride string) string {
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
		firstLine, agent.Branch, targetBranch, agent.Name, agent.Type, agent.Family, coAuthor)
}

func realGitMergeSquash(worktree, branch string) error {
	cmd := exec.Command("git", "merge", "--squash", branch)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func realGitCommit(worktree, message string) error {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git commit: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func realGitMergeAbort(worktree string) error {
	cmd := exec.Command("git", "merge", "--abort")
	cmd.Dir = worktree
	_ = cmd.Run()
	return nil
}

func realBranchExists(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}
