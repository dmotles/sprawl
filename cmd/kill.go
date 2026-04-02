package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// killDeps holds the dependencies for the kill command, enabling testability.
type killDeps struct {
	tmuxRunner tmux.Runner
	getenv     func(string) string
	writeFile  func(string, []byte, os.FileMode) error
	removeFile func(string) error
	sleepFunc  func(time.Duration)
}

var defaultKillDeps *killDeps

var killForce bool

func init() {
	killCmd.Flags().BoolVar(&killForce, "force", false, "SIGKILL immediately without graceful shutdown")
	rootCmd.AddCommand(killCmd)
}

var killCmd = &cobra.Command{
	Use:   "kill <agent-name>",
	Short: "Kill an agent process (preserves state for inspection)",
	Long:  "Stop an agent's process but preserve all state (worktree, branch, state file) for inspection.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps, err := resolveKillDeps()
		if err != nil {
			return err
		}
		return runKill(deps, args[0], killForce)
	},
}

func resolveKillDeps() (*killDeps, error) {
	if defaultKillDeps != nil {
		return defaultKillDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &killDeps{
		tmuxRunner: &tmux.RealRunner{TmuxPath: tmuxPath},
		getenv:     os.Getenv,
		writeFile:  os.WriteFile,
		removeFile: os.Remove,
		sleepFunc:  time.Sleep,
	}, nil
}

func runKill(deps *killDeps, agentName string, force bool) error {
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := state.LoadAgent(dendraRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	// Idempotent: already killed is a no-op with warning
	if agentState.Status == "killed" {
		fmt.Fprintf(os.Stderr, "Warning: agent %q is already killed\n", agentName)
		return nil
	}

	// Graceful shutdown (or force kill)
	sd := &agent.ShutdownDeps{
		TmuxRunner: deps.tmuxRunner,
		WriteFile:  deps.writeFile,
		RemoveFile: deps.removeFile,
		SleepFunc:  deps.sleepFunc,
	}
	agent.GracefulShutdown(sd, dendraRoot, agentState, force)

	// Update status to killed
	agentState.Status = "killed"
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Killed agent %q\n", agentName)
	return nil
}
