package claude

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestRealStarter_PidExposesSubprocessPID pins QUM-606 R5: the
// ManagedTransport returned by realStarter must expose the underlying
// claude subprocess's OS PID via Pid(), so the live-recover e2e harness
// (and unit tests) can assert subprocess lifetime without scraping `ps`.
//
// We spawn a long-lived placeholder binary (`sleep`) instead of `claude`
// because the production claude binary may not be on PATH in CI; the
// subject under test is realStarter's wiring of cmd.Process.Pid into
// transport.pid, which is binary-agnostic. The QUM-606 ctx-cancel kill
// path is now type-impossible after QUM-612 — Starter.Start no longer
// accepts a ctx parameter at all, so no caller can forward a request-scoped
// ctx into exec.CommandContext.
func TestRealStarter_PidExposesSubprocessPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-only test (sleep binary, signal-0 probe)")
	}
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep on PATH: %v", err)
	}

	starter := &realStarter{}

	transport, err := starter.Start(ExecSpec{
		Path: sleepBin,
		Args: []string{"sleep", "5"},
		Dir:  t.TempDir(),
		Env:  os.Environ(),
	})
	if err != nil {
		t.Fatalf("starter.Start: %v", err)
	}
	pid := transport.Pid()
	if pid <= 0 {
		t.Fatalf("Pid() = %d, want > 0", pid)
	}

	// Sanity probe: the subprocess must be findable via its PID.
	if _, err := os.FindProcess(pid); err != nil {
		t.Fatalf("FindProcess(%d): %v", pid, err)
	}

	// Cleanup: kill + wait, with a bounded deadline so a stuck subprocess
	// can't hang the test.
	_ = transport.Kill()
	waitDone := make(chan error, 1)
	go func() { waitDone <- transport.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return within 3s after Kill()")
	}
	_ = transport.Close()
}
