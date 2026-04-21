package claude

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMarkerWriter_PassthroughWithoutMatch(t *testing.T) {
	var sink bytes.Buffer
	var hits int32
	w := NewMarkerWriter(&sink, NoConversationMarker, 1<<20, func() {
		atomic.AddInt32(&hits, 1)
	})

	payload := []byte("system prompt ok\nclaude: ready\n")
	n, err := w.Write(payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write returned n=%d, want %d", n, len(payload))
	}
	if got := sink.String(); got != string(payload) {
		t.Errorf("passthrough mismatch: got %q, want %q", got, string(payload))
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("onMatch should not fire when marker absent; hits=%d", hits)
	}
}

func TestMarkerWriter_DetectsMarker_InvokesCallback(t *testing.T) {
	var sink bytes.Buffer
	var hits int32
	w := NewMarkerWriter(&sink, NoConversationMarker, 1<<20, func() {
		atomic.AddInt32(&hits, 1)
	})

	line := "No conversation found with session ID: abc-123\n"
	if _, err := io.WriteString(w, line); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sink.String(), line) {
		t.Errorf("marker text should still be forwarded to underlying writer; got %q", sink.String())
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("onMatch should fire exactly once; hits=%d", hits)
	}
}

func TestMarkerWriter_DetectsMarker_AcrossMultipleWrites(t *testing.T) {
	var sink bytes.Buffer
	var hits int32
	w := NewMarkerWriter(&sink, NoConversationMarker, 1<<20, func() {
		atomic.AddInt32(&hits, 1)
	})

	// Split marker across two Write calls to exercise the carry buffer.
	for _, chunk := range []string{"No conversation fo", "und with session ID: xyz\n"} {
		if _, err := io.WriteString(w, chunk); err != nil {
			t.Fatalf("unexpected error writing chunk %q: %v", chunk, err)
		}
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("onMatch should fire once when marker straddles writes; hits=%d", hits)
	}
}

func TestMarkerWriter_CallbackFiresOnlyOnce(t *testing.T) {
	var sink bytes.Buffer
	var hits int32
	w := NewMarkerWriter(&sink, NoConversationMarker, 1<<20, func() {
		atomic.AddInt32(&hits, 1)
	})

	for i := 0; i < 3; i++ {
		if _, err := io.WriteString(w, "No conversation found with session ID: repeat\n"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("onMatch must be invoked at most once across repeated marker hits; hits=%d", got)
	}
}

func TestMarkerWriter_StopsScanningAfterMaxBytes(t *testing.T) {
	var sink bytes.Buffer
	var hits int32
	// Tiny cap — scanner should disengage before the marker arrives.
	w := NewMarkerWriter(&sink, NoConversationMarker, 16, func() {
		atomic.AddInt32(&hits, 1)
	})

	// Fill past the cap with unrelated text, then emit the marker.
	if _, err := io.WriteString(w, strings.Repeat("x", 64)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := io.WriteString(w, "No conversation found with session ID: late\n"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("onMatch should not fire once maxBytes is exceeded; hits=%d", hits)
	}
	// Passthrough must still work past the cap.
	if !strings.Contains(sink.String(), "No conversation found") {
		t.Errorf("underlying writer must still receive data after cap; got %q", sink.String())
	}
}

type errWriter struct{ err error }

func (w errWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestMarkerWriter_PropagatesUnderlyingWriteError(t *testing.T) {
	want := errors.New("boom")
	w := NewMarkerWriter(errWriter{err: want}, NoConversationMarker, 1<<20, func() {})
	if _, err := w.Write([]byte("hi")); !errors.Is(err, want) {
		t.Errorf("expected underlying write error to propagate; got %v", err)
	}
}

// TestRunWithResumeWatch_MarkerInStderrKillsProcess is the integration-level
// acceptance test for QUM-261: a fake claude that emits the "No conversation
// found" marker to stderr and then hangs must be killed by RunWithResumeWatch
// within the resume-failure window, and the returned error must wrap
// ErrResumeFailed.
func TestRunWithResumeWatch_MarkerInStderrKillsProcess(t *testing.T) {
	// /bin/sh echoes the marker to stderr and then sleeps long enough that,
	// without the scanner-triggered kill, the test would time out.
	cmd := exec.Command("/bin/sh", "-c",
		`printf 'No conversation found with session ID: deadbeef\n' 1>&2; sleep 30`)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- RunWithResumeWatch(cmd) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrResumeFailed) {
			t.Errorf("expected error to wrap ErrResumeFailed, got %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("fallback took %v, want < 5s", d)
		}
		if !strings.Contains(stderr.String(), "No conversation found") {
			t.Errorf("underlying stderr writer did not see marker: %q", stderr.String())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("RunWithResumeWatch never returned; marker scanner did not kill the hung subprocess")
	}
}

// TestRunWithResumeWatch_MarkerInStdoutKillsProcess covers the stdout
// emission path — the issue's fix direction calls for watching both streams.
func TestRunWithResumeWatch_MarkerInStdoutKillsProcess(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c",
		`printf 'No conversation found with session ID: dead\n'; sleep 30`)

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- RunWithResumeWatch(cmd) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrResumeFailed) {
			t.Errorf("expected error to wrap ErrResumeFailed, got %v", err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("fallback took %v, want < 5s", d)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("RunWithResumeWatch never returned on stdout marker")
	}
}

// TestRunWithResumeWatch_NormalExit_ReturnsUnderlyingError verifies the
// scanner is transparent when no marker appears: a shell that exits cleanly
// returns a nil error; a shell that exits non-zero returns the usual
// *exec.ExitError, not ErrResumeFailed.
func TestRunWithResumeWatch_NormalExit_ReturnsUnderlyingError(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "echo hi; exit 0")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := RunWithResumeWatch(cmd); err != nil {
		t.Errorf("expected nil on clean exit, got %v", err)
	}

	cmd = exec.Command("/bin/sh", "-c", "exit 7")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := RunWithResumeWatch(cmd)
	if errors.Is(err, ErrResumeFailed) {
		t.Errorf("no marker emitted; err must not wrap ErrResumeFailed, got %v", err)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Errorf("expected *exec.ExitError for non-zero exit, got %T: %v", err, err)
	}
}
