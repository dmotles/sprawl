package cmd

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/dmotles/sprawl/internal/rootinit"
)

// printWeaveLockError emits a human-friendly message when AcquireWeaveLock
// fails. For contention (ErrWeaveAlreadyRunning) the message points the
// user at the existing weave session and at the escape hatch of removing a
// stale lock. Other errors fall through with a generic wrapper.
//
// namespace is retained for compatibility with older callers but is ignored in
// the same-process runtime. sprawlRoot is used to show the concrete path of
// the lock file.
func printWeaveLockError(w io.Writer, err error, namespace, sprawlRoot string) {
	if w == nil {
		return
	}
	_ = namespace
	lockPath := filepath.Join(sprawlRoot, ".sprawl", "memory", "weave.lock")

	var already *rootinit.AlreadyRunningError
	if errors.As(err, &already) {
		if already.PID > 0 {
			fmt.Fprintf(w, "Another weave session is already running (PID %d).\n", already.PID)
		} else {
			fmt.Fprintln(w, "Another weave session is already running.")
		}
		fmt.Fprintln(w, "  - Attach the existing `sprawl enter` terminal, or stop it before starting another weave session.")
		fmt.Fprintf(w, "  - If you believe it's dead, remove the stale lock: rm %s\n", lockPath)
		return
	}
	fmt.Fprintf(w, "Failed to acquire weave lock: %v\n", err)
}

func acquireOfflineLifecycle(sprawlRoot, commandName, toolName string) (*rootinit.WeaveLock, error) {
	if sprawlRoot == "" {
		return nil, fmt.Errorf("SPRAWL_ROOT environment variable is not set")
	}

	lock, err := rootinit.AcquireWeaveLock(sprawlRoot)
	switch {
	case err == nil:
		return lock, nil
	case errors.Is(err, rootinit.ErrWeaveAlreadyRunning):
		return nil, fmt.Errorf("standalone `sprawl %s` is unavailable while `sprawl enter` is running; use the `%s` MCP tool from the live weave session instead", commandName, toolName)
	default:
		return nil, fmt.Errorf("checking for active weave session: %w", err)
	}
}
