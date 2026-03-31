package cmd

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
	"github.com/spf13/cobra"
)

// killDeps holds the dependencies for the kill command, enabling testability.
type killDeps struct {
	tmuxRunner  tmux.Runner
	getenv      func(string) string
	signalFunc  func(pid int, sig syscall.Signal) error
	sleepFunc   func(time.Duration)
	processAlive func(pid int) bool
}

var defaultKillDeps *killDeps

var killForce bool

func init() {
	killCmd.Flags().BoolVar(&killForce, "force", false, "SIGKILL immediately without graceful shutdown")
	rootCmd.AddCommand(killCmd)
}

var killCmd = &cobra.Command{
	Use:   "kill <agent-name>",
	Short: "Kill an agent process (preserves state for respawn)",
	Long:  "Stop an agent's process but preserve all state (worktree, branch, state file) for respawn or inspection.",
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
		signalFunc: func(pid int, sig syscall.Signal) error {
			return syscall.Kill(pid, sig)
		},
		sleepFunc:    time.Sleep,
		processAlive: processIsAlive,
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

	// Kill the process(es) in the tmux window
	killProcesses(deps, agentState, force)

	// Close tmux window (best-effort)
	_ = deps.tmuxRunner.KillWindow(agentState.TmuxSession, agentState.TmuxWindow)

	// Update status to killed
	agentState.Status = "killed"
	if err := state.SaveAgent(dendraRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Killed agent %q\n", agentName)
	return nil
}

// killProcesses sends signals to processes in the agent's tmux window.
func killProcesses(deps *killDeps, agentState *state.AgentState, force bool) {
	pids, err := deps.tmuxRunner.ListWindowPIDs(agentState.TmuxSession, agentState.TmuxWindow)
	if err != nil {
		// Window may already be gone — not an error
		return
	}

	for _, pid := range pids {
		if force {
			_ = deps.signalFunc(pid, syscall.SIGKILL)
		} else {
			// Graceful: SIGTERM → wait 2s → SIGKILL
			_ = deps.signalFunc(pid, syscall.SIGTERM)
		}
	}

	if !force && len(pids) > 0 {
		deps.sleepFunc(2 * time.Second)
		for _, pid := range pids {
			if deps.processAlive(pid) {
				_ = deps.signalFunc(pid, syscall.SIGKILL)
			}
		}
	}
}

// processIsAlive checks if a process with the given PID exists.
func processIsAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}
