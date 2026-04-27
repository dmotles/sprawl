// Package agentops contains the business logic for agent spawn/merge/retire/kill
// operations, extracted from cmd/ so that the TUI supervisor (and any other
// caller that cannot import cmd due to cycles) can invoke the same behavior.
//
// Zero-behavior-change extraction: error messages, stderr output, and control
// flow match the cmd/ originals verbatim.
package agentops

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// FindSprawlBin returns the sprawl binary path for spawning child processes.
// If SPRAWL_BIN is set, it returns that value directly; otherwise it falls
// back to os.Executable().
func FindSprawlBin() (string, error) {
	if v := os.Getenv("SPRAWL_BIN"); v != "" {
		return v, nil
	}
	return os.Executable()
}

// RunBashScript executes an inline bash script with bash -e.
// The script runs in the given working directory with the provided env vars
// merged into the current environment.
func RunBashScript(script, workDir string, env map[string]string) ([]byte, error) {
	cmd := exec.Command("bash", "-e")
	cmd.Stdin = strings.NewReader(script)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return cmd.CombinedOutput()
}

// GitCurrentBranch returns the current branch name of the repo at the given root.
func GitCurrentBranch(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	branch := string(out)
	// Trim trailing newline
	if len(branch) > 0 && branch[len(branch)-1] == '\n' {
		branch = branch[:len(branch)-1]
	}
	return branch, nil
}

// RealBranchExists reports whether a local branch exists in the given repo.
//
// stdio is explicitly redirected to io.Discard so that a non-existent ref —
// which makes git print "fatal: Needed a single revision" to stderr — cannot
// inherit the parent's FD 2 in TUI mode (Bubble Tea alt-screen). See QUM-342
// (extends QUM-330's audit to merge/retire git callsites).
func RealBranchExists(repoRoot, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "refs/heads/"+branchName) //nolint:gosec // arguments are not user-controlled
	cmd.Dir = repoRoot
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

// RealGitBranchDelete force-deletes a git branch using 'git branch -D'.
func RealGitBranchDelete(repoRoot, branchName string) error {
	cmd := exec.Command("git", "branch", "-D", branchName)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -D %s: %s: %w", branchName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RealWorktreeRemove removes a git worktree.
func RealWorktreeRemove(repoRoot, worktreePath string, force bool) error {
	args := []string{"worktree", "remove", worktreePath}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RealGitBranchIsMerged checks if a branch is fully merged into the current branch.
func RealGitBranchIsMerged(repoRoot, branchName string) (bool, error) {
	cmd := exec.Command("git", "branch", "--merged")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git branch --merged: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		name = strings.TrimPrefix(name, "* ")
		name = strings.TrimPrefix(name, "+ ")
		if name == branchName {
			return true, nil
		}
	}
	return false, nil
}

// RealGitBranchSafeDelete deletes a branch using 'git branch -d' (safe delete, only if merged).
func RealGitBranchSafeDelete(repoRoot, branchName string) error {
	cmd := exec.Command("git", "branch", "-d", branchName)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git branch -d %s: %s: %w", branchName, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// RealGitStatus returns the porcelain status output for a worktree directory.
// Returns empty string if clean, non-empty if dirty.
func RealGitStatus(worktreePath string) (string, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RealGitUnmergedCommits returns a list of commits on branchName not reachable from main.
func RealGitUnmergedCommits(repoRoot, branchName string) ([]string, error) {
	revRange := "main.." + branchName
	cmd := exec.Command("git", "log", revRange, "--oneline") // #nosec G204 -- branchName is from agent state, not user input
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log %s: %w", revRange, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}
