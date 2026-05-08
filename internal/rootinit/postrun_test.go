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

	err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf, nil)
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
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		callOrder = append(callOrder, "consolidate")
		return nil
	}
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		callOrder = append(callOrder, "updatePK")
		return nil
	}

	err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard, nil)
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
	// QUM-283: consolidate and updatePK now run in parallel under an
	// errgroup, so their relative order is not specified.
}

func TestFinalizeHandoff_Signal_ConsolidateError_DoesNotFail(t *testing.T) {
	deps := newTestDeps(t)
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		return fmt.Errorf("consolidation failed")
	}
	var buf strings.Builder
	err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf, nil)
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
	err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf, nil)
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
		// QUM-521: newest is held back; PK ingests second-newest body.
		return []memory.Session{
			{SessionID: "s0", Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)},
			{SessionID: "s1", Timestamp: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC)},
		}, []string{"test summary", "newest body"}, nil
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

	err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard, nil)
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

func TestFinalizeHandoff_LogPrefix_TUIDoesNotPrintRootLoop(t *testing.T) {
	deps := newTestDeps(t)
	deps.LogPrefix = "[enter]"
	// signal present so we hit the consolidation path too
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}

	var buf strings.Builder
	if err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf, nil); err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "[root-loop]") {
		t.Errorf("TUI-mode output must not contain [root-loop] prefix; got: %q", got)
	}
	if !strings.Contains(got, "[enter]") {
		t.Errorf("expected [enter] prefix in TUI-mode output; got: %q", got)
	}
}

func TestFinalizeHandoff_LogPrefix_NoSignalPathRespectsPrefix(t *testing.T) {
	deps := newTestDeps(t)
	deps.LogPrefix = "[enter]"
	var buf strings.Builder
	if err := FinalizeHandoff(context.Background(), deps, "/fake/root", &buf, nil); err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "[root-loop]") {
		t.Errorf("expected no [root-loop] in TUI-mode no-signal output; got: %q", got)
	}
	if !strings.Contains(got, "[enter] session ended") {
		t.Errorf("expected '[enter] session ended' prefix; got: %q", got)
	}
}

// ---------------------------------------------------------------------
// QUM-521: runConsolidationPipeline holds back the most recent sealed
// session and prevents it from leaking into PK input.
// ---------------------------------------------------------------------

// fireConsolidationViaHandoff wires up a deps that triggers the
// consolidation pipeline through FinalizeHandoff (the synchronous test path
// runs runConsolidationPipeline inline via syncBackgroundConsolidate).
func fireConsolidationViaHandoff(t *testing.T, deps *Deps) {
	t.Helper()
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	if err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard, nil); err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
}

func TestRunConsolidationPipeline_ExcludesNewestSealedSession(t *testing.T) {
	deps := newTestDeps(t)

	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	deps.ListRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return []memory.Session{
			{SessionID: "s1", Timestamp: t1},
			{SessionID: "s2", Timestamp: t2},
			{SessionID: "s3", Timestamp: t3},
		}, []string{"b1", "b2", "b3"}, nil
	}

	var capturedExclude map[string]bool
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		capturedExclude = excludeIDs
		return nil
	}
	// Re-bind background consolidate so it picks up the updated deps.
	deps.BackgroundConsolidate = syncBackgroundConsolidate(deps)

	fireConsolidationViaHandoff(t, deps)

	if capturedExclude == nil {
		t.Fatal("expected ConsolidateExcluding to be called with non-nil excludeIDs")
	}
	if !capturedExclude["s3"] {
		t.Errorf("expected excludeIDs[s3]=true, got: %#v", capturedExclude)
	}
	for _, banned := range []string{"s1", "s2"} {
		if capturedExclude[banned] {
			t.Errorf("excludeIDs[%s] must be false; only newest sealed should be held back. got: %#v",
				banned, capturedExclude)
		}
	}
}

func TestRunConsolidationPipeline_PKSummaryUsesSecondNewest(t *testing.T) {
	deps := newTestDeps(t)

	// Use 3 sessions so bodies[0] (oldest) and bodies[len-2] (second-newest)
	// are distinct values, ensuring the test discriminates a buggy impl
	// that picks the oldest from a "drop-newest" slice.
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	deps.ListRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return []memory.Session{
			{SessionID: "s1", Timestamp: t1},
			{SessionID: "s2", Timestamp: t2},
			{SessionID: "s3", Timestamp: t3},
		}, []string{"b1", "b2", "b3"}, nil
	}
	// Live session id is empty so all three are sealed; the newest sealed
	// (s3/b3) is held back from PK input, leaving s2/b2 as the second-newest
	// summary the pipeline should pass to UpdatePersistentKnowledge.

	var capturedSummary string
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		capturedSummary = summary
		return nil
	}
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		return nil
	}
	deps.BackgroundConsolidate = syncBackgroundConsolidate(deps)

	fireConsolidationViaHandoff(t, deps)

	if capturedSummary != "b2" {
		t.Errorf("expected PK sessionSummary to be the second-newest body 'b2'; got %q", capturedSummary)
	}
}

func TestRunConsolidationPipeline_PKSummaryEmptyWhenOnlySealedIsHeldBack(t *testing.T) {
	deps := newTestDeps(t)

	ts := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	deps.ListRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return []memory.Session{{SessionID: "only-id", Timestamp: ts}}, []string{"only"}, nil
	}

	var capturedSummary string
	var capturedExclude map[string]bool
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		capturedSummary = summary
		return nil
	}
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		capturedExclude = excludeIDs
		return nil
	}
	deps.BackgroundConsolidate = syncBackgroundConsolidate(deps)

	fireConsolidationViaHandoff(t, deps)

	if capturedSummary != "" {
		t.Errorf("expected empty PK sessionSummary when only sealed session is held back; got %q", capturedSummary)
	}
	if !capturedExclude["only-id"] {
		t.Errorf("expected excludeIDs[only-id]=true; got %#v", capturedExclude)
	}
}

func TestRunConsolidationPipeline_NoSessions_NoCrash(t *testing.T) {
	deps := newTestDeps(t)

	deps.ListRecentSessions = func(root string, n int) ([]memory.Session, []string, error) {
		return nil, nil, nil
	}

	var capturedExclude map[string]bool
	calledExcluding := false
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		calledExcluding = true
		capturedExclude = excludeIDs
		return nil
	}
	deps.BackgroundConsolidate = syncBackgroundConsolidate(deps)

	// Must not panic.
	fireConsolidationViaHandoff(t, deps)

	if !calledExcluding {
		t.Error("expected ConsolidateExcluding to be called even with no sessions")
	}
	if len(capturedExclude) != 0 {
		t.Errorf("expected empty excludeIDs when no sessions exist; got %#v", capturedExclude)
	}
}

// ---------------------------------------------------------------------
// QUM-522: per-phase timeout. runConsolidationPipeline must not let a
// hung LLM call block forever; each phase runs under context.WithTimeout.
// ---------------------------------------------------------------------

func TestRunConsolidationPipeline_TimeoutAbortsPhase(t *testing.T) {
	// Override the per-phase timeout so the test completes quickly.
	prev := perPhaseTimeout
	perPhaseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { perPhaseTimeout = prev })

	deps := newTestDeps(t)

	// Timeline phase blocks until ctx is cancelled — simulates a hung
	// LLM call. It MUST observe ctx.Done() before the test deadline.
	timelineDone := make(chan struct{})
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		defer close(timelineDone)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}
	// PK phase returns promptly so we can verify it isn't blocked by the
	// timeline phase's timeout.
	pkDone := make(chan struct{})
	deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		defer close(pkDone)
		return nil
	}

	var buf strings.Builder
	start := time.Now()
	doneCh := make(chan struct{})
	go func() {
		runConsolidationPipeline(context.Background(), deps, "/fake/root", &buf, nil)
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runConsolidationPipeline did not return within 2s — per-phase timeout did not fire")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("pipeline took %s; expected ~50ms+slack", elapsed)
	}

	select {
	case <-timelineDone:
	case <-time.After(time.Second):
		t.Fatal("timeline phase never observed ctx cancellation")
	}
	select {
	case <-pkDone:
	case <-time.After(time.Second):
		t.Fatal("PK phase did not run / complete")
	}

	// Output should mention the timeout / deadline-exceeded so the user
	// sees an explanation rather than silent failure.
	out := strings.ToLower(buf.String())
	if !strings.Contains(out, "timeout") && !strings.Contains(out, "deadline") {
		t.Errorf("expected timeout/deadline warning in output; got %q", buf.String())
	}
}

func TestFinalizeHandoff_NoSignal_DoesNotConsolidate(t *testing.T) {
	deps := newTestDeps(t)
	var consolidateCalled bool
	deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		consolidateCalled = true
		return nil
	}
	err := FinalizeHandoff(context.Background(), deps, "/fake/root", io.Discard, nil)
	if err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	if consolidateCalled {
		t.Error("expected Consolidate NOT to be called without handoff signal")
	}
}
