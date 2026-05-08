//go:build unix

package sigdump_test

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/observe/sigdump"
)

func runtimeDir(root string) string {
	return filepath.Join(root, ".sprawl", "runtime")
}

// waitForFiles polls runtimeDir until at least n goroutine files and n fds
// files exist, or the deadline passes.
func waitForFiles(t *testing.T, root string, n int, timeout time.Duration) (goroutines, fds []string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	dir := runtimeDir(root)
	for time.Now().Before(deadline) {
		goroutines = nil
		fds = nil
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			switch {
			case strings.HasPrefix(name, "goroutines-"):
				goroutines = append(goroutines, filepath.Join(dir, name))
			case strings.HasPrefix(name, "fds-"):
				fds = append(fds, filepath.Join(dir, name))
			}
		}
		if len(goroutines) >= n && len(fds) >= n {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	return
}

// waitForGoroutineCount polls until at least n goroutine files exist or the
// deadline passes. Returns the slice of paths found at the last poll.
func waitForGoroutineCount(t *testing.T, root string, n int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	dir := runtimeDir(root)
	for time.Now().Before(deadline) {
		var goroutines []string
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, "goroutines-") {
				goroutines = append(goroutines, filepath.Join(dir, name))
			}
		}
		if len(goroutines) >= n {
			return goroutines
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Final read to return whatever we have.
	var goroutines []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "goroutines-") {
			goroutines = append(goroutines, filepath.Join(dir, name))
		}
	}
	return goroutines
}

func TestInstall_DumpsOnSIGUSR1(t *testing.T) {
	root := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sigdump.Install(ctx, root, logger)
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("sending SIGUSR1: %v", err)
	}

	gFiles, fdFiles := waitForFiles(t, root, 1, 2*time.Second)
	if len(gFiles) < 1 {
		t.Fatalf("expected at least 1 goroutines file, got %d", len(gFiles))
	}
	if len(fdFiles) < 1 {
		t.Fatalf("expected at least 1 fds file, got %d", len(fdFiles))
	}
	data, err := os.ReadFile(gFiles[0])
	if err != nil {
		t.Fatalf("reading goroutines file: %v", err)
	}
	if len(data) == 0 {
		t.Error("goroutines file is empty")
	}
	if !bytes.Contains(data, []byte("goroutine ")) {
		t.Errorf("goroutines file missing 'goroutine ' marker")
	}
}

func TestInstall_StopHaltsHandler(t *testing.T) {
	root := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sigdump.Install(ctx, root, logger)
	stop()

	// Protect the test process: after stop(), the implementation may or may
	// not have called signal.Reset(SIGUSR1). If it didn't, SIGUSR1 would
	// terminate the test binary (default action). Install our own throwaway
	// notifier so this process swallows the signal regardless of what the
	// implementation does. We are NOT requiring the implementation to call
	// signal.Reset — we are just keeping the test from killing itself.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGUSR1)
	defer signal.Stop(guard)

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("sending SIGUSR1: %v", err)
	}
	// Drain our guard so it doesn't leak across tests.
	select {
	case <-guard:
	case <-time.After(300 * time.Millisecond):
	}

	dir := runtimeDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading runtime dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no dump files after stop(), got %d entries", len(entries))
	}
}

func TestInstall_HandlesMultipleSignals(t *testing.T) {
	root := t.TempDir()
	logger := log.New(io.Discard, "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sigdump.Install(ctx, root, logger)
	defer stop()

	// Kernel/Go coalesce identical pending signals: if we send N SIGUSR1s
	// in a tight loop, the handler may only observe one. Synchronize via
	// the filesystem — for each signal, wait for the corresponding dump
	// file to appear before sending the next one.
	for i := 1; i <= 3; i++ {
		if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
			t.Fatalf("sending SIGUSR1 #%d: %v", i, err)
		}
		got := waitForGoroutineCount(t, root, i, 2*time.Second)
		if len(got) < i {
			t.Fatalf("after signal #%d: expected >=%d goroutine files, got %d", i, i, len(got))
		}
	}

	gFiles := waitForGoroutineCount(t, root, 3, 1*time.Second)
	if len(gFiles) < 3 {
		t.Fatalf("expected >=3 goroutine files after 3 signals, got %d", len(gFiles))
	}
	seen := make(map[string]struct{}, len(gFiles))
	for _, p := range gFiles {
		seen[filepath.Base(p)] = struct{}{}
	}
	if len(seen) < 3 {
		t.Errorf("expected >=3 distinct goroutine filenames, got %d distinct from %v", len(seen), gFiles)
	}
}
