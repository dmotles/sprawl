//go:build unix

package sigdump

import (
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Install starts a goroutine that listens for SIGUSR1 and writes a
// goroutine + fd dump into <sprawlRoot>/.sprawl/runtime on each signal.
// The returned stop function unregisters the signal handler and waits
// for the listener goroutine to exit. It is safe to call stop multiple
// times.
func Install(ctx context.Context, sprawlRoot string, logger *log.Logger) (stop func()) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	dir := filepath.Join(sprawlRoot, ".sprawl", "runtime")

	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGUSR1)

	innerCtx, cancel := context.WithCancel(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-innerCtx.Done():
				return
			case <-ch:
				gPath, fdPath, err := Dump(dir, time.Now(), ProcFDSource())
				if err != nil {
					logger.Printf("sigdump: dump error: %v (goroutines=%q fds=%q)", err, gPath, fdPath)
					continue
				}
				logger.Printf("sigdump: wrote goroutines=%s fds=%s", gPath, fdPath)
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(ch)
			cancel()
			// Nudge the goroutine in case it's blocked on the channel.
			// signal.Stop guarantees no further sends, so a close is safe
			// here — but we don't close ch to keep things simple; the
			// cancel above wakes the select.
			wg.Wait()
		})
	}
}
