package rootinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/memory"
)

// newTestDeps returns a Deps struct with benign defaults suitable for
// exercising Prepare / FinalizeHandoff in unit tests.
func newTestDeps(t *testing.T) *Deps {
	t.Helper()
	return &Deps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return "/fake/root"
			}
			return ""
		},
		BuildPrompt:        func(cfg agent.PromptConfig) string { return "system prompt" },
		BuildContextBlob:   func(sprawlRoot, rootName string) (string, error) { return "", nil },
		WriteSystemPrompt:  func(root, name, content string) (string, error) { return "/fake/prompt.md", nil },
		WriteLastSessionID: func(root, id string) error { return nil },
		ReadLastSessionID:  func(string) (string, error) { return "", nil },
		ReadFile:           func(path string) ([]byte, error) { return nil, os.ErrNotExist },
		RemoveFile:         func(path string) error { return nil },
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

func TestPrepare_ReturnsPreparedSession(t *testing.T) {
	deps := newTestDeps(t)
	deps.NewUUID = func() (string, error) { return "sess-123", nil }
	deps.WriteSystemPrompt = func(root, name, content string) (string, error) {
		return "/tmp/SYSTEM.md", nil
	}

	got, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare returned error: %v", err)
	}
	if got.SessionID != "sess-123" {
		t.Errorf("SessionID: got %q, want %q", got.SessionID, "sess-123")
	}
	if got.PromptPath != "/tmp/SYSTEM.md" {
		t.Errorf("PromptPath: got %q, want %q", got.PromptPath, "/tmp/SYSTEM.md")
	}
	if got.Model != DefaultModel {
		t.Errorf("Model: got %q, want %q", got.Model, DefaultModel)
	}
	if len(got.RootTools) == 0 {
		t.Error("expected RootTools to be populated")
	}
	if len(got.Disallowed) == 0 {
		t.Error("expected Disallowed to be populated")
	}
}

func TestPrepare_ModePropagatedToPromptConfig(t *testing.T) {
	deps := newTestDeps(t)
	var capturedCfg agent.PromptConfig
	deps.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "prompt"
	}

	_, err := Prepare(context.Background(), deps, ModeTUI, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if capturedCfg.Mode != "tui" {
		t.Errorf("cfg.Mode: got %q, want %q", capturedCfg.Mode, "tui")
	}
}

func TestPrepare_RootNamePropagatedToPromptConfig(t *testing.T) {
	deps := newTestDeps(t)
	var capturedCfg agent.PromptConfig
	deps.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "prompt"
	}
	_, _ = Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if capturedCfg.RootName != "weave" {
		t.Errorf("cfg.RootName: got %q, want %q", capturedCfg.RootName, "weave")
	}
}

func TestPrepare_TestMode_PropagatedWhenEnvSet(t *testing.T) {
	deps := newTestDeps(t)
	deps.Getenv = func(key string) string {
		if key == "SPRAWL_TEST_MODE" {
			return "1"
		}
		return ""
	}
	var capturedCfg agent.PromptConfig
	deps.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "prompt"
	}
	_, _ = Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if !capturedCfg.TestMode {
		t.Error("expected TestMode=true when SPRAWL_TEST_MODE=1")
	}
}

func TestPrepare_TestMode_NotSetWhenEnvUnset(t *testing.T) {
	deps := newTestDeps(t)
	var capturedCfg agent.PromptConfig
	deps.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "prompt"
	}
	_, _ = Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if capturedCfg.TestMode {
		t.Error("expected TestMode=false when SPRAWL_TEST_MODE is unset")
	}
}

func TestPrepare_ContextBlobPassedToPromptConfig(t *testing.T) {
	deps := newTestDeps(t)
	deps.BuildContextBlob = func(root, name string) (string, error) {
		return "## Active State\n\ntest blob\n", nil
	}
	var capturedCfg agent.PromptConfig
	deps.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "prompt"
	}
	_, _ = Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if capturedCfg.ContextBlob != "## Active State\n\ntest blob\n" {
		t.Errorf("ContextBlob: got %q", capturedCfg.ContextBlob)
	}
}

func TestPrepare_ContextBlobError_LogsAndContinues(t *testing.T) {
	deps := newTestDeps(t)
	deps.BuildContextBlob = func(root, name string) (string, error) {
		return "partial blob", fmt.Errorf("context blob errors: session read failed")
	}
	var capturedCfg agent.PromptConfig
	deps.BuildPrompt = func(cfg agent.PromptConfig) string {
		capturedCfg = cfg
		return "prompt"
	}
	var buf strings.Builder

	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", &buf)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if capturedCfg.ContextBlob != "partial blob" {
		t.Errorf("expected partial blob to be passed, got %q", capturedCfg.ContextBlob)
	}
	if !strings.Contains(buf.String(), "context blob") {
		t.Errorf("expected warning in stdout, got %q", buf.String())
	}
}

func TestPrepare_UUIDFailure_ReturnsError(t *testing.T) {
	deps := newTestDeps(t)
	deps.NewUUID = func() (string, error) { return "", errors.New("uuid failure") }

	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err == nil {
		t.Fatal("expected error when UUID generation fails")
	}
	if !strings.Contains(err.Error(), "session ID") {
		t.Errorf("expected error to mention session ID, got %v", err)
	}
}

func TestPrepare_WriteSystemPromptFailure_ReturnsError(t *testing.T) {
	deps := newTestDeps(t)
	deps.WriteSystemPrompt = func(root, name, content string) (string, error) {
		return "", errors.New("write failed")
	}
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err == nil {
		t.Fatal("expected error when WriteSystemPrompt fails")
	}
}

func TestPrepare_SessionIDWritten(t *testing.T) {
	deps := newTestDeps(t)
	deps.NewUUID = func() (string, error) { return "sess-xyz", nil }
	var idWritten string
	deps.WriteLastSessionID = func(root, id string) error {
		idWritten = id
		return nil
	}
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if idWritten != "sess-xyz" {
		t.Errorf("expected session ID 'sess-xyz' written, got %q", idWritten)
	}
}

func TestPrepare_MissedHandoff_AutoSummarizes(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "prev-session-id", nil }

	var autoCalled bool
	var capturedID, capturedCWD string
	deps.AutoSummarize = func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
		autoCalled = true
		capturedID = id
		capturedCWD = cwd
		return true, nil
	}

	var buf strings.Builder
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", &buf)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if !autoCalled {
		t.Fatal("expected AutoSummarize to be called")
	}
	if capturedID != "prev-session-id" {
		t.Errorf("autoSummarize sessionID: got %q", capturedID)
	}
	if capturedCWD != "/fake/root" {
		t.Errorf("autoSummarize cwd: got %q", capturedCWD)
	}
	if !strings.Contains(buf.String(), "Detected missed handoff") {
		t.Errorf("expected missed-handoff log, got %q", buf.String())
	}
}

func TestPrepare_MissedHandoff_AlreadySummarized_SkipsAutoSummarize(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "prev-session-id", nil }
	deps.HasSessionSummary = func(root, id string) (bool, error) { return true, nil }

	var autoCalled bool
	deps.AutoSummarize = func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
		autoCalled = true
		return false, nil
	}
	var buf strings.Builder
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", &buf)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if autoCalled {
		t.Error("should not call AutoSummarize when summary already exists")
	}
	if strings.Contains(buf.String(), "Detected missed handoff") {
		t.Error("should not print 'Detected missed handoff' when summary already exists")
	}
}

func TestPrepare_AlreadySummarized_RunsConsolidationBeforeClearingSessionID(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "prev-session-id", nil }
	deps.HasSessionSummary = func(root, id string) (bool, error) { return true, nil }

	var callOrder []string
	deps.WriteLastSessionID = func(root, id string) error {
		if id == "" {
			callOrder = append(callOrder, "clearSessionID")
		}
		return nil
	}
	deps.Consolidate = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		callOrder = append(callOrder, "consolidate")
		return nil
	}
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		callOrder = append(callOrder, "updatePK")
		return nil
	}

	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}

	consolidateIdx, clearIdx := -1, -1
	for i, name := range callOrder {
		if name == "consolidate" && consolidateIdx == -1 {
			consolidateIdx = i
		}
		if name == "clearSessionID" && clearIdx == -1 {
			clearIdx = i
		}
	}
	if consolidateIdx == -1 {
		t.Fatal("consolidate not called")
	}
	if clearIdx == -1 {
		t.Fatal("clearSessionID not called")
	}
	if consolidateIdx >= clearIdx {
		t.Errorf("expected consolidate before clearSessionID (got %d vs %d)", consolidateIdx, clearIdx)
	}
}

func TestPrepare_MissedHandoff_AutoSummarize_RunsConsolidation(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "prev-session-id", nil }
	deps.AutoSummarize = func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
		return true, nil
	}
	var consolidateCalled bool
	deps.Consolidate = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		return nil
	}
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if !consolidateCalled {
		t.Error("expected Consolidate to be called after auto-summarize")
	}
}

func TestPrepare_MissedHandoff_AutoSummarize_NoOp_SkipsConsolidation(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "prev-session-id", nil }
	deps.AutoSummarize = func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
		return false, nil
	}
	var consolidateCalled bool
	deps.Consolidate = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		return nil
	}
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if consolidateCalled {
		t.Error("expected Consolidate NOT to be called when auto-summarize is a no-op")
	}
}

func TestPrepare_MissedHandoff_AutoSummarizeError_Continues(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "prev-session-id", nil }
	deps.AutoSummarize = func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
		return false, errors.New("summarize failed")
	}
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("should not return error on AutoSummarize failure, got %v", err)
	}
}

func TestPrepare_FirstSession_NoLastID(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadLastSessionID = func(string) (string, error) { return "", nil }

	var autoCalled bool
	deps.AutoSummarize = func(ctx context.Context, root, cwd, home, id string, inv memory.ClaudeInvoker) (bool, error) {
		autoCalled = true
		return false, nil
	}
	_, err := Prepare(context.Background(), deps, ModeTmux, "/fake/root", "weave", io.Discard)
	if err != nil {
		t.Fatalf("Prepare error: %v", err)
	}
	if autoCalled {
		t.Error("expected AutoSummarize NOT to be called on first session")
	}
}
