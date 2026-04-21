//go:build unix

package tui

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// StderrRedirect captures stderr (both via the os.Stderr pointer and the
// underlying FD 2) into a log file. Restore reverses the swap.
//
// This exists because Bubble Tea's alt-screen render gets corrupted by any
// stray stderr write. Subprocesses launched with cmd.Stderr = os.Stderr
// inherit FD 2 from the parent, so simply swapping the os.Stderr pointer is
// insufficient — we must dup2 FD 2 to the log file. See QUM-304.
type StderrRedirect struct {
	origFD     int      // saved dup of FD 2
	origStderr *os.File // saved os.Stderr pointer
	logFile    *os.File
	path       string
	restored   bool
}

// RedirectStderr opens (creating parent dirs as needed) the given path and
// redirects both os.Stderr and FD 2 into it. The caller is responsible for
// invoking Restore() before process exit.
func RedirectStderr(path string) (*StderrRedirect, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}

	logFile, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // log path is chosen by caller (cmd/enter.go), not untrusted input
	if err != nil {
		return nil, err
	}

	origFD, err := syscall.Dup(2)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}

	if err := unix.Dup2(int(logFile.Fd()), 2); err != nil {
		_ = logFile.Close()
		_ = syscall.Close(origFD)
		return nil, err
	}

	origStderr := os.Stderr
	os.Stderr = logFile

	return &StderrRedirect{
		origFD:     origFD,
		origStderr: origStderr,
		logFile:    logFile,
		path:       path,
	}, nil
}

// Restore reverses RedirectStderr: restores os.Stderr to the original
// *os.File, dup2s FD 2 back to the saved descriptor, and closes the log file.
// Idempotent — subsequent calls are no-ops.
func (r *StderrRedirect) Restore() error {
	if r == nil || r.restored {
		return nil
	}

	var firstErr error

	os.Stderr = r.origStderr

	if err := unix.Dup2(r.origFD, 2); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := syscall.Close(r.origFD); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := r.logFile.Close(); err != nil && firstErr == nil {
		firstErr = err
	}

	r.restored = true
	return firstErr
}

// LogPath returns the path of the log file that stderr is being redirected to.
func (r *StderrRedirect) LogPath() string {
	return r.path
}
