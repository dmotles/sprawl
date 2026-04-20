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
// user at the existing session and at the escape hatch of removing a
// stale lock. For unsupported platforms, it says so plainly. Other errors
// fall through with a generic wrapper.
//
// namespace is the tmux session namespace ("" for the default) used to
// hint at the tmux attach command. sprawlRoot is used to show the
// concrete path of the lock file.
func printWeaveLockError(w io.Writer, err error, namespace, sprawlRoot string) {
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
		fmt.Fprintf(w, "  - If it's in tmux, attach with: tmux attach -t %sweave\n", namespace)
		fmt.Fprintln(w, "  - If it's a sprawl enter TUI, attach that terminal or Ctrl-C it.")
		fmt.Fprintf(w, "  - If you believe it's dead, remove the stale lock: rm %s\n", lockPath)
		return
	}
	fmt.Fprintf(w, "Failed to acquire weave lock: %v\n", err)
}
