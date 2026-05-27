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
	defaultGCDeps      *gcDeps
	gcApply            bool
	gcLogRetentionDays int
)

func init() {
	gcCmd.Flags().BoolVar(&gcApply, "apply", false, "Actually remove orphan dirs and worktrees (default is dry-run)")
	gcCmd.Flags().IntVar(&gcLogRetentionDays, "log-retention-days", 30, "Remove session wire logs older than N days")
	rootCmd.AddCommand(gcCmd)
}

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Clean up orphan agent directories under .sprawl/agents/",
	Long: "Sweeps .sprawl/agents/<name>/ directories that have no matching <name>.json. " +
		"Default is a dry-run report; pass --apply to remove. Refuses to remove dirs with files newer than 7 days.",
	Args: cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runGC(resolveGCDeps(), gcApply, gcLogRetentionDays)
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

func runGC(deps *gcDeps, apply bool, logRetentionDays int) error {
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

	orphanErrs, didSomething, err := runOrphanPass(deps, gcd, root, apply)
	if err != nil {
		return err
	}

	retention := time.Duration(logRetentionDays) * 24 * time.Hour
	logErrs, logDidSomething, err := runLogPass(deps, gcd, retention, apply)
	if err != nil {
		return err
	}
	didSomething = didSomething || logDidSomething

	if !didSomething {
		fmt.Fprintln(deps.errOut, "No orphan agent dirs or stale session logs found. Nothing to do.")
	}

	totalErrs := orphanErrs + logErrs
	if totalErrs > 0 {
		return fmt.Errorf("gc: %d error(s) during apply", totalErrs)
	}
	return nil
}

// runOrphanPass scans and (when apply) removes orphan agent dirs. It returns
// the number of removal errors and whether it found any orphans.
func runOrphanPass(deps *gcDeps, gcd agentops.GCDeps, root string, apply bool) (int, bool, error) {
	orphans, err := agentops.ScanOrphans(gcd)
	if err != nil {
		return 0, false, err
	}
	if len(orphans) == 0 {
		return 0, false, nil
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
		return 0, true, nil
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
	for _, e := range res.Errors {
		fmt.Fprintf(deps.errOut, "  %v\n", e)
	}
	return len(res.Errors), true, nil
}

// runLogPass scans and (when apply) removes stale session wire logs under
// .sprawl/logs/sessions/. Output uses a distinct "wirelog" marker so it can't
// be confused with the orphan-dir pass.
func runLogPass(deps *gcDeps, gcd agentops.GCDeps, retention time.Duration, apply bool) (int, bool, error) {
	recs, err := agentops.ScanSessionLogs(gcd, retention)
	if err != nil {
		return 0, false, err
	}

	staleCount := 0
	for _, r := range recs {
		if r.Stale {
			staleCount++
		}
	}
	if len(recs) == 0 {
		return 0, false, nil
	}

	if !apply {
		fmt.Fprintf(deps.errOut, "Scanning %s for stale session wire logs...\n",
			filepath.Join(gcd.SprawlRoot, ".sprawl", "logs", "sessions"))
		for _, r := range recs {
			marker := "fresh"
			if r.Stale {
				marker = "stale"
			}
			fmt.Fprintf(deps.out, "wirelog  %s  mtime=%s  [%s]\n",
				r.Path, r.Mtime.Format("2006-01-02"), marker)
		}
		fmt.Fprintf(deps.errOut, "%d session wire log(s) found, %d stale.\n", len(recs), staleCount)
		if staleCount > 0 {
			fmt.Fprintln(deps.errOut, "Re-run with --apply to remove stale wirelogs.")
		}
		return 0, true, nil
	}

	fmt.Fprintf(deps.errOut, "Removing %d stale session wire log(s)...\n", staleCount)
	res := agentops.ApplyLogGC(gcd, recs)
	for _, p := range res.Removed {
		fmt.Fprintf(deps.out, "removed  wirelog  %s\n", p)
	}
	fmt.Fprintf(deps.errOut, "%d wirelog(s) removed, %d error(s).\n", len(res.Removed), len(res.Errors))
	for _, e := range res.Errors {
		fmt.Fprintf(deps.errOut, "  %v\n", e)
	}
	return len(res.Errors), true, nil
}
