// Package claude: wirelog.go implements a best-effort NDJSON wire-capture
// writer (QUM-632). As of the wire-log-becomes-authoritative epic the writer is
// FRAME-ORIENTED: it buffers per-direction bytes at the tap, splits on '\n',
// and emits ONE envelope per newline-delimited Claude protocol frame (not one
// per io.TeeReader read chunk). Each envelope carries a single monotonic `seq`
// counter that is continuous across resumes — the file is opened O_APPEND under
// a stable sessionID, so on open the last seq is recovered from the existing
// file and numbering continues from there. seq starts at 1 for a fresh (or
// legacy seq-less) file; a legacy seq-less line decodes as seq 0.
//
// The writer is tee-safe: its Write always reports a full byte count and never
// returns an error, so wrapping a live transport in io.TeeReader /
// io.MultiWriter can never corrupt or stall the real protocol stream — capture
// failures degrade silently. Buffered bytes are never dropped: a trailing
// partial frame (no terminating '\n') is flushed as a final record on Close.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// wireLog appends NDJSON capture lines to a single session log file.
type wireLog struct {
	mu        sync.Mutex
	f         *os.File
	now       func() time.Time
	seq       int64             // single monotonic counter over the whole file
	bufs      map[string][]byte // per-direction partial-frame buffer
	closeOnce sync.Once
	closeErr  error
}

// wireLogLineEnvelope is the on-disk JSON shape: one object per line. Seq is
// always emitted (>= 1) by this writer; legacy files omit it (decodes as 0).
type wireLogLineEnvelope struct {
	TS  string `json:"ts"`
	Dir string `json:"dir"`
	Seq int64  `json:"seq"`
	Raw string `json:"raw"`
}

// newWireLog opens (creating parent dirs) the capture file in append mode and
// recovers the last seq from any pre-existing content so numbering continues
// monotonically across resumes.
func newWireLog(path string) (*wireLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating wire-log dir: %w", err)
	}
	last := recoverLastSeq(path)
	needFence := tornTail(path)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening wire-log: %w", err)
	}
	// Fence a crash-torn tail: if the existing file ends mid-envelope (no
	// trailing '\n' — only a hard crash/SIGKILL leaves this, since emitLocked
	// always terminates its line), append a newline so the incomplete remnant
	// sits on its own line. Otherwise the next appended envelope would splice
	// onto the unterminated remnant, making every post-crash frame unreachable
	// to a line-delimited reader. Best-effort (tee-safety): ignore errors.
	if needFence {
		_, _ = f.Write([]byte("\n"))
	}
	return &wireLog{
		f:    f,
		now:  time.Now,
		seq:  last,
		bufs: make(map[string][]byte),
	}, nil
}

// recoverLastSeq scans the existing file (if any) line by line and returns the
// maximum seq across all parseable envelopes, or 0 when the file is absent,
// empty, or entirely legacy (seq-less). Each envelope this writer emits is
// exactly one physical '\n'-terminated line (json.Marshal escapes any newline
// inside raw), so line-delimited scanning is exact. A torn/garbage line (e.g. a
// crash remnant) is skipped rather than aborting the scan — critical so that a
// second resume still sees seqs written after an earlier crash and never
// re-issues a duplicate.
func recoverLastSeq(path string) int64 {
	rf, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer rf.Close()

	br := bufio.NewReader(rf)
	var last int64
	for {
		line, rerr := br.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var env struct {
				Seq *int64 `json:"seq"`
			}
			if json.Unmarshal(trimmed, &env) == nil && env.Seq != nil && *env.Seq > last {
				last = *env.Seq
			}
		}
		if rerr != nil {
			break
		}
	}
	return last
}

// tornTail reports whether path exists, is non-empty, and does NOT end in a
// newline — i.e. a hard crash left an unterminated envelope at the tail.
func tornTail(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 {
		return false
	}
	rf, err := os.Open(path)
	if err != nil {
		return false
	}
	defer rf.Close()
	buf := make([]byte, 1)
	if _, err := rf.ReadAt(buf, fi.Size()-1); err != nil {
		return false
	}
	return buf[0] != '\n'
}

// dirWriter returns an io.Writer that tags every payload with dir.
func (w *wireLog) dirWriter(dir string) io.Writer {
	return &wireLogDirWriter{wl: w, dir: dir}
}

// record buffers p for its direction, emits one envelope per complete
// newline-delimited frame (delimiter retained in the record), and keeps any
// trailing partial for the next write. All errors are swallowed (tee-safety).
//
// The per-direction buffer grows until a '\n' arrives. Real Claude frames are
// newline-terminated, so it holds at most one in-flight frame; there is no
// hard cap here (the tee-safety contract forbids erroring), unlike
// protocol.Reader's DefaultMaxLineSize ceiling on the parse side.
func (w *wireLog) record(dir string, p []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return
	}
	w.bufs[dir] = append(w.bufs[dir], p...)
	data := w.bufs[dir]
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		w.emitLocked(dir, data[:i+1]) // include the '\n' delimiter
		data = data[i+1:]
	}
	// Retain the trailing partial (copied so it is not aliased to p's array).
	w.bufs[dir] = append([]byte(nil), data...)
}

// emitLocked marshals one envelope for raw and appends it. Caller holds mu.
func (w *wireLog) emitLocked(dir string, raw []byte) {
	w.seq++
	env := wireLogLineEnvelope{
		TS:  w.now().Format(time.RFC3339Nano),
		Dir: dir,
		Seq: w.seq,
		Raw: string(raw),
	}
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	b = append(b, '\n')
	if _, werr := w.f.Write(b); werr != nil {
		fmt.Fprintf(os.Stderr, "sprawl: wire-log write failed: %v\n", werr)
	}
}

// Close flushes any buffered trailing partials (never dropping bytes) and then
// closes the underlying file. Idempotent.
func (w *wireLog) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.f == nil {
			return
		}
		dirs := make([]string, 0, len(w.bufs))
		for dir := range w.bufs {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs) // deterministic flush order
		for _, dir := range dirs {
			if len(w.bufs[dir]) > 0 {
				w.emitLocked(dir, w.bufs[dir])
				w.bufs[dir] = nil
			}
		}
		w.closeErr = w.f.Close()
		w.f = nil
	})
	return w.closeErr
}

// wireLogDirWriter tees one direction of the transport into the wireLog.
type wireLogDirWriter struct {
	wl  *wireLog
	dir string
}

// Write records p and always reports a full, error-free write (tee-safety).
func (d *wireLogDirWriter) Write(p []byte) (int, error) {
	d.wl.record(d.dir, p)
	return len(p), nil
}
