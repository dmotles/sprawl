package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// slowInvoker blocks until ctx is done.
type slowInvoker struct{}

func (slowInvoker) Invoke(ctx context.Context, prompt string, opts ...InvokeOption) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestConsolidate_TimeoutBoundsInvoke verifies QUM-286: a hanging invoker
// does not block Consolidate indefinitely.
func TestConsolidate_TimeoutBoundsInvoke(t *testing.T) {
	root := t.TempDir()
	// Create 5 sessions so we have candidates (>3) AND there is no
	// existing timeline, so candidates are not filtered away by the
	// QUM-285 overlap logic.
	createTestSessions(t, root, 5)

	cfg := DefaultTimelineCompressionConfig()
	cfg.InvokeTimeout = 100 * time.Millisecond

	start := time.Now()
	err := Consolidate(context.Background(), root, slowInvoker{}, &cfg, consolidateTestNow())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from bounded invoker, got nil")
	}
	if elapsed > time.Second {
		t.Errorf("Consolidate waited %s; expected ~100ms", elapsed)
	}
}

// TestUpdatePersistentKnowledge_TimeoutBoundsInvoke: same for PK.
func TestUpdatePersistentKnowledge_TimeoutBoundsInvoke(t *testing.T) {
	root := t.TempDir()

	cfg := DefaultPersistentKnowledgeConfig()
	cfg.InvokeTimeout = 100 * time.Millisecond

	start := time.Now()
	err := UpdatePersistentKnowledge(context.Background(), root, slowInvoker{}, &cfg, "session body", "- entry")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error from bounded invoker, got nil")
	}
	if elapsed > time.Second {
		t.Errorf("UpdatePersistentKnowledge waited %s; expected ~100ms", elapsed)
	}
}

// modelCapturingInvoker records options passed to Invoke so tests can
// assert the model was threaded through.
type modelCapturingInvoker struct {
	lastModel string
	lastOpts  int
	response  string
}

func (m *modelCapturingInvoker) Invoke(ctx context.Context, prompt string, opts ...InvokeOption) (string, error) {
	m.lastOpts = len(opts)
	var cfg invokeConfig
	for _, o := range opts {
		o(&cfg)
	}
	m.lastModel = cfg.model
	if m.response == "" {
		return "", errors.New("no response configured")
	}
	return m.response, nil
}

// TestConsolidate_PassesModelFromConfig verifies QUM-284: the Model from
// config reaches the invoker as a WithModel option.
func TestConsolidate_PassesModelFromConfig(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	cfg := DefaultTimelineCompressionConfig()
	cfg.Model = "my-custom-model"

	inv := &modelCapturingInvoker{
		response: "- 2026-01-01T00:00:00Z: some summary",
	}
	if err := Consolidate(context.Background(), root, inv, &cfg, consolidateTestNow()); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if inv.lastModel != "my-custom-model" {
		t.Errorf("model = %q, want %q", inv.lastModel, "my-custom-model")
	}
}

func TestUpdatePersistentKnowledge_PassesModelFromConfig(t *testing.T) {
	root := t.TempDir()
	cfg := DefaultPersistentKnowledgeConfig()
	cfg.Model = "pk-model-42"

	inv := &modelCapturingInvoker{response: "- a knowledge item"}
	if err := UpdatePersistentKnowledge(context.Background(), root, inv, &cfg, "summary", "- tl"); err != nil {
		t.Fatalf("UpdatePersistentKnowledge: %v", err)
	}
	if inv.lastModel != "pk-model-42" {
		t.Errorf("model = %q, want %q", inv.lastModel, "pk-model-42")
	}
}

// TestConsolidate_PromptBoundedOnLargeCorpus verifies QUM-285: a large
// session corpus produces a prompt capped at MaxPromptChars.
func TestConsolidate_PromptBoundedOnLargeCorpus(t *testing.T) {
	root := t.TempDir()
	// 100 sessions, each with a large body — would produce a multi-megabyte
	// prompt without the budget.
	bodyChunk := strings.Repeat("x", 2000)
	for i := 0; i < 100; i++ {
		s := Session{
			SessionID:    fmt.Sprintf("big-%d", i),
			Timestamp:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour),
			AgentsActive: []string{"agent-a"},
		}
		if err := WriteSessionSummary(root, s, bodyChunk); err != nil {
			t.Fatalf("WriteSessionSummary[%d]: %v", i, err)
		}
	}

	cfg := DefaultTimelineCompressionConfig()
	cfg.MaxPromptChars = 20000 // small budget for the test

	var capturedPrompt string
	mock := &mockClaudeInvoker{response: "- 2026-01-02T00:00:00Z: something"}
	inv := claudeInvokerFunc(func(ctx context.Context, prompt string, opts ...InvokeOption) (string, error) {
		capturedPrompt = prompt
		return mock.Invoke(ctx, prompt, opts...)
	})
	if err := Consolidate(context.Background(), root, inv, &cfg, consolidateTestNow()); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if len(capturedPrompt) > cfg.MaxPromptChars {
		t.Errorf("prompt length %d exceeds MaxPromptChars=%d", len(capturedPrompt), cfg.MaxPromptChars)
	}
	if len(capturedPrompt) == 0 {
		t.Error("expected non-empty prompt")
	}
}

// TestFilterCandidatesByTimeline_DropsAlreadyRepresented verifies the
// QUM-285 filter: sessions older than the latest timeline entry are
// dropped except for OverlapSessions of back-context.
func TestFilterCandidatesByTimeline_DropsAlreadyRepresented(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessions := make([]Session, 10)
	bodies := make([]string, 10)
	for i := range sessions {
		sessions[i] = Session{SessionID: fmt.Sprintf("s-%d", i), Timestamp: base.Add(time.Duration(i) * time.Hour)}
		bodies[i] = fmt.Sprintf("body-%d", i)
	}
	// Timeline latest = sessions[4] timestamp.
	tl := []TimelineEntry{{Timestamp: sessions[4].Timestamp, Summary: "covered"}}

	// With overlap=2, we expect sessions starting from index (5-2)=3.
	filteredSessions, filteredBodies := filterCandidatesByTimeline(tl, sessions, bodies, 2)
	if len(filteredSessions) != 7 {
		t.Errorf("got %d sessions, want 7", len(filteredSessions))
	}
	if filteredSessions[0].SessionID != "s-3" {
		t.Errorf("first session = %q, want s-3", filteredSessions[0].SessionID)
	}
	if len(filteredBodies) != len(filteredSessions) {
		t.Errorf("bodies len mismatch: %d vs %d", len(filteredBodies), len(filteredSessions))
	}
}

// TestFilterCandidatesByTimeline_EmptyTimelineReturnsAll: no timeline
// means no filtering.
func TestFilterCandidatesByTimeline_EmptyTimelineReturnsAll(t *testing.T) {
	sessions := []Session{
		{SessionID: "a", Timestamp: time.Now()},
		{SessionID: "b", Timestamp: time.Now().Add(time.Hour)},
	}
	bodies := []string{"a-body", "b-body"}
	filtered, fb := filterCandidatesByTimeline(nil, sessions, bodies, 2)
	if len(filtered) != 2 || len(fb) != 2 {
		t.Errorf("expected all sessions returned, got %d/%d", len(filtered), len(fb))
	}
}

// claudeInvokerFunc adapts a plain function to the ClaudeInvoker interface.
type claudeInvokerFunc func(ctx context.Context, prompt string, opts ...InvokeOption) (string, error)

func (f claudeInvokerFunc) Invoke(ctx context.Context, prompt string, opts ...InvokeOption) (string, error) {
	return f(ctx, prompt, opts...)
}
