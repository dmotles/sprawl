package worktree

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// Capture stdout and stderr instead of letting the subprocess inherit
	// the parent's FD 1/2. In TUI mode (Bubble Tea alt-screen), git's
	// "Preparing worktree (...)" / "HEAD is now at ..." progress lines
	// otherwise paint on top of the rendered frame. See QUM-330 (stdout)
	// and QUM-304 (stderr, fixed at the parent process level).
	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", branchName, baseBranch)
	cmd.Dir = repoRoot
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(combined.String())
		if out != "" {
			return "", "", fmt.Errorf("creating git worktree: %w: %s", err, out)
		}
		return "", "", fmt.Errorf("creating git worktree: %w", err)
	}

	// QUM-837 cheap invariant: assert the new worktree's real HEAD is the
	// branch we asked for. `git worktree add -b` cannot silently detach or fall
	// back to another branch (it hard-errors on a name collision), so this never
	// fires in practice — but it guards against a future change to the add
	// invocation reintroducing a silent-divergence path at the source.
	if err := verifyWorktreeHEAD(worktreePath, branchName); err != nil {
		return "", "", err
	}

	if err := SetupBeadsRedirect(repoRoot, worktreePath); err != nil {
		return "", "", fmt.Errorf("setting up beads redirect: %w", err)
	}

	return worktreePath, branchName, nil
}

// verifyWorktreeHEAD asserts the worktree at worktreePath is checked out on
// branchName, returning an error otherwise. Branch detection uses
// `git symbolic-ref --short -q HEAD`, which yields empty (and a non-zero exit)
// on a detached HEAD — both treated as a mismatch here, since a freshly created
// agent worktree must be on its named branch. See QUM-837.
func verifyWorktreeHEAD(worktreePath, branchName string) error {
	cmd := exec.Command("git", "-C", worktreePath, "symbolic-ref", "--short", "-q", "HEAD") //nolint:gosec // arguments are not user-controlled
	cmd.Stderr = io.Discard
	out, _ := cmd.Output()
	got := strings.TrimSpace(string(out))
	if got != branchName {
		return fmt.Errorf("worktree %s HEAD is %q, want branch %q (QUM-837: refusing a worktree not checked out on its intended branch)", worktreePath, got, branchName)
	}
	return nil
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
//
// stdio is explicitly redirected to io.Discard so that a missing ref — which
// makes git print "fatal: Needed a single revision" to stderr — cannot inherit
// the parent's FD 2 in TUI mode. See QUM-342.
func branchExists(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName) //nolint:gosec // arguments are not user-controlled
	cmd.Dir = repoRoot
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}
