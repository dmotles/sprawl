package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
)

func writeActivity(t *testing.T, sprawlRoot, agentName string, entries []agentloop.ActivityEntry) {
	t.Helper()
	path := agentloop.ActivityPath(sprawlRoot, agentName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var buf bytes.Buffer
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestPeekActivity_ReturnsTail(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	entries := []agentloop.ActivityEntry{
		{TS: now, Kind: "assistant_text", Summary: "one"},
		{TS: now.Add(time.Second), Kind: "tool_use", Summary: "two", Tool: "Read"},
		{TS: now.Add(2 * time.Second), Kind: "result", Summary: "three"},
	}
	writeActivity(t, tmpDir, "ghost", entries)

	got, err := sup.PeekActivity(context.Background(), "ghost", 2)
	if err != nil {
		t.Fatalf("PeekActivity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Summary != "two" || got[1].Summary != "three" {
		t.Errorf("got %+v, want last two entries", got)
	}
	if got[0].Tool != "Read" {
		t.Errorf("Tool = %q, want Read", got[0].Tool)
	}
}

func TestPeekActivity_MissingFile(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	got, err := sup.PeekActivity(context.Background(), "ghost", 10)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0", len(got))
	}
}

func TestPeekActivity_InvalidName(t *testing.T) {
	sup, _ := newTestSupervisor(t)
	_, err := sup.PeekActivity(context.Background(), "../../etc/passwd", 10)
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}

func TestPeekActivity_TailZeroReturnsAll(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	entries := []agentloop.ActivityEntry{
		{TS: now, Kind: "a", Summary: "one"},
		{TS: now, Kind: "b", Summary: "two"},
	}
	writeActivity(t, tmpDir, "ghost", entries)

	got, err := sup.PeekActivity(context.Background(), "ghost", 0)
	if err != nil {
		t.Fatalf("PeekActivity: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("tail=0 should return all; got %d, want 2", len(got))
	}
}
