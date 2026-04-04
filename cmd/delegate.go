package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

type delegateDeps struct {
	getenv      func(string) string
	loadAgent   func(dendraRoot, name string) (*state.AgentState, error)
	enqueueTask func(dendraRoot, agentName, prompt string) (*state.Task, error)
}

var defaultDelegateDeps *delegateDeps

func resolveDelegateDeps() *delegateDeps {
	if defaultDelegateDeps != nil {
		return defaultDelegateDeps
	}
	return &delegateDeps{
		getenv:      os.Getenv,
		loadAgent:   state.LoadAgent,
		enqueueTask: state.EnqueueTask,
	}
}

func init() {
	rootCmd.AddCommand(delegateCmd)
}

var delegateCmd = &cobra.Command{
	Use:   "delegate <agent-name> <task>",
	Short: "Delegate a task to an existing agent",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveDelegateDeps()
		return runDelegate(deps, args[0], args[1])
	},
}

func runDelegate(deps *delegateDeps, agentName, prompt string) error {
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT environment variable is not set")
	}

	if prompt == "" {
		return fmt.Errorf("task prompt must not be empty")
	}

	agentState, err := deps.loadAgent(dendraRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	switch agentState.Status {
	case "killed", "retired", "retiring":
		return fmt.Errorf("cannot delegate to agent %q: status is %q", agentName, agentState.Status)
	}

	task, err := deps.enqueueTask(dendraRoot, agentName, prompt)
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Delegated task to %s (task ID: %s)\n", agentName, task.ID)
	return nil
}
