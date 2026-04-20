package rootinit

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAcquireWeaveLock_CleanState(t *testing.T) {
	root := t.TempDir()

	lock, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("AcquireWeaveLock returned error: %v", err)
	}
	if lock == nil {
		t.Fatal("AcquireWeaveLock returned nil lock")
	}
	t.Cleanup(func() { _ = lock.Release() })

	// Lock file should exist at the expected path.
	path := filepath.Join(root, ".sprawl", "memory", "weave.lock")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file not created at %s: %v", path, err)
	}
}

func TestAcquireWeaveLock_CreatesMemoryDir(t *testing.T) {
	root := t.TempDir()
	// Do not pre-create .sprawl/memory; AcquireWeaveLock should MkdirAll.

	lock, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("AcquireWeaveLock returned error: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	info, err := os.Stat(filepath.Join(root, ".sprawl", "memory"))
	if err != nil {
		t.Fatalf("memory dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory at .sprawl/memory")
	}
}

func TestAcquireWeaveLock_WritesPID(t *testing.T) {
	root := t.TempDir()

	lock, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("AcquireWeaveLock returned error: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	path := filepath.Join(root, ".sprawl", "memory", "weave.lock")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading lock file: %v", err)
	}
	pidStr := strings.TrimSpace(string(data))
	gotPID, err := strconv.Atoi(pidStr)
	if err != nil {
		t.Fatalf("lock file contents %q not a valid integer PID: %v", pidStr, err)
	}
	if gotPID != os.Getpid() {
		t.Fatalf("PID in lock file = %d, want %d", gotPID, os.Getpid())
	}
}

func TestAcquireWeaveLock_Contention(t *testing.T) {
	root := t.TempDir()

	first, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	t.Cleanup(func() { _ = first.Release() })

	second, err := AcquireWeaveLock(root)
	if err == nil {
		_ = second.Release()
		t.Fatal("expected second acquire to fail while first holds the lock")
	}

	var already *AlreadyRunningError
	if !errors.As(err, &already) {
		t.Fatalf("expected ErrWeaveAlreadyRunning/*AlreadyRunningError, got %T: %v", err, err)
	}
	if already.PID != os.Getpid() {
		t.Fatalf("AlreadyRunningError.PID = %d, want %d", already.PID, os.Getpid())
	}
	if !errors.Is(err, ErrWeaveAlreadyRunning) {
		t.Fatalf("errors.Is(err, ErrWeaveAlreadyRunning) = false")
	}
}

func TestAcquireWeaveLock_StaleFileRecovers(t *testing.T) {
	root := t.TempDir()

	// Simulate stale state: a lock file exists from a prior run, but no
	// process holds the fd. flock on a fresh fd should succeed.
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "memory", "weave.lock"), []byte("999999\n"), 0o644); err != nil {
		t.Fatalf("pre-write lock file: %v", err)
	}

	lock, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("expected stale lock to be reclaimable, got err: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	// PID should now reflect the current process, not the stale value.
	data, err := os.ReadFile(filepath.Join(root, ".sprawl", "memory", "weave.lock"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("invalid pid in file: %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("expected PID %d after stale-reclaim, got %d", os.Getpid(), pid)
	}
}

func TestWeaveLock_ReleaseAllowsReacquire(t *testing.T) {
	root := t.TempDir()

	first, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	second, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	_ = second.Release()
}

func TestWeaveLock_DoubleReleaseIsSafe(t *testing.T) {
	root := t.TempDir()
	lock, err := AcquireWeaveLock(root)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	// Second release should not panic and should not error.
	if err := lock.Release(); err != nil {
		t.Fatalf("second release should be a no-op, got: %v", err)
	}
}

func TestAlreadyRunningError_Message(t *testing.T) {
	e := &AlreadyRunningError{PID: 12345, Path: "/tmp/foo/weave.lock"}
	msg := e.Error()
	if !strings.Contains(msg, "12345") {
		t.Errorf("error message %q should contain PID", msg)
	}
	if !strings.Contains(msg, "already running") {
		t.Errorf("error message %q should mention 'already running'", msg)
	}
}
