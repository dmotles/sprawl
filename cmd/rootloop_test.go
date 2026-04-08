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

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/memory"
)

// syncBuffer is a thread-safe buffer for capturing spinner output in tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestRootLoopDeps creates a rootLoopDeps with sensible defaults for testing.
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
		findClaude:         func() (string, error) { return "/usr/bin/claude", nil },
		buildPrompt:        func(cfg agent.PromptConfig) string { return "system prompt" },
		buildContextBlob:   func(sprawlRoot, rootName string) (string, error) { return "", nil },
		writeSystemPrompt:  func(root, name, content string) (string, error) { return "/fake/prompt.md", nil },
		writeLastSessionID: func(root, id string) error { return nil },
		readFile:           func(path string) ([]byte, error) { return nil, os.ErrNotExist },
		removeFile:         func(path string) error { return nil },
		newUUID:            func() (string, error) { return "test-uuid", nil },
		runCommand:         func(name string, args []string) error { return nil },
		stdout:             io.Discard,
		readLastSessionID:  func(string) (string, error) { return "", nil },
		autoSummarize: func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
			return false, nil
		},
		userHomeDir:   func() (string, error) { return "/home/test", nil },
		newCLIInvoker: func() memory.ClaudeInvoker { return nil },
		consolidate: func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
			return nil
		},
		updatePersistentKnowledge: func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
			return nil
		},
		listRecentSessions: func(root string, n int) ([]memory.Session, []string, error) {
			return nil, nil, nil
		},
		readTimeline: func(root string) ([]memory.TimelineEntry, error) {
			return nil, nil
		},
		hasSessionSummary: func(sprawlRoot, sessionID string) (bool, error) {
			return false, nil
		},
	}
}

func TestRunRootSession_NormalExit_ReturnsNil(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	// runCommand returns nil (normal exit)
	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error for normal exit, got: %v", err)
	}
}

func TestRunRootSession_ClaudeExitError_ReturnsNil(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	// runCommand returns an *exec.ExitError (Claude ran but exited non-zero)
	deps.runCommand = func(name string, args []string) error {
		return &exec.ExitError{}
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error for ExitError (Claude ran but exited non-zero), got: %v", err)
	}
}

func TestRunRootSession_StartupFailure_ReturnsError(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	// runCommand returns a non-ExitError (startup failure)
	deps.runCommand = func(name string, args []string) error {
		return errors.New("command not found")
	}

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error for startup failure, got nil")
	}
	if !strings.Contains(err.Error(), "command not found") {
		t.Errorf("expected error to contain 'command not found', got: %v", err)
	}
}

func TestRunRootSession_HandoffSignal(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var handoffDeleted bool

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}

	deps.removeFile = func(path string) error {
		if strings.Contains(path, "handoff-signal") {
			handoffDeleted = true
		}
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !handoffDeleted {
		t.Error("expected handoff signal to be deleted")
	}
}

func TestRunRootSession_HandoffSignal_ClearsLastSessionID(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var lastSessionIDCleared bool

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.removeFile = func(path string) error { return nil }

	deps.writeLastSessionID = func(root, id string) error {
		if id == "" {
			lastSessionIDCleared = true
		}
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !lastSessionIDCleared {
		t.Error("expected last-session-id to be cleared after handoff signal processing")
	}
}

func TestRunRootSession_SessionEndWithoutHandoff(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var buf strings.Builder
	deps.stdout = &buf

	// No handoff signal (readFile returns ErrNotExist by default)
	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "session ended") {
		t.Errorf("expected output to contain 'session ended', got: %q", output)
	}
}

func TestRunRootSession_MissingSprawlRoot(t *testing.T) {
	deps := newTestRootLoopDeps(t)
	deps.getenv = func(key string) string { return "" }

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
	deps.findClaude = func() (string, error) {
		return "", errors.New("claude not found")
	}

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error when claude not found, got nil")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("expected error to mention claude, got: %v", err)
	}
}

func TestRunRootSession_ContextBlobPassedToPrompt(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "system prompt"
	}
	deps.buildContextBlob = func(sprawlRoot, rootName string) (string, error) {
		return "## Active State\n\ntest blob\n", nil
	}

	_ = runRootSession(context.Background(), deps)

	if capturedCfg.ContextBlob != "## Active State\n\ntest blob\n" {
		t.Errorf("expected context blob to be passed to buildPrompt, got: %q", capturedCfg.ContextBlob)
	}
}

func TestRunRootSession_ContextBlobError_LogsAndContinues(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var buf strings.Builder
	deps.stdout = &buf

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "system prompt"
	}
	deps.buildContextBlob = func(sprawlRoot, rootName string) (string, error) {
		return "partial blob", fmt.Errorf("context blob errors: session read failed")
	}

	err := runRootSession(context.Background(), deps)
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

func TestRunRootSession_TestMode_PropagatedToPromptConfig(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	// Override getenv to return "1" for SPRAWL_TEST_MODE
	originalGetenv := deps.getenv
	deps.getenv = func(key string) string {
		if key == "SPRAWL_TEST_MODE" {
			return "1"
		}
		return originalGetenv(key)
	}

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "system prompt"
	}

	_ = runRootSession(context.Background(), deps)

	if !capturedCfg.TestMode {
		t.Error("expected PromptConfig.TestMode to be true when SPRAWL_TEST_MODE=1")
	}
}

func TestRunRootSession_TestMode_NotSetWhenUnset(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var capturedCfg agent.PromptConfig
	deps.buildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "system prompt"
	}

	_ = runRootSession(context.Background(), deps)

	if capturedCfg.TestMode {
		t.Error("expected PromptConfig.TestMode to be false when SPRAWL_TEST_MODE is not set")
	}
}

func TestRunRootSession_MissedHandoff_AutoSummarizes(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var autoSummarizeCalled bool
	var capturedSessionID string
	var capturedCWD string
	var buf strings.Builder
	deps.stdout = &buf

	// readLastSessionID returns a previous session ID
	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "prev-session-id", nil
	}

	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		autoSummarizeCalled = true
		capturedSessionID = sessionID
		capturedCWD = cwd
		return true, nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !autoSummarizeCalled {
		t.Error("expected autoSummarize to be called")
	}
	if capturedSessionID != "prev-session-id" {
		t.Errorf("expected autoSummarize called with sessionID 'prev-session-id', got %q", capturedSessionID)
	}
	if capturedCWD != "/fake/root" {
		t.Errorf("expected autoSummarize called with cwd equal to sprawlRoot '/fake/root', got %q", capturedCWD)
	}

	output := buf.String()
	if !strings.Contains(output, "Detected missed handoff from previous session") {
		t.Errorf("expected stdout to contain missed handoff feedback message, got: %q", output)
	}
}

func TestRunRootSession_MissedHandoff_AlreadySummarized(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var claudeRan bool
	var autoSummarizeCalled bool
	var buf strings.Builder
	deps.stdout = &buf

	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "prev-session-id", nil
	}

	// Summary already exists for this session
	deps.hasSessionSummary = func(sprawlRoot, sessionID string) (bool, error) {
		return true, nil
	}

	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		autoSummarizeCalled = true
		return false, nil
	}

	deps.runCommand = func(name string, args []string) error {
		claudeRan = true
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !claudeRan {
		t.Error("expected claude to still run after already-summarized session")
	}

	output := buf.String()
	if strings.Contains(output, "Detected missed handoff") {
		t.Error("should NOT print 'Detected missed handoff' when session summary already exists")
	}

	if autoSummarizeCalled {
		t.Error("should NOT call autoSummarize when session summary already exists")
	}
}

func TestRunRootSession_MissedHandoff_Error_Continues(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var claudeRan bool

	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "prev-session-id", nil
	}

	// autoSummarize returns an error
	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		return false, fmt.Errorf("summarization failed")
	}

	deps.runCommand = func(name string, args []string) error {
		claudeRan = true
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error (should not abort on autoSummarize error), got: %v", err)
	}

	if !claudeRan {
		t.Error("expected claude to still run despite autoSummarize error")
	}
}

func TestRunRootSession_FirstSession_NoLastID(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var autoSummarizeCalled bool

	// readLastSessionID returns empty string (first session)
	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "", nil
	}

	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		autoSummarizeCalled = true
		return false, nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if autoSummarizeCalled {
		t.Error("expected autoSummarize NOT to be called when readLastSessionID returns empty string")
	}
}

func TestRunRootSession_HandoffSignal_CallsConsolidate(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var consolidateCalled bool
	var consolidateSprawlRoot string

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.removeFile = func(path string) error { return nil }

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		consolidateSprawlRoot = sprawlRoot
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !consolidateCalled {
		t.Error("expected consolidate to be called after handoff signal")
	}
	if consolidateSprawlRoot != "/fake/root" {
		t.Errorf("expected consolidate called with sprawlRoot '/fake/root', got %q", consolidateSprawlRoot)
	}
}

func TestRunRootSession_NoHandoff_DoesNotCallConsolidate(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var consolidateCalled bool

	// No handoff signal (readFile returns ErrNotExist by default)

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if consolidateCalled {
		t.Error("expected consolidate NOT to be called when there is no handoff signal")
	}
}

func TestRunRootSession_Consolidate_Error_DoesNotFail(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var buf strings.Builder
	deps.stdout = &buf

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.removeFile = func(path string) error { return nil }

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		return fmt.Errorf("consolidation failed")
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error (consolidation error should be warning only), got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "consolidat") {
		t.Errorf("expected warning about consolidation in output, got: %q", output)
	}
}

func TestRunRootSession_HandoffSignal_CallsUpdatePersistentKnowledge(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var updatePKCalled bool
	var capturedSummary string
	var capturedBullets string

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.removeFile = func(path string) error { return nil }

	deps.listRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return []memory.Session{
			{SessionID: "sess-1", Timestamp: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)},
		}, []string{"test summary"}, nil
	}

	deps.readTimeline = func(root string) ([]memory.TimelineEntry, error) {
		return []memory.TimelineEntry{
			{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Initial setup"},
			{Timestamp: time.Date(2026, 4, 2, 8, 0, 0, 0, time.UTC), Summary: "Added tests"},
		}, nil
	}

	deps.updatePersistentKnowledge = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
		updatePKCalled = true
		capturedSummary = sessionSummary
		capturedBullets = timelineBullets
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !updatePKCalled {
		t.Error("expected updatePersistentKnowledge to be called after handoff signal")
	}
	if capturedSummary != "test summary" {
		t.Errorf("expected sessionSummary %q, got %q", "test summary", capturedSummary)
	}
	// Timeline bullets should contain both entries formatted as bullets
	if !strings.Contains(capturedBullets, "Initial setup") {
		t.Error("expected timelineBullets to contain 'Initial setup'")
	}
	if !strings.Contains(capturedBullets, "Added tests") {
		t.Error("expected timelineBullets to contain 'Added tests'")
	}
}

func TestRunRootSession_NoHandoff_DoesNotCallUpdatePersistentKnowledge(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var updatePKCalled bool

	// No handoff signal (readFile returns ErrNotExist by default)

	deps.updatePersistentKnowledge = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
		updatePKCalled = true
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if updatePKCalled {
		t.Error("expected updatePersistentKnowledge NOT to be called when there is no handoff signal")
	}
}

func TestRunRootSession_UpdatePersistentKnowledge_Error_DoesNotFail(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var buf strings.Builder
	deps.stdout = &buf

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.removeFile = func(path string) error { return nil }

	deps.updatePersistentKnowledge = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
		return fmt.Errorf("knowledge update failed")
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error (PK update error should be warning only), got: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "knowledge") || !strings.Contains(output, "failed") {
		t.Errorf("expected warning about knowledge update failure in output, got: %q", output)
	}
}

func TestRunRootSession_HandoffSignal_UpdatePersistentKnowledge_AfterConsolidate(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var callOrder []string

	// Handoff signal file exists
	deps.readFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.removeFile = func(path string) error { return nil }

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		callOrder = append(callOrder, "consolidate")
		return nil
	}

	deps.updatePersistentKnowledge = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
		callOrder = append(callOrder, "updatePersistentKnowledge")
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if len(callOrder) < 2 {
		t.Fatalf("expected at least 2 calls (consolidate + updatePersistentKnowledge), got %d: %v", len(callOrder), callOrder)
	}

	// Find first occurrence of each
	consolidateIdx := -1
	updatePKIdx := -1
	for i, name := range callOrder {
		if name == "consolidate" && consolidateIdx == -1 {
			consolidateIdx = i
		}
		if name == "updatePersistentKnowledge" && updatePKIdx == -1 {
			updatePKIdx = i
		}
	}

	if consolidateIdx == -1 {
		t.Fatal("expected consolidate to be called")
	}
	if updatePKIdx == -1 {
		t.Fatal("expected updatePersistentKnowledge to be called")
	}
	if consolidateIdx >= updatePKIdx {
		t.Errorf("expected consolidate (idx=%d) to be called before updatePersistentKnowledge (idx=%d)", consolidateIdx, updatePKIdx)
	}
}

func TestRunRootSession_AutoSummarize_RunsConsolidation(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var consolidateCalled bool
	var updatePKCalled bool
	var capturedSummary string
	var capturedBullets string

	// Previous session exists (missed handoff scenario)
	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "prev-session-id", nil
	}

	// autoSummarize returns true (it actually summarized)
	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		return true, nil
	}

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		return nil
	}

	deps.listRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return []memory.Session{
			{SessionID: "prev-session-id", Timestamp: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)},
		}, []string{"auto-summarized content"}, nil
	}

	deps.readTimeline = func(root string) ([]memory.TimelineEntry, error) {
		return []memory.TimelineEntry{
			{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Timeline entry"},
		}, nil
	}

	deps.updatePersistentKnowledge = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
		updatePKCalled = true
		capturedSummary = sessionSummary
		capturedBullets = timelineBullets
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if !consolidateCalled {
		t.Error("expected consolidate to be called after auto-summarize")
	}
	if !updatePKCalled {
		t.Error("expected updatePersistentKnowledge to be called after auto-summarize")
	}
	if capturedSummary != "auto-summarized content" {
		t.Errorf("expected sessionSummary %q, got %q", "auto-summarized content", capturedSummary)
	}
	if !strings.Contains(capturedBullets, "Timeline entry") {
		t.Error("expected timelineBullets to contain 'Timeline entry'")
	}
}

func TestRunRootSession_AutoSummarize_NoOp_SkipsConsolidation(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var consolidateCalled bool

	// Previous session exists
	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "prev-session-id", nil
	}

	// autoSummarize returns false (already summarized, no-op)
	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		return false, nil
	}

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if consolidateCalled {
		t.Error("expected consolidate NOT to be called when auto-summarize is a no-op")
	}
}

func TestRunRootSession_AutoSummarize_ConsolidationError_Continues(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var claudeRan bool

	var buf strings.Builder
	deps.stdout = &buf

	// Previous session exists (missed handoff)
	deps.readLastSessionID = func(sprawlRoot string) (string, error) {
		return "prev-session-id", nil
	}

	deps.autoSummarize = func(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker memory.ClaudeInvoker) (bool, error) {
		return true, nil
	}

	deps.consolidate = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		return fmt.Errorf("consolidation failed")
	}

	deps.updatePersistentKnowledge = func(ctx context.Context, sprawlRoot string, invoker memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, sessionSummary string, timelineBullets string) error {
		return fmt.Errorf("knowledge update failed")
	}

	deps.runCommand = func(name string, args []string) error {
		claudeRan = true
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error (should continue despite consolidation errors), got: %v", err)
	}

	if !claudeRan {
		t.Error("expected claude to still run despite consolidation errors after auto-summarize")
	}

	output := buf.String()
	if !strings.Contains(output, "consolidat") {
		t.Errorf("expected warning about consolidation in output, got: %q", output)
	}
}

func TestRunRootSession_UUIDFailure_ReturnsError(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	deps.newUUID = func() (string, error) {
		return "", errors.New("uuid generation failed")
	}

	err := runRootSession(context.Background(), deps)
	if err == nil {
		t.Fatal("expected error when UUID generation fails, got nil")
	}
	if !strings.Contains(err.Error(), "session ID") {
		t.Errorf("expected error to mention session ID, got: %v", err)
	}
}

func TestRunRootSession_SessionIDWritten(t *testing.T) {
	deps := newTestRootLoopDeps(t)

	var sessionIDWritten string

	deps.newUUID = func() (string, error) {
		return "test-session-123", nil
	}

	deps.writeLastSessionID = func(root, id string) error {
		sessionIDWritten = id
		return nil
	}

	deps.runCommand = func(name string, args []string) error {
		return nil
	}

	err := runRootSession(context.Background(), deps)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	if sessionIDWritten != "test-session-123" {
		t.Errorf("expected session ID 'test-session-123' to be written, got %q", sessionIDWritten)
	}
}

func TestSpinner_StartsAndStops(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "testing...")
	// Give enough time for at least 1 frame at 150ms tick rate.
	time.Sleep(500 * time.Millisecond)
	sp.stop() // stop() blocks until goroutine exits and clears line

	output := buf.String()
	if !strings.Contains(output, "testing...") {
		t.Errorf("expected output to contain label 'testing...', got: %q", output)
	}
}

func TestSpinner_DisplaysElapsedTime(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "working...")
	time.Sleep(500 * time.Millisecond)
	sp.stop()

	output := buf.String()
	// Accept either (0s) or (1s) depending on scheduling.
	if !strings.Contains(output, "(0s)") && !strings.Contains(output, "(1s)") {
		t.Errorf("expected output to contain elapsed time like '(0s)' or '(1s)', got: %q", output)
	}
}

func TestSpinner_StopClearsLine(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "clearing...")
	time.Sleep(500 * time.Millisecond)
	// stop() uses WaitGroup to ensure goroutine has finished writing the
	// clear-line escape before returning. No race with buf.String() below.
	sp.stop()

	output := buf.String()
	if !strings.HasSuffix(output, "\033[2K\r") {
		t.Errorf("expected output to end with clear-line escape, got suffix: %q", output[max(0, len(output)-20):])
	}
}

func TestSpinner_CyclesThroughFrames(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "cycling...")
	time.Sleep(2 * time.Second) // enough for 10+ frames at 150ms tick
	sp.stop()

	output := buf.String()
	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	distinctFrames := 0
	for _, f := range frames {
		if strings.ContainsRune(output, f) {
			distinctFrames++
		}
	}
	if distinctFrames < 2 {
		t.Errorf("expected at least 2 distinct spinner frames, found %d in output: %q", distinctFrames, output)
	}
}

func TestSpinner_IncludesRootLoopPrefix(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "prefixed...")
	time.Sleep(500 * time.Millisecond)
	sp.stop()

	output := buf.String()
	if !strings.Contains(output, "[root-loop]") {
		t.Errorf("expected output to contain '[root-loop]' prefix, got: %q", output)
	}
}

func TestSpinner_ImmediateStop(t *testing.T) {
	// Verify stop works even if called before any frame renders.
	var buf syncBuffer
	sp := startSpinner(&buf, "quick...")
	sp.stop()

	output := buf.String()
	// Should at minimum have the clear-line escape from stop.
	if !strings.HasSuffix(output, "\033[2K\r") {
		t.Errorf("expected output to end with clear-line escape after immediate stop, got: %q", output)
	}
}
