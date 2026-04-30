package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/rootinit"
)

func TestAcquireOfflineLifecycle_HoldsWeaveLockUntilRelease(t *testing.T) {
	sprawlRoot := t.TempDir()

	lock, err := acquireOfflineLifecycle(sprawlRoot, "kill", "kill")
	if err != nil {
		t.Fatalf("acquireOfflineLifecycle() error: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	other, err := rootinit.AcquireWeaveLock(sprawlRoot)
	if err == nil {
		_ = other.Release()
		t.Fatal("expected second weave lock acquisition to fail while offline lifecycle lock is held")
	}
	if !errors.Is(err, rootinit.ErrWeaveAlreadyRunning) {
		t.Fatalf("error = %v, want ErrWeaveAlreadyRunning", err)
	}
}

func TestAcquireOfflineLifecycle_FailsClosedWhenWeaveOwnsLock(t *testing.T) {
	sprawlRoot := t.TempDir()
	live, err := rootinit.AcquireWeaveLock(sprawlRoot)
	if err != nil {
		t.Fatalf("AcquireWeaveLock: %v", err)
	}
	t.Cleanup(func() { _ = live.Release() })

	_, err = acquireOfflineLifecycle(sprawlRoot, "retire", "retire")
	if err == nil {
		t.Fatal("expected fail-closed error while live weave owns the lock")
	}
	if !strings.Contains(err.Error(), "sprawl enter") || !strings.Contains(err.Error(), "retire") {
		t.Fatalf("error = %q, want fail-closed guidance", err)
	}
}
