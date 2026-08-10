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
// StatusStopped is retained as a parseable token but never a write target;
// LoadAgent migrates it to suspended on read (QUM-1186) so we won't observe
// it here in practice.
func TerminalAgentError(sprawlRoot, name string) error {
	st, err := state.LoadAgent(sprawlRoot, name)
	if err != nil {
		return nil
	}
	if !state.IsTerminal(st.Status) {
		return nil
	}
	// QUM-1186: was "agent %q reported %s at %s; no longer running", built
	// from the deleted LastReportState/LastReportAt. Both the state and the
	// timestamp came from the agent's own self-report, so neither survives.
	// The Status is an OBSERVED fact and is what the message names now.
	// "no longer running" is kept verbatim — it is the phrase callers and the
	// e2e suite match on.
	return fmt.Errorf("agent %q is %s; no longer running", name, st.Status)
}
