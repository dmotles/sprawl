package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/agent"
	"github.com/dmotles/dendra/internal/memory"
)

// newTestSenseiLoopDeps creates a senseiLoopDeps with sensible defaults for testing.
func newTestSenseiLoopDeps(t *testing.T) *senseiLoopDeps {
	t.Helper()
	return &senseiLoopDeps{
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return "/fake/root"
			case "DENDRA_NAMESPACE":
				return "🌳"
			default:
				return ""
			}
		},
		findClaude:         func() (string, error) { return "/usr/bin/claude", nil },
		buildPrompt:        func(cfg agent.PromptConfig) string { return "system prompt" },
		buildContextBlob:   func(dendraRoot, rootName string) (string, error) { return "", nil },
		writeSystemPrompt:  func(root, name, content string) (string, error) { return "/fake/prompt.md", nil },
		writeLastSessionID: func(root, id string) error { return nil },
		readFile:            func(path string) ([]byte, error) { return nil, os.ErrNotExist },
		removeFile:          func(path string) error { return nil },
		newUUID:             func() (string, error) { return "test-uuid", nil },
		sleepFunc:           func(d time.Duration) {},
		runCommand:          func(name string, args []string) error { return nil },
		stdout:              io.Discard,
		readLastSessionID:   func(string) (string, error) { return "", nil },
		autoSummarize: func(ctx context.Context, dendraRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
			return false, nil
		},
		userHomeDir:   func() (string, error) { return "/home/test", nil },
		newCLIInvoker: func() memory.ClaudeInvoker { return nil },
	}
}

func TestRunSenseiLoop_SingleIteration_HandoffSignal(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var buildPromptCalls int
	var sessionIDsWritten []string
	var claudeLaunched int
	var handoffDeleted bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		mu.Lock()
		buildPromptCalls++
		mu.Unlock()
		return "system prompt"
	}

	uuidCounter := 0
	deps.newUUID = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		uuidCounter++
		return fmt.Sprintf("uuid-%d", uuidCounter), nil
	}

	deps.writeLastSessionID = func(root, id string) error {
		mu.Lock()
		sessionIDsWritten = append(sessionIDsWritten, id)
		mu.Unlock()
		return nil
	}

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}

	deps.removeFile = func(path string) error {
		if strings.Contains(path, "handoff-signal") {
			mu.Lock()
			handoffDeleted = true
			mu.Unlock()
		}
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		claudeLaunched++
		iteration := claudeLaunched
		mu.Unlock()
		// Cancel after 2nd iteration starts
		if iteration >= 2 {
			cancel()
		}
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if buildPromptCalls < 2 {
		t.Errorf("expected buildPrompt called at least 2 times, got %d", buildPromptCalls)
	}
	if len(sessionIDsWritten) < 2 {
		t.Errorf("expected at least 2 session IDs written, got %d", len(sessionIDsWritten))
	}
	if claudeLaunched < 2 {
		t.Errorf("expected claude launched at least 2 times, got %d", claudeLaunched)
	}
	if !handoffDeleted {
		t.Error("expected handoff signal to be deleted")
	}
}

func TestRunSenseiLoop_SessionEndWithoutHandoff(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var iterations int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No handoff signal (readFile returns ErrNotExist by default)

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		iterations++
		iter := iterations
		mu.Unlock()
		if iter >= 2 {
			cancel()
		}
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if iterations < 2 {
		t.Errorf("expected loop to restart without handoff signal, got %d iterations", iterations)
	}
}

func TestRunSenseiLoop_RetryOnStartupFailure(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var sleepCalls []time.Duration
	var runCalls int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.sleepFunc = func(d time.Duration) {
		mu.Lock()
		sleepCalls = append(sleepCalls, d)
		mu.Unlock()
	}

	// First call: non-ExitError (startup failure). Second call: success, then cancel.
	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		runCalls++
		call := runCalls
		mu.Unlock()
		if call == 1 {
			return errors.New("command not found")
		}
		cancel()
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(sleepCalls) < 1 {
		t.Fatal("expected at least one sleep call for retry")
	}
	if sleepCalls[0] != 5*time.Second {
		t.Errorf("expected sleep of 5s, got %v", sleepCalls[0])
	}
	if runCalls < 2 {
		t.Errorf("expected retry attempt, got %d runCommand calls", runCalls)
	}
}

func TestRunSenseiLoop_MaxRetriesExceeded(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	// All calls return a non-ExitError (startup failure)
	deps.runCommand = func(name string, args []string) error {
		return errors.New("command not found")
	}

	ctx := context.Background()
	err := runSenseiLoop(ctx, deps)
	if err == nil {
		t.Fatal("expected error after max retries, got nil")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("expected error to mention 3 retries, got: %v", err)
	}
}

func TestRunSenseiLoop_SignalStopsLoop(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately before running
	cancel()

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error on context cancellation, got: %v", err)
	}
}

func TestRunSenseiLoop_MissingDendraRoot(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)
	deps.getenv = func(key string) string { return "" }

	err := runSenseiLoop(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT, got nil")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("expected error to mention DENDRA_ROOT, got: %v", err)
	}
}

func TestRunSenseiLoop_SuccessResetsRetryCount(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var runCalls int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Call 1: startup failure (non-ExitError)
	// Call 2: success (ExitError or nil — normal exit)
	// Call 3: startup failure again
	// Call 4: success, then cancel
	// If retry count wasn't reset, call 3 would be "attempt 2" and we'd be closer to max.
	// We verify no max-retry error is returned.
	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		runCalls++
		call := runCalls
		mu.Unlock()
		switch call {
		case 1:
			return errors.New("command not found") // startup failure
		case 2:
			return nil // success resets counter
		case 3:
			return errors.New("command not found") // startup failure again
		case 4:
			cancel()
			return nil // success
		default:
			cancel()
			return nil
		}
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error (retry count should have reset), got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if runCalls < 4 {
		t.Errorf("expected at least 4 runCommand calls, got %d", runCalls)
	}
}

func TestRunSenseiLoop_SessionIDWrittenEachIteration(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var sessionIDs []string
	var iterations int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	uuidCounter := 0
	deps.newUUID = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		uuidCounter++
		return fmt.Sprintf("session-%d", uuidCounter), nil
	}

	deps.writeLastSessionID = func(root, id string) error {
		mu.Lock()
		sessionIDs = append(sessionIDs, id)
		mu.Unlock()
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		iterations++
		iter := iterations
		mu.Unlock()
		if iter >= 2 {
			cancel()
		}
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(sessionIDs) < 2 {
		t.Fatalf("expected at least 2 session IDs written, got %d", len(sessionIDs))
	}
	if sessionIDs[0] == sessionIDs[1] {
		t.Errorf("expected different UUIDs each iteration, got %q and %q", sessionIDs[0], sessionIDs[1])
	}
}

func TestRunSenseiLoop_PromptRebuiltEachIteration(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var promptCalls int
	var iterations int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		mu.Lock()
		promptCalls++
		mu.Unlock()
		return "system prompt"
	}

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		iterations++
		iter := iterations
		mu.Unlock()
		if iter >= 2 {
			cancel()
		}
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if promptCalls < 2 {
		t.Errorf("expected buildPrompt called at least 2 times, got %d", promptCalls)
	}
}

func TestRunSenseiLoop_ExitErrorNotCountedAsStartupFailure(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var runCalls int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// All calls return ExitError (simulating Claude running then exiting non-zero).
	// This should NOT trigger retry/max-retry logic — it's a normal exit.
	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		runCalls++
		call := runCalls
		mu.Unlock()
		if call >= 4 {
			cancel()
			return nil
		}
		// Return an exec.ExitError-like error (wrapping is enough for errors.As)
		return &exec.ExitError{}
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error (ExitError should not trigger max retries), got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Should have run 4 times without hitting max retries (which is 3)
	if runCalls < 4 {
		t.Errorf("expected at least 4 iterations (ExitError = normal exit), got %d", runCalls)
	}
}

func TestRunSenseiLoop_MaxRetriesExceeded_WithTimeout(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	deps.runCommand = func(name string, args []string) error {
		return errors.New("command not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := runSenseiLoop(ctx, deps)
	if err == nil {
		t.Fatal("expected error after max retries, got nil")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("expected error to mention 3 retries, got: %v", err)
	}
}

func TestRunSenseiLoop_FindClaudeFailure(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)
	deps.findClaude = func() (string, error) {
		return "", errors.New("claude not found")
	}

	err := runSenseiLoop(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error when claude not found, got nil")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("expected error to mention claude, got: %v", err)
	}
}

func TestRunSenseiLoop_ContextBlobPassedToPrompt(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		cancel() // stop after first iteration
		return "system prompt"
	}
	deps.buildContextBlob = func(dendraRoot, rootName string) (string, error) {
		return "## Active State\n\ntest blob\n", nil
	}

	_ = runSenseiLoop(ctx, deps)

	if capturedCfg.ContextBlob != "## Active State\n\ntest blob\n" {
		t.Errorf("expected context blob to be passed to buildPrompt, got: %q", capturedCfg.ContextBlob)
	}
}

func TestRunSenseiLoop_ContextBlobError_LogsAndContinues(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf strings.Builder
	deps.stdout = &buf

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		cancel() // stop after first iteration
		return "system prompt"
	}
	deps.buildContextBlob = func(dendraRoot, rootName string) (string, error) {
		return "partial blob", fmt.Errorf("context blob errors: session read failed")
	}

	err := runSenseiLoop(ctx, deps)
	// Should not fail due to context blob error
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Partial blob should still be passed
	if capturedCfg.ContextBlob != "partial blob" {
		t.Errorf("expected partial blob to be passed, got: %q", capturedCfg.ContextBlob)
	}

	// Warning should be logged
	output := buf.String()
	if !strings.Contains(output, "context blob") {
		t.Errorf("expected warning about context blob in output, got: %q", output)
	}
}

func TestRunSenseiLoop_TestMode_PropagatedToPromptConfig(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Override getenv to return "1" for DENDRA_TEST_MODE
	originalGetenv := deps.getenv
	deps.getenv = func(key string) string {
		if key == "DENDRA_TEST_MODE" {
			return "1"
		}
		return originalGetenv(key)
	}

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		cancel() // stop after first iteration
		return "system prompt"
	}

	_ = runSenseiLoop(ctx, deps)

	if !capturedCfg.TestMode {
		t.Error("expected PromptConfig.TestMode to be true when DENDRA_TEST_MODE=1")
	}
}

func TestRunSenseiLoop_TestMode_NotSetWhenUnset(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		cancel() // stop after first iteration
		return "system prompt"
	}

	_ = runSenseiLoop(ctx, deps)

	if capturedCfg.TestMode {
		t.Error("expected PromptConfig.TestMode to be false when DENDRA_TEST_MODE is not set")
	}
}

func TestRunSenseiLoop_MissedHandoff_AutoSummarizes(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var autoSummarizeCalled bool
	var capturedSessionID string
	var capturedCWD string
	var claudeRan bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// readLastSessionID returns a previous session ID
	deps.readLastSessionID = func(dendraRoot string) (string, error) {
		return "prev-session-id", nil
	}

	deps.autoSummarize = func(ctx context.Context, dendraRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		mu.Lock()
		autoSummarizeCalled = true
		capturedSessionID = sessionID
		capturedCWD = cwd
		mu.Unlock()
		return true, nil
	}

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		claudeRan = true
		mu.Unlock()
		cancel()
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !autoSummarizeCalled {
		t.Error("expected autoSummarize to be called")
	}
	if capturedSessionID != "prev-session-id" {
		t.Errorf("expected autoSummarize called with sessionID 'prev-session-id', got %q", capturedSessionID)
	}
	if capturedCWD != "/fake/root" {
		t.Errorf("expected autoSummarize called with cwd equal to dendraRoot '/fake/root', got %q", capturedCWD)
	}
	if !claudeRan {
		t.Error("expected claude to still run after autoSummarize")
	}
}

func TestRunSenseiLoop_MissedHandoff_AlreadySummarized(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var claudeRan bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.readLastSessionID = func(dendraRoot string) (string, error) {
		return "prev-session-id", nil
	}

	// autoSummarize returns false (already summarized)
	deps.autoSummarize = func(ctx context.Context, dendraRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		return false, nil
	}

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		claudeRan = true
		mu.Unlock()
		cancel()
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !claudeRan {
		t.Error("expected loop to continue normally and run claude")
	}
}

func TestRunSenseiLoop_MissedHandoff_Error_ContinuesLoop(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var claudeRan bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps.readLastSessionID = func(dendraRoot string) (string, error) {
		return "prev-session-id", nil
	}

	// autoSummarize returns an error
	deps.autoSummarize = func(ctx context.Context, dendraRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		return false, fmt.Errorf("summarization failed")
	}

	deps.runCommand = func(name string, args []string) error {
		mu.Lock()
		claudeRan = true
		mu.Unlock()
		cancel()
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error (loop should not abort on autoSummarize error), got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !claudeRan {
		t.Error("expected claude to still run despite autoSummarize error")
	}
}

func TestRunSenseiLoop_FirstSession_NoLastID(t *testing.T) {
	deps := newTestSenseiLoopDeps(t)

	var mu sync.Mutex
	var autoSummarizeCalled bool

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// readLastSessionID returns empty string (first session)
	deps.readLastSessionID = func(dendraRoot string) (string, error) {
		return "", nil
	}

	deps.autoSummarize = func(ctx context.Context, dendraRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		mu.Lock()
		autoSummarizeCalled = true
		mu.Unlock()
		return false, nil
	}

	deps.runCommand = func(name string, args []string) error {
		cancel()
		return nil
	}

	err := runSenseiLoop(ctx, deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if autoSummarizeCalled {
		t.Error("expected autoSummarize NOT to be called when readLastSessionID returns empty string")
	}
}
