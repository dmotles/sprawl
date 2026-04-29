package cmd

import (
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/rootinit"
	"github.com/dmotles/sprawl/internal/state"
)

func newTestKillDeps(t *testing.T) (*killDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()
	deps := &killDeps{
		Getenv: func(key string) string {
			if key == "SPRAWL_ROOT" {
				return tmpDir
			}
			return ""
		},
	}
	return deps, tmpDir
}

func saveKillTestAgent(t *testing.T, sprawlRoot string, agent *state.AgentState) {
	t.Helper()
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
}

func TestKill_InvalidAgentNameReturnsError(t *testing.T) {
	deps, _ := newTestKillDeps(t)
	err := runKill(deps, "../evil", false)
	if err == nil {
		t.Fatal("expected invalid agent name error")
	}
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Fatalf("error = %q, want invalid agent name", err)
	}
}

func TestKill_FailsClosedWhenLiveWeaveSessionOwnsRuntimes(t *testing.T) {
	deps, tmpDir := newTestKillDeps(t)
	saveKillTestAgent(t, tmpDir, &state.AgentState{Name: "alice", Status: "active"})

	lock, err := rootinit.AcquireWeaveLock(tmpDir)
	if err != nil {
		t.Fatalf("AcquireWeaveLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	err = runKill(deps, "alice", false)
	if err == nil {
		t.Fatal("expected standalone kill rejection")
	}
	if !strings.Contains(err.Error(), "sprawl enter") || !strings.Contains(err.Error(), "sprawl_kill") {
		t.Fatalf("error = %q, want sprawl enter + sprawl_kill guidance", err)
	}
}

func TestKill_HappyPathMarksAgentKilled(t *testing.T) {
	deps, tmpDir := newTestKillDeps(t)
	saveKillTestAgent(t, tmpDir, &state.AgentState{Name: "alice", Status: "active"})

	if err := runKill(deps, "alice", false); err != nil {
		t.Fatalf("runKill() error: %v", err)
	}

	agentState, err := state.LoadAgent(tmpDir, "alice")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if agentState.Status != "killed" {
		t.Fatalf("status = %q, want killed", agentState.Status)
	}
}

func TestKill_AlreadyKilledIsIdempotent(t *testing.T) {
	deps, tmpDir := newTestKillDeps(t)
	saveKillTestAgent(t, tmpDir, &state.AgentState{Name: "alice", Status: "killed"})

	if err := runKill(deps, "alice", false); err != nil {
		t.Fatalf("runKill() error: %v", err)
	}
}
