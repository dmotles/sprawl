package merge

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ValidateLog streams validate output to a timestamped log file under
// <sprawlRoot>/.sprawl/logs/ while also forwarding each line to an optional
// wrap sink (e.g. a TUI viewport). Use OpenValidateLog to construct one.
type ValidateLog struct {
	path string
	wrap func(string)

	mu       sync.Mutex
	file     *os.File
	finished bool
}

// OpenValidateLog creates a new validate log file under
// <sprawlRoot>/.sprawl/logs/, auto-creating the parent directory. The filename
// is derived from now().UTC() formatted as validate-YYYYMMDD-HHMMSS.log.
func OpenValidateLog(sprawlRoot string, wrap func(string), now func() time.Time) (*ValidateLog, error) {
	dir := filepath.Join(sprawlRoot, ".sprawl", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: matches existing .sprawl/logs perm convention
		return nil, fmt.Errorf("create logs dir: %w", err)
	}
	name := "validate-" + now().UTC().Format("20060102-150405") + ".log"
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // G302: validate logs intentionally world-readable for post-hoc less/tail
	if err != nil {
		return nil, fmt.Errorf("open validate log: %w", err)
	}
	return &ValidateLog{path: p, wrap: wrap, file: f}, nil
}

// Path returns the absolute path of the log file.
func (v *ValidateLog) Path() string {
	return v.path
}

// Write appends line+"\n" to the log file (best-effort; errors swallowed) and
// forwards line verbatim to the wrap sink if non-nil. Safe to call after
// Finish — the file write becomes a no-op but wrap forwarding still occurs.
func (v *ValidateLog) Write(line string) {
	v.mu.Lock()
	if v.file != nil {
		_, _ = v.file.WriteString(line + "\n")
	}
	v.mu.Unlock()
	if v.wrap != nil {
		v.wrap(line)
	}
}

// Sink returns a closure over Write suitable for passing to streaming APIs
// that accept a func(string) callback.
func (v *ValidateLog) Sink() func(string) {
	return v.Write
}

// Finish appends a trailer line summarizing the exit status and closes the
// log file. Idempotent — subsequent calls are no-ops.
func (v *ValidateLog) Finish(exitErr error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.finished {
		return
	}
	v.finished = true
	if v.file != nil {
		if exitErr == nil {
			_, _ = v.file.WriteString("[exit=0]\n")
		} else {
			_, _ = v.file.WriteString("[error=" + exitErr.Error() + "]\n")
		}
		_ = v.file.Close()
		v.file = nil
	}
}
