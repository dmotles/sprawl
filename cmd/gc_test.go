package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/state"
)

// QUM-632 ASSUMPTION: the implementer extends runGC to also run a session
// wire-log retention pass, changing its signature to
//
//	runGC(deps *gcDeps, apply bool, logRetentionDays int)
//
// All existing call sites below were updated to pass the default retention
// (30 days). The new log-pass tests live at the bottom of this file. If the
// implementer chooses a different plumbing shape, adjust these call sites to
// match — the behavioral assertions are what matter.
const testLogRetentionDays = 30

func newTestGCDeps(t *testing.T) (*gcDeps, *bytes.Buffer, *bytes.Buffer, *gcFakeState) {
	t.Helper()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	fs := &gcFakeState{
		root:         t.TempDir(),
		worktrees:    map[string]bool{},
		removeWTErr:  nil,
		removeAllErr: nil,
	}
	deps := &gcDeps{
		sprawlRoot: func() (string, error) { return fs.root, nil },
		listWorktrees: func(root string) (map[string]bool, error) {
			return fs.worktrees, nil
		},
		removeWorktree: func(root, path string) error {
			fs.removedWT = append(fs.removedWT, path)
			return fs.removeWTErr
		},
		removeAll: func(p string) error {
			fs.removedDirs = append(fs.removedDirs, p)
			return fs.removeAllErr
		},
		now:    func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) },
		out:    out,
		errOut: errOut,
	}
	return deps, out, errOut, fs
}

type gcFakeState struct {
	root         string
	worktrees    map[string]bool
	removedWT    []string
	removedDirs  []string
	removeWTErr  error
	removeAllErr error
}

// gcSeedOrphan creates an orphan dir under <root>/.sprawl/agents/<name> with
// a single file whose mtime is `mtime`.
func gcSeedOrphan(t *testing.T, root, name string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(state.AgentsDir(root), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(dir, "state.json")
	if err := os.WriteFile(file, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chtimes(file, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return dir
}

func TestRunGC_DryRunDefault_ListsAndHintsApply(t *testing.T) {
	deps, out, errOut, fs := newTestGCDeps(t)
	gcSeedOrphan(t, fs.root, "stale", deps.now().Add(-30*24*time.Hour))

	if err := runGC(deps, false, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}
	if !strings.Contains(out.String(), "stale") {
		t.Errorf("expected stale on stdout, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "Re-run with --apply") {
		t.Errorf("expected hint on errOut, got %q", errOut.String())
	}
	if len(fs.removedDirs) != 0 || len(fs.removedWT) != 0 {
		t.Errorf("dry-run mutated: dirs=%v wt=%v", fs.removedDirs, fs.removedWT)
	}
}

func TestRunGC_Apply_RemovesAndReports(t *testing.T) {
	deps, _, errOut, fs := newTestGCDeps(t)
	gcSeedOrphan(t, fs.root, "stale", deps.now().Add(-30*24*time.Hour))

	if err := runGC(deps, true, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}
	if len(fs.removedDirs) != 1 {
		t.Errorf("expected 1 removed, got %v", fs.removedDirs)
	}
	if !strings.Contains(errOut.String(), "removed") {
		t.Errorf("expected summary substring %q on errOut, got %q", "removed", errOut.String())
	}
}

func TestRunGC_EmptyState_NothingToDo(t *testing.T) {
	deps, _, errOut, _ := newTestGCDeps(t)
	if err := runGC(deps, false, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}
	if !strings.Contains(errOut.String(), "Nothing to do") {
		t.Errorf("expected nothing-to-do, got %q", errOut.String())
	}
}

func TestRunGC_ApplyWithErrors_ReturnsError(t *testing.T) {
	deps, _, errOut, fs := newTestGCDeps(t)
	fs.removeAllErr = errors.New("rm fail")
	gcSeedOrphan(t, fs.root, "stale", deps.now().Add(-30*24*time.Hour))

	err := runGC(deps, true, testLogRetentionDays)
	if err == nil {
		t.Fatalf("expected error")
	}
	if errOut.Len() == 0 {
		t.Errorf("expected errOut to have output")
	}
}

func TestRunGC_SprawlRootMissing_Errors(t *testing.T) {
	deps, _, _, _ := newTestGCDeps(t)
	deps.sprawlRoot = func() (string, error) { return "", errors.New("SPRAWL_ROOT unset") }

	if err := runGC(deps, false, testLogRetentionDays); err == nil {
		t.Errorf("expected error when sprawlRoot fails")
	}
}

func TestRunGC_FreshOrphanReportedAsFresh_DryRun(t *testing.T) {
	deps, out, errOut, fs := newTestGCDeps(t)
	gcSeedOrphan(t, fs.root, "recent", deps.now().Add(-1*time.Hour))

	if err := runGC(deps, false, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}
	combined := out.String() + errOut.String()
	if !strings.Contains(combined, "FRESH") {
		t.Errorf("expected FRESH marker in output, got out=%q err=%q", out.String(), errOut.String())
	}
}

func TestGCCmd_Registered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "gc" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("gc command not registered on rootCmd")
	}
	var gcCmd *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "gc" {
			gcCmd = c
			break
		}
	}
	if gcCmd.Flags().Lookup("apply") == nil {
		t.Errorf("gc command missing --apply flag")
	}
}

// --- QUM-632: session wire-log retention pass in `sprawl gc` ---

// gcSeedSessionLog writes <root>/.sprawl/logs/sessions/<agent>/<name>.ndjson
// with the given mtime and returns the absolute path.
func gcSeedSessionLog(t *testing.T, root, agent, name string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, ".sprawl", "logs", "sessions", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	full := filepath.Join(dir, name+".ndjson")
	if err := os.WriteFile(full, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := os.Chtimes(full, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return full
}

func TestGCCmd_HasLogRetentionFlag(t *testing.T) {
	var gc *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "gc" {
			gc = c
			break
		}
	}
	if gc == nil {
		t.Fatal("gc command not registered")
	}
	if gc.Flags().Lookup("log-retention-days") == nil {
		t.Errorf("gc command missing --log-retention-days flag")
	}
}

func TestRunGC_DryRun_ListsStaleSessionLogsDistinctly(t *testing.T) {
	deps, out, _, fs := newTestGCDeps(t)
	staleLog := gcSeedSessionLog(t, fs.root, "agentA", "old", deps.now().Add(-40*24*time.Hour))

	if err := runGC(deps, false, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}
	combined := out.String()
	// The stale log must be listed under a distinct marker so it isn't
	// conflated with the orphan-dir pass.
	if !strings.Contains(combined, "wirelog") && !strings.Contains(combined, "logs/sessions") {
		t.Errorf("expected a distinct wirelog/logs-sessions marker, got %q", combined)
	}
	if len(fs.removedDirs) != 0 {
		t.Errorf("dry-run must not remove anything, got %v", fs.removedDirs)
	}
	_ = staleLog
}

func TestRunGC_Apply_RemovesStaleSessionLogsKeepsFresh(t *testing.T) {
	deps, _, _, fs := newTestGCDeps(t)
	staleLog := gcSeedSessionLog(t, fs.root, "agentA", "old", deps.now().Add(-40*24*time.Hour))
	freshLog := gcSeedSessionLog(t, fs.root, "agentB", "fresh", deps.now())

	if err := runGC(deps, true, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}

	removedStale := false
	for _, p := range fs.removedDirs {
		if p == staleLog {
			removedStale = true
		}
		if p == freshLog {
			t.Errorf("fresh session log %q must not be removed", freshLog)
		}
	}
	if !removedStale {
		t.Errorf("stale session log %q should have been removed; removeAll calls=%v", staleLog, fs.removedDirs)
	}
}

// Regression for the gc.go early-return at "No orphan agent dirs found": the
// log-retention pass must still run when there are zero orphan agent dirs.
func TestRunGC_LogPassRunsWithZeroOrphans(t *testing.T) {
	deps, out, _, fs := newTestGCDeps(t)
	staleLog := gcSeedSessionLog(t, fs.root, "agentA", "old", deps.now().Add(-40*24*time.Hour))

	// No orphan agent dirs seeded — only a stale session log.
	if err := runGC(deps, true, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}

	found := false
	for _, p := range fs.removedDirs {
		if p == staleLog {
			found = true
		}
	}
	if !found {
		t.Errorf("log pass must run even with zero orphans; removeAll calls=%v out=%q", fs.removedDirs, out.String())
	}
}

func TestRunGC_OrphanAndLogOutputAreDistinguishable(t *testing.T) {
	deps, out, _, fs := newTestGCDeps(t)
	gcSeedOrphan(t, fs.root, "staleorphan", deps.now().Add(-30*24*time.Hour))
	gcSeedSessionLog(t, fs.root, "agentA", "stalelog", deps.now().Add(-40*24*time.Hour))

	if err := runGC(deps, false, testLogRetentionDays); err != nil {
		t.Fatalf("runGC err: %v", err)
	}
	combined := out.String()
	if !strings.Contains(combined, "staleorphan") {
		t.Errorf("orphan pass output missing, got %q", combined)
	}
	if !strings.Contains(combined, "wirelog") && !strings.Contains(combined, "logs/sessions") {
		t.Errorf("log pass output not distinguishable from orphan pass, got %q", combined)
	}
}
