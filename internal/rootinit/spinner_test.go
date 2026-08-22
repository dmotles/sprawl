package rootinit

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/testutil"
)

// spinnerWaitTimeout is the ceiling every wait below shares. It is a TIMEOUT, not a
// wait: the spinner ticks every 150ms (spinner.go), so each of these tests used
// to sleep 500ms — or 2s in the frame-cycling case — and pay it in full on every
// green run. testutil.Eventually exits as soon as the output it is waiting for
// appears, so the ceiling can be generous without costing anything.
const spinnerWaitTimeout = 5 * time.Second

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
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[root-loop]", "testing...")
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render its label", func() bool {
		return strings.Contains(buf.String(), "testing...")
	})
	sp.stop()

	if !strings.Contains(buf.String(), "testing...") {
		t.Errorf("expected 'testing...' label, got %q", buf.String())
	}
}

func TestSpinner_DisplaysElapsedTime(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[root-loop]", "working...")
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render an elapsed time", func() bool {
		out := buf.String()
		return strings.Contains(out, "(0s)") || strings.Contains(out, "(1s)")
	})
	sp.stop()

	out := buf.String()
	if !strings.Contains(out, "(0s)") && !strings.Contains(out, "(1s)") {
		t.Errorf("expected elapsed time, got %q", out)
	}
}

func TestSpinner_StopClearsLine(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[root-loop]", "clearing...")
	// Wait for at least one frame, so the clear-line suffix under test is
	// genuinely clearing something rather than being the only output.
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render a first frame", func() bool {
		return strings.Contains(buf.String(), "clearing...")
	})
	sp.stop()

	out := buf.String()
	if !strings.HasSuffix(out, "\033[2K\r") {
		t.Errorf("expected clear-line suffix, got tail %q", out[max(0, len(out)-20):])
	}
}

func TestSpinner_CyclesThroughFrames(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[root-loop]", "cycling...")
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render >=2 distinct frames", func() bool {
		return spinnerDistinctFrames(buf.String()) >= 2
	})
	sp.stop()

	out := buf.String()
	if distinct := spinnerDistinctFrames(out); distinct < 2 {
		t.Errorf("expected >=2 distinct frames, got %d in %q", distinct, out)
	}
}

// spinnerDistinctFrames counts how many of the spinner's frame runes appear in out.
func spinnerDistinctFrames(out string) int {
	frames := []rune{'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'}
	distinct := 0
	for _, f := range frames {
		if strings.ContainsRune(out, f) {
			distinct++
		}
	}
	return distinct
}

func TestSpinner_IncludesPrefix(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[root-loop]", "prefixed...")
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render its prefix", func() bool {
		return strings.Contains(buf.String(), "[root-loop]")
	})
	sp.stop()
	if !strings.Contains(buf.String(), "[root-loop]") {
		t.Errorf("expected [root-loop] prefix, got %q", buf.String())
	}
}

func TestSpinner_UsesCustomPrefix(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[enter]", "prefixed...")
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render its custom prefix", func() bool {
		return strings.Contains(buf.String(), "[enter]")
	})
	sp.stop()
	out := buf.String()
	if !strings.Contains(out, "[enter]") {
		t.Errorf("expected [enter] prefix, got %q", out)
	}
	if strings.Contains(out, "[root-loop]") {
		t.Errorf("unexpected [root-loop] prefix in TUI-mode spinner output: %q", out)
	}
}

func TestSpinner_EmptyPrefix(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "", "naked...")
	testutil.Eventually(t, spinnerWaitTimeout, "spinner to render its label", func() bool {
		return strings.Contains(buf.String(), "naked...")
	})
	sp.stop()
	out := buf.String()
	if strings.Contains(out, "[root-loop]") || strings.Contains(out, "[enter]") {
		t.Errorf("expected no bracketed mode prefix, got %q", out)
	}
	if !strings.Contains(out, "naked...") {
		t.Errorf("expected label to render without prefix, got %q", out)
	}
}

func TestSpinner_ImmediateStop(t *testing.T) {
	t.Parallel()

	var buf syncBuffer
	sp := startSpinner(&buf, "[root-loop]", "quick...")
	sp.stop()
	if !strings.HasSuffix(buf.String(), "\033[2K\r") {
		t.Errorf("expected clear-line after immediate stop, got %q", buf.String())
	}
}
