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
// sprawlRoot is used to show the concrete path of the lock file.
func printWeaveLockError(w io.Writer, err error, sprawlRoot string) {
	if w == nil {
		return
	}
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
