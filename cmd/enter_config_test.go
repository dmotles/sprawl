package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/config"
)

// writeEnterConfig plants a config.yaml under a fresh root and returns the root.
func writeEnterConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "state"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "config.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return root
}

// enterDepsForRoot builds the minimal runEnter deps used by the config-gate
// tests, mirroring TestEnter_DefaultsToCwdWhenSprawlRootEmpty. It reports
// whether runProgram was reached.
func enterDepsForRoot(root string, ran *bool) *enterDeps {
	return &enterDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		getwd: func() (string, error) { return root, nil },
		runProgram: func(tea.Model, func(func(tea.Msg))) error {
			*ran = true
			return nil
		},
	}
}

// TestRunEnter_BadConfig_FailsBeforeTUIStarts is the QUM-1086 AC that a config
// error is fatal in the `sprawl enter` path — the exact place where a broken
// config silently costs you the QUM-808 / QUM-837 main-protection guards,
// because worktree.setup is what installs them into every new agent worktree.
//
// The properties asserted, and why each one:
//   - the error is RETURNED from runEnter, so cobra prints it and exits
//     non-zero. Returning it rather than printing from inside runEnter is what
//     keeps it on the real terminal: runEnter returns long before the TUI's
//     stderr redirect and before deps.runProgram. (That ordering is verified
//     end-to-end by the sandbox capture in the issue, not by this test.)
//   - it is a *config.UnknownKeysError. This is the negative control: had
//     runEnter failed for some unrelated init reason, errors.As would not
//     match, so the test cannot pass by accident.
//   - runProgram is NEVER called: bubbletea must not take the screen.
//   - the message carries the offending key, its line, a did-you-mean, and the
//     full recognized-key reference — a user hitting this may have no AI
//     assistance available to work out what the valid keys are.
func TestRunEnter_BadConfig_FailsBeforeTUIStarts(t *testing.T) {
	root := writeEnterConfig(t, "validate: make validate\nworktree_setup: echo oops\n")

	ranProgram := false
	err := runEnter(enterDepsForRoot(root, &ranProgram))
	if err == nil {
		t.Fatal("runEnter must fail on a config with an unrecognized key")
	}
	var uke *config.UnknownKeysError
	if !errors.As(err, &uke) {
		t.Fatalf("runEnter must fail with *config.UnknownKeysError (it failed for some other reason): %T: %v", err, err)
	}
	if ranProgram {
		t.Error("runProgram must not be called: the TUI must never take the screen with a broken config")
	}

	msg := err.Error()
	for _, want := range []string{"worktree_setup", "line 2", "worktree.setup"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must contain %q; got:\n%s", want, msg)
		}
	}
	for _, k := range config.KnownKeys() {
		if !strings.Contains(msg, k) {
			t.Errorf("error must carry the full reference (missing %q); got:\n%s", k, msg)
		}
	}
}

// TestRunEnter_BadConfig_ChecksBeforeWeaveLock: the config gate must run BEFORE
// AcquireWeaveLock, so a user-fixable typo never takes a process-wide lock.
//
// The discriminating assertion is the absence of the lock FILE:
// internal/rootinit/lock.go deliberately leaves weave.lock on disk after
// release, so its existence proves the lock was taken. (Running runEnter twice
// would NOT discriminate — `defer lock.Release()` frees the flock on return, so
// a second run succeeds either way.)
func TestRunEnter_BadConfig_ChecksBeforeWeaveLock(t *testing.T) {
	root := writeEnterConfig(t, "bogus_key: 1\n")

	ranProgram := false
	err := runEnter(enterDepsForRoot(root, &ranProgram))
	var uke *config.UnknownKeysError
	if !errors.As(err, &uke) {
		t.Fatalf("want *config.UnknownKeysError, got %T: %v", err, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".sprawl", "memory", "weave.lock")); statErr == nil {
		t.Error("runEnter took the weave lock before checking the config; the config gate must come first")
	}
}

// TestRunEnter_GoodConfig_PassesTheConfigGate is the essential negative
// control: without it, a gate that rejects EVERY config would pass both tests
// above. The same harness with a valid config must get past the gate and reach
// runProgram.
func TestRunEnter_GoodConfig_PassesTheConfigGate(t *testing.T) {
	root := writeEnterConfig(t, "validate: make validate\nworktree.setup: echo hi\n")

	ranProgram := false
	err := runEnter(enterDepsForRoot(root, &ranProgram))
	if err != nil {
		var uke *config.UnknownKeysError
		if errors.As(err, &uke) {
			t.Fatalf("a valid config must not trip the config gate: %v", err)
		}
		t.Fatalf("runEnter with a valid config: %v", err)
	}
	if !ranProgram {
		t.Error("runProgram must be reached with a valid config")
	}
}
