package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

// Aliases so existing tests continue to compile.
type killDeps = agentops.KillDeps

var runKill = agentops.Kill

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
	RunE: func(_ *cobra.Command, args []string) error {
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
		TmuxRunner: &tmux.RealRunner{TmuxPath: tmuxPath},
		Getenv:     os.Getenv,
		WriteFile:  os.WriteFile,
		RemoveFile: os.Remove,
		SleepFunc:  time.Sleep,
	}, nil
}
