//go:build unix

package merge

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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

// collectingSink returns a thread-safe sink and a snapshot accessor.
func collectingSink() (sink func(string), snapshot func() []string) {
	var mu sync.Mutex
	var lines []string
	sink = func(s string) {
		mu.Lock()
		lines = append(lines, s)
		mu.Unlock()
	}
	snapshot = func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(lines))
		copy(out, lines)
		return out
	}
	return
}

// TestRealRunTestsStreaming_StreamsLines asserts each stdout line is sunk
// in order as the foreground command runs.
func TestRealRunTestsStreaming_StreamsLines(t *testing.T) {
	dir := t.TempDir()
	sink, snap := collectingSink()
	out, err := RealRunTestsStreaming(context.Background(), dir, "echo a; echo b; echo c", sink)
	if err != nil {
		t.Fatalf("RealRunTestsStreaming: %v\noutput=%s", err, out)
	}
	got := snap()
	want := []string{"a", "b", "c"}
	// Filter out heartbeat lines (none expected at this short duration).
	var seen []string
	for _, l := range got {
		if strings.HasPrefix(l, "[heartbeat]") {
			continue
		}
		seen = append(seen, l)
	}
	if len(seen) != len(want) {
		t.Fatalf("sunk lines = %v, want %v", seen, want)
	}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("sunk[%d] = %q, want %q", i, seen[i], w)
		}
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") || !strings.Contains(out, "c") {
		t.Errorf("returned output should contain all lines, got: %q", out)
	}
}

// TestRealRunTestsStreaming_DoesNotWaitForBackgroundedChild is the QUM-496
// regression: a foreground command that backgrounds a long-running child
// must not block RealRunTestsStreaming on the child's stdio.
func TestRealRunTestsStreaming_DoesNotWaitForBackgroundedChild(t *testing.T) {
	dir := t.TempDir()
	sink, _ := collectingSink()

	// `sleep 60 &` backgrounds in the same process group; setpgid + WaitDelay
	// must reap stdio handles so we return promptly.
	start := time.Now()
	_, err := RealRunTestsStreaming(context.Background(), dir, "sleep 60 &\necho fg-done", sink)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("RealRunTestsStreaming: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("QUM-496 regression: returned after %v (expected < 10s); backgrounded sleep blocked us", elapsed)
	}
}

// TestRealRunTestsStreaming_ContextCancel_KillsProcessGroup asserts the
// process group is signaled when ctx is cancelled, even when the leader
// ignores SIGTERM.
func TestRealRunTestsStreaming_ContextCancel_KillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	sink, _ := collectingSink()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// trap '' TERM ignores SIGTERM; only SIGKILL after WaitDelay should kill it.
	_, err := RealRunTestsStreaming(ctx, dir, "trap '' TERM; sleep 30", sink)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "killed") && !strings.Contains(err.Error(), "signal") {
		t.Errorf("error should reflect cancellation, got: %v", err)
	}
	if elapsed > 15*time.Second {
		t.Fatalf("cancel took %v; subprocess group not killed in time", elapsed)
	}
}

// TestRealRunTestsStreaming_TimeoutFromContext asserts ctx deadline cancels.
func TestRealRunTestsStreaming_TimeoutFromContext(t *testing.T) {
	dir := t.TempDir()
	sink, _ := collectingSink()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := RealRunTestsStreaming(ctx, dir, "sleep 30", sink)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("timeout took %v; subprocess not killed", elapsed)
	}
}

// TestRealRunTestsStreaming_Heartbeat asserts a heartbeat line is emitted
// while a long-running command is in progress. We override the heartbeat
// interval via the test-only knob to keep the test fast.
func TestRealRunTestsStreaming_Heartbeat(t *testing.T) {
	dir := t.TempDir()
	sink, snap := collectingSink()

	prev := heartbeatInterval
	heartbeatInterval = 80 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = prev })

	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()

	_, _ = RealRunTestsStreaming(ctx, dir, "echo started; sleep 5", sink)

	var found bool
	for _, l := range snap() {
		if strings.HasPrefix(l, "[heartbeat]") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected at least one [heartbeat] line, got: %v", snap())
	}
}
