package agentops_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/agentops"
	"github.com/dmotles/sprawl/internal/config"
)

// TestPrepareSpawn_ConfigLoadFailure_AbortsAndRemovesWorktree is the
// guard-install path, and it is the entire QUM-1086 defect class: today a
// config-load failure prints "Warning: could not load config" to os.Stderr —
// which inside `sprawl enter` is the redirected tui-stderr log nobody reads —
// and then CONTINUES, producing an agent worktree with no QUM-808 pre-commit
// guard and no QUM-837 reference-transaction guard. Nothing says so.
//
// The disposition is fatal, on the evidence that the surrounding block ALREADY
// aborts the spawn and removes the worktree when the setup script itself fails
// (spawn.go's setup-script error path). Aborting when the script is
// unreachable is consistency with that established policy, not a new severity.
func TestPrepareSpawn_ConfigLoadFailure_AbortsAndRemovesWorktree(t *testing.T) {
	tmpDir := t.TempDir()
	deps, creator := newBaseRefSpawnDeps(t, tmpDir)

	loadErr := errors.New("unrecognized key `worktree_setup` on line 2")
	deps.LoadConfig = func(string) (*config.Config, error) { return nil, loadErr }

	removed := false
	deps.WorktreeRemove = func(string, string, bool) error {
		removed = true
		return nil
	}
	scriptRan := false
	deps.RunScript = func(string, string, map[string]string) ([]byte, error) {
		scriptRan = true
		return nil, nil
	}

	_, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false)
	if err == nil {
		t.Fatal("PrepareSpawn must abort when the config cannot be loaded: an agent worktree without the commit guards must never be handed out silently")
	}
	if !errors.Is(err, loadErr) {
		t.Errorf("the underlying config error must be wrapped, not summarised away: %v", err)
	}
	if !strings.Contains(err.Error(), "guard") {
		t.Errorf("the error must say WHY this is fatal (the commit guards); got: %v", err)
	}
	if !removed {
		t.Error("the partially-created worktree must be removed, matching the setup-script failure path")
	}
	if scriptRan {
		t.Error("RunScript must not be called when the config could not be loaded")
	}
	if creator.capturedBase == "" {
		t.Error("the worktree must have been created before the config load — otherwise there is nothing to clean up and this test is not exercising the abort path")
	}
}

// TestPrepareSpawn_ConfigLoadsFine_StillRunsSetup is the negative control: the
// same harness with a working LoadConfig must run the setup script and succeed.
// Without this, the test above would also pass if PrepareSpawn simply always
// failed.
func TestPrepareSpawn_ConfigLoadsFine_StillRunsSetup(t *testing.T) {
	tmpDir := t.TempDir()
	deps, _ := newBaseRefSpawnDeps(t, tmpDir)
	deps.LoadConfig = func(string) (*config.Config, error) {
		return &config.Config{WorktreeSetup: "echo hi"}, nil
	}
	gotScript := ""
	deps.RunScript = func(script, _ string, _ map[string]string) ([]byte, error) {
		gotScript = script
		return nil, nil
	}
	removed := false
	deps.WorktreeRemove = func(string, string, bool) error {
		removed = true
		return nil
	}
	if _, err := agentops.PrepareSpawn(deps, "engineering", "engineer", "task body", "dmotles/test-branch", false); err != nil {
		t.Fatalf("PrepareSpawn with a valid config: %v", err)
	}
	if gotScript != "echo hi" {
		t.Errorf("setup script = %q, want %q (the typed WorktreeSetup field must drive the run)", gotScript, "echo hi")
	}
	// Without this, an implementation that always removed the worktree would
	// pass both tests in this file.
	if removed {
		t.Error("the worktree must NOT be removed on the happy path")
	}
}
