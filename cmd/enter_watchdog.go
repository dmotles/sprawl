package cmd

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// shouldEnableOrphanWatchdog reports whether `sprawl enter` should arm the
// orphan-detection goroutine. QUM-458 layer 3.
//
// Production `sprawl enter` is intended to outlive its launching shell (users
// run it in tmux, ssh detach, etc.); arming the watchdog there would falsely
// trigger. We gate it to sandbox/test contexts: SPRAWL_TEST_MODE=1 OR a
// SPRAWL_ROOT under /tmp/.
func shouldEnableOrphanWatchdog(getenv func(string) string, sprawlRoot string) bool {
	if getenv("SPRAWL_TEST_MODE") == "1" {
		return true
	}
	if sprawlRoot != "" && strings.HasPrefix(sprawlRoot, "/tmp/") {
		return true
	}
	return false
}

// installOrphanWatchdog arms the orphan watchdog when shouldEnableOrphanWatchdog
// returns true. Otherwise returns a no-op stop func without invoking
// tickerFactory or any of the dependency callbacks.
func installOrphanWatchdog(
	getenv func(string) string,
	sprawlRoot string,
	getppid func() int,
	statRoot func() error,
	quit func(),
	tickerFactory func() (<-chan time.Time, func()),
) (stop func()) {
	if !shouldEnableOrphanWatchdog(getenv, sprawlRoot) {
		return func() {}
	}
	// Capture startPPID synchronously here, before spawning the goroutine,
	// so the watchdog has a deterministic baseline immune to scheduler races.
	startPPID := getppid()
	first := true
	getppidShim := func() int {
		if first {
			first = false
			return startPPID
		}
		return getppid()
	}
	tick, tickStop := tickerFactory()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		runOrphanWatchdog(getppidShim, statRoot, quit, tick, stopCh)
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			tickStop()
			<-done
		})
	}
}

// runOrphanWatchdog is the pure body of the orphan-detection goroutine.
//
// Behavior contract (QUM-458):
//   - On each tick, observe getppid(). If it transitions from a non-1
//     startPPID to 1, call quit() once and return.
//   - If statRoot() returns os.IsNotExist error, call quit() once and return.
//     Other statRoot errors are ignored.
//   - If startPPID is already 1 at function entry, never call quit() based on
//     ppid (we cannot distinguish initial-orphan from later-orphan).
//   - When stop is closed, return without calling quit().
//   - quit() is invoked at most once.
func runOrphanWatchdog(getppid func() int, statRoot func() error, quit func(), tick <-chan time.Time, stop <-chan struct{}) {
	startPPID := getppid()
	fired := false
	fire := func(reason string) {
		if fired {
			return
		}
		fired = true
		fmt.Fprintf(os.Stderr, "[enter] orphan watchdog: %s, exiting\n", reason)
		quit()
	}
	for {
		select {
		case <-stop:
			return
		case _, ok := <-tick:
			if !ok {
				return
			}
			cur := getppid()
			if startPPID != 1 && cur == 1 {
				fire("ppid transitioned to 1")
				return
			}
			if err := statRoot(); err != nil && os.IsNotExist(err) {
				fire("SPRAWL_ROOT vanished")
				return
			}
		}
	}
}
