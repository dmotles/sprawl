package usage

import (
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_MultiTurnProducesOneRecordEach drives three successive turns
// through one Recorder and asserts three NDJSON lines, each carrying its own
// per-turn cost (QUM-368 AC §2). The Result frame's total_cost_usd is
// session-cumulative, so the stored column is the delta over the previous turn
// (QUM-1247) — the cost invariant itself is pinned in recorder_cost_delta_test.go.
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
	cumulative := []float64{0.01, 0.02, 0.04}
	wantDeltas := []float64{0.01, 0.01, 0.02}
	for i, c := range cumulative {
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
		if r.TotalCostUsd != wantDeltas[i] {
			t.Errorf("record[%d].TotalCostUsd = %v, want delta %v (cumulative fed in: %v)",
				i, r.TotalCostUsd, wantDeltas[i], cumulative[i])
		}
		if r.InputTokens != i+1 || r.OutputTokens != i+2 {
			t.Errorf("record[%d] tokens = (%d,%d), want (%d,%d)",
				i, r.InputTokens, r.OutputTokens, i+1, i+2)
		}
	}
}
