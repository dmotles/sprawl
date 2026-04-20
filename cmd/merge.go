package cmd

import (
	"os"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/spf13/cobra"
)

// Aliases so existing tests continue to compile.
type mergeDeps = agentops.MergeDeps

var runMerge = agentops.Merge

var defaultMergeDeps *mergeDeps

var (
	mergeMessage    string
	mergeNoValidate bool
	mergeDryRun     bool
)

func init() {
	mergeCmd.Flags().StringVarP(&mergeMessage, "message", "m", "", "Override the squash commit message")
	mergeCmd.Flags().BoolVar(&mergeNoValidate, "no-validate", false, "Skip post-merge test validation")
	mergeCmd.Flags().BoolVar(&mergeDryRun, "dry-run", false, "Show what would happen without making changes")
	rootCmd.AddCommand(mergeCmd)
}

var mergeCmd = &cobra.Command{
	Use:   "merge <agent-name>",
	Short: "Squash-merge an agent's branch into the current worktree",
	Long: `Squash the agent's commits, rebase onto your branch, and fast-forward merge.

The agent is NOT retired and the branch is NOT deleted. This is a pure
"pull in your work" operation. The agent stays alive and can continue
to receive work.

The merge acquires a file lock on the agent to prevent concurrent Claude
invocations during the branch rebase.`,
	Args: cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps := resolveMergeDeps()
		return runMerge(deps, args[0], mergeMessage, mergeNoValidate, mergeDryRun)
	},
}

func resolveMergeDeps() *mergeDeps {
	if defaultMergeDeps != nil {
		return defaultMergeDeps
	}
	return &mergeDeps{
		Getenv:        os.Getenv,
		LoadAgent:     state.LoadAgent,
		ListAgents:    state.ListAgents,
		GitStatus:     realGitStatus,
		BranchExists:  realBranchExists,
		CurrentBranch: gitCurrentBranch,
		LoadConfig:    config.Load,
		DoMerge:       merge.Merge,
		NewMergeDeps: func() *merge.Deps {
			return &merge.Deps{
				LockAcquire:     merge.RealLockAcquire,
				GitMergeBase:    merge.RealGitMergeBase,
				GitRevParseHead: merge.RealGitRevParseHead,
				GitResetSoft:    merge.RealGitResetSoft,
				GitCommit:       merge.RealGitCommit,
				GitRebase:       merge.RealGitRebase,
				GitRebaseAbort:  merge.RealGitRebaseAbort,
				GitFFMerge:      merge.RealGitFFMerge,
				GitResetHard:    merge.RealGitResetHard,
				RunTests:        merge.RealRunTests,
				WritePoke:       merge.RealWritePoke,
				Stderr:          os.Stderr,
			}
		},
		Stderr: os.Stderr,
	}
}
