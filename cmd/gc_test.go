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

	if err := runGC(deps, false); err != nil {
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

	if err := runGC(deps, true); err != nil {
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
	if err := runGC(deps, false); err != nil {
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

	err := runGC(deps, true)
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

	if err := runGC(deps, false); err == nil {
		t.Errorf("expected error when sprawlRoot fails")
	}
}

func TestRunGC_FreshOrphanReportedAsFresh_DryRun(t *testing.T) {
	deps, out, errOut, fs := newTestGCDeps(t)
	gcSeedOrphan(t, fs.root, "recent", deps.now().Add(-1*time.Hour))

	if err := runGC(deps, false); err != nil {
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
