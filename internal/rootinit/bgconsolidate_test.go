package rootinit

import (
	"context"
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
	deps.Consolidate = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		consolidateCalled.Store(true)
		return nil
	}

	done := StartBackgroundConsolidation(deps, root, io.Discard)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background consolidation did not complete within 5s")
	}
	if !consolidateCalled.Load() {
		t.Error("expected deps.Consolidate to be called by the goroutine")
	}

	// Lockfile should still exist (we don't delete it), but flock should be
	// released — WaitForBackgroundConsolidation must return quickly.
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
	deps.Consolidate = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		<-block
		return nil
	}

	done1 := StartBackgroundConsolidation(deps, root, io.Discard)

	// Second call must return a closed channel immediately (flock contention).
	var buf strings.Builder
	start := time.Now()
	done2 := StartBackgroundConsolidation(deps, root, &buf)
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
	deps.Consolidate = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		<-block
		return nil
	}
	// Swap in the real async background consolidation.
	deps.BackgroundConsolidate = func(sprawlRoot string, stdout io.Writer) <-chan struct{} {
		ch := StartBackgroundConsolidation(deps, sprawlRoot, stdout)
		go func() {
			<-ch
			close(finished)
		}()
		return ch
	}

	start := time.Now()
	if err := FinalizeHandoff(context.Background(), deps, root, io.Discard); err != nil {
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
	deps.Consolidate = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		<-block
		return nil
	}
	done := StartBackgroundConsolidation(deps, root, io.Discard)

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

// TestWaitForBackgroundConsolidation_TimesOutAndProceeds verifies the
// safety valve: if a prior consolidation hangs, Wait emits a warning and
// returns rather than blocking forever.
func TestWaitForBackgroundConsolidation_TimesOutAndProceeds(t *testing.T) {
	root := t.TempDir()

	block := make(chan struct{})
	defer close(block)

	deps := newTestDeps(t)
	deps.Consolidate = func(ctx context.Context, r string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time) error {
		<-block
		return nil
	}
	_ = StartBackgroundConsolidation(deps, root, io.Discard)

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
