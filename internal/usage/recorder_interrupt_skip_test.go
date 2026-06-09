package usage

import (
	"os"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_InterruptedTurnProducesNoRecord covers QUM-368 AC §5:
// EventInterrupted mid-turn discards the in-flight accumulator and writes
// nothing. A subsequent successful turn still succeeds. Same for
// EventBackendFaulted.
func TestRecorder_InterruptedTurnProducesNoRecord(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-interrupted"
	// Frames accumulate, then interrupt arrives → no write.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 7, OutputTokens: 8}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})

	// New turn after interrupt → succeeds.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.01))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := usageLogPath(tmp, "finn", sessionID)
	records := readNDJSONLines(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records after one interrupt + one success, want 1", len(records))
	}
	if records[0].InputTokens != 1 || records[0].OutputTokens != 1 || records[0].TotalCostUsd != 0.01 {
		t.Errorf("surviving record = %+v, want input=1 output=1 cost=0.01 (interrupted tokens must not leak)", records[0])
	}
}

func TestRecorder_FaultedTurnProducesNoRecord(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-faulted"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 4, OutputTokens: 4}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventBackendFaulted})

	// Drive a fresh, successful turn after the fault. The recorder must have
	// discarded the faulted accumulator; only this fresh turn should land on
	// disk. Mirrors the interrupt-skip pattern.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 2, OutputTokens: 3}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.02))

	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := usageLogPath(tmp, "finn", sessionID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected usage log at %q after fresh successful turn: %v", path, err)
	}
	records := readNDJSONLines(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records after fault + fresh success, want exactly 1: %+v", len(records), records)
	}
	if records[0].InputTokens != 2 || records[0].OutputTokens != 3 || records[0].TotalCostUsd != 0.02 {
		t.Errorf("surviving record = %+v, want input=2 output=3 cost=0.02 (faulted accumulator must not leak)", records[0])
	}
}
