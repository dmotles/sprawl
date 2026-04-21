package supervisor

import (
	"context"

	"github.com/dmotles/sprawl/internal/agentloop"
)

// AgentInfo describes an agent's current state as seen by the supervisor.
type AgentInfo struct {
	Name              string `json:"name"`
	Type              string `json:"type"`
	Family            string `json:"family"`
	Parent            string `json:"parent"`
	Status            string `json:"status"`
	Branch            string `json:"branch"`
	LastReportState   string `json:"last_report_state,omitempty"`
	LastReportSummary string `json:"last_report_summary,omitempty"`
}

// SendAsyncResult is returned by Supervisor.SendAsync. See
// docs/designs/messaging-overhaul.md §4.2.1.
type SendAsyncResult struct {
	MessageID string `json:"message_id"`
	QueuedAt  string `json:"queued_at"` // RFC3339
}

// LastReport is the structured last_report_* block from an agent's state.
type LastReport struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
	At      string `json:"at,omitempty"`
	State   string `json:"state,omitempty"`  // working, blocked, complete, failure
	Detail  string `json:"detail,omitempty"` // long-form detail, optional
}

// ReportStatusResult is returned by Supervisor.ReportStatus.
type ReportStatusResult struct {
	ReportedAt string `json:"reported_at"` // RFC3339
}

// PeekResult is returned by Supervisor.Peek. See
// docs/designs/messaging-overhaul.md §4.2.4.
type PeekResult struct {
	Status     string                    `json:"status"`
	LastReport LastReport                `json:"last_report"`
	Activity   []agentloop.ActivityEntry `json:"activity"`
}

// SpawnRequest holds parameters for spawning a new agent.
type SpawnRequest struct {
	Family string `json:"family"`
	Type   string `json:"type"`
	Prompt string `json:"prompt"`
	Branch string `json:"branch"`
}

// Supervisor manages agent lifecycle. All methods are safe for concurrent use.
type Supervisor interface {
	Spawn(ctx context.Context, req SpawnRequest) (*AgentInfo, error)
	Status(ctx context.Context) ([]AgentInfo, error)
	Delegate(ctx context.Context, agentName, task string) error
	Message(ctx context.Context, agentName, subject, body string) error
	Merge(ctx context.Context, agentName, message string, noValidate bool) error
	Retire(ctx context.Context, agentName string, merge, abandon bool) error
	Kill(ctx context.Context, agentName string) error
	Shutdown(ctx context.Context) error

	// Handoff persists a session summary (marked Handoff=true) for the
	// current weave session and writes the handoff-signal file consumed by
	// FinalizeHandoff. On success, it fires the HandoffRequested channel so
	// a host (e.g. the TUI) can tear down and restart the current session.
	// Returns an error for empty summaries or when session state is missing.
	Handoff(ctx context.Context, summary string) error

	// HandoffRequested returns a channel that receives one value each time
	// Handoff completes successfully. Consumers use it to trigger session
	// restart without blocking the MCP tool response.
	HandoffRequested() <-chan struct{}

	// PeekActivity returns up to `tail` of the most recent activity
	// entries recorded for the named agent, oldest-first. See
	// docs/designs/messaging-overhaul.md §4.4. A missing agent (no
	// activity file yet) yields an empty slice and nil error.
	PeekActivity(ctx context.Context, agentName string, tail int) ([]agentloop.ActivityEntry, error)

	// SendAsync queues a message for `to` via Maildir persist + harness
	// queue append-only log. Non-blocking: returns as soon as both writes
	// succeed. See docs/designs/messaging-overhaul.md §4.2.1.
	SendAsync(ctx context.Context, to, subject, body, replyTo string, tags []string) (*SendAsyncResult, error)

	// Peek returns an agent's status, last report, and the tail of its
	// activity ring in one call. See §4.2.4.
	Peek(ctx context.Context, agentName string, tail int) (*PeekResult, error)

	// ReportStatus is the canonical status channel: persists the reporter's
	// LastReport* fields, flips Status for complete/failure, and delivers a
	// structured async notification to the reporter's parent. See
	// docs/designs/messaging-overhaul.md §4.2.3. The reporter identity is
	// the supervisor's caller (r.callerName) when agentName is empty.
	ReportStatus(ctx context.Context, agentName, state, summary, detail string) (*ReportStatusResult, error)
}
