package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_SingleHappyPathTurn drives one assistant frame followed by one
// EventTurnCompleted through a Recorder constructed for a real (state-file-
// backed) agent. Asserts exactly one NDJSON record is written and every
// schema field is populated correctly (QUM-368 AC §1).
func TestRecorder_SingleHappyPathTurn(t *testing.T) {
	tmp := t.TempDir()
	agent := &state.AgentState{
		Name:   "finn",
		Type:   "engineer",
		Family: "engineering",
		Parent: "tower",
		Branch: "dmotles/qum-704-eng",
		Status: "active",
	}
	if err := state.SaveAgent(tmp, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-abc123"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{
		InputTokens:              6,
		OutputTokens:             8,
		CacheReadInputTokens:     12066,
		CacheCreationInputTokens: 14083,
	}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.0421))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := usageLogPath(tmp, "finn", sessionID)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected usage log at %q: %v", path, err)
	}
	records := readNDJSONLines(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(records), records)
	}
	r := records[0]
	if r.AgentName != "finn" {
		t.Errorf("AgentName = %q, want finn", r.AgentName)
	}
	if r.AgentType != "engineer" {
		t.Errorf("AgentType = %q, want engineer", r.AgentType)
	}
	if r.AgentFamily != "engineering" {
		t.Errorf("AgentFamily = %q, want engineering", r.AgentFamily)
	}
	if r.ParentName != "tower" {
		t.Errorf("ParentName = %q, want tower", r.ParentName)
	}
	if r.Branch != "dmotles/qum-704-eng" {
		t.Errorf("Branch = %q, want dmotles/qum-704-eng", r.Branch)
	}
	if r.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", r.SessionID, sessionID)
	}
	if r.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", r.Model)
	}
	if r.InputTokens != 6 || r.OutputTokens != 8 ||
		r.CacheReadInputTokens != 12066 || r.CacheCreationInputTokens != 14083 {
		t.Errorf("token fields = %+v, want input=6 output=8 read=12066 creation=14083", r)
	}
	if r.TotalCostUsd != 0.0421 {
		t.Errorf("TotalCostUsd = %v, want 0.0421", r.TotalCostUsd)
	}
	if r.Timestamp == "" {
		t.Error("Timestamp empty")
	}
}

// TestRecorder_RootWeaveEmptyBranchKey covers the QUM-368 AC that records for
// an agent without a worktree branch (root weave) still emit the "branch"
// key with an empty string — never absent.
func TestRecorder_RootWeaveEmptyBranchKey(t *testing.T) {
	tmp := t.TempDir()
	// Intentionally do NOT call state.SaveAgent — the Recorder must tolerate
	// a missing AgentState file (root weave case) and emit empty strings for
	// every metadata field.

	rec, err := NewRecorder(tmp, "weave")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-weave-1"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{
		InputTokens:  1,
		OutputTokens: 1,
	}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.01))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	path := usageLogPath(tmp, "weave", sessionID)
	raw, err := os.ReadFile(path) //nolint:gosec // test reads file path it constructed
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	// Parse the first NDJSON line as a map and assert every required schema
	// key is present (QUM-368 — column stability for downstream tooling).
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	if !scanner.Scan() {
		t.Fatalf("no lines in NDJSON file %q", path)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(scanner.Bytes(), &fields); err != nil {
		t.Fatalf("unmarshal NDJSON line as map: %v\nline: %s", err, scanner.Bytes())
	}
	requiredKeys := []string{
		"timestamp",
		"agent_name",
		"agent_type",
		"agent_family",
		"parent_name",
		"session_id",
		"branch",
		"model",
		"input_tokens",
		"output_tokens",
		"cache_read_input_tokens",
		"cache_creation_input_tokens",
		"total_cost_usd",
	}
	for _, k := range requiredKeys {
		if _, ok := fields[k]; !ok {
			t.Errorf("required schema key %q missing from NDJSON record; got keys: %v", k, mapKeys(fields))
		}
	}

	records := readNDJSONLines(t, path)
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].Branch != "" {
		t.Errorf("Branch = %q, want empty string", records[0].Branch)
	}
}

func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
