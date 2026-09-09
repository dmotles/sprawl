package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	tlPath := filepath.Join(root, ".sprawl", "memory", "timeline.md")
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
	tlPath := filepath.Join(root, ".sprawl", "memory", "timeline.md")
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
	tlPath := filepath.Join(root, ".sprawl", "memory", "timeline.md")
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

func TestReadTimeline_ParsesVariousISO8601(t *testing.T) {
	root := t.TempDir()
	tlPath := filepath.Join(root, ".sprawl", "memory", "timeline.md")
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
		{time.Date(2026, 4, 1, 10, 30, 0, 0, time.UTC), "Positive offset entry"}, // 15:30+05:00 = 10:30 UTC
		{time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), "Negative offset entry"},  // 08:00-04:00 = 12:00 UTC
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
