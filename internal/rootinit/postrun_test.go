package rootinit

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
)

func TestFinalizeHandoff_NoSignal_LogsSessionEnded(t *testing.T) {
	deps := newTestDeps(t)
	var buf strings.Builder

	err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf)
	if err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	if !strings.Contains(buf.String(), "session ended") {
		t.Errorf("expected 'session ended', got %q", buf.String())
	}
}

func TestFinalizeHandoff_Signal_CallsConsolidateAndClears(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}

	var callOrder []string
	deps.RemoveFile = func(path string) error {
		if strings.Contains(path, "handoff-signal") {
			callOrder = append(callOrder, "removeHandoff")
		}
		return nil
	}
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

	err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard)
	if err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}

	// Consolidation must happen before cleanup for crash safety.
	idx := map[string]int{"consolidate": -1, "removeHandoff": -1, "clearSessionID": -1, "updatePK": -1}
	for i, name := range callOrder {
		if _, ok := idx[name]; ok && idx[name] == -1 {
			idx[name] = i
		}
	}
	for k, v := range idx {
		if v == -1 {
			t.Fatalf("%s not called; callOrder=%v", k, callOrder)
		}
	}
	if idx["consolidate"] > idx["removeHandoff"] {
		t.Error("expected consolidate before removeHandoff")
	}
	if idx["consolidate"] > idx["clearSessionID"] {
		t.Error("expected consolidate before clearSessionID")
	}
	if idx["consolidate"] > idx["updatePK"] {
		t.Error("expected consolidate before updatePK")
	}
}

func TestFinalizeHandoff_Signal_ConsolidateError_DoesNotFail(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.Consolidate = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		return fmt.Errorf("consolidation failed")
	}
	var buf strings.Builder
	err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !strings.Contains(buf.String(), "consolidat") {
		t.Errorf("expected warning in output, got %q", buf.String())
	}
}

func TestFinalizeHandoff_Signal_UpdatePKError_DoesNotFail(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		return fmt.Errorf("knowledge update failed")
	}
	var buf strings.Builder
	err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !strings.Contains(buf.String(), "knowledge") {
		t.Errorf("expected knowledge warning, got %q", buf.String())
	}
}

func TestFinalizeHandoff_Signal_PassesSummaryAndTimelineToPK(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.ListRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return []memory.Session{{SessionID: "s1", Timestamp: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)}}, []string{"test summary"}, nil
	}
	deps.ReadTimeline = func(root string) ([]memory.TimelineEntry, error) {
		return []memory.TimelineEntry{
			{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Initial setup"},
			{Timestamp: time.Date(2026, 4, 2, 8, 0, 0, 0, time.UTC), Summary: "Added tests"},
		}, nil
	}

	var capturedSummary, capturedBullets string
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		capturedSummary = summary
		capturedBullets = bullets
		return nil
	}

	err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard)
	if err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	if capturedSummary != "test summary" {
		t.Errorf("summary: got %q", capturedSummary)
	}
	if !strings.Contains(capturedBullets, "Initial setup") || !strings.Contains(capturedBullets, "Added tests") {
		t.Errorf("timeline bullets missing entries: %q", capturedBullets)
	}
}

func TestFinalizeHandoff_NoSignal_DoesNotConsolidate(t *testing.T) {
	deps := newTestDeps(t)
	var consolidateCalled bool
	deps.Consolidate = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled = true
		return nil
	}
	err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard)
	if err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	if consolidateCalled {
		t.Error("expected Consolidate NOT to be called without handoff signal")
	}
}
