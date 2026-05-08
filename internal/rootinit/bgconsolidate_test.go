package rootinit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
)

// TestStartBackgroundConsolidation_RunsPipelineAndReleasesLock verifies the
// happy path: the flock is acquired, the pipeline runs, then the flock is
// released so a subsequent WaitForBackgroundConsolidation returns promptly.
func TestStartBackgroundConsolidation_RunsPipelineAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	var consolidateCalled atomic.Bool
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		consolidateCalled.Store(true)
		return nil
	}

	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background consolidation did not complete within 5s")
	}
	if !consolidateCalled.Load() {
		t.Error("expected deps.Consolidate to be called by the goroutine")
	}

	// Lockfile should be cleaned up after goroutine finishes.
	lockPath := consolidatingLockPath(root)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("expected lockfile to be removed after consolidation, but stat returned: %v", err)
	}

	// WaitForBackgroundConsolidation should return immediately (no lockfile).
	start := time.Now()
	WaitForBackgroundConsolidation(root, 2*time.Second, io.Discard, "[test]")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("WaitForBackgroundConsolidation blocked for %s after goroutine finished; expected near-zero", elapsed)
	}
}

// TestWaitForBackgroundConsolidation_NoLockfile returns immediately when
// no consolidation has ever run.
func TestWaitForBackgroundConsolidation_NoLockfile(t *testing.T) {
	root := t.TempDir()
	start := time.Now()
	WaitForBackgroundConsolidation(root, time.Second, io.Discard, "[test]")
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("expected immediate return, waited %s", elapsed)
	}
}

// TestStartBackgroundConsolidation_SkipsIfAlreadyLocked verifies that a
// second concurrent start is a no-op — the already-running consolidation
// will pick up the new sessions anyway.
func TestStartBackgroundConsolidation_SkipsIfAlreadyLocked(t *testing.T) {
	root := t.TempDir()

	// Block the first consolidation until we signal it to finish.
	block := make(chan struct{})
	deps := newTestDeps(t)
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-block
		return nil
	}

	done1 := StartBackgroundConsolidation(deps, root, io.Discard, nil)

	// Second call must return a closed channel immediately (flock contention).
	var buf strings.Builder
	start := time.Now()
	done2 := StartBackgroundConsolidation(deps, root, &buf, nil)
	select {
	case <-done2:
	default:
		t.Fatal("second StartBackgroundConsolidation did not return a closed channel")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("second call blocked for %s; expected near-zero", elapsed)
	}
	if !strings.Contains(buf.String(), "already in progress") {
		t.Errorf("expected skip message, got %q", buf.String())
	}

	// Release the first consolidation.
	close(block)
	<-done1
}

// TestFinalizeHandoff_ReturnsQuicklyWhilePipelineRuns verifies the
// headline QUM-282 acceptance: /handoff returns fast even when the
// pipeline is slow.
func TestFinalizeHandoff_ReturnsQuicklyWhilePipelineRuns(t *testing.T) {
	root := t.TempDir()

	deps := newTestDeps(t)
	// Read handoff-signal successfully.
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}
	// Block the pipeline so we know FinalizeHandoff didn't wait for it.
	block := make(chan struct{})
	finished := make(chan struct{})
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-block
		return nil
	}
	// Swap in the real async background consolidation.
	deps.BackgroundConsolidate = func(sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) <-chan struct{} {
		ch := StartBackgroundConsolidation(deps, sprawlRoot, stdout, events)
		go func() {
			<-ch
			close(finished)
		}()
		return ch
	}

	start := time.Now()
	if err := FinalizeHandoff(context.Background(), deps, root, io.Discard, nil); err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Errorf("FinalizeHandoff took %s while pipeline was blocked; expected <1s", elapsed)
	}

	// Release the pipeline so the goroutine can exit cleanly.
	close(block)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("background goroutine did not finish after unblocking")
	}
}

// TestWaitForBackgroundConsolidation_BlocksUntilGoroutineReleasesFlock is
// the serialization guarantee: the next handoff's wait returns only after
// the in-flight pipeline releases its flock.
func TestWaitForBackgroundConsolidation_BlocksUntilGoroutineReleasesFlock(t *testing.T) {
	root := t.TempDir()

	block := make(chan struct{})
	deps := newTestDeps(t)
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-block
		return nil
	}
	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)

	waitReturned := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		WaitForBackgroundConsolidation(root, 5*time.Second, io.Discard, "[test]")
		waitReturned <- time.Since(start)
	}()

	// Give the waiter a chance to block on the flock.
	time.Sleep(200 * time.Millisecond)
	select {
	case elapsed := <-waitReturned:
		t.Fatalf("Wait returned after %s while pipeline still blocked; should have waited", elapsed)
	default:
	}

	// Release the goroutine — Wait should return shortly after.
	close(block)
	select {
	case <-waitReturned:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after goroutine finished")
	}
	<-done
}

// TestStartBackgroundConsolidation_RemovesLockfileOnError verifies the
// lockfile is cleaned up even when the consolidation pipeline fails.
func TestStartBackgroundConsolidation_RemovesLockfileOnError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		return errors.New("simulated pipeline failure")
	}

	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background consolidation did not complete within 5s")
	}

	lockPath := consolidatingLockPath(root)
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("expected lockfile to be removed after failed consolidation, but stat returned: %v", err)
	}
}

// TestWaitForBackgroundConsolidation_TimesOutAndProceeds verifies the
// safety valve: if a prior consolidation hangs, Wait emits a warning and
// returns rather than blocking forever.
func TestWaitForBackgroundConsolidation_TimesOutAndProceeds(t *testing.T) {
	root := t.TempDir()

	block := make(chan struct{})
	defer close(block)

	deps := newTestDeps(t)
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-block
		return nil
	}
	_ = StartBackgroundConsolidation(deps, root, io.Discard, nil)

	var buf strings.Builder
	start := time.Now()
	WaitForBackgroundConsolidation(root, 250*time.Millisecond, &buf, "[test]")
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Errorf("returned too early (%s) — expected to wait at least 250ms", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("returned too late (%s) — expected around 250ms", elapsed)
	}
	if !strings.Contains(buf.String(), "did not finish") {
		t.Errorf("expected warning about timeout, got %q", buf.String())
	}
}

// --- QUM-391: Consolidation event channel tests ---

// TestStartBackgroundConsolidation_EmitsEvents verifies that when an events
// channel is provided, StartBackgroundConsolidation emits lifecycle events:
// a started event, at least one phase event, and a done event.
func TestStartBackgroundConsolidation_EmitsEvents(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		return nil
	}

	events := make(chan ConsolidationEvent, 10)
	done := StartBackgroundConsolidation(deps, root, io.Discard, events)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background consolidation did not complete within 5s")
	}

	// Drain and categorize events.
	close(events)
	var gotPhase, gotDone bool
	for ev := range events {
		if ev.Phase != "" {
			gotPhase = true
		}
		if ev.Done {
			gotDone = true
		}
	}
	if !gotPhase {
		t.Error("expected at least one phase event from StartBackgroundConsolidation")
	}
	if !gotDone {
		t.Error("expected a done event from StartBackgroundConsolidation")
	}
}

// ---------------------------------------------------------------------
// QUM-522: JSON-bodied lockfile + heartbeat + phase-label tests.
// ---------------------------------------------------------------------

// TestStartBackgroundConsolidation_WritesJSONLockBody verifies that while
// the consolidation goroutine is in-flight, the lockfile body is JSON
// containing this process's PID and a non-empty phase label.
//
// Test contract (QUM-522): StartBackgroundConsolidation must return a
// `<-chan struct{}` that closes when the goroutine fully exits, so tests
// can synchronize cleanup and avoid leaking the bg goroutine across tests.
func TestStartBackgroundConsolidation_WritesJSONLockBody(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	block := make(chan struct{})
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-block
		return nil
	}
	deps.UpdatePersistentKnowledge = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		<-block
		return nil
	}

	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)

	// Wait briefly for the goroutine to write the lockfile body.
	path := consolidatingLockPath(root)
	deadline := time.Now().Add(2 * time.Second)
	var got *lockState
	for time.Now().Before(deadline) {
		s, err := readLockState(path)
		if err == nil && s != nil && s.PID != 0 {
			got = s
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == nil {
		close(block)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("lockfile never appeared with a parseable JSON body")
	}
	if got.PID != os.Getpid() {
		t.Errorf("lockfile PID: got %d, want %d", got.PID, os.Getpid())
	}
	if got.Phase == "" {
		t.Error("lockfile phase must be non-empty while consolidation runs")
	}
	if got.StartedAt.IsZero() {
		t.Error("lockfile started_at must be set")
	}

	// Unblock the pipeline and wait for the goroutine to exit so it does
	// not leak past this test.
	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit within 2s after unblocking")
	}
}

// TestStartBackgroundConsolidation_HeartbeatUpdates verifies the heartbeat
// field advances over time while the pipeline runs.
func TestStartBackgroundConsolidation_HeartbeatUpdates(t *testing.T) {
	// Override the heartbeat interval so this test runs in <500ms.
	prev := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	block := make(chan struct{})
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-block
		return nil
	}
	deps.UpdatePersistentKnowledge = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		<-block
		return nil
	}

	// Test contract (QUM-522): StartBackgroundConsolidation returns a
	// <-chan struct{} that closes once the goroutine exits — required
	// so tests can avoid leaking the bg goroutine.
	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)
	path := consolidatingLockPath(root)

	// Wait for first heartbeat.
	var first *lockState
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := readLockState(path)
		if err == nil && s != nil && !s.LastHeartbeat.IsZero() {
			first = s
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if first == nil {
		close(block)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.Fatal("never observed an initial heartbeat in the lockfile")
	}

	// Poll until the heartbeat advances past `first` rather than sleeping
	// for a fixed interval — this avoids races on slow CI hosts and
	// returns as soon as the next tick lands.
	advancedDeadline := time.Now().Add(2 * time.Second)
	var advanced bool
	for time.Now().Before(advancedDeadline) {
		s2, err := readLockState(path)
		if err == nil && s2 != nil && s2.LastHeartbeat.After(first.LastHeartbeat) {
			advanced = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !advanced {
		close(block)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		t.Fatalf("heartbeat did not advance within 2s; last=%v", first.LastHeartbeat)
	}

	// Drain the goroutine before the test returns.
	close(block)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit within 2s after unblocking")
	}
}

// TestStartBackgroundConsolidation_PhaseLabelUpdates verifies the lockfile
// phase field tracks pipeline phase transitions: it should start at
// "starting" (or similar) and be updated to a phase-specific label once
// the timeline / persistent-knowledge phases begin.
func TestStartBackgroundConsolidation_PhaseLabelUpdates(t *testing.T) {
	prev := heartbeatInterval
	heartbeatInterval = 10 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	gate := make(chan struct{})
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		<-gate
		return nil
	}
	deps.UpdatePersistentKnowledge = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
		<-gate
		return nil
	}

	// Test contract (QUM-522): capture the done channel so we can drain
	// the goroutine before the test returns instead of leaking it.
	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)
	path := consolidatingLockPath(root)

	// Collect distinct phase values observed over a short window.
	observed := map[string]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s, err := readLockState(path)
		if err == nil && s != nil && s.Phase != "" {
			observed[s.Phase] = true
		}
		// Stop once we've seen at least one of the active phase labels.
		if observed["Consolidating timeline..."] || observed["Updating persistent knowledge..."] {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if !observed["Consolidating timeline..."] && !observed["Updating persistent knowledge..."] {
		t.Errorf("expected to observe an active-phase label in the lockfile; got: %v", observed)
	}

	// Drain the goroutine before the test returns.
	close(gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit within 2s after unblocking")
	}
}

// TestStartBackgroundConsolidation_NilEventsChannel_NoPanic verifies that
// passing a nil events channel does not cause a panic — existing callers
// that don't care about events should continue to work unchanged.
func TestStartBackgroundConsolidation_NilEventsChannel_NoPanic(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deps := newTestDeps(t)
	deps.ConsolidateExcluding = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
		return nil
	}

	// Pass nil events channel — must not panic.
	done := StartBackgroundConsolidation(deps, root, io.Discard, nil)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background consolidation did not complete within 5s")
	}
}

// TestFinalizeHandoff_ThreadsEventsToBackgroundConsolidate verifies that
// when FinalizeHandoff is called with an events channel, the events are
// threaded through to the BackgroundConsolidate call and the channel
// receives consolidation events.
func TestFinalizeHandoff_ThreadsEventsToBackgroundConsolidate(t *testing.T) {
	root := t.TempDir()

	deps := newTestDeps(t)
	// Read handoff-signal successfully.
	deps.ReadFile = func(path string) ([]byte, error) {
		if strings.Contains(path, "handoff-signal") {
			return []byte("signal"), nil
		}
		return nil, os.ErrNotExist
	}

	var capturedEvents chan<- ConsolidationEvent
	deps.BackgroundConsolidate = func(sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) <-chan struct{} {
		capturedEvents = events
		ch := make(chan struct{})
		close(ch)
		if events != nil {
			events <- ConsolidationEvent{Phase: "test phase"}
			events <- ConsolidationEvent{Done: true, Duration: 2 * time.Second}
		}
		return ch
	}

	events := make(chan ConsolidationEvent, 10)
	if err := FinalizeHandoff(context.Background(), deps, root, io.Discard, events); err != nil {
		t.Fatalf("FinalizeHandoff error: %v", err)
	}

	if capturedEvents == nil {
		t.Fatal("BackgroundConsolidate should receive the events channel from FinalizeHandoff")
	}

	// Verify events were received.
	close(events)
	var gotPhase, gotDone bool
	for ev := range events {
		if ev.Phase == "test phase" {
			gotPhase = true
		}
		if ev.Done {
			gotDone = true
		}
	}
	if !gotPhase {
		t.Error("expected phase event threaded through FinalizeHandoff")
	}
	if !gotDone {
		t.Error("expected done event threaded through FinalizeHandoff")
	}
}
