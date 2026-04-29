package cmd

import (
	"github.com/dmotles/sprawl/internal/agentloop"
	backend "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

func buildAgentSessionSpec(agentState *state.AgentState, promptPath, sprawlRoot string) backend.SessionSpec {
	return agentloop.BuildAgentSessionSpec(agentState, promptPath, sprawlRoot, nil)
}

func newClaudeBackendProcess(spec backend.SessionSpec, observer agentloop.Observer) processManager {
	return agentloop.NewClaudeBackendProcess(spec, observer)
}
