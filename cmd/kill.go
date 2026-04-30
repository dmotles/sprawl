package cmd

import (
	"os"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/spf13/cobra"
)

// Aliases so existing tests continue to compile.
type killDeps = agentops.KillDeps

// runKill wraps agentops.Kill so the CLI entry point emits its deprecation
// warning in a single place exercised by both the cobra RunE and tests.
func runKill(deps *killDeps, agentName string, force bool) error {
	deprecationWarning("kill", "kill")
	if deps != nil {
		sprawlRoot := deps.Getenv("SPRAWL_ROOT")
		lock, err := acquireOfflineLifecycle(sprawlRoot, "kill", "kill")
		if err != nil {
			return err
		}
		defer func() { _ = lock.Release() }()
	}
	return agentops.Kill(deps, agentName, force)
}

var defaultKillDeps *killDeps

var killForce bool

func init() {
	killCmd.Flags().BoolVar(&killForce, "force", false, "SIGKILL immediately without graceful shutdown")
	rootCmd.AddCommand(killCmd)
}

var killCmd = &cobra.Command{
	Use:   "kill <agent-name>",
	Short: "Deprecated offline cleanup; use sprawl enter + kill for live runtimes",
	Long:  "When no weave session is running, mark an agent as killed while preserving its state for inspection. If `sprawl enter` is active, use the kill MCP tool from the live weave session instead.",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return runKill(resolveKillDeps(), args[0], killForce)
	},
}

func resolveKillDeps() *killDeps {
	if defaultKillDeps != nil {
		return defaultKillDeps
	}

	return &killDeps{
		Getenv: os.Getenv,
	}
}
