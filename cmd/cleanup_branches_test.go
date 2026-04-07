package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

func newTestCleanupBranchesDeps(t *testing.T) (*cleanupBranchesDeps, *bytes.Buffer, string) {
	t.Helper()
	tmpDir := t.TempDir()
	os.MkdirAll(state.AgentsDir(tmpDir), 0o755)
	var buf bytes.Buffer

	deps := &cleanupBranchesDeps{
		getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
		listBranches:   func() ([]string, error) { return nil, nil },
		mergedBranches: func() ([]string, error) { return nil, nil },
		deleteBranch:   func(name string) error { return nil },
		listAgents:     state.ListAgents,
		stdout:         &buf,
	}
	return deps, &buf, tmpDir
}

func TestCleanupBranches_HappyPath(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}

	var deleted []string
	deps.deleteBranch = func(name string) error {
		deleted = append(deleted, name)
		return nil
	}

	err := runCleanupBranches(deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(deleted) != 2 {
		t.Fatalf("expected 2 deletions, got %d", len(deleted))
	}

	out := buf.String()
	if !strings.Contains(out, "Deleted 2 merged branches:") {
		t.Errorf("expected 'Deleted 2 merged branches:' in output, got: %s", out)
	}
	if !strings.Contains(out, "  feat-a") {
		t.Errorf("expected '  feat-a' in output, got: %s", out)
	}
	if !strings.Contains(out, "  feat-b") {
		t.Errorf("expected '  feat-b' in output, got: %s", out)
	}
}

func TestCleanupBranches_AgentProtected(t *testing.T) {
	deps, buf, tmpDir := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}

	// Create an agent that uses feat-b
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "bob",
		Branch: "feat-b",
		Status: "active",
	})

	var deleted []string
	deps.deleteBranch = func(name string) error {
		deleted = append(deleted, name)
		return nil
	}

	err := runCleanupBranches(deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(deleted) != 1 || deleted[0] != "feat-a" {
		t.Fatalf("expected only feat-a deleted, got: %v", deleted)
	}

	out := buf.String()
	if !strings.Contains(out, "Deleted 1 merged branch") {
		t.Errorf("expected 'Deleted 1 merged branch' in output, got: %s", out)
	}
}

func TestCleanupBranches_UnmergedHiddenByDefault(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b", "feat-c"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a"}, nil
	}

	var deleted []string
	deps.deleteBranch = func(name string) error {
		deleted = append(deleted, name)
		return nil
	}

	err := runCleanupBranches(deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(deleted) != 1 || deleted[0] != "feat-a" {
		t.Fatalf("expected only feat-a deleted, got: %v", deleted)
	}

	out := buf.String()
	// Should show count summary, not individual branch names
	if !strings.Contains(out, "Skipped 2 unmerged branches") {
		t.Errorf("expected skipped count summary in output, got: %s", out)
	}
	if !strings.Contains(out, "--verbose") {
		t.Errorf("expected --verbose hint in output, got: %s", out)
	}
	// Should NOT list individual branch names
	if strings.Contains(out, "  feat-b") {
		t.Errorf("should not list individual branches without --verbose, got: %s", out)
	}
	if strings.Contains(out, "  feat-c") {
		t.Errorf("should not list individual branches without --verbose, got: %s", out)
	}
}

func TestCleanupBranches_UnmergedShownWithVerbose(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)
	deps.verbose = true

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b", "feat-c"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a"}, nil
	}

	var deleted []string
	deps.deleteBranch = func(name string) error {
		deleted = append(deleted, name)
		return nil
	}

	err := runCleanupBranches(deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(deleted) != 1 || deleted[0] != "feat-a" {
		t.Fatalf("expected only feat-a deleted, got: %v", deleted)
	}

	out := buf.String()
	if !strings.Contains(out, "Skipped 2 branches (not fully merged):") {
		t.Errorf("expected verbose skipped message in output, got: %s", out)
	}
	if !strings.Contains(out, "  feat-b") {
		t.Errorf("expected '  feat-b' in verbose output, got: %s", out)
	}
	if !strings.Contains(out, "  feat-c") {
		t.Errorf("expected '  feat-c' in verbose output, got: %s", out)
	}
}

func TestCleanupBranches_DryRun(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}

	deleteCalled := false
	deps.deleteBranch = func(name string) error {
		deleteCalled = true
		return nil
	}

	err := runCleanupBranches(deps, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if deleteCalled {
		t.Error("deleteBranch should not be called in dry-run mode")
	}

	out := buf.String()
	if !strings.Contains(out, "[dry-run] Would delete 2 merged branches:") {
		t.Errorf("expected dry-run output, got: %s", out)
	}
}

func TestCleanupBranches_NothingToDo(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return nil, nil
	}

	err := runCleanupBranches(deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "No merged branches to clean up.") {
		t.Errorf("expected 'No merged branches to clean up.' in output, got: %s", out)
	}
}

func TestCleanupBranches_MixedScenario(t *testing.T) {
	deps, buf, tmpDir := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b", "feat-c", "feat-d"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b", "feat-c"}, nil
	}

	// feat-b is protected by an agent
	createTestAgent(t, tmpDir, &state.AgentState{
		Name:   "bob",
		Branch: "feat-b",
		Status: "active",
	})

	var deleted []string
	deps.deleteBranch = func(name string) error {
		deleted = append(deleted, name)
		return nil
	}

	err := runCleanupBranches(deps, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// feat-a and feat-c should be deleted (merged, no agent)
	if len(deleted) != 2 {
		t.Fatalf("expected 2 deletions, got %d: %v", len(deleted), deleted)
	}

	out := buf.String()
	// feat-d is unmerged — default (non-verbose) should show count only
	if !strings.Contains(out, "Skipped 1 unmerged branch") {
		t.Errorf("expected skipped count summary for unmerged, got: %s", out)
	}
	// Should NOT list individual branch names without --verbose
	if strings.Contains(out, "  feat-d") {
		t.Errorf("should not list individual branches without --verbose, got: %s", out)
	}
}

func TestCleanupBranches_MissingSprawlRoot(t *testing.T) {
	deps, _, _ := newTestCleanupBranchesDeps(t)
	deps.getenv = func(key string) string { return "" }

	err := runCleanupBranches(deps, false)
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("expected error to mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestCleanupBranches_DryRunWithUnmerged(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a"}, nil
	}

	err := runCleanupBranches(deps, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "[dry-run] Would delete 1 merged branch") {
		t.Errorf("expected dry-run output, got: %s", out)
	}
	// Default (non-verbose): count summary only
	if !strings.Contains(out, "Would skip 1 unmerged branch") {
		t.Errorf("expected 'Would skip' count summary in output, got: %s", out)
	}
	if !strings.Contains(out, "--verbose") {
		t.Errorf("expected --verbose hint in output, got: %s", out)
	}
}

func TestCleanupBranches_DryRunUnmergedVerbose(t *testing.T) {
	deps, buf, _ := newTestCleanupBranchesDeps(t)
	deps.verbose = true

	deps.listBranches = func() ([]string, error) {
		return []string{"feat-a", "feat-b"}, nil
	}
	deps.mergedBranches = func() ([]string, error) {
		return []string{"feat-a"}, nil
	}

	err := runCleanupBranches(deps, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Would skip 1 branch (not fully merged):") {
		t.Errorf("expected verbose 'Would skip' in output, got: %s", out)
	}
	if !strings.Contains(out, "  feat-b") {
		t.Errorf("expected '  feat-b' in verbose output, got: %s", out)
	}
}

func TestParseBranchOutput_WorktreePlusPrefix(t *testing.T) {
	input := "  sprawl/alder\n+ dmotles/qum-123-wire-feature\n* main\n  feat-b\n"
	got := parseBranchOutput(input)

	expected := []string{"sprawl/alder", "dmotles/qum-123-wire-feature", "feat-b"}
	if len(got) != len(expected) {
		t.Fatalf("expected %d branches, got %d: %v", len(expected), len(got), got)
	}
	for i, want := range expected {
		if got[i] != want {
			t.Errorf("branch[%d]: expected %q, got %q", i, want, got[i])
		}
	}
}
