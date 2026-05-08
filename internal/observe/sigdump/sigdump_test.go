package sigdump_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/observe/sigdump"
)

// tsFormat must match the format used by sigdump.Dump for filename
// timestamps. Picked for nanosecond uniqueness + filesystem safety.
const tsFormat = "20060102T150405.000000000Z"

// fakeFDSource is a deterministic FDSource for unit tests.
type fakeFDSource struct {
	entries []sigdump.FDEntry
	err     error
}

func (f *fakeFDSource) Snapshot() ([]sigdump.FDEntry, error) {
	return f.entries, f.err
}

func TestDump_WritesGoroutinesFile(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 8, 12, 34, 56, 123456789, time.UTC)
	src := &fakeFDSource{entries: []sigdump.FDEntry{{FD: 0, Target: "/dev/null"}}}

	gPath, _, err := sigdump.Dump(dir, now, src)
	if err != nil {
		t.Fatalf("Dump() error: %v", err)
	}
	if gPath == "" {
		t.Fatal("Dump returned empty goroutines path")
	}
	data, err := os.ReadFile(gPath)
	if err != nil {
		t.Fatalf("reading goroutines file: %v", err)
	}
	if !strings.Contains(string(data), "goroutine ") {
		t.Errorf("goroutines file missing 'goroutine ' marker; got: %q", string(data))
	}
}

func TestDump_WritesFDsFileFromMockSource(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	src := &fakeFDSource{entries: []sigdump.FDEntry{
		{FD: 0, Target: "/dev/null"},
		{FD: 1, Target: "pipe:[123]"},
	}}

	_, fdsPath, err := sigdump.Dump(dir, now, src)
	if err != nil {
		t.Fatalf("Dump() error: %v", err)
	}
	if fdsPath == "" {
		t.Fatal("Dump returned empty fds path")
	}
	data, err := os.ReadFile(fdsPath)
	if err != nil {
		t.Fatalf("reading fds file: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "0\t/dev/null\n") {
		t.Errorf("fds file missing fd 0 entry; got: %q", got)
	}
	if !strings.Contains(got, "1\tpipe:[123]\n") {
		t.Errorf("fds file missing fd 1 entry; got: %q", got)
	}
}

func TestDump_ReturnsTimestampedPaths(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 8, 12, 34, 56, 123456789, time.UTC)
	src := &fakeFDSource{}

	gPath, fdsPath, err := sigdump.Dump(dir, now, src)
	if err != nil {
		t.Fatalf("Dump() error: %v", err)
	}
	ts := now.UTC().Format(tsFormat)
	wantG := "goroutines-" + ts + ".txt"
	wantF := "fds-" + ts + ".txt"
	if filepath.Base(gPath) != wantG {
		t.Errorf("goroutines filename = %q, want %q", filepath.Base(gPath), wantG)
	}
	if filepath.Base(fdsPath) != wantF {
		t.Errorf("fds filename = %q, want %q", filepath.Base(fdsPath), wantF)
	}
}

func TestDump_CreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	now := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	src := &fakeFDSource{entries: []sigdump.FDEntry{{FD: 2, Target: "/dev/tty"}}}

	gPath, fdsPath, err := sigdump.Dump(dir, now, src)
	if err != nil {
		t.Fatalf("Dump() error: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected dir to be created: %v", err)
	}
	if _, err := os.Stat(gPath); err != nil {
		t.Errorf("goroutines file not written: %v", err)
	}
	if _, err := os.Stat(fdsPath); err != nil {
		t.Errorf("fds file not written: %v", err)
	}
}

// TestDump_FDSourceErrorStillWritesGoroutines documents the contract:
// when FDSource.Snapshot returns an error, Dump still writes the goroutine
// dump and returns its path (non-empty), but returns an empty fdsPath and
// a non-nil err describing the FD failure.
func TestDump_FDSourceErrorStillWritesGoroutines(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 8, 12, 34, 56, 0, time.UTC)
	src := &fakeFDSource{err: errors.New("boom")}

	gPath, fdsPath, err := sigdump.Dump(dir, now, src)
	if err == nil {
		t.Fatal("expected error from Dump when FDSource fails")
	}
	if gPath == "" {
		t.Fatal("expected non-empty goroutines path even when FDSource fails")
	}
	if _, statErr := os.Stat(gPath); statErr != nil {
		t.Errorf("goroutines file should still be written on FD failure: %v", statErr)
	}
	data, readErr := os.ReadFile(gPath)
	if readErr != nil {
		t.Fatalf("reading goroutines file: %v", readErr)
	}
	if !strings.Contains(string(data), "goroutine ") {
		t.Errorf("goroutines file missing 'goroutine ' marker; got: %q", string(data))
	}
	if fdsPath != "" {
		t.Errorf("fdsPath = %q, want empty when FDSource fails", fdsPath)
	}
}
