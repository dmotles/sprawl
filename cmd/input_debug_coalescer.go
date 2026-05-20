// THROWAWAY DIAGNOSTIC: QUM-608 Path 2 prototype. Pairs with
// cmd/input_debug.go. Safe to delete along with the rest of the input-debug
// command (registration is in input_debug.go's init()).
//
// Purpose: a byte-level stdin coalescer that bypasses tmux's bracketed-paste
// gating entirely. It reads raw bytes from an underlying io.Reader (typically
// os.Stdin), opens a short "burst window" after the first byte, drains any
// bytes that arrive within the window, and — when the accumulated buffer looks
// like a paste (large + no ESC bytes) — wraps the buffer in synthetic
// bracketed-paste markers (ESC[200~ ... ESC[201~). Ultraviolet's decoder
// (inside Bubble Tea v2) sees those markers and emits a single tea.PasteMsg,
// regardless of whether the host terminal/tmux ever set DECSET 2004.

package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/muesli/cancelreader"
)

const (
	coalesceBurstCapBytes = 1 << 20 // 1 MiB sanity cap per Read.
	coalesceSmallByteCap  = 3       // <=3 bytes never wrapped.
	coalesceChunkSize     = 4096
)

var (
	bracketedPasteStart = []byte("\x1b[200~")
	bracketedPasteEnd   = []byte("\x1b[201~")
)

// coalescer wraps an io.Reader (typically os.Stdin) and coalesces tight bursts
// of bytes into a single Read return, optionally wrapping with bracketed-paste
// markers so a downstream terminal decoder emits one PasteEvent.
type coalescer struct {
	src    cancelreader.CancelReader
	window time.Duration
	log    *debugLogger

	chunks  chan []byte
	readErr chan error

	closeOnce sync.Once
	closed    chan struct{}
	// done is closed by readLoop when it exits (EOF or cancel). Callers
	// piping stdin can watch this to know when input is exhausted and
	// drive program shutdown.
	done chan struct{}

	// State shared only with Read (single-caller assumption; Bubble Tea calls
	// Read serially from a single goroutine).
	pending    []byte
	eofPending bool
	storedErr  error
}

// newCoalescer constructs a coalescer and starts the background read loop.
// The returned *coalescer is an io.ReadCloser. window controls how long Read
// will wait after the first byte for additional bytes to arrive.
//
// The source reader is wrapped in a cancelreader so that Close() can
// interrupt an in-flight blocking Read (epoll on Linux for *os.File-backed
// readers such as os.Stdin; fallback no-op for non-file readers — those
// only unblock when the underlying reader returns naturally).
func newCoalescer(src io.Reader, window time.Duration, lg *debugLogger) *coalescer {
	cr, err := cancelreader.NewReader(src)
	if err != nil {
		// cancelreader.NewReader can fail (e.g. epoll create on Linux); fall
		// back to the unfancy fallback wrapper which still gives us a
		// CancelReader interface, just without true read-interrupt.
		cr = &noopCancelReader{r: src}
	}
	c := &coalescer{
		src:     cr,
		window:  window,
		log:     lg,
		chunks:  make(chan []byte, 16),
		readErr: make(chan error, 1),
		closed:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// noopCancelReader is a last-resort fallback when cancelreader.NewReader
// itself errors. Cancel() flips an internal flag that future Reads observe;
// in-flight reads cannot be interrupted.
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

// readLoop reads raw bytes from src in chunks and forwards them into c.chunks.
// On error/EOF (or when cancelled via Close) it sends the error to c.readErr
// (buffered, size 1) and closes c.chunks. It never closes c.readErr — the
// Read consumer drains it once.
func (c *coalescer) readLoop() {
	defer close(c.done)
	buf := make([]byte, coalesceChunkSize)
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
			// Map cancelreader.ErrCanceled to io.EOF — to downstream
			// consumers a cancelled stdin is just end-of-input.
			if errors.Is(err, cancelreader.ErrCanceled) {
				err = io.EOF
			}
			c.readErr <- err
			close(c.chunks)
			return
		}
	}
}

// Done returns a channel that is closed when the background read loop
// exits (input reader returned EOF/error, or Close was called). Callers
// driving the coalescer over a pipe can watch this to detect end-of-input.
func (c *coalescer) Done() <-chan struct{} { return c.done }

// Read implements io.Reader. It blocks until at least one byte is available,
// then opens a burst window of c.window and drains any additional bytes that
// arrive during the window. The accumulated buffer is wrapped with
// bracketed-paste markers when it looks like a paste (large + ESC-free); the
// decision is logged for post-mortem analysis.
func (c *coalescer) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	// Drain any leftover from a previous Read first.
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		// Never return (n>0, EOF) together — io.Reader contract allows it but
		// ultraviolet's sendBytes (terminal_reader.go) discards bytes when
		// err != nil, which silently invalidates the paste payload. Defer
		// EOF to the next call.
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
	for len(buf) < coalesceBurstCapBytes {
		select {
		case chunk, ok := <-c.chunks:
			if !ok {
				c.eofPending = true
				c.storedErr = c.consumeReadErr()
				break drainLoop
			}
			buf = append(buf, chunk...)
			// Reset the burst window on every chunk received within the
			// window — a paste trickling in slower than `window` still
			// coalesces into one PasteMsg instead of fragmenting.
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

	c.log.write(debugRecord{
		Kind: "coalesce-read",
		Notes: fmt.Sprintf(
			"bytes_in=%d bytes_out=%d wrapped=%v window_ms=%d duration_ns=%d",
			len(buf), len(out), wrap, c.window.Milliseconds(), duration.Nanoseconds(),
		),
	})

	n := copy(p, out)
	if n < len(out) {
		c.pending = make([]byte, len(out)-n)
		copy(c.pending, out[n:])
		return n, nil
	}
	// Per io.Reader best practice, never return (n>0, EOF) in the same
	// call: consumers like ultraviolet's sendBytes discard the bytes when
	// err != nil. The next Read picks up c.eofPending → (0, EOF).
	return n, nil
}

// consumeReadErr returns the error reported by readLoop, defaulting to io.EOF
// if the loop closed chunks without an explicit error (shouldn't happen, but
// guard against it).
func (c *coalescer) consumeReadErr() error {
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

// shouldWrapBurst returns true if buf looks like a paste body that should be
// wrapped with bracketed-paste markers. Heuristic:
//   - small buffers (<= coalesceSmallByteCap) are real keystrokes; pass through.
//   - any 0x1b (ESC) byte signals a real escape sequence (arrow keys, function
//     keys, mouse events, already-wrapped paste, etc.); pass through to let
//     the downstream decoder handle it.
//   - otherwise wrap.
func shouldWrapBurst(buf []byte) bool {
	if len(buf) <= coalesceSmallByteCap {
		return false
	}
	if bytes.IndexByte(buf, 0x1b) >= 0 {
		return false
	}
	return true
}

// Close releases the coalescer. It is safe to call multiple times. Close
// cancels the underlying cancelreader to interrupt an in-flight blocking
// Read so the background goroutine can exit promptly. For *os.File-backed
// readers (os.Stdin) on Linux this uses epoll; for non-File readers the
// fallback cannot interrupt a pending Read, but future Reads return
// cancelreader.ErrCanceled so the loop still exits.
func (c *coalescer) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.src.Cancel()
	})
	return nil
}
