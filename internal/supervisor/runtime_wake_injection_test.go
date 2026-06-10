// QUM-726: AgentRuntime.Wake must thread the supplied restartInjection
// through both the resume-attempt spec AND the fresh-fallback spec.
//
// RED phase: the runtime.Wake implementation currently ignores the new
// `restartInjection` parameter (it accepts it for the signature, but
// neither spec carries the value). These tests fail until both specs are
// updated.
package supervisor

import (
	"context"
	"testing"

	"github.com/dmotles/sprawl/internal/state"
)

// TestRuntimeWake_PlumbsRestartInjection_ResumeSpec asserts the resume
// attempt's spec carries the injection.
func TestRuntimeWake_PlumbsRestartInjection_ResumeSpec(t *testing.T) {
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{}
	agent := testAgentState("alice")
	agent.Status = state.StatusPaused
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	const injection = "INJECTION-TEXT"
	res, err := rt.Wake(context.Background(), injection)
	if err != nil {
		t.Fatalf("Wake: %v", err)
	}
	if res == nil {
		t.Fatal("Wake returned nil result")
	}

	specs := starter.snapshotSpecs()
	if len(specs) == 0 {
		t.Fatal("starter received zero specs")
	}
	if specs[0].RestartInjection != injection {
		t.Errorf("resume spec.RestartInjection = %q, want %q", specs[0].RestartInjection, injection)
	}
}

// TestRuntimeWake_PlumbsRestartInjection_FreshFallbackSpec asserts the
// fresh-fallback spec ALSO carries the injection: an agent that wakes via
// the fallback path still needs the wake notice as its first turn.
func TestRuntimeWake_PlumbsRestartInjection_FreshFallbackSpec(t *testing.T) {
	shortenWakeTimeouts(t)

	starter := &wakeCapturingStarter{
		fireResumeFailOn: 1,
	}
	agent := testAgentState("alice")
	agent.Status = state.StatusPaused
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: t.TempDir(),
		Agent:      agent,
		Starter:    starter,
	})

	const injection = "INJECTION-TEXT"
	if _, err := rt.Wake(context.Background(), injection); err != nil {
		t.Fatalf("Wake: %v", err)
	}

	specs := starter.snapshotSpecs()
	if len(specs) < 2 {
		t.Fatalf("captured %d specs, want >= 2 (resume + fallback fresh)", len(specs))
	}
	if specs[0].RestartInjection != injection {
		t.Errorf("resume spec.RestartInjection = %q, want %q", specs[0].RestartInjection, injection)
	}
	if specs[1].RestartInjection != injection {
		t.Errorf("fresh-fallback spec.RestartInjection = %q, want %q", specs[1].RestartInjection, injection)
	}
}
