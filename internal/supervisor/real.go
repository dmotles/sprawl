package supervisor

import (
	"context"
	"fmt"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
)

// Config holds configuration for the real supervisor.
type Config struct {
	SprawlRoot string
	CallerName string
}

// Real is the production implementation of Supervisor.
type Real struct {
	sprawlRoot string
	callerName string
}

// NewReal creates a new real supervisor.
func NewReal(cfg Config) *Real {
	return &Real{
		sprawlRoot: cfg.SprawlRoot,
		callerName: cfg.CallerName,
	}
}

func (r *Real) Status(_ context.Context) ([]AgentInfo, error) {
	agents, err := state.ListAgents(r.sprawlRoot)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}

	result := make([]AgentInfo, 0, len(agents))
	for _, a := range agents {
		result = append(result, AgentInfo{
			Name:   a.Name,
			Type:   a.Type,
			Family: a.Family,
			Parent: a.Parent,
			Status: a.Status,
			Branch: a.Branch,
		})
	}
	return result, nil
}

func (r *Real) Delegate(_ context.Context, agentName, task string) error {
	agentState, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	switch agentState.Status {
	case "killed", "retired", "retiring":
		return fmt.Errorf("cannot delegate to agent %q: status is %q", agentName, agentState.Status)
	}

	if task == "" {
		return fmt.Errorf("task prompt must not be empty")
	}

	_, err = state.EnqueueTask(r.sprawlRoot, agentName, task)
	if err != nil {
		return fmt.Errorf("enqueuing task: %w", err)
	}
	return nil
}

func (r *Real) Message(_ context.Context, agentName, subject, body string) error {
	_, err := state.LoadAgent(r.sprawlRoot, agentName)
	if err != nil {
		return fmt.Errorf("agent %q not found: %w", agentName, err)
	}

	return messages.Send(r.sprawlRoot, r.callerName, agentName, subject, body)
}

func (r *Real) Spawn(_ context.Context, _ SpawnRequest) (*AgentInfo, error) {
	return nil, fmt.Errorf("spawn not yet implemented in TUI supervisor")
}

func (r *Real) Merge(_ context.Context, _, _ string, _ bool) error {
	return fmt.Errorf("merge not yet implemented in TUI supervisor")
}

func (r *Real) Retire(_ context.Context, _ string, _, _ bool) error {
	return fmt.Errorf("retire not yet implemented in TUI supervisor")
}

func (r *Real) Kill(_ context.Context, _ string) error {
	return fmt.Errorf("kill not yet implemented in TUI supervisor")
}

func (r *Real) Shutdown(_ context.Context) error {
	return nil
}
