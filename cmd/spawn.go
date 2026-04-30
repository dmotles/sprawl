package cmd

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/spf13/cobra"
)

// Type and symbol aliases so existing cmd tests continue to compile with
// minimal churn while the real implementation lives in internal/agentops.
type spawnDeps = agentops.SpawnDeps

// runSpawn wraps agentops.Spawn so the CLI entry point can emit its
// deprecation warning in a single place that both the cobra RunE and
// existing unit tests exercise.
func runSpawn(_ *spawnDeps, _, _, _, _ string) error {
	deprecationWarning("spawn", "spawn")
	return fmt.Errorf("standalone `sprawl spawn` is no longer supported after the same-process cutover; start `sprawl enter` and use the `spawn` MCP tool")
}

var spawnAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Deprecated: use sprawl enter + spawn",
	Long:  "The standalone spawn CLI no longer starts child runtimes. Start `sprawl enter` and use the `spawn` MCP tool from the live weave session instead.",
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSpawn(nil, spawnFamily, spawnType, spawnPrompt, spawnBranch)
	},
}

var (
	spawnFamily string
	spawnType   string
	spawnPrompt string
	spawnBranch string
)

func init() {
	spawnCmd.PersistentFlags().StringVar(&spawnFamily, "family", "", "agent family: engineering, product, qa")
	spawnCmd.PersistentFlags().StringVar(&spawnType, "type", "", "agent type: manager, researcher, engineer, tester, code-merger")
	spawnCmd.PersistentFlags().StringVar(&spawnPrompt, "prompt", "", "task description for the agent")
	_ = spawnCmd.MarkPersistentFlagRequired("family")
	_ = spawnCmd.MarkPersistentFlagRequired("type")
	_ = spawnCmd.MarkPersistentFlagRequired("prompt")

	// Keep the legacy flag shape intact so deprecated CLI calls still surface the
	// same guidance before they fail closed.
	spawnAgentCmd.Flags().StringVar(&spawnBranch, "branch", "", "git branch name for the agent's worktree")
	_ = spawnAgentCmd.MarkFlagRequired("branch")

	spawnCmd.AddCommand(spawnAgentCmd)
	rootCmd.AddCommand(spawnCmd)
}

var spawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Deprecated: use sprawl enter + spawn",
	Long:  "The standalone spawn CLI no longer starts child runtimes. Start `sprawl enter` and use the `spawn` MCP tool from the live weave session instead.",
	RunE: func(_ *cobra.Command, _ []string) error {
		return runSpawn(nil, spawnFamily, spawnType, spawnPrompt, spawnBranch)
	},
}

// Aliases for helper functions used across cmd/ (including by tests).
var (
	FindSprawlBin           = agentops.FindSprawlBin
	runBashScript           = agentops.RunBashScript
	gitCurrentBranch        = agentops.GitCurrentBranch
	realBranchExists        = agentops.RealBranchExists
	realGitBranchDelete     = agentops.RealGitBranchDelete
	realWorktreeRemove      = agentops.RealWorktreeRemove
	realGitBranchIsMerged   = agentops.RealGitBranchIsMerged
	realGitBranchSafeDelete = agentops.RealGitBranchSafeDelete
	realGitStatus           = agentops.RealGitStatus
	realGitUnmergedCommits  = agentops.RealGitUnmergedCommits
)
