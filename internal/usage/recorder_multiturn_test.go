package usage

import (
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_MultiTurnProducesOneRecordEach drives three successive turns
// through one Recorder and asserts three NDJSON lines, each carrying its own
// per-turn total_cost_usd from the Result frame (QUM-368 AC §2).
func TestRecorder_MultiTurnProducesOneRecordEach(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-multi"
	costs := []float64{0.01, 0.02, 0.04}
	for i, c := range costs {
		rec.Handle(assistantEvent(t, sessionID, protocol.Usage{
			InputTokens:  i + 1,
			OutputTokens: i + 2,
		}, "claude-opus-4-7"))
		rec.Handle(turnCompletedEvent(sessionID, c))
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := usageLogPath(tmp, "finn", sessionID)
	records := readNDJSONLines(t, path)
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	for i, r := range records {
		if r.TotalCostUsd != costs[i] {
			t.Errorf("record[%d].TotalCostUsd = %v, want %v", i, r.TotalCostUsd, costs[i])
		}
		if r.InputTokens != i+1 || r.OutputTokens != i+2 {
			t.Errorf("record[%d] tokens = (%d,%d), want (%d,%d)",
				i, r.InputTokens, r.OutputTokens, i+1, i+2)
		}
	}
}
