package cmd

// QUM-636 Part 2: failing unit tests (TDD red phase) for the shutdown
// watchdog. These reference `installShutdownWatchdog`, which does not yet
// exist — this file fails to compile until the implementer lands
// cmd/shutdown_watchdog.go.
//
// Contract (mirrors installOrphanWatchdog's injection style in
// cmd/enter_watchdog.go):
//
//	// installShutdownWatchdog arms a timer; if disarm() isn't called before the
//	// timeout fires, it writes a goroutine dump to logW, calls release(), then
//	// exit(code). The timerFactory is injected (mirrors tickerFactory) so tests
//	// drive the timeout deterministically with no real sleeps.
//	func installShutdownWatchdog(
//	    timerFactory  func() (<-chan time.Time, func()),
//	    dump          func(w io.Writer),
//	    logW          io.Writer,
//	    release       func(),
//	    exit          func(code int),
//	) (disarm func())
//
// Ordering invariant on timeout: dump -> release -> exit.

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestShutdownWatchdog_OnTimeout_DumpsReleasesAndExits proves that when the
// injected timer fires before disarm, the watchdog dumps goroutines to logW,
// best-effort releases the lock, then exits with code 1 — in that order.
func TestShutdownWatchdog_OnTimeout_DumpsReleasesAndExits(t *testing.T) {
	timerCh := make(chan time.Time, 1)
	timerFactory := func() (<-chan time.Time, func()) {
		return timerCh, func() {}
	}

	var (
		mu         sync.Mutex
		order      []string
		logW       bytes.Buffer
		dumpCalled atomic.Bool
		relCalled  atomic.Bool
		exitCode   atomic.Int32
	)
	exitCode.Store(-1)
	exited := make(chan struct{})

	dump := func(w io.Writer) {
		dumpCalled.Store(true)
		mu.Lock()
		order = append(order, "dump")
		mu.Unlock()
		// Write a marker so logW is non-empty even though the real impl's
		// runtime.Stack output would also be present.
		_, _ = io.WriteString(w, "GOROUTINE-DUMP-MARKER\n")
	}
	release := func() {
		relCalled.Store(true)
		mu.Lock()
		order = append(order, "release")
		mu.Unlock()
	}
	exit := func(code int) {
		exitCode.Store(int32(code))
		mu.Lock()
		order = append(order, "exit")
		mu.Unlock()
		close(exited)
	}

	disarm := installShutdownWatchdog(timerFactory, dump, &logW, release, exit)
	defer disarm()

	// Fire the timeout.
	timerCh <- time.Now()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not call exit after timer fired")
	}

	if !dumpCalled.Load() {
		t.Error("dump was not called on timeout")
	}
	if !relCalled.Load() {
		t.Error("release was not called on timeout")
	}
	if got := exitCode.Load(); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
	if logW.Len() == 0 {
		t.Error("logW is empty; expected goroutine dump written before exit")
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	want := []string{"dump", "release", "exit"}
	if len(got) != len(want) {
		t.Fatalf("call order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call order = %v, want %v", got, want)
		}
	}
}

// TestShutdownWatchdog_Disarm_PreventsExit proves that calling disarm() before
// the timer fires cancels the watchdog: even if the timer chan later delivers,
// neither dump nor exit run.
func TestShutdownWatchdog_Disarm_PreventsExit(t *testing.T) {
	timerCh := make(chan time.Time, 1)
	timerFactory := func() (<-chan time.Time, func()) {
		return timerCh, func() {}
	}

	var dumpCalled, exitCalled atomic.Bool
	dump := func(w io.Writer) { dumpCalled.Store(true) }
	release := func() {}
	exit := func(code int) { exitCalled.Store(true) }

	disarm := installShutdownWatchdog(timerFactory, dump, io.Discard, release, exit)

	// Disarm immediately, then attempt to fire the timer.
	disarm()
	close(timerCh)

	// Give any wrongly-running goroutine a chance to act.
	time.Sleep(100 * time.Millisecond)

	if dumpCalled.Load() {
		t.Error("dump called after disarm; watchdog was not cancelled")
	}
	if exitCalled.Load() {
		t.Error("exit called after disarm; watchdog was not cancelled")
	}
}

// TestShutdownWatchdog_Disarm_Idempotent proves disarm() may be called multiple
// times without panicking or blocking.
func TestShutdownWatchdog_Disarm_Idempotent(t *testing.T) {
	timerFactory := func() (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}
	disarm := installShutdownWatchdog(timerFactory,
		func(w io.Writer) {}, io.Discard, func() {}, func(code int) {})

	done := make(chan struct{})
	go func() {
		disarm()
		disarm()
		disarm()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("repeated disarm() blocked")
	}
}

// TestDefaultGoroutineDump_WritesStack proves the production default dump
// function writes a non-trivial goroutine stack to its writer. The watchdog
// tests inject a fake dump; this pins the real one.
//
// RED today: defaultGoroutineDump does not yet exist.
func TestDefaultGoroutineDump_WritesStack(t *testing.T) {
	var buf bytes.Buffer
	defaultGoroutineDump(&buf)
	if buf.Len() == 0 || !strings.Contains(buf.String(), "goroutine") {
		t.Fatalf("default dump did not write a goroutine stack; got %d bytes", buf.Len())
	}
}
