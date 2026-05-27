// Package claude: wirelog.go implements a best-effort NDJSON wire-capture
// writer (QUM-632). Each line records one stdin/stdout payload verbatim,
// tagged with a timestamp and direction. The writer is tee-safe: its Write
// always reports a full byte count and never returns an error, so wrapping a
// live transport in io.TeeReader / io.MultiWriter can never corrupt or stall
// the real protocol stream — capture failures degrade silently.
package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// wireLog appends NDJSON capture lines to a single session log file.
type wireLog struct {
	mu        sync.Mutex
	f         *os.File
	now       func() time.Time
	closeOnce sync.Once
	closeErr  error
}

// wireLogLineEnvelope is the on-disk JSON shape: one object per line.
type wireLogLineEnvelope struct {
	TS  string `json:"ts"`
	Dir string `json:"dir"`
	Raw string `json:"raw"`
}

// newWireLog opens (creating parent dirs) the capture file in append mode.
func newWireLog(path string) (*wireLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("creating wire-log dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening wire-log: %w", err)
	}
	return &wireLog{f: f, now: time.Now}, nil
}

// dirWriter returns an io.Writer that tags every payload with dir.
func (w *wireLog) dirWriter(dir string) io.Writer {
	return &wireLogDirWriter{wl: w, dir: dir}
}

// record marshals one envelope and appends it under the mutex. All errors are
// swallowed (tee-safety contract).
func (w *wireLog) record(dir string, p []byte) {
	env := wireLogLineEnvelope{
		TS:  w.now().Format(time.RFC3339Nano),
		Dir: dir,
		Raw: string(p),
	}
	b, err := json.Marshal(env)
	if err != nil {
		return
	}
	b = append(b, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return
	}
	if _, werr := w.f.Write(b); werr != nil {
		fmt.Fprintf(os.Stderr, "sprawl: wire-log write failed: %v\n", werr)
	}
}

// Close closes the underlying file. Idempotent.
func (w *wireLog) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.f != nil {
			w.closeErr = w.f.Close()
		}
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
