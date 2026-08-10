// QUM-1186 lane 3: the idle-reaper ticker.
//
// Structurally a twin of blurbticker_test.go, and for the same reason: the
// Started()/Stopped() pair is set INSIDE the loop goroutine, so a test can
// prove the goroutine RAN rather than that a struct was constructed. A ticker
// that is built and never started is the failure this file exists to catch —
// it is silent, and its only symptom is a fleet that slowly eats memory.
package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reclaimRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *reclaimRecorder) fn() func(ctx context.Context, rt *AgentRuntime) {
	return func(_ context.Context, rt *AgentRuntime) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.names = append(r.names, rt.Name())
	}
}

func (r *reclaimRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.names)
}

type fakeIdleLister struct{ rts []*AgentRuntime }

func (f *fakeIdleLister) List() []*AgentRuntime { return f.rts }

func waitForReclaims(t *testing.T, rec *reclaimRecorder, target int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rec.count() >= target {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d Reclaim calls; got %d", target, rec.count())
}

// TestIdleReaper_Loop_SweepsOnEveryTick pins one sweep per tick — and, via the
// exact total, pins OUT an eager sweep at Start() that the waits alone would
// absorb.
func TestIdleReaper_Loop_SweepsOnEveryTick(t *testing.T) {
	t.Parallel()
	rt := newIdleTestRuntime(t, nil)
	rec := &reclaimRecorder{}
	ticks := make(chan time.Time, 2)
	var gotInterval atomic.Int64

	ir := newIdleReaper(idleReaperDeps{
		Registry: &fakeIdleLister{rts: []*AgentRuntime{rt}},
		Reclaim:  rec.fn(),
		Interval: func() time.Duration { return 90 * time.Second },
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
			gotInterval.Store(int64(d))
			return ticks, func() {}
		},
	})
	ir.Start()
	defer ir.Stop()

	ticks <- time.Now()
	waitForReclaims(t, rec, 1)
	ticks <- time.Now()
	waitForReclaims(t, rec, 2)

	if n := rec.count(); n != 2 {
		t.Errorf("Reclaim calls = %d after exactly 2 ticks, want 2 (no sweep outside a tick)", n)
	}
	if got := time.Duration(gotInterval.Load()); got != 90*time.Second {
		t.Errorf("ticker interval = %v, want the configured 90s; a hardcoded cadence makes the sweep knob a lie", got)
	}
}

// TestIdleReaper_LoopReachesItsTickerInstall: with a NewTicker that never
// delivers, Started() must still flip — evidence the loop goroutine got as far
// as installing its ticker, rather than evidence that a struct was built.
//
// Honest limit, stated so nobody over-reads it: this test alone cannot tell
// "started is stored inside loop()" from "started is stored in Start()". What
// it does catch is a loop that never reaches its ticker install. The
// distinguishing observable for the misplaced Store is the pair
// TestIdleReaper_Loop_SweepsOnEveryTick (no sweeps) and
// TestIdleReaper_StopWithoutStartDoesNotDeadlock's sibling case — a Start()
// that marks launched without a goroutine wedges Stop() on doneCh, and the
// recorded control for that mutation was a 20s test-binary timeout panic, not
// a clean failure.
func TestIdleReaper_LoopReachesItsTickerInstall(t *testing.T) {
	t.Parallel()
	ir := newIdleReaper(idleReaperDeps{
		Registry:  &fakeIdleLister{},
		Reclaim:   func(context.Context, *AgentRuntime) {},
		Interval:  func() time.Duration { return time.Hour },
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
	})
	ir.Start()
	defer ir.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ir.Started() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Started() never became true; the loop goroutine did not run")
}

func TestIdleReaper_StopWithoutStartDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	ir := newIdleReaper(idleReaperDeps{})
	done := make(chan struct{})
	go func() { ir.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() without Start() blocked")
	}
	if !ir.Stopped() {
		t.Error("Stopped() = false after a Stop with no goroutine to wait for, want true")
	}
}

// TestNewReal_StartsAndStopsIdleReaper is the composition pin. It is the
// assertion that would have caught "the reaper exists but nothing runs it" —
// the exact shape of the bug this lane is here to prevent, since the only other
// symptom is RSS growth nobody is watching.
func TestNewReal_StartsAndStopsIdleReaper(t *testing.T) {
	t.Parallel()
	// The reaper is DISABLED by default (QUM-1197), so this test opts in via
	// config — which is also what makes it the paired positive for
	// TestNewReal_IdleReaperDisabledWhenThresholdZero: same code path, same
	// polling loop, opposite knob, opposite expected observable.
	root := t.TempDir()
	dir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("idle_reclaim.after: \"15m\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	r, err := NewReal(Config{SprawlRoot: root, CallerName: "weave"})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}
	if r.idleReaper == nil {
		t.Fatal("Real.idleReaper = nil after NewReal with idle_reclaim.after=15m; want a started idle reaper")
	}

	deadline := time.Now().Add(2 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		if r.idleReaper.Started() {
			running = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !running {
		t.Fatal("idle reaper goroutine did not start within 2s of NewReal; NewReal must call idleReaper.Start()")
	}

	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !r.idleReaper.Stopped() {
		t.Fatal("idle reaper still running after Shutdown; Shutdown must call idleReaper.Stop()")
	}
}

// TestNewReal_IdleReaperDisabledWhenThresholdZero is the NEGATIVE control for
// the pair above: direction is "the probe must stay QUIET". With the threshold
// explicitly zero the goroutine must never run at all, so Started() staying
// false is the observable. Paired with TestNewReal_StartsAndStopsIdleReaper —
// which fires in the same conditions minus the knob — it proves the polling
// loop can distinguish the two, rather than always timing out.
func TestNewReal_IdleReaperDisabledWhenThresholdZero(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, ".sprawl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("idle_reclaim.after: \"0\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Written explicitly rather than relying on the absent-key default, so this
	// test keeps testing the KNOB rather than the default if the default moves.
	r, err := NewReal(Config{SprawlRoot: root, CallerName: "weave"})
	if err != nil {
		t.Fatalf("NewReal: %v", err)
	}

	// 500ms against the paired positive's observed cost: in
	// TestNewReal_StartsAndStopsIdleReaper the goroutine sets Started() within
	// a single 10ms poll, so 500ms is ~50x the margin the positive needs. The
	// pairing is what makes this window defensible — a window nobody has
	// watched the positive fit inside is just a guess.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if r.idleReaper != nil && r.idleReaper.Started() {
			t.Fatal("idle reaper started with the threshold set to 0; 0 must disable it entirely")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}
