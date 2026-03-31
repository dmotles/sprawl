package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Creator abstracts git worktree operations for testability.
type Creator interface {
	Create(repoRoot, agentName, baseBranch string) (worktreePath, branchName string, err error)
}

// RealCreator implements Creator using real git commands.
type RealCreator struct{}

// Create creates a new git worktree for an agent.
// The worktree is placed at <repoRoot>/.dendra/worktrees/<agentName>/
// on a new branch named dendra/<agentName> based off baseBranch.
func (r *RealCreator) Create(repoRoot, agentName, baseBranch string) (string, string, error) {
	worktreesDir := filepath.Join(repoRoot, ".dendra", "worktrees")
	if err := os.MkdirAll(worktreesDir, 0755); err != nil {
		return "", "", fmt.Errorf("creating worktrees directory: %w", err)
	}

	worktreePath := filepath.Join(worktreesDir, agentName)
	branchName := "dendra/" + agentName

	// If the branch already exists (e.g. from a previously retired agent with the
	// same name), delete it so we can reuse the name. The old work was either
	// already merged or intentionally abandoned when the agent was retired.
	if branchExists(repoRoot, branchName) {
		delCmd := exec.Command("git", "branch", "-D", branchName)
		delCmd.Dir = repoRoot
		if out, err := delCmd.CombinedOutput(); err != nil {
			return "", "", fmt.Errorf("deleting stale branch %s: %s: %w", branchName, out, err)
		}
	}

	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName, baseBranch)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("creating git worktree: %w", err)
	}

	return worktreePath, branchName, nil
}

// branchExists checks whether a local git branch with the given name exists.
func branchExists(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName)
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}
