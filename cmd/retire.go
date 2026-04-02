package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// retireDeps holds the dependencies for the retire command, enabling testability.
type retireDeps struct {
	tmuxRunner     tmux.Runner
	getenv         func(string) string
	writeFile      func(string, []byte, os.FileMode) error
	removeFile     func(string) error
	sleepFunc      func(time.Duration)
	worktreeRemove func(repoRoot, worktreePath string, force bool) error
	gitStatus      func(worktreePath string) (string, error)
	removeAll      func(string) error
}

var defaultRetireDeps *retireDeps

var (
	retireCascade bool
	retireForce   bool
)

func init() {
	retireCmd.Flags().BoolVar(&retireCascade, "cascade", false, "Retire agent and all descendants bottom-up")
	retireCmd.Flags().BoolVar(&retireForce, "force", false, "Skip dirty worktree check and orphan children")
	rootCmd.AddCommand(retireCmd)
}

var retireCmd = &cobra.Command{
	Use:   "retire <agent-name>",
	Short: "Full teardown: stop process, close tmux, remove worktree, delete state",
	Long:  "Full agent teardown. Stops the process, closes the tmux window, removes the git worktree, and deletes the state file (freeing the name). The git branch is always preserved.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveRetireDeps()
		if err != nil {
			return err
		}
		return runRetire(deps, args[0], retireCascade, retireForce)
	},
}

func resolveRetireDeps() (*retireDeps, error) {
	if defaultRetireDeps != nil {
		return defaultRetireDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &retireDeps{
		tmuxRunner:     &tmux.RealRunner{TmuxPath: tmuxPath},
		getenv:         os.Getenv,
		writeFile:      os.WriteFile,
		removeFile:     os.Remove,
		sleepFunc:      time.Sleep,
		worktreeRemove: realWorktreeRemove,
		gitStatus:      realGitStatus,
		removeAll:      os.RemoveAll,
	}, nil
}

func runRetire(deps *retireDeps, agentName string, cascade, force bool) error {
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := state.LoadAgent(dendraRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	// If already in "retiring" state, resume from where we left off (crash recovery)
	if agentState.Status == "retiring" {
		rd := buildRetireDeps(deps)
		if err := agent.RetireAgent(rd, dendraRoot, agentState, force, true); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Retired agent %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
		return nil
	}

	// Check for children
	if !cascade && !force {
		children, err := findChildren(dendraRoot, agentName)
		if err != nil {
			return fmt.Errorf("checking children: %w", err)
		}
		if len(children) > 0 {
			names := make([]string, len(children))
			for i, c := range children {
				names[i] = c.Name
			}
			return fmt.Errorf("%s has %d active children: %s\nUse --cascade to retire %s and all descendants.\nUse --force to retire %s only (children become orphans).",
				agentName, len(children), strings.Join(names, ", "), agentName, agentName)
		}
	}

	// Cascade: retire children first (depth-first, bottom-up)
	if cascade {
		children, err := findChildren(dendraRoot, agentName)
		if err != nil {
			return fmt.Errorf("checking children: %w", err)
		}
		for _, child := range children {
			if err := runRetire(deps, child.Name, true, force); err != nil {
				return fmt.Errorf("retiring child %s: %w", child.Name, err)
			}
		}
	}

	// Crash-safe checkpoint: mark as "retiring"
	agentState.Status = "retiring"
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	rd := buildRetireDeps(deps)
	if err := agent.RetireAgent(rd, dendraRoot, agentState, force, false); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Retired agent %q (branch %s preserved)\n", agentState.Name, agentState.Branch)
	return nil
}

func buildRetireDeps(deps *retireDeps) *agent.RetireDeps {
	return &agent.RetireDeps{
		TmuxRunner:     deps.tmuxRunner,
		WriteFile:      deps.writeFile,
		RemoveFile:     deps.removeFile,
		SleepFunc:      deps.sleepFunc,
		WorktreeRemove: deps.worktreeRemove,
		GitStatus:      deps.gitStatus,
		RemoveAll:      deps.removeAll,
		ReadDir:        os.ReadDir,
		ArchiveMessage: messages.Archive,
		Stderr:         os.Stderr,
	}
}

// findChildren returns all agents that have the given name as their parent.
func findChildren(dendraRoot, parentName string) ([]*state.AgentState, error) {
	agents, err := state.ListAgents(dendraRoot)
	if err != nil {
		return nil, err
	}
	var children []*state.AgentState
	for _, a := range agents {
		if a.Parent == parentName {
			children = append(children, a)
		}
	}
	return children, nil
}

// realWorktreeRemove removes a git worktree.
func realWorktreeRemove(repoRoot, worktreePath string, force bool) error {
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

// realGitStatus returns the porcelain status output for a worktree directory.
// Returns empty string if clean, non-empty if dirty.
func realGitStatus(worktreePath string) (string, error) {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
