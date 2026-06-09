package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// TestStatus_TotalCostUsdSourcedFromUsageTreewalk covers the QUM-368 reader
// migration: after AgentState.TotalCostUsd is retired, Real.Status must
// source per-agent cost by summing total_cost_usd from the on-disk
// .sprawl/logs/usage/<agent>/*.ndjson fixtures. This is the supervisor-level
// integration assertion that internal/usage.SumForAgent is wired into the
// Status response.
func TestStatus_TotalCostUsdSourcedFromUsageTreewalk(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "finn",
		Type:   "engineer",
		Status: "active",
	})

	// Drop two NDJSON fixture lines under .sprawl/logs/usage/finn/ such that
	// the sum of their total_cost_usd values is the expected supervisor
	// reading. Field names match the production schema (see
	// internal/usage.Record).
	dir := filepath.Join(tmpDir, ".sprawl", "logs", "usage", "finn")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	records := []map[string]any{
		{
			"timestamp":                   "2026-06-09T07:00:00Z",
			"agent_name":                  "finn",
			"agent_type":                  "engineer",
			"agent_family":                "",
			"parent_name":                 "",
			"session_id":                  "sess-1",
			"branch":                      "",
			"model":                       "claude-opus-4-7",
			"input_tokens":                10,
			"output_tokens":               20,
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 0,
			"total_cost_usd":              0.07,
		},
		{
			"timestamp":                   "2026-06-09T07:01:00Z",
			"agent_name":                  "finn",
			"agent_type":                  "engineer",
			"agent_family":                "",
			"parent_name":                 "",
			"session_id":                  "sess-1",
			"branch":                      "",
			"model":                       "claude-opus-4-7",
			"input_tokens":                30,
			"output_tokens":               40,
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 0,
			"total_cost_usd":              0.13,
		},
	}
	path := filepath.Join(dir, "sess-1.ndjson")
	f, err := os.Create(path) //nolint:gosec // test fixture path constructed above
	if err != nil {
		t.Fatalf("Create %q: %v", path, err)
	}
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var finn *AgentInfo
	for i := range got {
		if got[i].Name == "finn" {
			finn = &got[i]
			break
		}
	}
	if finn == nil {
		t.Fatal("missing finn from Status() response")
	}
	const want = 0.20
	if d := finn.TotalCostUsd - want; d > 1e-9 || d < -1e-9 {
		t.Errorf("finn.TotalCostUsd = %v, want %v (sum of fixture total_cost_usd values)", finn.TotalCostUsd, want)
	}
}
