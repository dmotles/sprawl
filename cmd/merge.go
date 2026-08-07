package cmd

import (
	"context"
	"os"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/merge"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/spf13/cobra"
)

// Aliases so existing tests continue to compile.
type mergeDeps = agentops.MergeDeps

// runMerge wraps agentops.Merge so the CLI/cmd test surface keeps a single
// error-return signature; the MergeOutcome is consumed by MCP callers.
var runMerge = func(ctx context.Context, deps *mergeDeps, agentName, messageOverride string, noValidate, dryRun bool) error {
	// salvagingTerminalAgent=false: the CLI merge is the ordinary path.
	_, err := agentops.Merge(ctx, deps, agentName, messageOverride, noValidate, dryRun, false)
	return err
}

var defaultMergeDeps *mergeDeps

var (
	mergeMessage    string
	mergeNoValidate bool
	mergeDryRun     bool
)

func init() {
	// Kept registered, and REFUSED at runtime rather than removed. Removing it
	// would make cobra error on `-m` (loud, fine) but the MCP tool's equivalent
	// silently drops unknown JSON properties, so the two surfaces cannot be
	// retired the same way. Both refuse instead. See agentops.ErrMessageOverrideRetired.
	mergeCmd.Flags().StringVarP(&mergeMessage, "message", "m", "", "(no longer supported) the engine creates no commit — squash on the agent's branch first (QUM-1087)")
	mergeCmd.Flags().BoolVar(&mergeNoValidate, "no-validate", false, "Skip validation of the rebased tree (it runs BEFORE the parent is touched)")
	mergeCmd.Flags().BoolVar(&mergeDryRun, "dry-run", false, "Show what would happen without making changes")
	rootCmd.AddCommand(mergeCmd)
}

var mergeCmd = &cobra.Command{
	Use:   "merge <agent-name>",
	Short: "Rebase an agent's branch onto yours, validate it, then fast-forward",
	Long: `Rebase the agent's branch onto your branch, run validation on the rebased
tree IN THE AGENT'S WORKTREE, and only then fast-forward your branch onto it.

Your branch is mutated exactly once, forward-only, after the tree is already
known good. If validation fails your branch is byte-identical to before and
there is nothing to undo. The agent's individual commits land as they are —
no squash commit is created. To land the work as one commit, squash on the
agent's branch yourself first.

The agent is NOT retired and the branch is NOT deleted. This is a pure
"pull in your work" operation. The agent stays alive and can continue
to receive work.

On failure your branch is untouched, and the error names recovery refs under
refs/sprawl/premerge/<agent>/<timestamp>/{agent,parent}. The single exception is
called out in the error text: if the fast-forward succeeded and another writer
moved your branch immediately afterwards, the merge reports that, and your work
did land.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveMergeDeps()
		return runMerge(cmd.Context(), deps, args[0], mergeMessage, mergeNoValidate, mergeDryRun)
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
		GitStatus:     agentops.RealGitStatus,
		BranchExists:  agentops.RealBranchExists,
		CurrentBranch: agentops.GitCurrentBranch,
		LoadConfig:    config.Load,
		DoMerge:       merge.Merge,
		NewMergeDeps:  func() *merge.Deps { return merge.RealDeps(os.Stderr) },
		Stderr:        os.Stderr,
	}
}
