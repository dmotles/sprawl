package cmd

import (
	"io"
	"sync"
	"time"

	"github.com/dmotles/sprawl/internal/observe/sigdump"
)

// installShutdownWatchdog arms a timer; if disarm() isn't called before the
// timer fires, it writes a goroutine dump to logW, calls release(), then
// exit(code) — in that order. The timerFactory is injected (mirrors
// installOrphanWatchdog's tickerFactory) so tests drive the timeout
// deterministically with no real sleeps.
//
// Ordering invariant on timeout: dump -> release -> exit.
//
// If disarm() is called before the timer fires, the watchdog cancels cleanly:
// dump/release/exit never run, even if the timer channel later delivers or is
// closed. disarm() is idempotent and non-blocking-safe (the goroutine always
// exits on the stop signal).
func installShutdownWatchdog(
	timerFactory func() (<-chan time.Time, func()),
	dump func(w io.Writer),
	logW io.Writer,
	release func(),
	exit func(code int),
) (disarm func()) {
	tick, tickStop := timerFactory()
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		fired := false
		fire := func() {
			if fired {
				return
			}
			fired = true
			dump(logW)
			release()
			exit(1)
		}
		for {
			select {
			case <-stopCh:
				return
			case _, ok := <-tick:
				if !ok {
					return
				}
				fire()
				return
			}
		}
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

// defaultGoroutineDump writes a full goroutine stack dump to w. It is the
// production dump used by installShutdownWatchdog.
func defaultGoroutineDump(w io.Writer) {
	_, _ = w.Write(sigdump.CaptureGoroutines())
}
