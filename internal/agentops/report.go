package agentops

import (
	"fmt"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// Report state values — matches the enum in docs/designs/messaging-overhaul.md §4.2.3.
const (
	ReportStateWorking  = "working"
	ReportStateBlocked  = "blocked"
	ReportStateComplete = "complete"
	ReportStateFailure  = "failure"
)

// ReportResult is returned by Report.
type ReportResult struct {
	ReportedAt string // RFC3339
	// MessageID is retained as a deprecated, always-empty field. QUM-559:
	// Report no longer writes to the maildir or harness queue — the
	// supervisor owns parent notification via the in-process ephemeral
	// ring.
	MessageID string
}

// ReportDeps holds injectable dependencies for Report. Nil fields default to
// the production implementation. QUM-559: SendMessage and Enqueue were
// removed; Report is state-only.
type ReportDeps struct {
	LoadAgent func(sprawlRoot, name string) (*state.AgentState, error)
	SaveAgent func(sprawlRoot string, agent *state.AgentState) error
	Now       func() time.Time
}

func (d *ReportDeps) loadAgent(sprawlRoot, name string) (*state.AgentState, error) {
	if d.LoadAgent != nil {
		return d.LoadAgent(sprawlRoot, name)
	}
	return state.LoadAgent(sprawlRoot, name)
}

func (d *ReportDeps) saveAgent(sprawlRoot string, a *state.AgentState) error {
	if d.SaveAgent != nil {
		return d.SaveAgent(sprawlRoot, a)
	}
	return state.SaveAgent(sprawlRoot, a)
}

func (d *ReportDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// legacyType maps a report state to the back-compat LastReportType token
// (status/done/problem) used by pre-QUM-295 consumers.
func legacyType(state string) string {
	switch state {
	case ReportStateComplete:
		return "done"
	case ReportStateFailure:
		return "problem"
	default:
		return "status"
	}
}

// ValidReportState returns true for recognized report states.
func ValidReportState(state string) bool {
	switch state {
	case ReportStateWorking, ReportStateBlocked, ReportStateComplete, ReportStateFailure:
		return true
	}
	return false
}

// Report is the canonical persistence path for agent status reports (the
// `report_status` MCP tool delegates here).
//
// QUM-559: Report is state-only — it loads the reporter's agent state,
// updates the LastReport* fields, and persists. The supervisor owns parent
// notification via the in-process ephemeral status-notification ring; no
// maildir or harness queue write happens here.
//
// QUM-625 M4: Report no longer touches the Status field. Status is a pure
// liveness axis; the report outcome (complete/failure) lives solely on
// LastReportState (and the back-compat LastReportType token).
//
// QUM-668 partially reverses the M4 stance for TERMINAL outcomes: when the
// outcome is complete/failure, Report atomically flips Status to the matching
// terminal liveness (stopped/faulted) in the same save so a subsequent boot
// never observes an "active" zombie next to a terminal LastReportState.
//
// See docs/designs/messaging-overhaul.md §4.2.3 / §4.7.
func Report(deps *ReportDeps, sprawlRoot, agentName, stateVal, summary string) (ReportResult, error) {
	if deps == nil {
		deps = &ReportDeps{}
	}
	if agentName == "" {
		return ReportResult{}, fmt.Errorf("agent name must not be empty")
	}
	if !ValidReportState(stateVal) {
		return ReportResult{}, fmt.Errorf("invalid report state %q: must be one of working, blocked, complete, failure", stateVal)
	}
	if strings.TrimSpace(summary) == "" {
		return ReportResult{}, fmt.Errorf("summary must not be empty")
	}

	agentState, err := deps.loadAgent(sprawlRoot, agentName)
	if err != nil {
		return ReportResult{}, fmt.Errorf("loading agent state: %w", err)
	}

	reportedAt := deps.now().UTC().Format(time.RFC3339)
	agentState.LastReportState = stateVal
	agentState.LastReportType = legacyType(stateVal)
	agentState.LastReportMessage = summary
	agentState.LastReportAt = reportedAt

	// QUM-668: terminal report outcomes drive the liveness Status to its
	// terminal value atomically in the same save. Non-terminal reports
	// (working/blocked) leave Status untouched. This partially reverses
	// QUM-625 M4 — outcome still lives on LastReportState, but terminal
	// outcomes also drive the Status axis to a terminal liveness so that
	// (a) MCP tools can give a clear "no longer running" error and
	// (b) the resume filter never has to second-guess an active-but-done
	// agent.
	switch stateVal {
	case ReportStateComplete:
		// QUM-787: state=complete lands the agent in StatusComplete
		// (revivable per the QUM-786 lifecycle arc). StatusStopped is no
		// longer a write target.
		agentState.Status = state.StatusComplete
	case ReportStateFailure:
		agentState.Status = state.StatusFaulted
	}

	if err := deps.saveAgent(sprawlRoot, agentState); err != nil {
		return ReportResult{}, fmt.Errorf("saving agent state: %w", err)
	}

	return ReportResult{ReportedAt: reportedAt}, nil
}
