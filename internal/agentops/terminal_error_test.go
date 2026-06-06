// Tests for QUM-680: agentops.TerminalAgentError helper.
//
// RED phase: TerminalAgentError does not exist yet — these tests must fail to
// compile (or, after stub creation, fail at runtime). The helper inspects an
// agent's persisted state and returns a descriptive error iff the agent is in
// a terminal lifecycle status (stopped / faulted / retired / killed). For all
// other cases — including missing state JSON, which preserves the QUM-404
// "missing JSON" semantics — it returns nil so callers can proceed with their
// existing reconcile / not-found behavior.
package agentops

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

func TestTerminalAgentError(t *testing.T) {
	cases := []struct {
		name           string
		seed           *state.AgentState // nil => do not write state file
		wantErr        bool
		mustContain    []string
		mustNotContain []string
	}{
		{
			name:    "missing JSON returns nil (QUM-404 semantics preserved)",
			seed:    nil,
			wantErr: false,
		},
		{
			name: "active status returns nil",
			seed: &state.AgentState{
				Name:   "alice",
				Status: state.StatusActive,
			},
			wantErr: false,
		},
		{
			name: "running status returns nil",
			seed: &state.AgentState{
				Name:   "alice",
				Status: state.StatusRunning,
			},
			wantErr: false,
		},
		{
			name: "suspended status returns nil",
			seed: &state.AgentState{
				Name:   "alice",
				Status: state.StatusSuspended,
			},
			wantErr: false,
		},
		{
			name: "stopped status returns descriptive error",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusStopped,
				LastReportState: "complete",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: true,
			mustContain: []string{
				`agent "alice"`,
				"complete",
				"2026-06-06T12:00:00Z",
				"no longer running",
			},
		},
		{
			name: "faulted status returns descriptive error",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusFaulted,
				LastReportState: "failure",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: true,
			mustContain: []string{
				`agent "alice"`,
				"failure",
				"2026-06-06T12:00:00Z",
				"no longer running",
			},
		},
		{
			name: "retired status returns descriptive error",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusRetired,
				LastReportState: "complete",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: true,
			mustContain: []string{
				`agent "alice"`,
				"complete",
				"no longer running",
			},
		},
		{
			name: "killed status returns descriptive error",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusKilled,
				LastReportState: "failure",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: true,
			mustContain: []string{
				`agent "alice"`,
				"failure",
				"no longer running",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			if tc.seed != nil {
				if err := state.SaveAgent(tmpDir, tc.seed); err != nil {
					t.Fatalf("SaveAgent: %v", err)
				}
			}

			err := TerminalAgentError(tmpDir, "alice")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("TerminalAgentError: got nil, want non-nil error")
				}
				for _, s := range tc.mustContain {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q missing substring %q", err.Error(), s)
					}
				}
				for _, s := range tc.mustNotContain {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q must not contain %q", err.Error(), s)
					}
				}
			} else if err != nil {
				t.Fatalf("TerminalAgentError: got error %v, want nil", err)
			}
		})
	}
}
