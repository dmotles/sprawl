//go:build unix

package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These tests cover the stderr-redirect helper used by the TUI to catch
// stderr output (including subprocess-inherited stderr) and route it to a
// log file instead of bleeding into the TUI view. See QUM-304.
//
// Tests are sequential — they mutate the process-global os.Stderr and FD 2,
// so no t.Parallel().

func TestRedirectStderr_InProcessWrite(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "out.log")

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr: %v", err)
	}
	defer func() {
		_ = r.Restore()
	}()

	fmt.Fprintln(os.Stderr, "hello-from-go")

	if err := r.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "hello-from-go") {
		t.Errorf("log missing expected content; got %q", string(data))
	}
}

// TestRedirectStderr_SubprocessInheritance is the QUM-304 regression anchor.
// A subprocess launched with cmd.Stderr = os.Stderr inherits FD 2 of the
// parent. If RedirectStderr only rebound the *os.File pointer (not the
// underlying FD 2), the child's stderr would still land on the original
// terminal — bleeding into the TUI. This test proves the FD-level dup2.
func TestRedirectStderr_SubprocessInheritance(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "out.log")

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr: %v", err)
	}
	defer func() {
		_ = r.Restore()
	}()

	cmd := exec.Command("sh", "-c", "echo from-child-stderr >&2")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("cmd.Run: %v", err)
	}

	if err := r.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "from-child-stderr") {
		t.Errorf("QUM-304 regression: subprocess stderr did not reach log; got %q", string(data))
	}
}

func TestRedirectStderr_RawFD2Write(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "out.log")

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr: %v", err)
	}
	defer func() {
		_ = r.Restore()
	}()

	if _, err := syscall.Write(2, []byte("raw-fd-bytes\n")); err != nil {
		t.Fatalf("syscall.Write(2, ...): %v", err)
	}

	if err := r.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "raw-fd-bytes") {
		t.Errorf("raw FD-2 write did not reach log; got %q", string(data))
	}
}

func TestRedirectStderr_RestoreIdempotent(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "out.log")

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr: %v", err)
	}

	if err := r.Restore(); err != nil {
		t.Fatalf("first Restore: %v", err)
	}
	if err := r.Restore(); err != nil {
		t.Fatalf("second Restore should be a no-op, got: %v", err)
	}
}

func TestRedirectStderr_RestoresOsStderrPointer(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "out.log")

	origStderr := os.Stderr

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr: %v", err)
	}

	if os.Stderr == origStderr {
		t.Errorf("expected os.Stderr to be swapped after RedirectStderr, but pointer unchanged")
	}

	if err := r.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if os.Stderr != origStderr {
		t.Errorf("expected os.Stderr to be restored to original pointer after Restore")
	}
}

func TestRedirectStderr_CreatesParentDirs(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "nested", "dirs", "out.log")

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr should create parent dirs, got: %v", err)
	}
	defer func() {
		_ = r.Restore()
	}()

	parent := filepath.Dir(logPath)
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("stat parent dir: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected parent path %q to be a directory", parent)
	}

	if err := r.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

func TestRedirectStderr_LogPath(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "out.log")

	r, err := RedirectStderr(logPath)
	if err != nil {
		t.Fatalf("RedirectStderr: %v", err)
	}
	defer func() {
		_ = r.Restore()
	}()

	if got := r.LogPath(); got != logPath {
		t.Errorf("LogPath() = %q, want %q", got, logPath)
	}
}

func TestRedirectStderr_InvalidPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root — filesystem permissions are bypassed, cannot verify error path")
	}

	tmp := t.TempDir()
	roDir := filepath.Join(tmp, "ro")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatalf("Chmod ro: %v", err)
	}
	t.Cleanup(func() {
		// Restore write perms so tempdir cleanup can remove it.
		_ = os.Chmod(roDir, 0o755)
	})

	logPath := filepath.Join(roDir, "x.log")

	origStderr := os.Stderr

	r, err := RedirectStderr(logPath)
	if err == nil {
		if r != nil {
			_ = r.Restore()
		}
		t.Fatalf("expected error for unwritable path %q, got nil", logPath)
	}

	if os.Stderr != origStderr {
		t.Errorf("os.Stderr should be unchanged after failed RedirectStderr")
	}

	// Ensure the (non-)log file was not created.
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("expected log file to not exist after failed redirect, stat err = %v", err)
	}
}
