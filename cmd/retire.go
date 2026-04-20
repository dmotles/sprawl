package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/tmux"
	"github.com/spf13/cobra"
)

// Aliases so existing tests continue to compile.
type retireDeps = agentops.RetireDeps

// runRetire wraps agentops.Retire, threading the retireNoValidate flag value
// through at call time (tests still use 7 positional args).
func runRetire(deps *retireDeps, agentName string, cascade, force, abandon, mergeFirst, yes bool) error {
	return agentops.Retire(deps, agentName, cascade, force, abandon, mergeFirst, yes, retireNoValidate)
}

var defaultRetireDeps *retireDeps

var (
	retireCascade    bool
	retireForce      bool
	retireAbandon    bool
	retireMerge      bool
	retireYes        bool
	retireNoValidate bool
)

func init() {
	retireCmd.Flags().BoolVar(&retireCascade, "cascade", false, "Retire agent and all descendants bottom-up")
	retireCmd.Flags().BoolVar(&retireForce, "force", false, "Skip dirty worktree check and orphan children")
	retireCmd.Flags().BoolVar(&retireAbandon, "abandon", false, "Discard the agent's work and delete its branch")
	retireCmd.Flags().BoolVar(&retireMerge, "merge", false, "Merge the agent's work into your branch before retiring")
	retireCmd.Flags().BoolVar(&retireYes, "yes", false, "Acknowledge safety warnings (unmerged commits, live process) and proceed")
	retireCmd.Flags().BoolVar(&retireNoValidate, "no-validate", false, "Skip post-merge test validation")
	rootCmd.AddCommand(retireCmd)
}

var retireCmd = &cobra.Command{
	Use:   "retire <agent-name>",
	Short: "Full teardown: stop process, close tmux, remove worktree, delete state",
	Long:  "Full agent teardown. Three workflows:\n\n  sprawl retire <agent>          preserve branch, warn if unmerged\n  sprawl retire --merge <agent>   merge into your branch, then retire\n  sprawl retire --abandon <agent> delete branch and all work",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		deps, err := resolveRetireDeps()
		if err != nil {
			return err
		}
		return runRetire(deps, args[0], retireCascade, retireForce, retireAbandon, retireMerge, retireYes)
	},
}

func resolveRetireDeps() (*retireDeps, error) {
	if defaultRetireDeps != nil {
		return defaultRetireDeps, nil
	}

	tmuxPath, err := tmux.FindTmux()
	if err != nil {
		return nil, fmt.Errorf("tmux is required but not found")
	}

	return &retireDeps{
		TmuxRunner:          &tmux.RealRunner{TmuxPath: tmuxPath},
		Getenv:              os.Getenv,
		WriteFile:           os.WriteFile,
		RemoveFile:          os.Remove,
		SleepFunc:           time.Sleep,
		WorktreeRemove:      realWorktreeRemove,
		GitStatus:           realGitStatus,
		RemoveAll:           os.RemoveAll,
		GitBranchDelete:     realGitBranchDelete,
		GitBranchIsMerged:   realGitBranchIsMerged,
		GitBranchSafeDelete: realGitBranchSafeDelete,
		DoMerge:             merge.Merge,
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
		LoadAgent:          state.LoadAgent,
		CurrentBranch:      gitCurrentBranch,
		GitUnmergedCommits: realGitUnmergedCommits,
		LoadConfig:         config.Load,
		RunScript:          runBashScript,
	}, nil
}
