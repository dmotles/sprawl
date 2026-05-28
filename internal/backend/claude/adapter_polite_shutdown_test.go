// QUM-638: polite-shutdown handshake (SIGTERM → grace → SIGKILL) on the
// claude transport. These tests exercise the real subprocess signal path
// via realStarter; they need an OS that supports POSIX signals.

package claude

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

func TestResolveTermGrace(t *testing.T) {
	cases := []struct {
		name string
		env  string
		set  bool
		want time.Duration
	}{
		{name: "unset falls back to default", set: false, want: defaultTermGrace},
		{name: "empty falls back to default", env: "", set: true, want: defaultTermGrace},
		{name: "valid short duration", env: "50ms", set: true, want: 50 * time.Millisecond},
		{name: "negative passes through (immediate-escalate test seam)", env: "-1s", set: true, want: -1 * time.Second},
		{name: "unparseable falls back to default", env: "not-a-duration", set: true, want: defaultTermGrace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("SPRAWL_TERM_GRACE", tc.env)
			} else {
				t.Setenv("SPRAWL_TERM_GRACE", "")
			}
			got := resolveTermGrace()
			if got != tc.want {
				t.Errorf("resolveTermGrace() = %v, want %v", got, tc.want)
			}
		})
	}
}

// alivePID reports whether pid is still a live process (signal(0) probe).
func alivePID(t *testing.T, pid int) bool {
	t.Helper()
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// TestTransport_Close_TerminatesViaSIGTERM proves QUM-638 happy path: a
// subprocess that honors SIGTERM exits within the grace period and is NOT
// escalated to SIGKILL. Asserts the wall-clock budget so a regression that
// silently waited the full grace surfaces.
func TestTransport_Close_TerminatesViaSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	bashBin, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash on PATH: %v", err)
	}

	// Generous grace so we know any sub-grace exit is from SIGTERM, not
	// the escalation timer.
	t.Setenv("SPRAWL_TERM_GRACE", "2s")

	starter := &realStarter{}
	tr, err := starter.Start(ExecSpec{
		Path: bashBin,
		Args: []string{"-c", "exec sleep 30"},
		Dir:  t.TempDir(),
		Env:  os.Environ(),
	})
	if err != nil {
		t.Fatalf("starter.Start: %v", err)
	}
	pid := tr.Pid()
	if pid <= 0 {
		t.Fatalf("Pid() = %d, want > 0", pid)
	}

	// Give the subprocess a beat to exec sleep + install default signal handlers.
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	_ = tr.Close()
	closeElapsed := time.Since(start)

	waitDone := make(chan struct{})
	go func() { _ = tr.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return within 3s of Close() — terminate path stalled")
	}

	if closeElapsed >= 2*time.Second {
		t.Errorf("Close() took %v, want < 2s grace — SIGKILL escalation likely fired on a SIGTERM-respecting child", closeElapsed)
	}
	if alivePID(t, pid) {
		t.Errorf("process %d still alive after Close()+Wait()", pid)
	}
}

// TestTransport_Close_EscalatesToSIGKILL proves the escalation branch: a
// subprocess that ignores SIGTERM is SIGKILLed after the grace period
// expires. Uses a tiny grace so the test is fast and deterministic.
func TestTransport_Close_EscalatesToSIGKILL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	bashBin, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash on PATH: %v", err)
	}

	// Tiny grace so escalation fires fast and the test stays snappy.
	const grace = 100 * time.Millisecond
	t.Setenv("SPRAWL_TERM_GRACE", grace.String())

	starter := &realStarter{}
	tr, err := starter.Start(ExecSpec{
		Path: bashBin,
		// trap "" TERM tells bash ignore SIGTERM (SIG_IGN). The script then
		// loops sleep forever so SIGKILL is the only way to tear it down.
		// `wait` is avoided because bash's wait builtin returns on any
		// trapped signal — even a SIG_IGN'd one — which would make the
		// child a fake-ignorer that exits on SIGTERM anyway.
		Args:   []string{"-c", `trap "" TERM; while :; do sleep 1; done`},
		Dir:    t.TempDir(),
		Env:    os.Environ(),
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("starter.Start: %v", err)
	}
	pid := tr.Pid()

	// Let bash install the trap.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	_ = tr.Close()
	closeElapsed := time.Since(start)

	// alive check happens BEFORE Wait so we know whether SIGKILL landed.
	// (Wait may take a moment due to stdout-pipe inheritance by sleep 30 child.)
	time.Sleep(50 * time.Millisecond)
	if alivePID(t, pid) {
		t.Errorf("process %d still alive 50ms after SIGKILL escalation", pid)
	}

	waitDone := make(chan struct{})
	go func() { _ = tr.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Logf("Wait() did not return within 5s — cmd.Wait likely blocked on stdout pipe held by orphaned sleep child; escalation itself succeeded (process dead).")
	}

	if closeElapsed < grace {
		t.Errorf("Close() returned in %v, want >= %v (grace before escalation)", closeElapsed, grace)
	}
	if closeElapsed > 2*time.Second {
		t.Errorf("Close() took %v, want < 2s (escalation should be prompt after grace)", closeElapsed)
	}
}

// TestTransport_Close_AlreadyExitedFastPath: a subprocess that exited
// before Close() runs should be a clean no-op — no signal sent, no grace
// timer slept, Close returns immediately.
func TestTransport_Close_AlreadyExitedFastPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal test")
	}
	bashBin, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("no bash on PATH: %v", err)
	}

	// Long grace would expose any incorrect fall-through-to-timer.
	t.Setenv("SPRAWL_TERM_GRACE", "5s")

	starter := &realStarter{}
	tr, err := starter.Start(ExecSpec{
		Path: bashBin,
		Args: []string{"-c", "true"},
		Dir:  t.TempDir(),
		Env:  os.Environ(),
	})
	if err != nil {
		t.Fatalf("starter.Start: %v", err)
	}

	// Give bash time to exit cleanly. The cmd.Wait reaper goroutine then
	// closes the `exited` chan, so terminateProcess takes the fast path.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && alivePID(t, tr.Pid()) {
		time.Sleep(10 * time.Millisecond)
	}

	start := time.Now()
	_ = tr.Close()
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Errorf("Close() of already-exited subprocess took %v, want fast — terminate did not honor the already-exited fast path", elapsed)
	}
	_ = tr.Wait()
}

// TestTransport_Close_NilTerminateDegradesGracefully: transports constructed
// without a terminate closure (existing test seams that build &transport{...}
// directly) must keep working — Close just closes the writer.
func TestTransport_Close_NilTerminateDegradesGracefully(t *testing.T) {
	tr := &transport{writer: protocol.NewWriter(&bytes.Buffer{})}
	if err := tr.Close(); err != nil {
		t.Errorf("Close on terminate-less transport returned %v, want nil", err)
	}
}
