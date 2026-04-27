//go:build unix

package worktree

import (
	"io"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// TestBranchExists_DoesNotLeakStderr is a QUM-342 regression test.
// `git rev-parse --verify` writes to stderr when the ref is missing.
// In TUI mode, an inherited FD 2 paints those messages onto the alt-screen.
func TestBranchExists_DoesNotLeakStderr(t *testing.T) {
	repo := initTestRepo(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer func() { _ = r.Close() }()

	origFD1, err := syscall.Dup(1)
	if err != nil {
		_ = w.Close()
		t.Fatalf("dup(1): %v", err)
	}
	origFD2, err := syscall.Dup(2)
	if err != nil {
		_ = syscall.Close(origFD1)
		_ = w.Close()
		t.Fatalf("dup(2): %v", err)
	}
	if err := unix.Dup2(int(w.Fd()), 1); err != nil {
		_ = syscall.Close(origFD1)
		_ = syscall.Close(origFD2)
		_ = w.Close()
		t.Fatalf("dup2(1): %v", err)
	}
	if err := unix.Dup2(int(w.Fd()), 2); err != nil {
		_ = syscall.Close(origFD1)
		_ = syscall.Close(origFD2)
		_ = w.Close()
		t.Fatalf("dup2(2): %v", err)
	}
	origStdout, origStderr := os.Stdout, os.Stderr
	os.Stdout = w
	os.Stderr = w

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		os.Stdout = origStdout
		os.Stderr = origStderr
		_ = unix.Dup2(origFD1, 1)
		_ = unix.Dup2(origFD2, 2)
		_ = syscall.Close(origFD1)
		_ = syscall.Close(origFD2)
		_ = w.Close()
	}
	t.Cleanup(restore)

	got := branchExists(repo, "definitely-not-a-branch-xyz")

	restore()

	if got {
		t.Errorf("branchExists for missing branch returned true")
	}

	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(leaked) > 0 {
		t.Errorf("QUM-342 regression: branchExists leaked %d bytes to inherited stdio: %q",
			len(leaked), string(leaked))
	}
}
