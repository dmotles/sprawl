package cmd

import (
	"errors"
	"io/fs"
	"sync/atomic"
	"testing"
	"time"
)

func TestShouldEnableOrphanWatchdog(t *testing.T) {
	tests := []struct {
		name       string
		envTestMod string
		root       string
		want       bool
	}{
		{"test_mode_with_tmp_root", "1", "/tmp/sprawl-test-foo", true},
		{"test_mode_with_home_root", "1", "/home/u/.sprawl", true},
		{"unset_with_tmp_root", "", "/tmp/sprawl-x", true},
		{"unset_with_home_root", "", "/home/u/.sprawl", false},
		{"unset_empty_root", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k == "SPRAWL_TEST_MODE" {
					return tt.envTestMod
				}
				return ""
			}
			got := shouldEnableOrphanWatchdog(getenv, tt.root)
			if got != tt.want {
				t.Errorf("shouldEnableOrphanWatchdog(%q, %q) = %v, want %v",
					tt.envTestMod, tt.root, got, tt.want)
			}
		})
	}
}

// runWatchdog drives runOrphanWatchdog in a goroutine and returns:
//   - a function to deliver one tick (blocks until the watchdog has consumed it
//     or returned),
//   - a function to close the stop channel and wait for the watchdog goroutine,
//   - a *int32 holding the quit invocation count.
type watchdogHarness struct {
	tickCh   chan time.Time
	stopCh   chan struct{}
	done     chan struct{}
	quitN    *int32
	getppidF func() int
	statF    func() error
}

func runWatchdog(getppid func() int, statRoot func() error) *watchdogHarness {
	h := &watchdogHarness{
		// Unbuffered: h.tick(t) blocks until the goroutine receives, so each
		// tick send synchronizes with the watchdog's read of getppid()/statRoot()
		// for that tick before the parent test moves on.
		tickCh:   make(chan time.Time),
		stopCh:   make(chan struct{}),
		done:     make(chan struct{}),
		quitN:    new(int32),
		getppidF: getppid,
		statF:    statRoot,
	}
	quit := func() {
		atomic.AddInt32(h.quitN, 1)
	}
	// Capture startPPID synchronously *before* spawning the goroutine, then
	// hand the watchdog a shim that returns the captured value on its first
	// call. This mirrors what installOrphanWatchdog does in production and
	// makes the harness immune to a scheduler race where the parent mutates
	// ppid between Store(...) and the goroutine's first getppid() read.
	startPPID := getppid()
	first := true
	getppidShim := func() int {
		if first {
			first = false
			return startPPID
		}
		return getppid()
	}
	go func() {
		defer close(h.done)
		runOrphanWatchdog(getppidShim, statRoot, quit, h.tickCh, h.stopCh)
	}()
	return h
}

func (h *watchdogHarness) tick(t *testing.T) {
	t.Helper()
	select {
	case h.tickCh <- time.Now():
	case <-h.done:
		// Watchdog already exited; ignore.
	case <-time.After(time.Second):
		t.Fatal("tick send blocked")
	}
}

func (h *watchdogHarness) stop(t *testing.T) {
	t.Helper()
	close(h.stopCh)
	select {
	case <-h.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not exit on stop")
	}
}

func (h *watchdogHarness) waitDone(t *testing.T, d time.Duration) bool {
	t.Helper()
	select {
	case <-h.done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestInstallOrphanWatchdog_ProductionDoesNotArm asserts that in a
// production-shaped environment (SPRAWL_TEST_MODE unset, SPRAWL_ROOT under
// /home), the install seam returns a no-op stop func and does NOT spawn a
// watchdog goroutine. Provable via the tickerFactory: it must never be
// invoked, and getppid/statRoot must never be queried.
func TestInstallOrphanWatchdog_ProductionDoesNotArm(t *testing.T) {
	var tickerCalls, ppidCalls, statCalls atomic.Int32
	tickerFactory := func() (<-chan time.Time, func()) {
		tickerCalls.Add(1)
		ch := make(chan time.Time)
		return ch, func() {}
	}
	getppid := func() int { ppidCalls.Add(1); return 1234 }
	statRoot := func() error { statCalls.Add(1); return nil }
	getenv := func(k string) string { return "" }
	stop := installOrphanWatchdog(getenv, "/home/u/.sprawl", getppid, statRoot, func() {}, tickerFactory)
	if stop == nil {
		t.Fatal("installOrphanWatchdog returned nil stop func")
	}
	// Give any wrongly-spawned goroutine a chance to run.
	time.Sleep(50 * time.Millisecond)
	stop()
	if got := tickerCalls.Load(); got != 0 {
		t.Errorf("tickerFactory called %d times in production env; want 0", got)
	}
	if got := ppidCalls.Load(); got != 0 {
		t.Errorf("getppid queried %d times in production env; want 0", got)
	}
	if got := statCalls.Load(); got != 0 {
		t.Errorf("statRoot queried %d times in production env; want 0", got)
	}
}

// TestInstallOrphanWatchdog_SandboxArms asserts that in a sandbox-shaped
// environment (SPRAWL_TEST_MODE=1), the install seam DOES arm the
// watchdog: tickerFactory is invoked, and after delivering a tick that
// induces a real trigger, quit fires.
func TestInstallOrphanWatchdog_SandboxArms(t *testing.T) {
	var tickerCalls atomic.Int32
	tickCh := make(chan time.Time, 4)
	tickerFactory := func() (<-chan time.Time, func()) {
		tickerCalls.Add(1)
		return tickCh, func() {}
	}
	var ppid atomic.Int32
	ppid.Store(1234)
	getppid := func() int { return int(ppid.Load()) }
	statRoot := func() error { return nil }
	var quitN atomic.Int32
	quit := func() { quitN.Add(1) }
	getenv := func(k string) string {
		if k == "SPRAWL_TEST_MODE" {
			return "1"
		}
		return ""
	}
	stop := installOrphanWatchdog(getenv, "/tmp/sprawl-test-x", getppid, statRoot, quit, tickerFactory)
	defer stop()
	// Drive a real trigger: ppid 1234 -> 1.
	tickCh <- time.Now()
	ppid.Store(1)
	tickCh <- time.Now()
	// Wait for quit.
	deadline := time.Now().Add(2 * time.Second)
	for quitN.Load() == 0 && time.Now().Before(deadline) {
		select {
		case tickCh <- time.Now():
		case <-time.After(50 * time.Millisecond):
		}
	}
	if got := tickerCalls.Load(); got != 1 {
		t.Errorf("tickerFactory called %d times in sandbox env; want 1", got)
	}
	if got := quitN.Load(); got < 1 {
		t.Errorf("quit not invoked after sandbox ppid->1 trigger; got %d, want >=1", got)
	}
}

func TestRunOrphanWatchdog_StablePpidNoQuit(t *testing.T) {
	h := runWatchdog(func() int { return 1234 }, func() error { return nil })
	h.tick(t)
	h.tick(t)
	h.stop(t)
	if got := atomic.LoadInt32(h.quitN); got != 0 {
		t.Errorf("quit called %d times for stable ppid; want 0", got)
	}
}

func TestRunOrphanWatchdog_PpidTransitionsToInitTriggersQuit(t *testing.T) {
	var ppid atomic.Int32
	ppid.Store(1234)
	h := runWatchdog(func() int { return int(ppid.Load()) }, func() error { return nil })
	h.tick(t)
	ppid.Store(1)
	h.tick(t)
	if !h.waitDone(t, 2*time.Second) {
		// Force exit so test doesn't hang.
		close(h.stopCh)
		<-h.done
	}
	if got := atomic.LoadInt32(h.quitN); got != 1 {
		t.Errorf("quit called %d times after ppid->1 transition; want 1", got)
	}
}

func TestRunOrphanWatchdog_StartingAtInitNeverQuits(t *testing.T) {
	h := runWatchdog(func() int { return 1 }, func() error { return nil })
	h.tick(t)
	h.tick(t)
	h.tick(t)
	h.stop(t)
	if got := atomic.LoadInt32(h.quitN); got != 0 {
		t.Errorf("quit called %d times; ppid started at 1 should never trigger", got)
	}
}

func TestRunOrphanWatchdog_StatRootENOENTTriggersQuit(t *testing.T) {
	h := runWatchdog(func() int { return 1234 },
		func() error { return &fs.PathError{Op: "stat", Err: fs.ErrNotExist} })
	h.tick(t)
	if !h.waitDone(t, 2*time.Second) {
		close(h.stopCh)
		<-h.done
	}
	if got := atomic.LoadInt32(h.quitN); got != 1 {
		t.Errorf("quit called %d times after ENOENT; want 1", got)
	}
}

func TestRunOrphanWatchdog_StatRootOtherErrorIgnored(t *testing.T) {
	h := runWatchdog(func() int { return 1234 },
		func() error { return errors.New("permission denied") })
	h.tick(t)
	h.tick(t)
	h.stop(t)
	if got := atomic.LoadInt32(h.quitN); got != 0 {
		t.Errorf("quit called %d times on non-ENOENT error; want 0", got)
	}
}

func TestRunOrphanWatchdog_QuitInvokedAtMostOnce(t *testing.T) {
	// Test contract: across a real trigger AND any subsequent ticks, quit
	// fires EXACTLY once. This fails against:
	//   - the no-op stub (count = 0, want 1)
	//   - a "fires every tick" implementation (count > 1)
	//   - an implementation that re-triggers on stop (count > 1)
	var ppid atomic.Int32
	ppid.Store(1234)
	h := runWatchdog(func() int { return int(ppid.Load()) }, func() error { return nil })
	// First, induce a real trigger.
	h.tick(t)
	ppid.Store(1)
	h.tick(t)
	// Wait for the watchdog to observe the transition and fire quit at least
	// once.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(h.quitN) < 1 && time.Now().Before(deadline) {
		select {
		case h.tickCh <- time.Now():
		case <-h.done:
		case <-time.After(50 * time.Millisecond):
		}
	}
	if got := atomic.LoadInt32(h.quitN); got < 1 {
		t.Fatalf("quit not invoked after ppid->1 transition; got %d, want >=1", got)
	}
	// Now pump additional ticks. Any sane impl should have already returned,
	// but if it hasn't, extra ticks must NOT increment quitN further.
	for i := 0; i < 5; i++ {
		select {
		case h.tickCh <- time.Now():
		case <-h.done:
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !h.waitDone(t, 2*time.Second) {
		close(h.stopCh)
		<-h.done
	}
	if got := atomic.LoadInt32(h.quitN); got != 1 {
		t.Errorf("quit called %d times across trigger + 5 follow-up ticks; want exactly 1", got)
	}
}
