package supervisor

import (
	"context"

	"github.com/dmotles/sprawl/internal/agentloop"
)

// AgentInfo describes an agent's current state as seen by the supervisor.
type AgentInfo struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Family string `json:"family"`
	Parent string `json:"parent"`
	Status string `json:"status"`
	Branch string `json:"branch"`
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
}
