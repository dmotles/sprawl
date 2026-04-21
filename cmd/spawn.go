package cmd

import (
	"fmt"
	"os"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/dmotles/sprawl/internal/worktree"
	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

// Type and symbol aliases so existing cmd tests continue to compile with
// minimal churn while the real implementation lives in internal/agentops.
type spawnDeps = agentops.SpawnDeps

var (
	runSpawn       = agentops.Spawn
	supportedTypes = agentops.SupportedTypes
	validTypes     = agentops.ValidTypes
	validFamilies  = agentops.ValidFamilies
	isValidType    = agentops.IsValidType
	isValidFamily  = agentops.IsValidFamily
)

var spawnAgentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Spawn a new agent",
	Long:  "Spawn a new agent with the given family, type, and task prompt.",
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveSpawnDeps()
		if err != nil {
			return err
		}
		_, err = runSpawn(deps, spawnFamily, spawnType, spawnPrompt, spawnBranch)
		return err
	},
}

var defaultSpawnDeps *spawnDeps

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

	// --branch applies only to `spawn agent` (which creates its own worktree).
	// `spawn subagent` shares the parent's worktree/branch and must not require it.
	spawnAgentCmd.Flags().StringVar(&spawnBranch, "branch", "", "git branch name for the agent's worktree")
	_ = spawnAgentCmd.MarkFlagRequired("branch")

	spawnCmd.AddCommand(spawnAgentCmd)
	rootCmd.AddCommand(spawnCmd)
}

var spawnCmd = &cobra.Command{
	Use:   "spawn",
	Short: "Spawn a new agent",
	Long:  "Spawn a new agent with the given family, type, and task prompt.",
	RunE: func(_ *cobra.Command, _ []string) error {
		deps, err := resolveSpawnDeps()
		if err != nil {
			return err
		}
		_, err = runSpawn(deps, spawnFamily, spawnType, spawnPrompt, spawnBranch)
		return err
	},
}

func resolveSpawnDeps() (*spawnDeps, error) {
	if defaultSpawnDeps != nil {
		return defaultSpawnDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &spawnDeps{
		TmuxRunner:      &tmux.RealRunner{TmuxPath: tmuxPath},
		WorktreeCreator: &worktree.RealCreator{},
		Getenv:          os.Getenv,
		CurrentBranch:   gitCurrentBranch,
		FindSprawl:      FindSprawlBin,
		NewSpawnLock: func(lockPath string) (func() error, func() error) {
			fl := flock.New(lockPath)
			return fl.Lock, fl.Unlock
		},
		LoadConfig:     config.Load,
		RunScript:      runBashScript,
		WorktreeRemove: realWorktreeRemove,
	}, nil
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
