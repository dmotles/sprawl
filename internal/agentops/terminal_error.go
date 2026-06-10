package agentops

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/state"
)

// TerminalAgentError returns a clearer error when an MCP tool targets an
// agent whose persisted Status is terminal (stopped/faulted/retired/killed)
// and no live runtime is registered. Callers MUST only invoke it when the
// live runtime is absent.
//
// Returns nil if state.LoadAgent fails (preserves the QUM-404 missing-JSON
// path) or if the persisted Status is not terminal. QUM-680.
func TerminalAgentError(sprawlRoot, name string) error {
	st, err := state.LoadAgent(sprawlRoot, name)
	if err != nil {
		return nil
	}
	// Intentionally narrower than state.IsTerminal (QUM-739): `died` and
	// `resume_failed` have their own handling on the send_message path
	// (QUM-725 dead-routing routes up to a live ancestor; QUM-708 wake) and
	// must NOT short-circuit here.
	switch st.Status {
	case state.StatusStopped, state.StatusFaulted, state.StatusRetired, state.StatusKilled:
	default:
		return nil
	}
	reportState := st.LastReportState
	if reportState == "" {
		reportState = st.Status
	}
	at := st.LastReportAt
	if at == "" {
		at = "unknown time"
	}
	return fmt.Errorf("agent %q reported %s at %s; no longer running", name, reportState, at)
}
