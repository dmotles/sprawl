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
			// QUM-787: a state file seeded with Status=stopped +
			// LastReportState=complete is migrated to Status=complete on
			// LoadAgent (stopped is no longer a write target). Complete
			// is NOT in the TerminalAgentError set per the QUM-786
			// lifecycle arc — it is revivable via wake/delegate — so
			// TerminalAgentError returns nil.
			name: "stopped+complete migrates to complete; not terminal",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusStopped,
				LastReportState: "complete",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: false,
		},
		{
			// QUM-789: faulted is wake_if_offline-recoverable (revivable),
			// NOT terminal. The QUM-726 gate at the Real.Delegate /
			// Real.SendMessage layer handles it.
			name: "faulted status returns nil (revivable via wake_if_offline)",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusFaulted,
				LastReportState: "failure",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: false,
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
			// QUM-789: retiring is terminal (parent-decided permanent state).
			name: "retiring status returns descriptive error",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusRetiring,
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
			// QUM-789: killed is wake_if_offline-recoverable, NOT terminal.
			name: "killed status returns nil (revivable via wake_if_offline)",
			seed: &state.AgentState{
				Name:            "alice",
				Status:          state.StatusKilled,
				LastReportState: "failure",
				LastReportAt:    "2026-06-06T12:00:00Z",
			},
			wantErr: false,
		},
		{
			// QUM-739: TerminalAgentError is intentionally narrower than
			// state.IsTerminal — died has its own QUM-725 route-up handling
			// on the send_message path and must NOT short-circuit here.
			name: "died status returns nil (route-up handles it)",
			seed: &state.AgentState{
				Name:   "alice",
				Status: state.StatusDied,
			},
			wantErr: false,
		},
		{
			// QUM-739: paused is wake-able, NOT terminal.
			name: "paused status returns nil",
			seed: &state.AgentState{
				Name:   "alice",
				Status: state.StatusPaused,
			},
			wantErr: false,
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
