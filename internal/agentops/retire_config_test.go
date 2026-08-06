package agentops

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRunTeardownScript_ConfigLoadFailure_IsObservable pins the fix for the
// bare `if err != nil { return }` in runTeardownScript — a silent arm that
// neither warns nor fails (the QUM-997 non-asserting-fallback shape, in
// production code).
//
// The severity stays "skip and continue": the function's own doc comment
// already declares teardown best-effort, a script FAILURE only warns, and
// aborting a retire over a config typo would strand the agent in `retiring`,
// which the lifecycle contract treats as terminal-ish. So the defect is the
// silence, not the severity — and the fix is to make the skip observable.
//
// Asserted through the existing Checkpoint seam rather than by scraping
// os.Stderr, so no new writer dependency is introduced.
func TestRunTeardownScript_ConfigLoadFailure_IsObservable(t *testing.T) {
	var steps []string
	scriptRan := false
	deps := &RetireDeps{
		LoadConfig: func(string) (*config.Config, error) {
			return nil, errors.New("unrecognized key `liveness` on line 4")
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) {
			scriptRan = true
			return nil, nil
		},
		Checkpoint: func(step string, _ ...any) { steps = append(steps, step) },
	}

	runTeardownScript(deps, "/tmp/does-not-matter", &state.AgentState{
		Name:     "agent-x",
		Worktree: "/tmp/wt",
	})

	if scriptRan {
		t.Error("teardown must not run when the config could not be loaded")
	}
	found := false
	for _, s := range steps {
		if s == "retire.teardown-config-error" {
			found = true
		}
	}
	if !found {
		t.Errorf("a config-load failure must emit an observable checkpoint, not return silently; checkpoints seen: %v", steps)
	}
}

// TestRunTeardownScript_ConfigLoadsFine_RunsScript is the negative control:
// with a working LoadConfig the script runs and no error checkpoint is
// emitted. Without it, the test above would pass on an implementation that
// always emitted the checkpoint.
func TestRunTeardownScript_ConfigLoadsFine_RunsScript(t *testing.T) {
	var steps []string
	gotScript := ""
	deps := &RetireDeps{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{WorktreeTeardown: "echo bye"}, nil
		},
		RunScript: func(script, _ string, _ map[string]string) ([]byte, error) {
			gotScript = script
			return nil, nil
		},
		Checkpoint: func(step string, _ ...any) { steps = append(steps, step) },
	}

	runTeardownScript(deps, "/tmp/does-not-matter", &state.AgentState{
		Name:     "agent-x",
		Worktree: "/tmp/wt",
	})

	if gotScript != "echo bye" {
		t.Errorf("teardown script = %q, want %q (the typed WorktreeTeardown field must drive the run)", gotScript, "echo bye")
	}
	for _, s := range steps {
		if s == "retire.teardown-config-error" {
			t.Error("no config-error checkpoint may be emitted when the config loads fine")
		}
	}
}

// captureStderr swaps os.Stderr for a pipe, runs fn, and returns what was
// written. Mirrors the swap in helpers_stdio_leak_test.go.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}

// TestRunTeardownScript_NilCheckpoint_StillWarnsOnStderr covers the PRODUCTION
// shape and is the assertion that makes the fix real: Real.composeCheckpoint
// returns nil when there is no call-log logger, so deps.Checkpoint is frequently
// nil in real runs. An implementation that emitted ONLY the checkpoint would
// pass the two tests above and be completely invisible in production.
//
// The stderr warning is the observable that always reaches the operator, and it
// matches the convention of the sibling failure path twenty lines below in the
// same function (`Warning: worktree teardown script failed ...`).
func TestRunTeardownScript_NilCheckpoint_StillWarnsOnStderr(t *testing.T) {
	deps := &RetireDeps{
		LoadConfig: func(string) (*config.Config, error) {
			return nil, errors.New("unrecognized key `liveness` on line 4")
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) {
			t.Error("teardown must not run when the config could not be loaded")
			return nil, nil
		},
		// Checkpoint deliberately nil — the production shape.
	}

	out := captureStderr(t, func() {
		runTeardownScript(deps, "/tmp/does-not-matter", &state.AgentState{
			Name:     "agent-x",
			Worktree: "/tmp/wt",
		})
	})

	if out == "" {
		t.Fatal("with a nil Checkpoint, a config-load failure must still warn on stderr; " +
			"a checkpoint-only fix is a no-op for every run without a call-log logger")
	}
	for _, want := range []string{"liveness", "agent-x", "teardown"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning must contain %q (the cause, the agent, and what was skipped); got:\n%s", want, out)
		}
	}
}

// TestRunTeardownScript_NilCheckpoint_HappyPathIsQuiet is the negative control
// for the test above: with a nil Checkpoint and a working config, the script
// runs and no warning is emitted. Without it, an implementation that always
// warned would pass.
func TestRunTeardownScript_NilCheckpoint_HappyPathIsQuiet(t *testing.T) {
	ran := false
	deps := &RetireDeps{
		LoadConfig: func(string) (*config.Config, error) {
			return &config.Config{WorktreeTeardown: "echo bye"}, nil
		},
		RunScript: func(string, string, map[string]string) ([]byte, error) {
			ran = true
			return nil, nil
		},
	}

	out := captureStderr(t, func() {
		runTeardownScript(deps, "/tmp/does-not-matter", &state.AgentState{
			Name:     "agent-x",
			Worktree: "/tmp/wt",
		})
	})

	if !ran {
		t.Error("the teardown script must run when the config loads fine")
	}
	if strings.Contains(out, "could not load") {
		t.Errorf("no config-load warning may be emitted on the happy path; got:\n%s", out)
	}
}
