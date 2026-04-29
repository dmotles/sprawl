package cmd

import (
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

func createTestAgent(t *testing.T, sprawlRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
}
