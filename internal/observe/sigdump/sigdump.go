// Package sigdump provides on-demand goroutine and file-descriptor
// snapshots for live processes. It is designed to be triggered by a
// signal (SIGUSR1 on unix) so an operator can capture runtime state from
// a wedged process without restarting it.
package sigdump

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// tsFormat is the timestamp layout used in dump filenames. Nanosecond
// precision keeps successive dumps distinct, and the layout sorts
// lexicographically by time.
const tsFormat = "20060102T150405.000000000Z"

// FDEntry describes a single open file descriptor.
type FDEntry struct {
	FD     int
	Target string
}

// FDSource produces a snapshot of currently open file descriptors.
// Implementations should be safe to call from a signal-handler goroutine.
type FDSource interface {
	Snapshot() ([]FDEntry, error)
}

// Dump writes a goroutine stack dump and a file-descriptor snapshot into
// dir. It returns the paths it wrote. If fdSource.Snapshot returns an
// error, the goroutine dump is still written and its path is returned;
// fdsPath will be empty and err will be the snapshot error.
func Dump(dir string, now time.Time, fdSource FDSource) (goroutinesPath, fdsPath string, err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("sigdump: mkdir %s: %w", dir, err)
	}

	ts := now.UTC().Format(tsFormat)
	goroutinesPath = filepath.Join(dir, "goroutines-"+ts+".txt")
	stack := CaptureGoroutines()
	if writeErr := os.WriteFile(goroutinesPath, stack, 0o600); writeErr != nil {
		return "", "", fmt.Errorf("sigdump: write goroutines: %w", writeErr)
	}

	entries, snapErr := fdSource.Snapshot()
	if snapErr != nil {
		return goroutinesPath, "", fmt.Errorf("sigdump: fd snapshot: %w", snapErr)
	}

	fdsPath = filepath.Join(dir, "fds-"+ts+".txt")
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%d\t%s\n", e.FD, e.Target)
	}
	if writeErr := os.WriteFile(fdsPath, []byte(b.String()), 0o600); writeErr != nil {
		return goroutinesPath, "", fmt.Errorf("sigdump: write fds: %w", writeErr)
	}

	return goroutinesPath, fdsPath, nil
}

// CaptureGoroutines invokes runtime.Stack with a growing buffer until the
// full dump fits, capped at 64 MiB to bound memory.
func CaptureGoroutines() []byte {
	const (
		initial = 256 * 1024
		maxSize = 64 * 1024 * 1024
	)
	buf := make([]byte, initial)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		if len(buf) >= maxSize {
			return buf[:n]
		}
		next := len(buf) * 2
		if next > maxSize {
			next = maxSize
		}
		buf = make([]byte, next)
	}
}

// procFDSource reads /proc/self/fd to enumerate file descriptors.
type procFDSource struct{}

// ProcFDSource returns an FDSource backed by /proc/self/fd. On platforms
// without procfs, Snapshot will return an error.
func ProcFDSource() FDSource {
	return procFDSource{}
}

func (procFDSource) Snapshot() ([]FDEntry, error) {
	const dir = "/proc/self/fd"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := make([]FDEntry, 0, len(entries))
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			target = fmt.Sprintf("(readlink error: %v)", err)
		}
		out = append(out, FDEntry{FD: fd, Target: target})
	}
	return out, nil
}
