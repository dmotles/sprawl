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

	if err := SetupBeadsRedirect(repoRoot, worktreePath); err != nil {
		return "", "", fmt.Errorf("setting up beads redirect: %w", err)
	}

	return worktreePath, branchName, nil
}

// SetupBeadsRedirect creates a .beads/redirect file in the worktree pointing
// back to the main repo's .beads/ directory. This allows the bd CLI to find the
// beads database when running from an agent worktree. If .beads/ doesn't exist
// in the repo root, this is a no-op. If a redirect already exists, it is not
// overwritten.
func SetupBeadsRedirect(repoRoot, worktreePath string) error {
	mainBeads := filepath.Join(repoRoot, ".beads")
	if _, err := os.Stat(mainBeads); err != nil {
		return nil //nolint:nilerr // no .beads/ in repo root is not an error
	}

	wtBeads := filepath.Join(worktreePath, ".beads")
	redirectPath := filepath.Join(wtBeads, "redirect")

	if _, err := os.Stat(redirectPath); err == nil {
		return nil // redirect already exists
	}

	if err := os.MkdirAll(wtBeads, 0o755); err != nil { //nolint:gosec // G301: world-readable dir is intentional
		return fmt.Errorf("creating .beads directory in worktree: %w", err)
	}

	relPath, err := filepath.Rel(wtBeads, mainBeads)
	if err != nil {
		return fmt.Errorf("computing relative path to main .beads: %w", err)
	}

	if err := os.WriteFile(redirectPath, []byte(relPath+"\n"), 0o644); err != nil { //nolint:gosec // G306: world-readable file is intentional
		return fmt.Errorf("writing .beads/redirect: %w", err)
	}

	return nil
}

// branchExists checks whether a local git branch with the given name exists.
func branchExists(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName) //nolint:gosec // arguments are not user-controlled
	cmd.Dir = repoRoot
	return cmd.Run() == nil
}
