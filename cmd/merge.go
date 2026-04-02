package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

type mergeDeps struct {
	getenv          func(string) string
	loadAgent       func(dendraRoot, name string) (*state.AgentState, error)
	listAgents      func(dendraRoot string) ([]*state.AgentState, error)
	gitMergeSquash  func(worktree, branch string) error
	gitCommit       func(worktree, message string) (string, error)
	gitMergeAbort   func(worktree string) error
	gitStatus       func(worktree string) (string, error)
	branchExists    func(repoRoot, branchName string) bool
	currentBranch   func(repoRoot string) (string, error)
	retireAgent     func(dendraRoot string, agent *state.AgentState) error
	gitBranchDelete func(repoRoot, branchName string) error
	runTests        func(dir string) (string, error)
	gitResetHard    func(worktree string) error
	dirExists       func(path string) bool
	stderr          io.Writer
}

var defaultMergeDeps *mergeDeps

var (
	mergeMessage    string
	mergeNoValidate bool
	mergeForce      bool
)

func init() {
	mergeCmd.Flags().StringVarP(&mergeMessage, "message", "m", "", "Override the squash commit message")
	mergeCmd.Flags().BoolVar(&mergeNoValidate, "no-validate", false, "Skip pre-merge and post-merge test validation")
	mergeCmd.Flags().BoolVar(&mergeForce, "force", false, "Force merge even if agent has not reported done")
	rootCmd.AddCommand(mergeCmd)
}

var mergeCmd = &cobra.Command{
	Use:   "merge <agent-name>",
	Short: "Squash-merge an agent's branch into the current worktree",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMergeDeps()
		return runMerge(deps, args[0], mergeMessage, mergeNoValidate, mergeForce)
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
		retireAgent: func(dendraRoot string, a *state.AgentState) error {
			// TmuxRunner and SleepFunc are intentionally nil: skipShutdown=true
			// means no tmux interaction occurs during merge cleanup.
			rd := &agent.RetireDeps{
				WriteFile:      os.WriteFile,
				RemoveFile:     os.Remove,
				WorktreeRemove: realWorktreeRemove,
				GitStatus:      realGitStatus,
				RemoveAll:      os.RemoveAll,
				Stderr:         os.Stderr,
			}
			return agent.RetireAgent(rd, dendraRoot, a, true, true)
		},
		gitBranchDelete: realGitBranchDelete,
		runTests:        realRunTests,
		gitResetHard:    realGitResetHard,
		dirExists:       realDirExists,
		stderr:          os.Stderr,
	}
}

func runMerge(deps *mergeDeps, agentName, messageOverride string, noValidate bool, force bool) error {
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

	// Precondition 4: Agent status is "done" (unless --force)
	if agent.Status != "done" && !force {
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

	// Precondition 8: Agent's worktree is clean (unless --force)
	if !force {
		agentStatus, err := deps.gitStatus(agent.Worktree)
		if err != nil {
			return fmt.Errorf("checking agent worktree status: %w", err)
		}
		if agentStatus != "" {
			return fmt.Errorf("Agent %q has uncommitted changes in worktree. Ask the agent to commit, or use --force to discard.", agentName)
		}
	}

	// Force-retire: when --force and agent is not done, retire FIRST to stop the agent
	forceRetired := false
	if force && agent.Status != "done" {
		fmt.Fprintf(deps.stderr, "Force-merging non-done agent %q (status: %q) -- retiring first\n", agentName, agent.Status)
		if err := deps.retireAgent(dendraRoot, agent); err != nil {
			return fmt.Errorf("force-retire of agent %q failed: %w", agentName, err)
		}
		forceRetired = true
	}

	// Phase 2: Pre-merge validation (skip if force-retired; worktree is gone)
	if !noValidate && !forceRetired {
		if deps.dirExists(agent.Worktree) {
			fmt.Fprintf(deps.stderr, "Validating %s... running build & tests\n", agent.Branch)
			output, err := deps.runTests(agent.Worktree)
			if err != nil {
				truncated := truncateOutput(output, 50)
				return fmt.Errorf("Validation failed: tests failed on branch %s\n%s\nUse --no-validate to skip validation", agent.Branch, truncated)
			}
		}
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
	commitHash, err := deps.gitCommit(callerAgent.Worktree, commitMsg)
	if err != nil {
		return fmt.Errorf("commit failed: %w", err)
	}

	// Phase 4: Post-merge validation
	if !noValidate {
		output, err := deps.runTests(callerAgent.Worktree)
		if err != nil {
			if resetErr := deps.gitResetHard(callerAgent.Worktree); resetErr != nil {
				fmt.Fprintf(deps.stderr, "WARNING: rollback (git reset --hard HEAD~1) failed: %v\n", resetErr)
			}
			if forceRetired {
				fmt.Fprintf(deps.stderr, "⚠ Agent %q was already retired during force-merge. Branch preserved.\n", agentName)
			}
			truncated := truncateOutput(output, 50)
			if forceRetired {
				return fmt.Errorf("Post-merge validation failed: tests fail after merging %s into %s\nThe merge commit has been rolled back. Your branch is back to its pre-merge state.\n%s\n⚠ Agent %q was already retired during force-merge. Branch preserved.\nUse --no-validate to skip validation.", agentName, targetBranch, truncated, agentName)
			}
			return fmt.Errorf("Post-merge validation failed: tests fail after merging %s into %s\nMerge rollback complete: reset the merge commit. Your branch is back to its pre-merge state.\n%s\nAgent %q has NOT retired. Branch preserved.\nUse --no-validate to skip validation, or ask the agent to fix the issue.", agentName, targetBranch, truncated, agentName)
		}
	}

	// Phase 5: Post-merge cleanup (warnings, not errors)
	retireSucceeded := true
	if forceRetired {
		// Already retired before merge — nothing to do
	} else if err := deps.retireAgent(dendraRoot, agent); err != nil {
		retireSucceeded = false
		fmt.Fprintf(deps.stderr, "⚠ Merge successful, but cleanup incomplete:\n")
		fmt.Fprintf(deps.stderr, "  Squash commit created: %s\n", commitHash)
		fmt.Fprintf(deps.stderr, "  Warning: could not retire agent %q: %v\n", agentName, err)
		fmt.Fprintf(deps.stderr, "  Run: dendra retire %s --force\n", agentName)
	}

	branchDeleted := true
	if err := deps.gitBranchDelete(dendraRoot, agent.Branch); err != nil {
		branchDeleted = false
		fmt.Fprintf(deps.stderr, "⚠ Warning: could not delete branch %s: %v\n", agent.Branch, err)
		fmt.Fprintf(deps.stderr, "  Run: git branch -D %s\n", agent.Branch)
	}

	// Phase 6: Success output
	fmt.Fprintf(deps.stderr, "Merged agent %q (branch %s) into %s\n", agentName, agent.Branch, targetBranch)
	fmt.Fprintf(deps.stderr, "  Squash commit: %s %q\n", commitHash, buildFirstLine(agent))
	if branchDeleted {
		fmt.Fprintf(deps.stderr, "  Branch %s deleted\n", agent.Branch)
	}
	if retireSucceeded {
		fmt.Fprintf(deps.stderr, "  Agent %s retired\n", agentName)
	}

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

func realGitCommit(worktree, message string) (string, error) {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git commit: %s", strings.TrimSpace(string(out)))
	}
	hashCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	hashCmd.Dir = worktree
	hashOut, err := hashCmd.Output()
	if err != nil {
		return "unknown", nil
	}
	return strings.TrimSpace(string(hashOut)), nil
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

func realGitBranchDelete(repoRoot, branchName string) error {
	cmd := exec.Command("git", "branch", "-d", branchName)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func realRunTests(dir string) (string, error) {
	cmd := exec.Command("bash", "-c", "make build && go test ./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func realGitResetHard(worktree string) error {
	cmd := exec.Command("git", "reset", "--hard", "HEAD~1")
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

func realDirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
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

func buildFirstLine(a *state.AgentState) string {
	if a.LastReportMessage != "" {
		return a.Name + ": " + a.LastReportMessage
	}
	return fmt.Sprintf("%s: merge branch '%s'", a.Name, a.Branch)
}
