package agentops

import (
	"fmt"
	"os"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
)

// KillDeps holds the injectable dependencies for Kill.
type KillDeps struct {
	TmuxRunner tmux.Runner
	Getenv     func(string) string
	WriteFile  func(string, []byte, os.FileMode) error
	RemoveFile func(string) error
	SleepFunc  func(time.Duration)
}

// Kill stops an agent's process but preserves all state for inspection.
func Kill(deps *KillDeps, agentName string, force bool) error {
	if err := agent.ValidateName(agentName); err != nil {
		return err
	}

	sprawlRoot := deps.Getenv("SPRAWL_ROOT")
	if sprawlRoot == "" {
		return fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	// Load agent state
	agentState, err := state.LoadAgent(sprawlRoot, agentName)
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
		TmuxRunner: deps.TmuxRunner,
		WriteFile:  deps.WriteFile,
		RemoveFile: deps.RemoveFile,
		SleepFunc:  deps.SleepFunc,
	}
	agent.GracefulShutdown(sd, sprawlRoot, agentState, force)

	// Update status to killed
	agentState.Status = "killed"
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Killed agent %q\n", agentName)
	return nil
}
