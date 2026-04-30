package rootinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gofrs/flock"
)

// consolidatingLockName is the flock-backed lockfile that marks an in-flight
// background consolidation pipeline. Next-handoff callers wait on this lock
// before scheduling another run so rapid back-to-back handoffs serialize.
const consolidatingLockName = ".consolidating"

// BackgroundConsolidationTimeout is the default upper bound on how long
// callers wait for an in-flight consolidation to complete before giving up
// and proceeding. Picked to cover the slow-prompt + slow-model combination
// that motivated QUM-282.
const BackgroundConsolidationTimeout = 120 * time.Second

func consolidatingLockPath(sprawlRoot string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "memory", consolidatingLockName)
}

// WaitForBackgroundConsolidation blocks until any in-flight consolidation
// completes, or timeout elapses. Returns immediately if no consolidation is
// pending. The flock is released after acquisition. On timeout, a warning
// is emitted to stdout and the function returns — the caller proceeds.
func WaitForBackgroundConsolidation(sprawlRoot string, timeout time.Duration, stdout io.Writer, prefix string) {
	path := consolidatingLockPath(sprawlRoot)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}
	fl := flock.New(path)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil {
		// Context-expired is the timeout case, not an I/O error.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			if stdout != nil {
				fmt.Fprintf(stdout, "%s warning: prior consolidation did not finish within %s\n", prefix, timeout)
			}
			return
		}
		if stdout != nil {
			fmt.Fprintf(stdout, "%s warning: waiting for prior consolidation: %v\n", prefix, err)
		}
		return
	}
	if !ok {
		if stdout != nil {
			fmt.Fprintf(stdout, "%s warning: prior consolidation did not finish within %s\n", prefix, timeout)
		}
		return
	}
	_ = fl.Unlock()
}

// StartBackgroundConsolidation acquires the consolidation flock and runs
// the pipeline in a goroutine, returning a channel closed when the
// goroutine exits. If another consolidation is already running (flock
// contention) or the memory dir cannot be created, the returned channel is
// closed immediately — the already-running consolidation will pick up the
// new sessions anyway.
//
// The goroutine uses context.Background() so it outlives the caller's
// context. Per-invocation timeouts inside the pipeline (see
// memory.TimelineCompressionConfig.InvokeTimeout) keep it bounded.
func StartBackgroundConsolidation(deps *Deps, sprawlRoot string, stdout io.Writer, events chan<- ConsolidationEvent) <-chan struct{} {
	done := make(chan struct{})
	memDir := filepath.Join(sprawlRoot, ".sprawl", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil { //nolint:gosec // G301: match existing memory dir perms
		fmt.Fprintf(stdout, "%s warning: creating memory dir for consolidation lock: %v\n", deps.LogPrefix, err)
		close(done)
		return done
	}
	path := consolidatingLockPath(sprawlRoot)
	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		fmt.Fprintf(stdout, "%s warning: acquiring consolidation lock: %v\n", deps.LogPrefix, err)
		close(done)
		return done
	}
	if !locked {
		fmt.Fprintf(stdout, "%s consolidation already in progress, skipping\n", deps.LogPrefix)
		close(done)
		return done
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644) //nolint:gosec // G306: world-readable, matches adjacent memory files

	go func() {
		defer close(done)
		defer func() { _ = fl.Unlock() }()
		sendConsolidationEvent(events, ConsolidationEvent{Phase: "consolidation started"})
		start := time.Now()
		runConsolidationPipeline(context.Background(), deps, sprawlRoot, stdout, events)
		sendConsolidationEvent(events, ConsolidationEvent{Done: true, Duration: time.Since(start)})
	}()
	return done
}
