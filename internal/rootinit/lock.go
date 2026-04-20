package rootinit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/gofrs/flock"
)

// ErrWeaveAlreadyRunning is a sentinel error indicating another weave
// session is holding the flock on `.sprawl/memory/weave.lock`. The concrete
// error returned by AcquireWeaveLock is *AlreadyRunningError, which
// unwraps to this sentinel via errors.Is.
var ErrWeaveAlreadyRunning = errors.New("weave already running")

// AlreadyRunningError describes a failed lock acquisition. It carries the
// PID recorded in the lock file (may be 0 if the file could not be read)
// and the path of the lock file so the caller can print a useful message.
type AlreadyRunningError struct {
	PID  int
	Path string
}

func (e *AlreadyRunningError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("another weave session is already running (PID %d)", e.PID)
	}
	return "another weave session is already running"
}

// Unwrap lets errors.Is(err, ErrWeaveAlreadyRunning) succeed.
func (e *AlreadyRunningError) Unwrap() error { return ErrWeaveAlreadyRunning }

// WeaveLock represents an exclusive, process-scoped advisory flock on
// `<sprawlRoot>/.sprawl/memory/weave.lock`. The lock is released either by
// Release() or automatically when the process exits (the kernel drops the
// flock when the file descriptor is closed).
type WeaveLock struct {
	path     string
	fl       *flock.Flock
	released atomic.Bool
}

// Path returns the absolute path of the lock file.
func (l *WeaveLock) Path() string { return l.path }

// AcquireWeaveLock tries to obtain a non-blocking exclusive flock on
// `<sprawlRoot>/.sprawl/memory/weave.lock`. On success it writes the
// current process PID to the file and returns the held lock. On
// contention it returns an *AlreadyRunningError that unwraps to
// ErrWeaveAlreadyRunning.
//
// Stale lock files (where the previous holder died without releasing) are
// reclaimed transparently: a fresh fd acquires the advisory lock because
// the kernel released it when the prior fd closed, regardless of whether
// the file persists.
//
// The `.sprawl/memory` directory is created with MkdirAll(0755) if missing.
func AcquireWeaveLock(sprawlRoot string) (*WeaveLock, error) {
	memDir := filepath.Join(sprawlRoot, ".sprawl", "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil { //nolint:gosec // G301: match other memory-dir perms (world-readable is intentional)
		return nil, fmt.Errorf("creating memory dir: %w", err)
	}
	path := filepath.Join(memDir, "weave.lock")

	fl := flock.New(path)
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("flocking weave.lock: %w", err)
	}
	if !locked {
		// Contention: read the existing PID for diagnostics. Best-effort.
		return nil, &AlreadyRunningError{PID: readLockPID(path), Path: path}
	}

	// Overwrite the file with our PID. gofrs/flock keeps the file open
	// internally; we write via a separate handle. Truncate first in case
	// the stale contents are longer than our PID string.
	if writeErr := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); writeErr != nil { //nolint:gosec // G306: match adjacent memory files
		_ = fl.Unlock()
		return nil, fmt.Errorf("writing PID to weave.lock: %w", writeErr)
	}

	return &WeaveLock{path: path, fl: fl}, nil
}

// Release drops the advisory lock and closes the underlying file. It is
// safe to call multiple times. The lock file itself is left on disk;
// future invocations reclaim it via flock.
func (l *WeaveLock) Release() error {
	if l == nil || l.released.Swap(true) {
		return nil
	}
	return l.fl.Unlock()
}

// readLockPID reads and parses a decimal PID from the lock file. Returns 0
// if the file is missing, empty, or malformed.
func readLockPID(path string) int {
	data, err := os.ReadFile(path) //nolint:gosec // lock file path is derived from sprawlRoot
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}
