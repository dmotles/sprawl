package claude

import (
	"bytes"
	"io"
	"sync"
)

// NoConversationMarker is the error string claude prints when `--resume <id>`
// is invoked against a session the server cannot find. The process typically
// stays alive after printing it, awaiting user input — so we cannot rely on
// the subprocess-exits-within-5s heuristic to detect the failure.
//
// Watch stderr for this marker; on match, kill the subprocess and let the TUI
// fall back to a fresh session.
const NoConversationMarker = "No conversation found with session ID:"

// ResumeMarkerScanCap bounds how many bytes of each stream we inspect for the
// marker before giving up. Large enough to cover any reasonable startup
// banner + error, small enough that false positives deep in a long session
// cannot trip it.
const ResumeMarkerScanCap = 64 * 1024

// NewMarkerWriter returns an io.Writer that forwards every write to underlying
// while scanning the first maxBytes of output for marker. On match, onMatch is
// invoked exactly once. Scanning self-disables after maxBytes to avoid false
// positives mid-session.
//
// The writer is safe to use from the single goroutine that owns the cmd's
// stderr/stdout pipe; concurrent writers are not expected and not protected
// against beyond onMatch's once-only guarantee.
//
// ─────────────────────────────────────────────────────────────────────────
// HAZARD: subprocess stdio TTY-vs-pipe — WRAP STDERR, NEVER STDOUT
// ─────────────────────────────────────────────────────────────────────────
//
// Wrap only a subprocess's stderr with this writer. Its stdout MUST stay the
// caller's *os.File.
//
// Why: os/exec assigns cmd.Stdout directly to the child's fd 1 ONLY when the
// value is an *os.File. Any other io.Writer (e.g. this markerWriter) forces
// os/exec to allocate an anonymous pipe and hand its write end to the child,
// which then sees fd 1 as a pipe, not a TTY. Claude Code >=2.1 does an
// isatty() check on stdout at startup and, when fd 1 is not a TTY, silently
// auto-switches into `--print` (non-interactive) mode, which requires a
// prompt on argv or stdin and exits immediately on stdin EOF.
//
// Wrapping stderr is safe: Claude only TTY-checks stdout, and
// NoConversationMarker is emitted on stderr anyway. If you need to intercept
// stdout, use a PTY (e.g. github.com/creack/pty), not an io.Writer wrapper.
//
// Background: QUM-261 and QUM-308. See also the "Subprocess stdio: TTY vs
// pipe" section in the /go-cli-best-practices skill.
// ─────────────────────────────────────────────────────────────────────────
func NewMarkerWriter(underlying io.Writer, marker string, maxBytes int, onMatch func()) io.Writer {
	return &markerWriter{
		underlying: underlying,
		marker:     []byte(marker),
		maxBytes:   maxBytes,
		onMatch:    onMatch,
	}
}

type markerWriter struct {
	underlying io.Writer
	marker     []byte
	maxBytes   int
	onMatch    func()

	// carry holds the tail of prior writes so a marker split across Write
	// calls still matches. Bounded by len(marker)-1.
	carry []byte
	// scanned counts bytes fed into the scanner so we can disengage past
	// maxBytes.
	scanned int
	// fired guards onMatch against being invoked more than once.
	fired sync.Once
	// done becomes true once scanning is disabled (matched, capped, or
	// short-circuited). Subsequent writes pass through without scanning.
	done bool
}

func (w *markerWriter) Write(p []byte) (int, error) {
	// Always forward first so the underlying writer sees every byte.
	n, err := w.underlying.Write(p)
	if err != nil {
		return n, err
	}

	if w.done || w.onMatch == nil || len(w.marker) == 0 {
		return n, nil
	}

	// Combine carry + this write, scan, then update carry.
	buf := make([]byte, 0, len(w.carry)+len(p))
	buf = append(buf, w.carry...)
	buf = append(buf, p...)

	if bytes.Contains(buf, w.marker) {
		w.fired.Do(w.onMatch)
		w.done = true
		w.carry = nil
		return n, nil
	}

	w.scanned += len(p)
	if w.scanned >= w.maxBytes {
		w.done = true
		w.carry = nil
		return n, nil
	}

	// Retain the last (len(marker)-1) bytes so a marker split across the
	// boundary still matches on the next write.
	tail := len(w.marker) - 1
	if tail < 0 {
		tail = 0
	}
	if len(buf) > tail {
		w.carry = append(w.carry[:0], buf[len(buf)-tail:]...)
	} else {
		w.carry = append(w.carry[:0], buf...)
	}
	return n, nil
}
