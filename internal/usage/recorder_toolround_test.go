package usage

import (
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_MultipleAssistantFramesSumIntoOneRecord covers QUM-368 AC §3:
// tool-use round-trips emit multiple assistant frames inside one turn; all
// token counts must sum into a single output record.
func TestRecorder_MultipleAssistantFramesSumIntoOneRecord(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-toolround"
	frames := []protocol.Usage{
		{InputTokens: 10, OutputTokens: 20, CacheReadInputTokens: 100, CacheCreationInputTokens: 1},
		{InputTokens: 30, OutputTokens: 40, CacheReadInputTokens: 200, CacheCreationInputTokens: 2},
		{InputTokens: 50, OutputTokens: 60, CacheReadInputTokens: 300, CacheCreationInputTokens: 3},
	}
	for _, u := range frames {
		rec.Handle(assistantEvent(t, sessionID, u, "claude-opus-4-7"))
	}
	rec.Handle(turnCompletedEvent(sessionID, 0.15))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	r := records[0]
	if r.InputTokens != 90 {
		t.Errorf("InputTokens = %d, want 90", r.InputTokens)
	}
	if r.OutputTokens != 120 {
		t.Errorf("OutputTokens = %d, want 120", r.OutputTokens)
	}
	if r.CacheReadInputTokens != 600 {
		t.Errorf("CacheReadInputTokens = %d, want 600", r.CacheReadInputTokens)
	}
	if r.CacheCreationInputTokens != 6 {
		t.Errorf("CacheCreationInputTokens = %d, want 6", r.CacheCreationInputTokens)
	}
	if r.TotalCostUsd != 0.15 {
		t.Errorf("TotalCostUsd = %v, want 0.15", r.TotalCostUsd)
	}
}
