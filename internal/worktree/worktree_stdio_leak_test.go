//go:build unix

package worktree

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// TestRealCreator_Create_DoesNotLeakSubprocessStdout is the QUM-330 regression
// anchor. `git worktree add` prints "Preparing worktree (new branch '...')"
// and "HEAD is now at ..." to stdout. In TUI mode (Bubble Tea alt-screen),
// any subprocess that inherits the parent's FD 1 paints those strings on top
// of the rendered frame. The fix in worktree.Create must redirect cmd.Stdout
// (and cmd.Stderr) so the child cannot reach FD 1 of the test/parent process.
//
// Strategy: dup2 FD 1 of this test binary to a pipe before invoking Create,
// then restore. Anything the git child wrote to its inherited FD 1 lands in
// the pipe. Assert the pipe is empty.
//
// Cross-reference: QUM-304 fixed the analogous stderr leak from the Claude
// subprocess.
func TestRealCreator_Create_DoesNotLeakSubprocessStdout(t *testing.T) {
	repo := initTestRepo(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer func() { _ = r.Close() }()

	origFD1, err := syscall.Dup(1)
	if err != nil {
		_ = w.Close()
		t.Fatalf("syscall.Dup(1): %v", err)
	}
	if err := unix.Dup2(int(w.Fd()), 1); err != nil {
		_ = w.Close()
		_ = syscall.Close(origFD1)
		t.Fatalf("unix.Dup2: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		os.Stdout = origStdout
		_ = unix.Dup2(origFD1, 1)
		_ = syscall.Close(origFD1)
		_ = w.Close()
	}
	t.Cleanup(restore)

	creator := &RealCreator{}
	wtPath, _, createErr := creator.Create(repo, "alice", "feature/leak-check", "main")

	// Restore FD 1 BEFORE reading the pipe so io.ReadAll sees EOF.
	restore()

	if createErr != nil {
		t.Fatalf("Create: %v", createErr)
	}

	leaked, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	if len(leaked) > 0 {
		t.Errorf("QUM-330 regression: git worktree add leaked %d bytes to FD 1: %q",
			len(leaked), string(leaked))
	}

	cleanup := exec.Command("git", "worktree", "remove", "--force", wtPath)
	cleanup.Dir = repo
	_ = cleanup.Run()
}

// TestRealCreator_Create_FailureSurfacesGitOutput verifies that when git fails,
// the error wraps the captured stderr/stdout so callers (and the MCP tool's
// returned content) still get useful diagnostics — i.e., redirecting away
// from FD 1/2 must not silently swallow the output.
func TestRealCreator_Create_FailureSurfacesGitOutput(t *testing.T) {
	repo := initTestRepo(t)

	// Pre-create the branch so `git worktree add -b` fails with a known message.
	mk := exec.Command("git", "branch", "feature/already-exists", "main")
	mk.Dir = repo
	if out, err := mk.CombinedOutput(); err != nil {
		t.Fatalf("setup branch: %s: %v", out, err)
	}

	creator := &RealCreator{}
	_, _, err := creator.Create(repo, "alice", "feature/already-exists", "main")
	if err == nil {
		t.Fatalf("expected Create to fail when branch already exists")
	}

	msg := err.Error()
	// git's actual error text includes "already exists" — assert that substring
	// is reachable from the wrapped error so the MCP tool can show it.
	if !containsAny(msg, []string{"already exists", "fatal"}) {
		t.Errorf("error message does not surface git diagnostics: %q", msg)
	}
	_ = filepath.Separator
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if len(sub) == 0 {
			continue
		}
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	n := len(sub)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(s); i++ {
		if s[i:i+n] == sub {
			return i
		}
	}
	return -1
}
