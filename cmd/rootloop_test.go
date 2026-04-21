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
	"github.com/dmotles/sprawl/internal/claude"
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
		now:        time.Now,
		stdout:     io.Discard,
		rootinit:   newStubRootinitDeps(),
		acquireLock: func(string) (weaveLockReleaser, error) {
			return stubLock{}, nil
		},
	}
}

// stubLock is a no-op weaveLockReleaser used in unit tests.
type stubLock struct{}

func (stubLock) Release() error { return nil }

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

func TestRunRootSession_ResumePath_UsesResumeFlag(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	// Set up resume scenario: prior session ID present, no summary → resume.
	deps.rootinit.ReadLastSessionID = func(string) (string, error) { return "prev-sess", nil }
	deps.rootinit.HasSessionSummary = func(root, id string) (bool, error) { return false, nil }

	var capturedArgs []string
	deps.runCommand = func(name string, args []string) error {
		capturedArgs = args
		return nil
	}

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !argsContainPair(capturedArgs, "--resume", "prev-sess") {
		t.Errorf("expected --resume prev-sess; got %v", capturedArgs)
	}
	for i, a := range capturedArgs {
		if a == "--system-prompt-file" {
			t.Errorf("resume must omit --system-prompt-file (found at %d); got %v", i, capturedArgs)
		}
		if a == "--session-id" {
			t.Errorf("resume must omit --session-id (found at %d); got %v", i, capturedArgs)
		}
	}
}

func TestRunRootSession_ResumeFailure_RetriesFresh(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.rootinit.ReadLastSessionID = func(string) (string, error) { return "dead-sess", nil }
	deps.rootinit.HasSessionSummary = func(root, id string) (bool, error) { return false, nil }
	deps.rootinit.NewUUID = func() (string, error) { return "fresh-sess", nil }
	deps.rootinit.WriteSystemPrompt = func(root, name, content string) (string, error) {
		return "/tmp/SYSTEM.md", nil
	}

	// Force the clock to return an instant-exit elapsed < resumeFailureWindow.
	fakeNow := time.Unix(0, 0)
	deps.now = func() time.Time {
		t := fakeNow
		fakeNow = fakeNow.Add(100 * time.Millisecond)
		return t
	}

	var logBuf strings.Builder
	deps.stdout = &logBuf

	calls := 0
	var secondArgs []string
	deps.runCommand = func(name string, args []string) error {
		calls++
		if calls == 1 {
			// First invocation (resume) fails fast.
			return &exec.ExitError{}
		}
		// Second invocation is the fresh retry.
		secondArgs = args
		return nil
	}

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected 2 claude invocations (resume + retry), got %d", calls)
	}
	if !argsContainPair(secondArgs, "--session-id", "fresh-sess") {
		t.Errorf("retry should use fresh session ID; got args %v", secondArgs)
	}
	if !argsContainPair(secondArgs, "--system-prompt-file", "/tmp/SYSTEM.md") {
		t.Errorf("retry should use --system-prompt-file; got args %v", secondArgs)
	}
	for _, a := range secondArgs {
		if a == "--resume" {
			t.Errorf("retry must not use --resume; got %v", secondArgs)
		}
	}
	if !strings.Contains(logBuf.String(), "resume failed for dead-sess") {
		t.Errorf("expected resume-failure log message; got %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "falling back to fresh session") {
		t.Errorf("expected fallback log message; got %q", logBuf.String())
	}
}

// TestRunRootSession_ResumeFailure_MarkerError_RetriesRegardlessOfWindow
// covers QUM-261: the subprocess stayed alive long past resumeFailureWindow
// but the "No conversation found" marker scanner still tripped. The fallback
// must fire whenever runCommand returns claude.ErrResumeFailed, regardless of
// how long ago claude was launched.
func TestRunRootSession_ResumeFailure_MarkerError_RetriesRegardlessOfWindow(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.rootinit.ReadLastSessionID = func(string) (string, error) { return "dead-sess", nil }
	deps.rootinit.HasSessionSummary = func(root, id string) (bool, error) { return false, nil }
	deps.rootinit.NewUUID = func() (string, error) { return "fresh-sess", nil }
	deps.rootinit.WriteSystemPrompt = func(root, name, content string) (string, error) {
		return "/tmp/SYSTEM.md", nil
	}

	// Clock advances well past the elapsed-time heuristic. The marker-based
	// fallback must still fire because runCommand returned ErrResumeFailed.
	fakeNow := time.Unix(0, 0)
	deps.now = func() time.Time {
		t := fakeNow
		fakeNow = fakeNow.Add(resumeFailureWindow + 30*time.Second)
		return t
	}

	var logBuf strings.Builder
	deps.stdout = &logBuf

	calls := 0
	var secondArgs []string
	deps.runCommand = func(name string, args []string) error {
		calls++
		if calls == 1 {
			return claude.ErrResumeFailed
		}
		secondArgs = args
		return nil
	}

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if calls != 2 {
		t.Fatalf("expected 2 invocations (resume + fresh retry) on marker match; got %d", calls)
	}
	if !argsContainPair(secondArgs, "--session-id", "fresh-sess") {
		t.Errorf("retry should use fresh session ID; got %v", secondArgs)
	}
	for _, a := range secondArgs {
		if a == "--resume" {
			t.Errorf("retry must not use --resume; got %v", secondArgs)
		}
	}
	if !strings.Contains(logBuf.String(), "resume failed for dead-sess") {
		t.Errorf("expected resume-failure log; got %q", logBuf.String())
	}
}

func TestRunRootSession_ResumeFailure_OutsideWindow_DoesNotRetry(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.rootinit.ReadLastSessionID = func(string) (string, error) { return "live-sess", nil }
	deps.rootinit.HasSessionSummary = func(root, id string) (bool, error) { return false, nil }

	// Clock advances past the detection window on the first call, so a
	// non-zero exit is treated as a normal mid-session exit (bash loop case).
	fakeNow := time.Unix(0, 0)
	deps.now = func() time.Time {
		t := fakeNow
		fakeNow = fakeNow.Add(resumeFailureWindow + time.Second)
		return t
	}

	calls := 0
	deps.runCommand = func(name string, args []string) error {
		calls++
		return &exec.ExitError{}
	}

	if err := runRootSession(context.Background(), deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 invocation (no retry beyond window); got %d", calls)
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
