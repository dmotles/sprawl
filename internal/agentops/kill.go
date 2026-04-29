package agentops

import (
	"fmt"
	"os"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/state"
)

// KillDeps holds the injectable dependencies for Kill.
type KillDeps struct {
	Getenv func(string) string
}

// Kill stops an agent's process but preserves all state for inspection.
func Kill(deps *KillDeps, agentName string, _ bool) error {
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

	// Update status to killed
	agentState.Status = "killed"
	if err := state.SaveAgent(sprawlRoot, agentState); err != nil {
		return fmt.Errorf("updating agent state: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Killed agent %q\n", agentName)
	return nil
}
