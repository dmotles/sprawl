package supervisor

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// AC5: the report timestamp must reach AgentInfo, or the MCP status tool
// renders an age for a field the supervisor never fills — which is exactly
// how `status` shipped with a report state and no timestamp at all. The
// value is carried as the stored RFC3339 string so "never reported" (empty)
// stays distinguishable from "stored but unparseable". QUM-1154.
func TestReal_Status_CarriesLastReportAt(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	const wantAt = "2026-06-06T12:00:00Z"
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:            "reporter",
		Type:            "engineer",
		Parent:          "weave",
		Status:          "active",
		LastReportState: "working",
		LastReportAt:    wantAt,
	})
	// Negative control: a known-clean subject — an agent that never reported —
	// must carry an empty timestamp, not a synthesized one.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "quiet",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})
	// The string carry is only justified if an unparseable stored value
	// survives verbatim rather than being normalized into "never reported".
	// Without this the type-choice rationale on AgentInfo.LastReportAt is a
	// comment, not an asserted claim.
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:            "corrupt",
		Type:            "engineer",
		Parent:          "weave",
		Status:          "active",
		LastReportState: "working",
		LastReportAt:    "garbage",
	})

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	byName := map[string]AgentInfo{}
	for _, a := range agents {
		byName[a.Name] = a
	}

	reporter, ok := byName["reporter"]
	if !ok {
		t.Fatalf("agent %q missing from Status: %+v", "reporter", agents)
	}
	if reporter.LastReportAt != wantAt {
		t.Errorf("reporter.LastReportAt = %q, want %q", reporter.LastReportAt, wantAt)
	}

	quiet, ok := byName["quiet"]
	if !ok {
		t.Fatalf("agent %q missing from Status: %+v", "quiet", agents)
	}
	if quiet.LastReportAt != "" {
		t.Errorf("never-reported agent got LastReportAt = %q, want empty", quiet.LastReportAt)
	}

	corrupt, ok := byName["corrupt"]
	if !ok {
		t.Fatalf("agent %q missing from Status: %+v", "corrupt", agents)
	}
	if corrupt.LastReportAt != "garbage" {
		t.Errorf("corrupt.LastReportAt = %q, want %q passed through verbatim", corrupt.LastReportAt, "garbage")
	}
}
