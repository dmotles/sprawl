//go:build unix

package merge

import (
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// initTestRepoForLeakCheck creates a tmp git repo with one commit on main.
func initTestRepoForLeakCheck(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %s: %v", args, out, err)
		}
	}
	return dir
}

// captureFDs dup2s FD 1 and FD 2 to a pipe.
func captureFDs(t *testing.T) (*os.File, func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origFD1, err := syscall.Dup(1)
	if err != nil {
		_ = r.Close()
		_ = w.Close()
		t.Fatalf("dup(1): %v", err)
	}
	origFD2, err := syscall.Dup(2)
	if err != nil {
		_ = syscall.Close(origFD1)
		_ = r.Close()
		_ = w.Close()
		t.Fatalf("dup(2): %v", err)
	}
	if err := unix.Dup2(int(w.Fd()), 1); err != nil {
		_ = syscall.Close(origFD1)
		_ = syscall.Close(origFD2)
		_ = r.Close()
		_ = w.Close()
		t.Fatalf("dup2(1): %v", err)
	}
	if err := unix.Dup2(int(w.Fd()), 2); err != nil {
		_ = syscall.Close(origFD1)
		_ = syscall.Close(origFD2)
		_ = r.Close()
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
	return r, restore
}

// TestRealGitRebaseAbort_DoesNotLeakStdio is a QUM-342 regression test.
// `git rebase --abort` with no rebase in progress writes "fatal: No rebase
// in progress?" to stderr. Without explicit FD redirection, that stderr
// inherits the parent's FD 2 — in TUI mode, that's the alt-screen.
func TestRealGitRebaseAbort_DoesNotLeakStdio(t *testing.T) {
	repo := initTestRepoForLeakCheck(t)

	r, restore := captureFDs(t)

	err := RealGitRebaseAbort(repo)

	restore()

	// RealGitRebaseAbort intentionally swallows errors (best-effort cleanup).
	if err != nil {
		t.Errorf("RealGitRebaseAbort returned non-nil error %v (best-effort contract violated)", err)
	}

	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(leaked) > 0 {
		t.Errorf("QUM-342 regression: RealGitRebaseAbort leaked %d bytes to inherited stdio: %q",
			len(leaked), string(leaked))
	}
}
