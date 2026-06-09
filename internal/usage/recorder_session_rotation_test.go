package usage

import (
	"os"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_SessionRotationOpensNewFile covers QUM-368 AC §4: when the
// SessionID on EventProtocolMessage changes mid-runtime, the Recorder must
// close the prior file and open a new one. Records must not cross over.
func TestRecorder_SessionRotationOpensNewFile(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	// First session.
	rec.Handle(assistantEvent(t, "sess-A", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-A", 0.001))

	// Second session.
	rec.Handle(assistantEvent(t, "sess-B", protocol.Usage{InputTokens: 9, OutputTokens: 9}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-B", 0.999))

	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pathA := usageLogPath(tmp, "finn", "sess-A")
	pathB := usageLogPath(tmp, "finn", "sess-B")
	if _, err := os.Stat(pathA); err != nil {
		t.Fatalf("expected file for sess-A at %q: %v", pathA, err)
	}
	if _, err := os.Stat(pathB); err != nil {
		t.Fatalf("expected file for sess-B at %q: %v", pathB, err)
	}

	recA := readNDJSONLines(t, pathA)
	recB := readNDJSONLines(t, pathB)
	if len(recA) != 1 || len(recB) != 1 {
		t.Fatalf("len(recA)=%d len(recB)=%d, want 1 each", len(recA), len(recB))
	}
	if recA[0].SessionID != "sess-A" || recA[0].InputTokens != 1 || recA[0].TotalCostUsd != 0.001 {
		t.Errorf("sess-A record = %+v, want session_id=sess-A input=1 cost=0.001", recA[0])
	}
	if recB[0].SessionID != "sess-B" || recB[0].InputTokens != 9 || recB[0].TotalCostUsd != 0.999 {
		t.Errorf("sess-B record = %+v, want session_id=sess-B input=9 cost=0.999", recB[0])
	}
}

// TestRecorder_SessionRotationOnAssistantFrameMidStream verifies the
// "session-snoop on assistant frames" contract: when two assistant frames
// arrive back-to-back with different session_ids (no intervening
// EventTurnCompleted), the recorder rotates files on the second frame.
//
// Chosen semantic (per QUM-368 plan): the sess-A accumulator was in-flight
// and is DISCARDED on mid-stream rotation — flushes only happen on
// EventTurnCompleted. So sess-A's file must NOT exist; sess-B's file gets
// exactly one record after the eventual TurnCompleted(sess-B).
func TestRecorder_SessionRotationOnAssistantFrameMidStream(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	// assistant(sess-A) — accumulates, but no terminal frame.
	rec.Handle(assistantEvent(t, "sess-A", protocol.Usage{InputTokens: 5, OutputTokens: 5}, "claude-opus-4-7"))
	// assistant(sess-B) — recorder must snoop here and rotate before any
	// EventTurnCompleted arrives. sess-A accumulator is dropped.
	rec.Handle(assistantEvent(t, "sess-B", protocol.Usage{InputTokens: 9, OutputTokens: 9}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-B", 0.123))

	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pathA := usageLogPath(tmp, "finn", "sess-A")
	if _, err := os.Stat(pathA); err == nil {
		t.Errorf("expected NO file for sess-A (mid-stream rotation discards in-flight accumulator); file exists at %q", pathA)
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %q: %v", pathA, err)
	}

	pathB := usageLogPath(tmp, "finn", "sess-B")
	recB := readNDJSONLines(t, pathB)
	if len(recB) != 1 {
		t.Fatalf("len(recB)=%d, want 1", len(recB))
	}
	if recB[0].SessionID != "sess-B" || recB[0].InputTokens != 9 || recB[0].TotalCostUsd != 0.123 {
		t.Errorf("sess-B record = %+v, want session_id=sess-B input=9 cost=0.123", recB[0])
	}
}
