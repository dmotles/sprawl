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
	"github.com/dmotles/sprawl/internal/state"
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

// QUM-331: When an agent state file declares a CreatedAt, PeekActivity must
// hide entries with TS < CreatedAt — those came from a prior incarnation that
// reused the same name, and should not appear in the TUI activity panel even
// though the on-disk activity.ndjson is append-only.
func TestPeekActivity_FiltersEntriesBeforeCreatedAt(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	createdAt := time.Date(2026, 4, 24, 17, 27, 0, 0, time.UTC)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:      "ratz",
		Status:    "active",
		CreatedAt: createdAt.Format(time.RFC3339),
	})

	// Mix of entries from a "prior" ratz (before createdAt) and the current
	// incarnation (at/after createdAt).
	entries := []agentloop.ActivityEntry{
		{TS: createdAt.Add(-72 * time.Hour), Kind: "system", Summary: "stale-old-1"},
		{TS: createdAt.Add(-1 * time.Second), Kind: "tool_use", Summary: "stale-old-2", Tool: "Bash"},
		{TS: createdAt, Kind: "system", Summary: "fresh-boundary"},
		{TS: createdAt.Add(time.Second), Kind: "assistant_text", Summary: "fresh-1"},
		{TS: createdAt.Add(2 * time.Second), Kind: "tool_use", Summary: "fresh-2", Tool: "Read"},
	}
	writeActivity(t, tmpDir, "ratz", entries)

	got, err := sup.PeekActivity(context.Background(), "ratz", 0)
	if err != nil {
		t.Fatalf("PeekActivity: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (only entries at or after CreatedAt); got %+v", len(got), got)
	}
	wantSummaries := []string{"fresh-boundary", "fresh-1", "fresh-2"}
	for i, e := range got {
		if e.Summary != wantSummaries[i] {
			t.Errorf("entry[%d].Summary = %q, want %q", i, e.Summary, wantSummaries[i])
		}
		if e.TS.Before(createdAt) {
			t.Errorf("entry[%d].TS = %v is before CreatedAt %v — must be filtered out", i, e.TS, createdAt)
		}
	}
}

// QUM-331: Tail must apply AFTER the CreatedAt filter — otherwise tail=N can
// be entirely consumed by stale pre-incarnation entries and the panel goes
// blank for the current agent.
func TestPeekActivity_TailAppliesAfterCreatedAtFilter(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	createdAt := time.Date(2026, 4, 24, 17, 27, 0, 0, time.UTC)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:      "ratz",
		Status:    "active",
		CreatedAt: createdAt.Format(time.RFC3339),
	})

	entries := []agentloop.ActivityEntry{
		{TS: createdAt.Add(-3 * time.Hour), Kind: "system", Summary: "stale-1"},
		{TS: createdAt.Add(-2 * time.Hour), Kind: "system", Summary: "stale-2"},
		{TS: createdAt.Add(-1 * time.Hour), Kind: "system", Summary: "stale-3"},
		{TS: createdAt.Add(time.Second), Kind: "assistant_text", Summary: "fresh-1"},
		{TS: createdAt.Add(2 * time.Second), Kind: "assistant_text", Summary: "fresh-2"},
	}
	writeActivity(t, tmpDir, "ratz", entries)

	got, err := sup.PeekActivity(context.Background(), "ratz", 2)
	if err != nil {
		t.Fatalf("PeekActivity: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Summary != "fresh-1" || got[1].Summary != "fresh-2" {
		t.Errorf("got %+v, want [fresh-1, fresh-2]", got)
	}
}

// QUM-331: Without an agent state file (or with an unparseable CreatedAt),
// PeekActivity falls back to returning all on-disk entries — preserves prior
// behavior on edge cases and avoids hiding data when the filter input is
// untrustworthy.
func TestPeekActivity_NoStateFile_ReturnsAll(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	entries := []agentloop.ActivityEntry{
		{TS: now, Kind: "a", Summary: "one"},
		{TS: now.Add(time.Second), Kind: "b", Summary: "two"},
	}
	writeActivity(t, tmpDir, "ghost", entries)
	// Note: deliberately NOT calling saveTestAgent — no .json state file.

	got, err := sup.PeekActivity(context.Background(), "ghost", 0)
	if err != nil {
		t.Fatalf("PeekActivity: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2 (no state file means no filter)", len(got))
	}
}

func TestPeekActivity_UnparseableCreatedAt_ReturnsAll(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:      "ratz",
		Status:    "active",
		CreatedAt: "not-a-timestamp",
	})

	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	entries := []agentloop.ActivityEntry{
		{TS: now, Kind: "a", Summary: "one"},
		{TS: now.Add(time.Second), Kind: "b", Summary: "two"},
	}
	writeActivity(t, tmpDir, "ratz", entries)

	got, err := sup.PeekActivity(context.Background(), "ratz", 0)
	if err != nil {
		t.Fatalf("PeekActivity: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want 2 (unparseable CreatedAt means no filter)", len(got))
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
