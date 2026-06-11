package supervisor

// QUM-790 — wake accept-set: reject only {retired, retiring}; accept everything
// else (including the new StatusComplete from QUM-787 and any pre-launch
// Unstarted snapshot).

import (
	"context"
	"strings"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// TestWake_AcceptsComplete pins the QUM-790 contract: an agent whose durable
// disk Status is "complete" (per QUM-787) must be wakeable. Wake should attempt
// to resume the prior session (Resume=true, SessionID carried from snapshot)
// just like any other offline-but-revivable status.
func TestWake_AcceptsComplete(t *testing.T) {
	runWakeAcceptanceTest(t, state.StatusComplete)
}

// TestWake_RejectsRetired pins QUM-790: a retired agent must NOT be wakeable.
// Wake must return an error mentioning "retired" and must not invoke the
// starter at all.
func TestWake_RejectsRetired(t *testing.T) {
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{}
	agent := testAgentState("alice")
	agent.Status = state.StatusRetired
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	res, err := rt.Wake(context.Background(), "")
	if err == nil {
		t.Fatalf("Wake on retired agent: err=nil, want non-nil reject")
	}
	if res != nil {
		t.Errorf("WakeResult on retired = %+v, want nil", res)
	}
	if !strings.Contains(err.Error(), "retired") {
		t.Errorf("Wake error = %v, want one mentioning %q", err, "retired")
	}
	if got := starter.callCount(); got != 0 {
		t.Errorf("starter.Start calls = %d, want 0 (must reject before invoking starter)", got)
	}
}

// TestWake_RejectsRetiring pins QUM-790: a retiring agent (in-flight teardown)
// must NOT be wakeable.
func TestWake_RejectsRetiring(t *testing.T) {
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{}
	agent := testAgentState("alice")
	agent.Status = state.StatusRetiring
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	res, err := rt.Wake(context.Background(), "")
	if err == nil {
		t.Fatalf("Wake on retiring agent: err=nil, want non-nil reject")
	}
	if res != nil {
		t.Errorf("WakeResult on retiring = %+v, want nil", res)
	}
	if !strings.Contains(err.Error(), "retiring") {
		t.Errorf("Wake error = %v, want one mentioning %q", err, "retiring")
	}
	if got := starter.callCount(); got != 0 {
		t.Errorf("starter.Start calls = %d, want 0 (must reject before invoking starter)", got)
	}
}
