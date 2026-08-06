package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// writeUsageFixture drops one NDJSON line per cost under
// .sprawl/logs/usage/<agent>/<session>.ndjson. Field names match the
// production schema (see internal/usage.Record).
func writeUsageFixture(t *testing.T, sprawlRoot, agent, session string, costs ...float64) {
	t.Helper()
	dir := filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, session+".ndjson")
	f, err := os.Create(path) //nolint:gosec // test fixture path constructed above
	if err != nil {
		t.Fatalf("Create %q: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for i, cost := range costs {
		rec := map[string]any{
			"timestamp":                   "2026-06-09T07:00:00Z",
			"agent_name":                  agent,
			"agent_type":                  "engineer",
			"session_id":                  session,
			"model":                       "claude-opus-4-7",
			"input_tokens":                10 * (i + 1),
			"output_tokens":               20 * (i + 1),
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 0,
			"total_cost_usd":              cost,
		}
		if err := enc.Encode(rec); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
}

func statusFor(t *testing.T, sup *Real, name string) *AgentInfo {
	t.Helper()
	got, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for i := range got {
		if got[i].Name == name {
			return &got[i]
		}
	}
	t.Fatalf("missing %s from Status() response", name)
	return nil
}

// TestStatus_SessionCostUsd_ScopedToPersistedSessionID is the QUM-1093
// supervisor-level assertion: Real.Status must report only the cost recorded
// for the session named by the agent's persisted AgentState.SessionID, not the
// lifetime sum over every session file under .sprawl/logs/usage/<agent>/.
func TestStatus_SessionCostUsd_ScopedToPersistedSessionID(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Status:    "active",
		SessionID: "sess-2",
	})
	writeUsageFixture(t, tmpDir, "finn", "sess-1", 1.00)
	writeUsageFixture(t, tmpDir, "finn", "sess-2", 0.07, 0.13)
	writeUsageFixture(t, tmpDir, "finn", "sess-3", 10.00)

	finn := statusFor(t, sup, "finn")
	if d := finn.SessionCostUsd - 0.20; d > 1e-9 || d < -1e-9 {
		t.Errorf("finn.SessionCostUsd = %v, want 0.20 (sess-2 only)", finn.SessionCostUsd)
	}
	if d := finn.SessionCostUsd - 11.20; d < 1e-9 && d > -1e-9 {
		t.Errorf("finn.SessionCostUsd = %v, which is the LIFETIME sum across all session files", finn.SessionCostUsd)
	}
}

// TestStatus_SessionCostUsd_NoSessionIDReportsZero pins the no-fallback
// decision: an agent with no session yet reports 0 even though session logs
// exist on disk under its name. A lifetime fallback here would make the
// QUM-1093 over-report intermittent rather than fixed.
func TestStatus_SessionCostUsd_NoSessionIDReportsZero(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "finn",
		Type:   "engineer",
		Status: "active",
	})
	writeUsageFixture(t, tmpDir, "finn", "sess-1", 0.20)

	finn := statusFor(t, sup, "finn")
	if finn.SessionCostUsd != 0 {
		t.Errorf("finn.SessionCostUsd = %v, want exactly 0 for an agent with no session ID", finn.SessionCostUsd)
	}
}

// TestStatus_SessionCostUsd_SessionIDWithNoFileReportsZero distinguishes "no
// session" from "session started, no usage recorded yet" at the wiring level.
func TestStatus_SessionCostUsd_SessionIDWithNoFileReportsZero(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Status:    "active",
		SessionID: "sess-9",
	})
	writeUsageFixture(t, tmpDir, "finn", "sess-1", 0.20)
	writeUsageFixture(t, tmpDir, "finn", "sess-2", 0.30)

	finn := statusFor(t, sup, "finn")
	if finn.SessionCostUsd != 0 {
		t.Errorf("finn.SessionCostUsd = %v, want exactly 0 when the session has no usage log yet", finn.SessionCostUsd)
	}
}

// TestStatus_SessionCostUsd_PrefersPersistedStateOverRuntimeSnapshot pins the
// resolution source deliberately (QUM-1093 decision): the persisted
// AgentState.SessionID, not the in-memory runtime snapshot's. Note the snapshot
// is the more LIVE of the two — it carries the id the recorder is currently
// naming its file from, unpersisted — so this is a deliberate choice of the
// resumable id over the live one, not of the accurate one over a guess; see
// sessionUsageCostForAgent's comment. Asserted rather than left to inference so
// that reversing the choice is a deliberate edit to this test.
func TestStatus_SessionCostUsd_PrefersPersistedStateOverRuntimeSnapshot(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Status:    state.StatusActive,
		SessionID: "sess-disk",
	})
	sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      &state.AgentState{Name: "finn", Status: state.StatusActive, SessionID: "sess-snapshot"},
	})
	writeUsageFixture(t, tmpDir, "finn", "sess-disk", 0.20)
	writeUsageFixture(t, tmpDir, "finn", "sess-snapshot", 7.00)

	finn := statusFor(t, sup, "finn")
	if d := finn.SessionCostUsd - 0.20; d > 1e-9 || d < -1e-9 {
		t.Errorf("finn.SessionCostUsd = %v, want 0.20 (persisted sess-disk, not snapshot sess-snapshot)", finn.SessionCostUsd)
	}
}
