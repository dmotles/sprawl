package supervisor

import "context"

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
}
