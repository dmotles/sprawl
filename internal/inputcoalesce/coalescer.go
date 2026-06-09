// Package inputcoalesce implements a byte-level stdin coalescer that
// bypasses tmux's bracketed-paste gating. It reads raw bytes from an
// underlying io.Reader (typically os.Stdin), opens a short "burst window"
// after the first byte, drains any bytes that arrive within the window,
// and — when the accumulated buffer looks like a paste (large + no ESC
// bytes) — wraps the buffer in synthetic bracketed-paste markers
// (ESC[200~ ... ESC[201~). Ultraviolet's decoder (inside Bubble Tea v2)
// sees those markers and emits a single tea.PasteMsg, regardless of
// whether the host terminal/tmux ever set DECSET 2004.
//
// Background: QUM-608. Validated prototype in commit 2a5e51f.
package inputcoalesce

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/muesli/cancelreader"
)

const (
	// BurstCapBytes is the per-Read sanity cap.
	BurstCapBytes = 1 << 20 // 1 MiB

	// SmallByteCap is the size at or below which a burst is never wrapped
	// (those are real keystrokes, not pastes).
	SmallByteCap = 3

	// ChunkSize is the readLoop's per-syscall buffer size.
	ChunkSize = 4096

	// DefaultWindow is the recommended burst window for sprawl enter.
	DefaultWindow = 5 * time.Millisecond
)

var (
	bracketedPasteStart = []byte("\x1b[200~")
	bracketedPasteEnd   = []byte("\x1b[201~")
)

// LogFunc receives a single human-readable diagnostic record per Read.
// Set to nil to disable logging. The notes string contains structured
// key=value fields (bytes_in, bytes_out, wrapped, window_ms, duration_ns).
type LogFunc func(notes string)

// Coalescer wraps an io.Reader (typically os.Stdin) and coalesces tight
// bursts of bytes into a single Read return, optionally wrapping with
// bracketed-paste markers so a downstream terminal decoder emits one
// PasteEvent.
type Coalescer struct {
	src    cancelreader.CancelReader
	srcFd  uintptr // Fd of the original *os.File source, or ^0 if N/A.
	window time.Duration
	log    LogFunc

	chunks  chan []byte
	readErr chan error

	closeOnce sync.Once
	closed    chan struct{}

	// State shared only with Read (single-caller assumption; Bubble Tea
	// calls Read serially from a single goroutine).
	pending    []byte
	eofPending bool
	storedErr  error
}

// New constructs a Coalescer and starts the background read loop. The
// returned *Coalescer is an io.ReadCloser. window controls how long
// Read will wait after the first byte for additional bytes to arrive.
// Pass nil for log to disable diagnostic logging.
//
// The source reader is wrapped in a cancelreader so that Close() can
// interrupt an in-flight blocking Read (epoll on Linux for *os.File-
// backed readers such as os.Stdin; fallback no-op for non-file readers
// — those only unblock when the underlying reader returns naturally).
func New(src io.Reader, window time.Duration, log LogFunc) *Coalescer {
	cr, err := cancelreader.NewReader(src)
	if err != nil {
		cr = &noopCancelReader{r: src}
	}
	// Capture the underlying *os.File's Fd so we can satisfy the
	// term.File interface and let Bubble Tea v2 put the TTY into raw
	// mode. Without this, bubbletea's initInput type-asserts the input
	// against term.File, sees no match (because the Coalescer hides the
	// underlying *os.File), and skips term.MakeRaw — leaving the kernel
	// line discipline in cooked mode. Bytes injected via tmux send-keys
	// then get echoed to the terminal and stay buffered in the kernel
	// until a newline arrives, so the coalescer never sees them.
	srcFd := ^uintptr(0)
	if f, ok := src.(*os.File); ok {
		srcFd = f.Fd()
	}
	c := &Coalescer{
		src:     cr,
		srcFd:   srcFd,
		window:  window,
		log:     log,
		chunks:  make(chan []byte, 16),
		readErr: make(chan error, 1),
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Fd returns the file descriptor of the underlying *os.File source if
// any, otherwise ^uintptr(0). This lets Bubble Tea v2 detect the
// Coalescer as a `term.File` and put the real TTY into raw mode via
// term.MakeRaw.
func (c *Coalescer) Fd() uintptr { return c.srcFd }

// Write satisfies the io.Writer half of `term.File` for Bubble Tea v2's
// `p.input.(term.File)` type assertion. Stdin is read-only, so writes
// always fail with a fixed error. Bubbletea never actually writes to
// its input — it only needs the interface to satisfy the assertion and
// retrieve the underlying Fd.
func (c *Coalescer) Write(_ []byte) (int, error) {
	return 0, errors.New("inputcoalesce: Coalescer is read-only")
}

// noopCancelReader is a last-resort fallback when cancelreader.NewReader
// itself errors. Cancel() flips an internal flag that future Reads
// observe; in-flight reads cannot be interrupted.
type noopCancelReader struct {
	r        io.Reader
	mu       sync.Mutex
	canceled bool
}

func (n *noopCancelReader) Read(p []byte) (int, error) {
	n.mu.Lock()
	c := n.canceled
	n.mu.Unlock()
	if c {
		return 0, cancelreader.ErrCanceled
	}
	return n.r.Read(p)
}

func (n *noopCancelReader) Cancel() bool {
	n.mu.Lock()
	n.canceled = true
	n.mu.Unlock()
	return false
}

func (n *noopCancelReader) Close() error { return nil }

// readLoop reads raw bytes from src in chunks and forwards them into
// c.chunks. On error/EOF (or when cancelled via Close) it sends the
// error to c.readErr (buffered, size 1) and closes c.chunks. It never
// closes c.readErr — the Read consumer drains it once.
func (c *Coalescer) readLoop() {
	buf := make([]byte, ChunkSize)
	for {
		n, err := c.src.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case c.chunks <- chunk:
			case <-c.closed:
				c.readErr <- io.EOF
				close(c.chunks)
				return
			}
		}
		if err != nil {
			if errors.Is(err, cancelreader.ErrCanceled) {
				err = io.EOF
			}
			c.readErr <- err
			close(c.chunks)
			return
		}
	}
}

// Read implements io.Reader. It blocks until at least one byte is
// available, then opens a burst window of c.window and drains any
// additional bytes that arrive during the window. The accumulated
// buffer is wrapped with bracketed-paste markers when it looks like a
// paste (large + ESC-free); the decision is logged when log != nil.
func (c *Coalescer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	// Drain any leftover from a previous Read first.
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		// Never return (n>0, EOF) together — io.Reader contract allows
		// it but ultraviolet's sendBytes (terminal_reader.go) discards
		// bytes when err != nil, which silently invalidates the paste
		// payload. Defer EOF to the next call.
		return n, nil
	}
	if c.eofPending {
		err := c.storedErr
		c.storedErr = nil
		c.eofPending = false
		if err == nil {
			err = io.EOF
		}
		return 0, err
	}

	// Block for the first chunk.
	var first []byte
	select {
	case chunk, ok := <-c.chunks:
		if !ok {
			return 0, c.consumeReadErr()
		}
		first = chunk
	case <-c.closed:
		return 0, io.EOF
	}

	// Open the burst window and drain greedily.
	t0 := time.Now()
	buf := make([]byte, 0, max(len(first)*2, 256))
	buf = append(buf, first...)

	timer := time.NewTimer(c.window)
drainLoop:
	for len(buf) < BurstCapBytes {
		select {
		case chunk, ok := <-c.chunks:
			if !ok {
				c.eofPending = true
				c.storedErr = c.consumeReadErr()
				break drainLoop
			}
			buf = append(buf, chunk...)
			// Reset the burst window on every chunk received within
			// the window — a paste trickling in slower than `window`
			// still coalesces into one PasteMsg instead of fragmenting.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(c.window)
		case <-timer.C:
			break drainLoop
		case <-c.closed:
			c.eofPending = true
			c.storedErr = io.EOF
			break drainLoop
		}
	}
	timer.Stop()
	duration := time.Since(t0)

	// Apply wrap heuristic.
	wrap := shouldWrapBurst(buf)
	var out []byte
	if wrap {
		out = make([]byte, 0, len(buf)+len(bracketedPasteStart)+len(bracketedPasteEnd))
		out = append(out, bracketedPasteStart...)
		out = append(out, buf...)
		out = append(out, bracketedPasteEnd...)
	} else {
		out = buf
	}

	if c.log != nil {
		c.log(fmt.Sprintf(
			"bytes_in=%d bytes_out=%d wrapped=%v window_ms=%d duration_ns=%d",
			len(buf), len(out), wrap, c.window.Milliseconds(), duration.Nanoseconds(),
		))
	}

	n := copy(p, out)
	if n < len(out) {
		c.pending = make([]byte, len(out)-n)
		copy(c.pending, out[n:])
		return n, nil
	}
	// Per io.Reader best practice, never return (n>0, EOF) in the
	// same call: consumers like ultraviolet's sendBytes discard the
	// bytes when err != nil. The next Read picks up c.eofPending →
	// (0, EOF).
	return n, nil
}

// consumeReadErr returns the error reported by readLoop, defaulting to
// io.EOF if the loop closed chunks without an explicit error (shouldn't
// happen, but guard against it).
func (c *Coalescer) consumeReadErr() error {
	select {
	case err := <-c.readErr:
		if err == nil {
			return io.EOF
		}
		return err
	default:
		return io.EOF
	}
}

// shouldWrapBurst returns true if buf looks like a paste body that
// should be wrapped with bracketed-paste markers. Heuristic:
//   - small buffers (<= SmallByteCap) are real keystrokes; pass through.
//   - any 0x1b (ESC) byte signals a real escape sequence (arrow keys,
//     function keys, mouse events, already-wrapped paste, etc.); pass
//     through to let the downstream decoder handle it.
//   - otherwise wrap.
func shouldWrapBurst(buf []byte) bool {
	if len(buf) <= SmallByteCap {
		return false
	}
	if bytes.IndexByte(buf, 0x1b) >= 0 {
		return false
	}
	return true
}

// Close releases the Coalescer. It is safe to call multiple times.
// Close cancels the underlying cancelreader to interrupt an in-flight
// blocking Read so the background goroutine can exit promptly. For
// *os.File-backed readers (os.Stdin) on Linux this uses epoll; for
// non-File readers the fallback cannot interrupt a pending Read, but
// future Reads return cancelreader.ErrCanceled so the loop still exits.
func (c *Coalescer) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.src.Cancel()
	})
	return nil
}
