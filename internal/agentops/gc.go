// Package agentops: gc.go implements orphan agent directory detection and
// cleanup. An "orphan" is a directory under .sprawl/agents/<name>/ that has
// no matching sibling <name>.json file. ScanOrphans is a pure read-only
// inspector; ApplyGC performs the removals via injected callbacks so it can
// be exercised without touching disk or git.
package agentops

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// FreshnessWindow is the cutoff age: any orphan dir whose newest descendant
// mtime is within this window of `now` is considered Fresh and is refused
// for removal.
const FreshnessWindow = 7 * 24 * time.Hour

// OrphanRecord describes one orphan agent directory.
type OrphanRecord struct {
	Name         string
	DirPath      string
	NewestMtime  time.Time
	Fresh        bool
	WorktreePath string
}

// GCDeps bundles the dependencies needed to scan/apply gc. All function
// fields must be non-nil before use.
type GCDeps struct {
	Now            func() time.Time
	ListWorktrees  func() (map[string]bool, error)
	RemoveWorktree func(path string) error
	RemoveAll      func(path string) error
	SprawlRoot     string
}

// DefaultLogRetention is the default age cutoff for session wire-log files:
// logs whose mtime is older than this are eligible for removal (QUM-632).
const DefaultLogRetention = 30 * 24 * time.Hour

// SessionLogRecord describes one session wire-log file under
// .sprawl/logs/sessions/<agent>/.
type SessionLogRecord struct {
	Path  string
	Mtime time.Time
	Stale bool
}

// ScanSessionLogs walks .sprawl/logs/sessions/ and returns a record for every
// *.ndjson file, flagging those older than retention as Stale. Returns
// nil,nil if the sessions directory does not exist. Non-.ndjson files are
// ignored. Records are sorted by Path for determinism.
func ScanSessionLogs(deps GCDeps, retention time.Duration) ([]SessionLogRecord, error) {
	sessionsDir := filepath.Join(deps.SprawlRoot, ".sprawl", "logs", "sessions")
	if _, err := os.Stat(sessionsDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat sessions dir %s: %w", sessionsDir, err)
	}

	cutoff := deps.Now().Add(-retention)
	var recs []SessionLogRecord
	err := filepath.WalkDir(sessionsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".ndjson") {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		mt := info.ModTime()
		recs = append(recs, SessionLogRecord{
			Path:  path,
			Mtime: mt,
			Stale: mt.Before(cutoff),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk sessions dir %s: %w", sessionsDir, err)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].Path < recs[j].Path })
	return recs, nil
}

// ApplyLogGC removes every Stale session-log record via deps.RemoveAll. Fresh
// records are ignored. Never returns an error; inspect res.Errors instead.
func ApplyLogGC(deps GCDeps, recs []SessionLogRecord) ApplyResult {
	var res ApplyResult
	for _, r := range recs {
		if !r.Stale {
			continue
		}
		if err := deps.RemoveAll(r.Path); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("removing session log %s: %w", r.Path, err))
		} else {
			res.Removed = append(res.Removed, r.Path)
		}
	}
	return res
}

// ApplyResult records the outcome of ApplyGC.
type ApplyResult struct {
	Removed       []string
	WorktreesGone []string
	Skipped       []OrphanRecord
	Errors        []error
}

// ScanOrphans walks .sprawl/agents/ and returns an OrphanRecord for every
// directory that lacks a sibling <name>.json. Returns nil,nil if the agents
// directory itself does not exist. Errors from ListWorktrees are swallowed
// (the cross-reference is best-effort).
func ScanOrphans(deps GCDeps) ([]OrphanRecord, error) {
	agentsDir := state.AgentsDir(deps.SprawlRoot)
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read agents dir %s: %w", agentsDir, err)
	}

	// Build a set of names that have a JSON sibling.
	jsonNames := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".json") {
			jsonNames[strings.TrimSuffix(name, ".json")] = true
		}
	}

	// Cross-reference worktrees (best-effort).
	worktrees := map[string]bool{}
	if deps.ListWorktrees != nil {
		if m, lerr := deps.ListWorktrees(); lerr == nil && m != nil {
			worktrees = m
		}
	}

	var orphans []OrphanRecord
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "_orphaned" {
			// QUM-668: the quarantine directory itself is not an orphan — skip.
			continue
		}
		if jsonNames[name] {
			continue
		}
		dirPath := filepath.Join(agentsDir, name)
		newest, werr := newestMtime(dirPath)
		if werr != nil {
			// If we can't walk, skip rather than abort.
			continue
		}
		fresh := newest.After(deps.Now().Add(-FreshnessWindow))
		wt := ""
		candidate := filepath.Join(deps.SprawlRoot, ".sprawl", "worktrees", name)
		if worktrees[candidate] {
			wt = candidate
		}
		orphans = append(orphans, OrphanRecord{
			Name:         name,
			DirPath:      dirPath,
			NewestMtime:  newest,
			Fresh:        fresh,
			WorktreePath: wt,
		})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Name < orphans[j].Name })
	return orphans, nil
}

// newestMtime returns the most recent mtime found by walking dirPath
// (including dirPath itself and all descendants).
func newestMtime(dirPath string) (time.Time, error) {
	var newest time.Time
	sawFile := false
	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // tolerate unreadable entries
		}
		if path == dirPath {
			return nil // exclude the root dir itself; only descendants count
		}
		if d.IsDir() {
			return nil // descend but don't sample dir mtimes
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mt := info.ModTime()
		if !sawFile || mt.After(newest) {
			newest = mt
			sawFile = true
		}
		return nil
	})
	if err != nil {
		return newest, err
	}
	if !sawFile {
		// Empty dir — fall back to the dir's own mtime. We deliberately skip
		// the dir's mtime when files exist because os.MkdirAll stamps a fresh
		// mtime on the parent at creation, which would make every empty-on-disk
		// agent dir appear "fresh" even if no real activity happened. The
		// fallback path is safety-biased toward retention: on POSIX, removing
		// the last child updates the parent mtime, so an empty-but-recent dir
		// will be flagged Fresh and skipped — the safe direction.
		if info, serr := os.Stat(dirPath); serr == nil {
			newest = info.ModTime()
		}
	}
	return newest, nil
}

// ApplyGC iterates orphans (in caller-supplied order), removing each non-fresh
// entry's worktree (if any) before its dir. Fresh entries are recorded in
// Skipped. Errors from RemoveWorktree do not abort dir removal. Never returns
// a non-nil error; inspect Errors instead.
func ApplyGC(deps GCDeps, orphans []OrphanRecord) ApplyResult {
	var res ApplyResult
	for _, r := range orphans {
		if r.Fresh {
			res.Skipped = append(res.Skipped, r)
			continue
		}
		if r.WorktreePath != "" {
			if err := deps.RemoveWorktree(r.WorktreePath); err != nil {
				res.Errors = append(res.Errors, fmt.Errorf("removing worktree %s: %w", r.WorktreePath, err))
			} else {
				res.WorktreesGone = append(res.WorktreesGone, r.WorktreePath)
			}
		}
		if err := deps.RemoveAll(r.DirPath); err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("removing dir %s: %w", r.DirPath, err))
		} else {
			res.Removed = append(res.Removed, r.DirPath)
		}
	}
	return res
}

// ParseWorktreeListPorcelain parses the output of `git worktree list
// --porcelain` and returns a set of worktree paths. Lines that don't start
// with "worktree " (HEAD, branch, detached, bare, blank) are ignored.
func ParseWorktreeListPorcelain(out []byte) map[string]bool {
	m := map[string]bool{}
	s := bufio.NewScanner(bytes.NewReader(out))
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "worktree ") {
			m[strings.TrimPrefix(line, "worktree ")] = true
		}
	}
	return m
}

// DefaultListWorktrees shells out to `git worktree list --porcelain` rooted
// at sprawlRoot. Used as the production binding for GCDeps.ListWorktrees.
func DefaultListWorktrees(sprawlRoot string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", sprawlRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list: %w", err)
	}
	return ParseWorktreeListPorcelain(out), nil
}

// DefaultRemoveWorktree shells out to `git worktree remove --force <path>`.
func DefaultRemoveWorktree(sprawlRoot, path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", sprawlRoot, "worktree", "remove", "--force", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove %s: %s: %w", path, strings.TrimSpace(string(out)), err)
	}
	return nil
}
