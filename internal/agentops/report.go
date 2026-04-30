package agentops

import (
	"fmt"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/messages"
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
	MessageID  string // harness queue entry id (empty if no parent)
}

// ReportDeps holds injectable dependencies for Report. Nil fields default to
// the production implementation.
type ReportDeps struct {
	LoadAgent   func(sprawlRoot, name string) (*state.AgentState, error)
	SaveAgent   func(sprawlRoot string, agent *state.AgentState) error
	SendMessage func(sprawlRoot, from, to, subject, body string, opts ...messages.SendOption) error
	Enqueue     func(sprawlRoot, to string, e agentloop.Entry) (agentloop.Entry, error)
	Now         func() time.Time
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

func (d *ReportDeps) sendMessage(sprawlRoot, from, to, subject, body string, opts ...messages.SendOption) error {
	if d.SendMessage != nil {
		return d.SendMessage(sprawlRoot, from, to, subject, body, opts...)
	}
	return messages.Send(sprawlRoot, from, to, subject, body, opts...)
}

func (d *ReportDeps) enqueue(sprawlRoot, to string, e agentloop.Entry) (agentloop.Entry, error) {
	if d.Enqueue != nil {
		return d.Enqueue(sprawlRoot, to, e)
	}
	return agentloop.Enqueue(sprawlRoot, to, e)
}

func (d *ReportDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// subjectToken returns the canonical subject prefix for a report state.
func subjectToken(state string) string {
	switch state {
	case ReportStateWorking:
		return "STATUS"
	case ReportStateBlocked:
		return "BLOCKED"
	case ReportStateComplete:
		return "COMPLETE"
	case ReportStateFailure:
		return "FAILURE"
	}
	return strings.ToUpper(state)
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

// Report is the canonical persistence path for agent status reports (both
// `sprawl report` CLI and the `report_status` MCP tool delegate here).
//
// It loads the reporter's agent state, updates the LastReport* fields and
// (for complete/failure) the Status field, persists, and — if the reporter
// has a parent — delivers the report to the parent via Maildir + harness
// queue (async class) so the notification survives without tmux send-keys.
//
// See docs/designs/messaging-overhaul.md §4.2.3 / §4.7.
func Report(deps *ReportDeps, sprawlRoot, agentName, stateVal, summary, detail string) (ReportResult, error) {
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
	agentState.LastReportDetail = detail
	agentState.LastReportAt = reportedAt

	switch stateVal {
	case ReportStateComplete:
		agentState.Status = "done"
	case ReportStateFailure:
		agentState.Status = "problem"
	}

	if err := deps.saveAgent(sprawlRoot, agentState); err != nil {
		return ReportResult{}, fmt.Errorf("saving agent state: %w", err)
	}

	result := ReportResult{ReportedAt: reportedAt}
	if agentState.Parent == "" {
		return result, nil
	}

	token := subjectToken(stateVal)
	subject := fmt.Sprintf("[%s] %s → %s", token, agentState.Name, summary)
	body := summary
	if strings.TrimSpace(detail) != "" {
		body = summary + "\n\n" + detail
	}

	if err := deps.sendMessage(sprawlRoot, agentState.Name, agentState.Parent, subject, body); err != nil {
		// Delivery failure is non-fatal — state is persisted.
		return result, fmt.Errorf("sending message to parent: %w", err)
	}

	entry, err := deps.enqueue(sprawlRoot, agentState.Parent, agentloop.Entry{
		Class:   agentloop.ClassAsync,
		From:    agentState.Name,
		Subject: subject,
		Body:    body,
		Tags:    []string{"status", stateVal},
	})
	if err != nil {
		return result, fmt.Errorf("enqueuing async report: %w", err)
	}
	result.MessageID = entry.ID
	return result, nil
}
