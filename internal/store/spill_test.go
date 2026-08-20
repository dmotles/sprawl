package store

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newTestSpiller(t *testing.T, now time.Time) (*FileSpiller, string) {
	t.Helper()
	root := t.TempDir()
	return &FileSpiller{Root: root, Now: func() time.Time { return now }}, root
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: test-controlled temp path
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			out = append(out, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func sampleRecord() SpillRecord {
	return SpillRecord{
		EventID:            uuid.New(),
		ProjectID:          uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaID:           SeedID("run_started", 1),
		SchemaName:         "run_started",
		SchemaVersion:      1,
		Payload:            json.RawMessage(`{"agent_name":"finn"}`),
		At:                 time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
		Reason:             "connection refused",
	}
}

func TestFileSpiller_WritesNDJSONUnderTheSpillDir(t *testing.T) {
	day := time.Date(2026, 8, 19, 23, 30, 0, 0, time.UTC)
	s, root := newTestSpiller(t, day)

	if err := s.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write second: %v", err)
	}

	path := filepath.Join(SpillDir(root), "2026-08-19.ndjson")
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("spill file holds %d line(s), want 2 — two spills must append, not overwrite", len(lines))
	}
	var rec SpillRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("a spill line must be parseable JSON, or a replay cannot read it: %v", err)
	}
	if rec.Version != SpillRecordVersion {
		t.Errorf("spill record version = %d, want %d — a replayer that cannot tell which format it is reading has to guess", rec.Version, SpillRecordVersion)
	}
	if rec.SchemaName != "run_started" {
		t.Errorf("SchemaName = %q, want run_started", rec.SchemaName)
	}
	if rec.Reason == "" {
		t.Error("Reason is empty; a replay cannot distinguish a transient outage from a permanent rejection without it")
	}
}

// TestFileSpiller_FileModeIsOwnerOnly pins 0600 on the spill file.
//
// Spilled payloads carry whatever an event carried — agent names, goal text,
// git SHAs. On a shared host that is not world-readable material, and this repo's
// own hosts run many agents under one uid.
func TestFileSpiller_FileModeIsOwnerOnly(t *testing.T) {
	s, root := newTestSpiller(t, time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC))
	if err := s.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write: %v", err)
	}
	fi, err := os.Stat(filepath.Join(SpillDir(root), "2026-08-19.ndjson"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("spill file mode is %#o; it must not be group- or world-accessible", perm)
	}
}

// TestFileSpiller_DayRollover pins one file per UTC day, so retention can work
// at file granularity rather than having to parse and rewrite a single log.
func TestFileSpiller_DayRollover(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 8, 19, 23, 59, 0, 0, time.UTC)
	s := &FileSpiller{Root: root, Now: func() time.Time { return day }}

	if err := s.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write day 1: %v", err)
	}
	day = day.Add(2 * time.Minute) // crosses midnight UTC
	if err := s.Write(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Write day 2: %v", err)
	}

	for _, name := range []string{"2026-08-19.ndjson", "2026-08-20.ndjson"} {
		if lines := readLines(t, filepath.Join(SpillDir(root), name)); len(lines) != 1 {
			t.Errorf("%s holds %d line(s), want 1", name, len(lines))
		}
	}
}

// TestFileSpiller_DeadLetterIsSeparateFromSpill pins that an unreplayable record
// goes somewhere distinct rather than being dropped or silently re-spilled.
//
// It is the mechanism behind "never a silent drop": a replayer meeting an
// invalid or unresolvable line must move it here with a reason, because a
// skipped line is indistinguishable from a line that was never written.
func TestFileSpiller_DeadLetterIsSeparateFromSpill(t *testing.T) {
	day := time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)
	s, root := newTestSpiller(t, day)

	if err := s.DeadLetter(sampleRecord(), "schema_id unknown to this build"); err != nil {
		t.Fatalf("DeadLetter: %v", err)
	}

	dead := filepath.Join(DeadLetterDir(root), "2026-08-19.ndjson")
	lines := readLines(t, dead)
	if len(lines) != 1 {
		t.Fatalf("dead-letter file holds %d line(s), want 1", len(lines))
	}
	var rec SpillRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("dead-letter line does not parse: %v", err)
	}
	if !strings.Contains(rec.Reason, "unknown to this build") {
		t.Errorf("Reason = %q; a dead letter without a reason cannot be triaged", rec.Reason)
	}

	// The spill file itself must NOT have been written: a dead letter is a
	// terminal outcome, and re-spilling it would loop forever on every replay.
	if _, err := os.Stat(filepath.Join(SpillDir(root), "2026-08-19.ndjson")); !os.IsNotExist(err) {
		t.Errorf("DeadLetter also wrote the spill file (err=%v); a dead letter must be terminal", err)
	}
}

// TestFileSpiller_PruneRemovesExpiredAndKeepsFresh pins age-based retention.
// Both directions: without the second leg, a Prune that deleted everything
// would pass the first.
func TestFileSpiller_PruneRemovesExpiredAndKeepsFresh(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	s, root := newTestSpiller(t, now)
	dir := SpillDir(root)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	stale := filepath.Join(dir, "2026-07-01.ndjson")
	fresh := filepath.Join(dir, "2026-08-18.ndjson")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	if err := os.Chtimes(stale, now.Add(-40*24*time.Hour), now.Add(-40*24*time.Hour)); err != nil {
		t.Fatalf("chtimes stale: %v", err)
	}
	if err := os.Chtimes(fresh, now.Add(-24*time.Hour), now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("chtimes fresh: %v", err)
	}

	removed, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != 1 || removed[0] != stale {
		t.Errorf("Prune removed %v, want exactly [%s]", removed, stale)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the past-retention file survived Prune (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("Prune deleted a file inside the retention window: %v", err)
	}
}

// TestFileSpiller_PruneReportsWhatItRemoved pins that Prune returns the paths.
//
// Silent deletion of an outage artifact leaves an operator unable to tell "there
// was no outage" from "the evidence was reaped", which are the two readings of an
// empty spill directory.
func TestFileSpiller_PruneReportsWhatItRemoved(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	s, root := newTestSpiller(t, now)
	dir := SpillDir(root)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var want []string
	for _, name := range []string{"2026-06-01.ndjson", "2026-06-02.ndjson"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(p, now.Add(-60*24*time.Hour), now.Add(-60*24*time.Hour)); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		want = append(want, p)
	}
	removed, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(removed) != len(want) {
		t.Errorf("Prune reported %d removal(s), want %d — an unreported deletion is indistinguishable from no outage", len(removed), len(want))
	}
}

// TestFileSpiller_PruneNeverEvictsDeadLetters pins that the byte cap cannot
// reap the dead-letter directory. Those files are the record of what could not
// be replayed; evicting them turns a recorded failure into a silent drop, which
// is the one outcome the whole spill design forbids.
func TestFileSpiller_PruneNeverEvictsDeadLetters(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	s, root := newTestSpiller(t, now)
	if err := s.DeadLetter(sampleRecord(), "unresolvable"); err != nil {
		t.Fatalf("DeadLetter: %v", err)
	}
	dead := filepath.Join(DeadLetterDir(root), "2026-08-19.ndjson")
	if err := os.Chtimes(dead, now.Add(-400*24*time.Hour), now.Add(-400*24*time.Hour)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if _, err := s.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Errorf("Prune deleted a dead-letter file that was well past the retention window: %v — a dead letter must outlive retention or the failure it records is lost", err)
	}
}

// TestFileSpiller_PruneOnMissingDirIsNotAnError pins the common case: nothing
// has ever spilled. Erroring here would make a routine maintenance call noisy on
// every healthy host.
func TestFileSpiller_PruneOnMissingDirIsNotAnError(t *testing.T) {
	s := &FileSpiller{Root: t.TempDir()}
	removed, err := s.Prune()
	if err != nil {
		t.Fatalf("Prune on a host that has never spilled must not error: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("Prune reported %d removal(s) with no spill directory", len(removed))
	}
}

// TestSpillDir_IsUnderTheGitignoredSprawlTree pins the PATH, because the
// gitignore guarantee is a property of the path rather than of the writer.
//
// The repo ignores `.sprawl/*` with only `.sprawl/config.yaml` negated, so a
// spill under .sprawl/logs is covered. Moving the spill anywhere else would
// start committing event payloads — including employer-context material — into a
// PUBLIC repo. scripts/test-gitignore-classes.sh asserts the ignore itself;
// this asserts that the code still writes where that assertion looks.
func TestSpillDir_IsUnderTheGitignoredSprawlTree(t *testing.T) {
	got := SpillDir("/repo")
	want := filepath.Join("/repo", ".sprawl", "logs", "ledger-spill")
	if got != want {
		t.Errorf("SpillDir = %q, want %q — the gitignore coverage asserted by scripts/test-gitignore-classes.sh is keyed on this path", got, want)
	}
	if !strings.HasPrefix(DeadLetterDir("/repo"), got) {
		t.Errorf("DeadLetterDir %q is not under the spill dir %q", DeadLetterDir("/repo"), got)
	}
}
