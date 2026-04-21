package claude

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// NoConversationMarker is the error string claude prints when `--resume <id>`
// is invoked against a session the server cannot find. The process typically
// stays alive after printing it, awaiting user input — so we cannot rely on
// the subprocess-exits-within-5s heuristic to detect the failure.
//
// Watch stdout/stderr for this marker; on match, kill the subprocess and let
// the rootloop / TUI fall back to a fresh session.
const NoConversationMarker = "No conversation found with session ID:"

// ResumeMarkerScanCap bounds how many bytes of each stream we inspect for the
// marker before giving up. Large enough to cover any reasonable startup
// banner + error, small enough that false positives deep in a long session
// cannot trip it.
const ResumeMarkerScanCap = 64 * 1024

// ErrResumeFailed wraps runCommand errors when the marker scanner tripped.
// Callers use errors.Is to short-circuit the elapsed-time heuristic and
// retry with a fresh session regardless of how long the subprocess lived.
var ErrResumeFailed = errors.New("claude resume failed: no conversation found")

// ResumeWatchPipeDrainDelay bounds how long cmd.Wait will block on pipe I/O
// after the subprocess exits. Used as the default for cmd.WaitDelay in
// RunWithResumeWatch. See that function's doc for rationale.
const ResumeWatchPipeDrainDelay = 2 * time.Second

// NewMarkerWriter returns an io.Writer that forwards every write to underlying
// while scanning the first maxBytes of output for marker. On match, onMatch is
// invoked exactly once. Scanning self-disables after maxBytes to avoid false
// positives mid-session.
//
// The writer is safe to use from the single goroutine that owns the cmd's
// stderr/stdout pipe; concurrent writers are not expected and not protected
// against beyond onMatch's once-only guarantee.
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

// RunWithResumeWatch runs cmd, teeing its stderr through a marker scanner
// that watches for NoConversationMarker. If the marker fires, cmd.Process is
// killed and the returned error wraps ErrResumeFailed (joined with cmd.Wait's
// error for debugging).
//
// Callers may pre-set cmd.Stdout / cmd.Stderr to route passthrough to a
// specific writer; nil defaults to os.Stdout / os.Stderr. cmd.Stdin is left
// untouched.
//
// If cmd.WaitDelay is zero, it is set to ResumeWatchPipeDrainDelay so that
// orphaned grandchildren that inherited the stderr pipe FD (e.g. an MCP
// server spawned by claude, or `sleep` forked by a shell-based fake) cannot
// block cmd.Wait's pipe-copy goroutine indefinitely after the main process
// exits.
//
// ─────────────────────────────────────────────────────────────────────────
// HAZARD: subprocess stdio TTY-vs-pipe — DO NOT WRAP cmd.Stdout
// ─────────────────────────────────────────────────────────────────────────
//
// Only stderr is wrapped by the marker scanner. cmd.Stdout MUST be left as
// the caller's *os.File (normally os.Stdout inherited from the tmux pane).
//
// Why: os/exec assigns cmd.Stdout directly to the child's fd 1 ONLY when the
// value is an *os.File. Any other io.Writer (e.g. our markerWriter, a
// bytes.Buffer, a tee) forces os/exec to allocate an anonymous pipe, copy
// from the pipe into the writer in a goroutine, and hand the pipe's write
// end to the child. The child now sees fd 1 as a pipe, not a TTY.
//
// Claude Code ≥2.1 does an isatty() check on stdout at startup and, when fd
// 1 is not a TTY, silently auto-switches into `--print` (non-interactive)
// mode, which requires a prompt on argv or stdin and exits immediately on
// stdin EOF. This bricks `sprawl init` and any other interactive spawn.
//
// Wrapping stderr is safe: Claude only TTY-checks stdout. The
// "No conversation found with session ID:" marker is emitted on stderr
// anyway, so stdout scanning would be pointless as well as broken.
//
// Rule of thumb for this repo: do NOT assign a non-*os.File io.Writer to
// cmd.Stdout of a Claude / TUI / interactive subprocess unless you have
// verified the child tolerates a pipe on fd 1. If you need to intercept
// stdout, use a PTY (e.g. github.com/creack/pty) rather than an io.Writer
// wrapper.
//
// Background: QUM-261 (the sprawl init regression) and QUM-308 (this
// documentation pass). Fix commit: 7c801f5. See also the "Subprocess stdio:
// TTY vs pipe" section in the /go-cli-best-practices skill.
// ─────────────────────────────────────────────────────────────────────────
func RunWithResumeWatch(cmd *exec.Cmd) error {
	// See the HAZARD block above before touching cmd.Stdout: assigning a
	// non-*os.File here silently flips Claude into --print mode.
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = ResumeWatchPipeDrainDelay
	}

	var tripped sync.Once
	var markerHit bool
	kill := func() {
		tripped.Do(func() {
			markerHit = true
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
	}

	cmd.Stderr = NewMarkerWriter(cmd.Stderr, NoConversationMarker, ResumeMarkerScanCap, kill)

	runErr := cmd.Run()
	if markerHit {
		if runErr != nil {
			return errors.Join(ErrResumeFailed, runErr)
		}
		return ErrResumeFailed
	}
	return runErr
}
