package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/rootinit"
)

// newStubRootinitDeps returns a rootinit.Deps with every callback stubbed to
// a benign no-op. Tests for runRootSession mutate individual fields as needed.
func newStubRootinitDeps() *rootinit.Deps {
	return &rootinit.Deps{
		Getenv:             func(string) string { return "" },
		BuildPrompt:        func(agent.PromptConfig) string { return "prompt" },
		BuildContextBlob:   func(root, name string) (string, error) { return "", nil },
		WriteSystemPrompt:  func(root, name, content string) (string, error) { return "/fake/prompt.md", nil },
		WriteLastSessionID: func(root, id string) error { return nil },
		ReadLastSessionID:  func(string) (string, error) { return "", nil },
		ReadFile:           func(path string) ([]byte, error) { return nil, os.ErrNotExist },
		RemoveFile:         func(string) error { return nil },
		NewUUID:            func() (string, error) { return "test-uuid", nil },
		UserHomeDir:        func() (string, error) { return "/home/test", nil },
		NewCLIInvoker:      func() memory.ClaudeInvoker { return nil },
		HasSessionSummary:  func(root, id string) (bool, error) { return false, nil },
		AutoSummarize: func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
			return false, nil
		},
		Consolidate: func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
			return nil
		},
		UpdatePersistentKnowledge: func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
			return nil
		},
		ListRecentSessions: func(root string, n int) ([]memory.Session, []string, error) { return nil, nil, nil },
		ReadTimeline:       func(root string) ([]memory.TimelineEntry, error) { return nil, nil },
	}
}

// newTestRootLoopDeps builds a rootLoopDeps wired for a unit test: stubbed
// environment, claude binary present, no-op runCommand, discarded stdout, and
// a stubbed rootinit.Deps.
func newTestRootLoopDeps(t *testing.T) *rootLoopDeps {
	t.Helper()
	return &rootLoopDeps{
		getenv: func(key string) string {
			switch key {
			case "SPRAWL_ROOT":
				return "/fake/root"
			case "SPRAWL_NAMESPACE":
				return "🌳"
			default:
				return ""
			}
		},
		findClaude: func() (string, error) { return "/usr/bin/claude", nil },
		runCommand: func(name string, args []string) error { return nil },
		stdout:     io.Discard,
		rootinit:   newStubRootinitDeps(),
	}
}

func TestRunRootSession_MissingSprawlRoot(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.getenv = func(string) string { return "" }

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error for missing SPRAWL_ROOT, got nil")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("expected error to mention SPRAWL_ROOT, got: %v", err)
	}
}

func TestRunRootSession_FindClaudeFailure(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.findClaude = func() (string, error) { return "", errors.New("claude not found") }

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error when claude not found, got nil")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("expected error to mention claude, got: %v", err)
	}
}

func TestRunRootSession_NormalExit_ReturnsNil(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.runCommand = func(name string, args []string) error { return nil }

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestRunRootSession_ClaudeExitError_ReturnsNil(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.runCommand = func(name string, args []string) error { return &exec.ExitError{} }

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("expected nil on ExitError, got %v", err)
	}
}

func TestRunRootSession_StartupFailure_ReturnsError(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.runCommand = func(name string, args []string) error { return errors.New("command not found") }

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error on startup failure, got nil")
	}
	if !strings.Contains(err.Error(), "command not found") {
		t.Errorf("expected wrapped 'command not found', got %v", err)
	}
}

func TestRunRootSession_PreparePropagatesError(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.rootinit.NewUUID = func() (string, error) { return "", errors.New("uuid boom") }

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected rootinit.Prepare error to propagate")
	}
	if !strings.Contains(err.Error(), "session ID") {
		t.Errorf("expected error to mention session ID, got %v", err)
	}
}

func TestRunRootSession_LaunchOptsUseTmuxMode(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	// Capture the mode passed to BuildPrompt to verify tmux-mode propagation.
	var capturedMode string
	deps.rootinit.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedMode = cfg.Mode
		return "prompt"
	}

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedMode != string(rootinit.ModeTmux) {
		t.Errorf("expected Mode=%q, got %q", rootinit.ModeTmux, capturedMode)
	}
}

func TestRunRootSession_PassesSessionIDAndPromptPathToClaude(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.rootinit.NewUUID = func() (string, error) { return "sess-abc", nil }
	deps.rootinit.WriteSystemPrompt = func(root, name, content string) (string, error) {
		return "/abs/SYSTEM.md", nil
	}

	var capturedArgs []string
	deps.runCommand = func(name string, args []string) error {
		capturedArgs = args
		return nil
	}

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !argsContainPair(capturedArgs, "--session-id", "sess-abc") {
		t.Errorf("expected --session-id sess-abc; got %v", capturedArgs)
	}
	if !argsContainPair(capturedArgs, "--system-prompt-file", "/abs/SYSTEM.md") {
		t.Errorf("expected --system-prompt-file /abs/SYSTEM.md; got %v", capturedArgs)
	}
}
