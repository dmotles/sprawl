package agentops

import (
	"fmt"

	"github.com/dmotles/sprawl/internal/state"
)

// TerminalAgentError returns a clearer error when an MCP tool targets an
// agent whose persisted Status is truly terminal (retired/retiring) and no
// live runtime is registered. Callers MUST only invoke it when the live
// runtime is absent.
//
// Returns nil if state.LoadAgent fails (preserves the QUM-404 missing-JSON
// path) or if the persisted Status is not terminal. QUM-680, narrowed by
// QUM-789 lifecycle arc #2.
//
// QUM-789: the set is now exactly state.IsTerminal — {retired, retiring}.
// StatusComplete is revivable (delegate/send_message auto-wake it).
// StatusFaulted / StatusKilled / StatusDied / StatusPaused /
// StatusResumeFailed are revivable via the QUM-726 wake_if_offline gate and
// must NOT short-circuit here so they remain introspectable via peek.
// StatusStopped is retained as a parseable token but never a write target
// post-QUM-787; LoadAgent migrates it to complete/faulted on read so we
// won't observe it here in practice.
func TerminalAgentError(sprawlRoot, name string) error {
	st, err := state.LoadAgent(sprawlRoot, name)
	if err != nil {
		return nil
	}
	if !state.IsTerminal(st.Status) {
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
