package claude

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
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
