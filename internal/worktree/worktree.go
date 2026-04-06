package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Creator abstracts git worktree operations for testability.
type Creator interface {
	Create(repoRoot, agentName, branchName, baseBranch string) (worktreePath string, branch string, err error)
}

// RealCreator implements Creator using real git commands.
type RealCreator struct{}

// Create creates a new git worktree for an agent.
// The worktree is placed at <repoRoot>/.sprawl/worktrees/<agentName>/
// on a new branch with the given branchName based off baseBranch.
func (r *RealCreator) Create(repoRoot, agentName, branchName, baseBranch string) (string, string, error) {
	worktreesDir := filepath.Join(repoRoot, ".sprawl", "worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil { //nolint:gosec // G301: world-readable worktrees dir is intentional
		return "", "", fmt.Errorf("creating worktrees directory: %w", err)
	}

	worktreePath := filepath.Join(worktreesDir, agentName)

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
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName) //nolint:gosec // arguments are not user-controlled
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}
