// Package cmd: gc.go implements `sprawl gc`, which sweeps orphan agent
// directories under .sprawl/agents/<name>/ that lack a sibling <name>.json.
// Defaults to a dry-run report; --apply performs removals. Refuses to delete
// directories whose descendants are newer than the 7-day freshness window.
package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/agentops"
)

// gcDeps wires runGC to the outside world. Tests substitute in-memory fakes
// for each field; production uses the bindings in resolveGCDeps.
type gcDeps struct {
	sprawlRoot     func() (string, error)
	listWorktrees  func(root string) (map[string]bool, error)
	removeWorktree func(root, path string) error
	removeAll      func(path string) error
	now            func() time.Time
	out, errOut    io.Writer
}

var (
	defaultGCDeps *gcDeps
	gcApply       bool
)

func init() {
	gcCmd.Flags().BoolVar(&gcApply, "apply", false, "Actually remove orphan dirs and worktrees (default is dry-run)")
	rootCmd.AddCommand(gcCmd)
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Clean up orphan agent directories under .sprawl/agents/",
	Long: "Sweeps .sprawl/agents/<name>/ directories that have no matching <name>.json. " +
		"Default is a dry-run report; pass --apply to remove. Refuses to remove dirs with files newer than 7 days.",
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runGC(resolveGCDeps(), gcApply)
	},
}

func resolveGCDeps() *gcDeps {
	if defaultGCDeps != nil {
		return defaultGCDeps
	}
	return &gcDeps{
		sprawlRoot: func() (string, error) {
			r := os.Getenv("SPRAWL_ROOT")
			if r == "" {
				return "", fmt.Errorf("SPRAWL_ROOT environment variable is not set")
			}
			return r, nil
		},
		listWorktrees:  agentops.DefaultListWorktrees,
		removeWorktree: agentops.DefaultRemoveWorktree,
		removeAll:      os.RemoveAll,
		now:            time.Now,
		out:            os.Stdout,
		errOut:         os.Stderr,
	}
}

func runGC(deps *gcDeps, apply bool) error {
	root, err := deps.sprawlRoot()
	if err != nil {
		return err
	}
	gcd := agentops.GCDeps{
		Now:            deps.now,
		ListWorktrees:  func() (map[string]bool, error) { return deps.listWorktrees(root) },
		RemoveWorktree: func(p string) error { return deps.removeWorktree(root, p) },
		RemoveAll:      deps.removeAll,
		SprawlRoot:     root,
	}
	orphans, err := agentops.ScanOrphans(gcd)
	if err != nil {
		return err
	}
	if len(orphans) == 0 {
		fmt.Fprintln(deps.errOut, "No orphan agent dirs found. Nothing to do.")
		return nil
	}

	freshCount := 0
	removableCount := 0
	for _, o := range orphans {
		if o.Fresh {
			freshCount++
		} else {
			removableCount++
		}
	}

	if !apply {
		fmt.Fprintf(deps.errOut, "Scanning %s for orphan directories...\n", filepath.Join(root, ".sprawl", "agents"))
		for _, o := range orphans {
			wt := "none"
			if o.WorktreePath != "" {
				wt = "registered"
			}
			fresh := ""
			if o.Fresh {
				fresh = "  [FRESH]"
			}
			fmt.Fprintf(deps.out, "orphan  %s  mtime=%s  worktree=%s%s\n",
				o.Name, o.NewestMtime.Format("2006-01-02"), wt, fresh)
		}
		fmt.Fprintf(deps.errOut, "%d orphan(s) found, %d removable, %d fresh.\n",
			len(orphans), removableCount, freshCount)
		if removableCount > 0 {
			fmt.Fprintln(deps.errOut, "Re-run with --apply to remove.")
		} else {
			fmt.Fprintln(deps.errOut, "All orphans are <7 days old; re-run later to clean up.")
		}
		return nil
	}

	fmt.Fprintf(deps.errOut, "Removing %d orphan agent dir(s)...\n", removableCount)
	res := agentops.ApplyGC(gcd, orphans)

	// Index of worktrees removed for suffix annotation.
	wtGone := map[string]bool{}
	for _, p := range res.WorktreesGone {
		wtGone[p] = true
	}
	for _, o := range orphans {
		if o.Fresh {
			fmt.Fprintf(deps.out, "skipped  %s  FRESH\n", o.Name)
			continue
		}
		// Only emit "removed" if we actually removed the dir.
		dirRemoved := false
		for _, p := range res.Removed {
			if p == o.DirPath {
				dirRemoved = true
				break
			}
		}
		if !dirRemoved {
			continue
		}
		suffix := " (dir)"
		if o.WorktreePath != "" && wtGone[o.WorktreePath] {
			suffix = " (dir + worktree)"
		}
		fmt.Fprintf(deps.out, "removed  %s%s\n", o.Name, suffix)
	}

	fmt.Fprintf(deps.errOut, "%d removed, %d fresh (skipped), %d error(s).\n",
		len(res.Removed), len(res.Skipped), len(res.Errors))
	if len(res.Errors) > 0 {
		for _, e := range res.Errors {
			fmt.Fprintf(deps.errOut, "  %v\n", e)
		}
		return fmt.Errorf("gc: %d error(s) during apply", len(res.Errors))
	}
	fmt.Fprintln(deps.errOut, "Done. Re-run to confirm clean state.")
	return nil
}
