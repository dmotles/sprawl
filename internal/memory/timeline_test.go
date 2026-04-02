package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteAndReadTimeline_RoundTrip(t *testing.T) {
	root := t.TempDir()
	entries := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "Initial project setup"},
		{Timestamp: time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC), Summary: "Implemented messaging system"},
		{Timestamp: time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC), Summary: "Added test coverage"},
	}

	if err := WriteTimeline(root, entries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for i, e := range entries {
		if !got[i].Timestamp.Equal(e.Timestamp) {
			t.Errorf("entry[%d].Timestamp = %v, want %v", i, got[i].Timestamp, e.Timestamp)
		}
		if got[i].Summary != e.Summary {
			t.Errorf("entry[%d].Summary = %q, want %q", i, got[i].Summary, e.Summary)
		}
	}
}

func TestReadTimeline_MissingFile(t *testing.T) {
	root := t.TempDir()

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestReadTimeline_EmptyFile(t *testing.T) {
	root := t.TempDir()
	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	if err := os.MkdirAll(filepath.Dir(tlPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(tlPath, []byte(""), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestReadTimeline_HeaderOnly(t *testing.T) {
	root := t.TempDir()
	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	if err := os.MkdirAll(filepath.Dir(tlPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(tlPath, []byte("# Session Timeline\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestReadTimeline_MalformedLines(t *testing.T) {
	root := t.TempDir()
	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	if err := os.MkdirAll(filepath.Dir(tlPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := strings.Join([]string{
		"# Session Timeline",
		"",
		"- 2026-04-01T10:00:00Z: Valid entry one",
		"This is not a list item",
		"- malformed no timestamp here",
		"<!-- a comment -->",
		"- 2026-04-01T14:30:00Z: Valid entry two",
		"",
		"- not-a-date: Something else",
		"- 2026-04-02T09:00:00Z: Valid entry three",
	}, "\n")

	if err := os.WriteFile(tlPath, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: unexpected error: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}

	want := []struct {
		ts      time.Time
		summary string
	}{
		{time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), "Valid entry one"},
		{time.Date(2026, 4, 1, 14, 30, 0, 0, time.UTC), "Valid entry two"},
		{time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC), "Valid entry three"},
	}
	for i, w := range want {
		if !got[i].Timestamp.Equal(w.ts) {
			t.Errorf("entry[%d].Timestamp = %v, want %v", i, got[i].Timestamp, w.ts)
		}
		if got[i].Summary != w.summary {
			t.Errorf("entry[%d].Summary = %q, want %q", i, got[i].Summary, w.summary)
		}
	}
}

func TestWriteTimeline_EmptySlice(t *testing.T) {
	root := t.TempDir()

	if err := WriteTimeline(root, []TimelineEntry{}); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	data, err := os.ReadFile(tlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "# Session Timeline\n" {
		t.Errorf("file contents = %q, want %q", string(data), "# Session Timeline\n")
	}
}

func TestWriteTimeline_CreatesDirectories(t *testing.T) {
	root := t.TempDir()
	// The memory directory should not exist yet
	memDir := filepath.Join(root, ".dendra", "memory")
	if _, err := os.Stat(memDir); err == nil {
		t.Fatalf("memory dir should not exist before write")
	}

	entries := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), Summary: "Created via test"},
	}
	if err := WriteTimeline(root, entries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	if _, err := os.Stat(tlPath); err != nil {
		t.Fatalf("timeline.md should exist after write: %v", err)
	}
}

func TestAppendTimelineEntries_ToExisting(t *testing.T) {
	root := t.TempDir()

	initial := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "First"},
		{Timestamp: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), Summary: "Second"},
	}
	if err := WriteTimeline(root, initial); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	additional := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC), Summary: "Third"},
		{Timestamp: time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC), Summary: "Fourth"},
	}
	if err := AppendTimelineEntries(root, additional); err != nil {
		t.Fatalf("AppendTimelineEntries: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}

	wantSummaries := []string{"First", "Second", "Third", "Fourth"}
	for i, w := range wantSummaries {
		if got[i].Summary != w {
			t.Errorf("entry[%d].Summary = %q, want %q", i, got[i].Summary, w)
		}
	}

	// Verify chronological order
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("entry[%d] (%v) is before entry[%d] (%v)", i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}

func TestAppendTimelineEntries_ToMissing(t *testing.T) {
	root := t.TempDir()

	entries := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "From nothing"},
	}
	if err := AppendTimelineEntries(root, entries); err != nil {
		t.Fatalf("AppendTimelineEntries: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Summary != "From nothing" {
		t.Errorf("Summary = %q, want %q", got[0].Summary, "From nothing")
	}
}

func TestWriteTimeline_NormalizesToUTC(t *testing.T) {
	root := t.TempDir()

	est := time.FixedZone("EST", -5*60*60)
	entries := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, est), Summary: "Eastern time entry"},
	}
	if err := WriteTimeline(root, entries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// Read the raw file to verify UTC formatting
	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	data, err := os.ReadFile(tlPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	// 10:00 EST = 15:00 UTC
	if !strings.Contains(content, "2026-04-01T15:00:00Z") {
		t.Errorf("file should contain UTC timestamp 2026-04-01T15:00:00Z, got:\n%s", content)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	wantUTC := time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC)
	if !got[0].Timestamp.Equal(wantUTC) {
		t.Errorf("Timestamp = %v, want %v", got[0].Timestamp, wantUTC)
	}
}

func TestReadTimeline_ParsesVariousISO8601(t *testing.T) {
	root := t.TempDir()
	tlPath := filepath.Join(root, ".dendra", "memory", "timeline.md")
	if err := os.MkdirAll(filepath.Dir(tlPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	content := strings.Join([]string{
		"# Session Timeline",
		"",
		"- 2026-04-01T10:00:00Z: UTC entry",
		"- 2026-04-01T15:30:00+05:00: Positive offset entry",
		"- 2026-04-01T08:00:00-04:00: Negative offset entry",
	}, "\n")

	if err := os.WriteFile(tlPath, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: unexpected error: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}

	want := []struct {
		utc     time.Time
		summary string
	}{
		{time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), "UTC entry"},
		{time.Date(2026, 4, 1, 10, 30, 0, 0, time.UTC), "Positive offset entry"},  // 15:30+05:00 = 10:30 UTC
		{time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), "Negative offset entry"},   // 08:00-04:00 = 12:00 UTC
	}
	for i, w := range want {
		gotUTC := got[i].Timestamp.UTC()
		if !gotUTC.Equal(w.utc) {
			t.Errorf("entry[%d].Timestamp.UTC() = %v, want %v", i, gotUTC, w.utc)
		}
		if got[i].Summary != w.summary {
			t.Errorf("entry[%d].Summary = %q, want %q", i, got[i].Summary, w.summary)
		}
	}
}

func TestAppendTimelineEntries_MergesSorted(t *testing.T) {
	root := t.TempDir()

	initial := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC), Summary: "First"},
		{Timestamp: time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC), Summary: "Third"},
		{Timestamp: time.Date(2026, 4, 1, 18, 0, 0, 0, time.UTC), Summary: "Fifth"},
	}
	if err := WriteTimeline(root, initial); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// Append entries that interleave with existing ones
	interleaved := []TimelineEntry{
		{Timestamp: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), Summary: "Second"},
		{Timestamp: time.Date(2026, 4, 1, 16, 0, 0, 0, time.UTC), Summary: "Fourth"},
		{Timestamp: time.Date(2026, 4, 1, 20, 0, 0, 0, time.UTC), Summary: "Sixth"},
	}
	if err := AppendTimelineEntries(root, interleaved); err != nil {
		t.Fatalf("AppendTimelineEntries: %v", err)
	}

	got, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	if len(got) != 6 {
		t.Fatalf("got %d entries, want 6", len(got))
	}

	wantSummaries := []string{"First", "Second", "Third", "Fourth", "Fifth", "Sixth"}
	for i, w := range wantSummaries {
		if got[i].Summary != w {
			t.Errorf("entry[%d].Summary = %q, want %q", i, got[i].Summary, w)
		}
	}

	// Verify strict chronological order
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("entry[%d] (%v) is before entry[%d] (%v) — not sorted", i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}
