package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/dmotles/dendra/internal/state"
	"github.com/spf13/cobra"
)

var cleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Cleanup commands",
}

var cleanupBranchesCmd = &cobra.Command{
	Use:   "branches",
	Short: "Delete merged branches not owned by any agent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		deps := resolveCleanupBranchesDeps()
		return runCleanupBranches(deps, cleanupBranchesDryRun)
	},
}

type cleanupBranchesDeps struct {
	getenv         func(string) string
	listBranches   func() ([]string, error)
	mergedBranches func() ([]string, error)
	deleteBranch   func(name string) error
	listAgents     func(dendraRoot string) ([]*state.AgentState, error)
	stdout         io.Writer
}

var defaultCleanupBranchesDeps *cleanupBranchesDeps

var cleanupBranchesDryRun bool

func init() {
	cleanupBranchesCmd.Flags().BoolVar(&cleanupBranchesDryRun, "dry-run", false, "Show what would be deleted without deleting")
	cleanupCmd.AddCommand(cleanupBranchesCmd)
	rootCmd.AddCommand(cleanupCmd)
}

func resolveCleanupBranchesDeps() *cleanupBranchesDeps {
	if defaultCleanupBranchesDeps != nil {
		return defaultCleanupBranchesDeps
	}
	return &cleanupBranchesDeps{
		getenv:         os.Getenv,
		listBranches:   realListBranches,
		mergedBranches: realMergedBranches,
		deleteBranch:   realDeleteBranch,
		listAgents:     state.ListAgents,
		stdout:         os.Stdout,
	}
}

func parseBranchOutput(output string) []string {
	var branches []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		branches = append(branches, line)
	}
	return branches
}

func realListBranches() ([]string, error) {
	out, err := exec.Command("git", "branch").Output()
	if err != nil {
		return nil, fmt.Errorf("listing branches: %w", err)
	}
	return parseBranchOutput(string(out)), nil
}

func realMergedBranches() ([]string, error) {
	out, err := exec.Command("git", "branch", "--merged").Output()
	if err != nil {
		return nil, fmt.Errorf("listing merged branches: %w", err)
	}
	return parseBranchOutput(string(out)), nil
}

func realDeleteBranch(name string) error {
	if err := exec.Command("git", "branch", "-d", name).Run(); err != nil {
		return fmt.Errorf("deleting branch %q: %w", name, err)
	}
	return nil
}

func runCleanupBranches(deps *cleanupBranchesDeps, dryRun bool) error {
	dendraRoot := deps.getenv("DENDRA_ROOT")
	if dendraRoot == "" {
		return fmt.Errorf("DENDRA_ROOT is not set")
	}

	allBranches, err := deps.listBranches()
	if err != nil {
		return err
	}

	merged, err := deps.mergedBranches()
	if err != nil {
		return err
	}

	agents, err := deps.listAgents(dendraRoot)
	if err != nil {
		return err
	}

	// Build set of branches owned by agents.
	agentBranches := make(map[string]bool)
	for _, a := range agents {
		if a.Branch != "" {
			agentBranches[a.Branch] = true
		}
	}

	// Build set of merged branches.
	mergedSet := make(map[string]bool)
	for _, b := range merged {
		mergedSet[b] = true
	}

	// Categorize branches.
	var toDelete []string
	var unmerged []string
	for _, b := range allBranches {
		if mergedSet[b] {
			if !agentBranches[b] {
				toDelete = append(toDelete, b)
			}
		} else {
			unmerged = append(unmerged, b)
		}
	}

	w := deps.stdout

	if len(toDelete) == 0 {
		fmt.Fprintln(w, "No merged branches to clean up.")
		return nil
	}

	if dryRun {
		fmt.Fprintf(w, "[dry-run] Would delete %d merged %s:\n", len(toDelete), branchWord(len(toDelete)))
		for _, b := range toDelete {
			fmt.Fprintf(w, "  %s\n", b)
		}
		if len(unmerged) > 0 {
			fmt.Fprintf(w, "Would skip %d %s (not fully merged):\n", len(unmerged), branchWord(len(unmerged)))
			for _, b := range unmerged {
				fmt.Fprintf(w, "  %s\n", b)
			}
		}
		return nil
	}

	// Delete merged branches.
	for _, b := range toDelete {
		if err := deps.deleteBranch(b); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "Deleted %d merged %s:\n", len(toDelete), branchWord(len(toDelete)))
	for _, b := range toDelete {
		fmt.Fprintf(w, "  %s\n", b)
	}

	if len(unmerged) > 0 {
		fmt.Fprintf(w, "Skipped %d %s (not fully merged):\n", len(unmerged), branchWord(len(unmerged)))
		for _, b := range unmerged {
			fmt.Fprintf(w, "  %s\n", b)
		}
	}

	return nil
}

func branchWord(n int) string {
	if n == 1 {
		return "branch"
	}
	return "branches"
}
