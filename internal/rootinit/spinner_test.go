package rootinit

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a thread-safe buffer for capturing spinner output in tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSpinner_StartsAndStops(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "testing...")
	time.Sleep(500 * time.Millisecond)
	sp.stop()

	if !strings.Contains(buf.String(), "testing...") {
		t.Errorf("expected 'testing...' label, got %q", buf.String())
	}
}

func TestSpinner_DisplaysElapsedTime(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "working...")
	time.Sleep(500 * time.Millisecond)
	sp.stop()

	out := buf.String()
	if !strings.Contains(out, "(0s)") && !strings.Contains(out, "(1s)") {
		t.Errorf("expected elapsed time, got %q", out)
	}
}

func TestSpinner_StopClearsLine(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "clearing...")
	time.Sleep(500 * time.Millisecond)
	sp.stop()

	out := buf.String()
	if !strings.HasSuffix(out, "\033[2K\r") {
		t.Errorf("expected clear-line suffix, got tail %q", out[max(0, len(out)-20):])
	}
}

func TestSpinner_CyclesThroughFrames(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "cycling...")
	time.Sleep(2 * time.Second)
	sp.stop()

	out := buf.String()
	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	distinct := 0
	for _, f := range frames {
		if strings.ContainsRune(out, f) {
			distinct++
		}
	}
	if distinct < 2 {
		t.Errorf("expected >=2 distinct frames, got %d in %q", distinct, out)
	}
}

func TestSpinner_IncludesRootLoopPrefix(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "prefixed...")
	time.Sleep(500 * time.Millisecond)
	sp.stop()
	if !strings.Contains(buf.String(), "[root-loop]") {
		t.Errorf("expected [root-loop] prefix, got %q", buf.String())
	}
}

func TestSpinner_ImmediateStop(t *testing.T) {
	var buf syncBuffer
	sp := startSpinner(&buf, "quick...")
	sp.stop()
	if !strings.HasSuffix(buf.String(), "\033[2K\r") {
		t.Errorf("expected clear-line after immediate stop, got %q", buf.String())
	}
}
