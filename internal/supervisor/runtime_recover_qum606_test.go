package supervisor

import (
	"context"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

// recoverRecordingStarter is a RuntimeStarter that counts Start calls and
// hands out per-call sessions. Pre-QUM-612 this type also recorded the ctx
// passed to Start so a dynamic test could prove AgentRuntime.Recover did not
// forward the caller's ctx (QUM-606 R1). QUM-612 dropped the ctx parameter
// from RuntimeStarter.Start entirely, so the QUM-606 R1 invariant is now
// enforced at the type level — no production caller can pass a request-scoped
// ctx to Start because the signature does not accept one. The dynamic test
// is therefore retired (see git history for the prior assertion).
type recoverRecordingStarter struct {
	startCalls   int
	sessionMaker func(call int) *runtimeTestSession
}

func (s *recoverRecordingStarter) Start(spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.startCalls++
	if s.sessionMaker != nil {
		return s.sessionMaker(s.startCalls), nil
	}
	return &runtimeTestSession{
		sessionID: "sess-" + spec.Name,
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}, nil
}

// TestAgentRuntime_Recover_FlipsLifecycleStoppedOnFault pins QUM-606 R4:
// when starter.Start returns success but the new handle is born
// terminally-faulted (e.g. --resume cookie rejected, or the resumed
// transcript immediately re-wedges), Recover MUST:
//   - tear the new handle down (StopAbandon),
//   - return a non-nil error to the caller,
//   - flip Lifecycle to Stopped,
//   - emit RuntimeEventStopped so the TUI fault banner re-fires.
func TestAgentRuntime_Recover_FlipsLifecycleStoppedOnFault(t *testing.T) {
	prevTimeout := recoverHealthProbeTimeout
	prevStop := recoverStopAbandonTimeout
	recoverHealthProbeTimeout = 300 * time.Millisecond
	recoverStopAbandonTimeout = 1 * time.Second
	t.Cleanup(func() {
		recoverHealthProbeTimeout = prevTimeout
		recoverStopAbandonTimeout = prevStop
	})

	// First call returns a healthy session; second (recover-path) returns
	// a pre-faulted session so the health probe fails.
	starter := &recoverRecordingStarter{
		sessionMaker: func(call int) *runtimeTestSession {
			sess := &runtimeTestSession{
				sessionID: "sess-bob",
				caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
			}
			if call >= 2 {
				sess.terminallyFaulted = true
			}
			return sess
		},
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("bob"),
		Starter:    starter,
	})

	events, unsub := rt.Subscribe(16)
	defer unsub()

	if err := rt.Start(); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	// Poison the live handle so Recover proceeds.
	rt.mu.RLock()
	first := rt.handle.(*runtimeTestSession)
	rt.mu.RUnlock()
	first.terminallyFaulted = true

	err := rt.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover returned nil error but the recover-path handle is pre-faulted; want non-nil error (QUM-606 R4)")
	}

	if got := rt.Snapshot().Lifecycle; got != RuntimeLifecycleStopped {
		t.Errorf("Lifecycle after failed Recover = %q, want %q", got, RuntimeLifecycleStopped)
	}

	if starter.startCalls < 2 {
		t.Fatalf("starter.startCalls = %d, want >= 2", starter.startCalls)
	}

	deadline := time.After(2 * time.Second)
	sawStopped := false
loop:
	for {
		select {
		case ev := <-events:
			if ev.Kind == RuntimeEventStopped {
				sawStopped = true
				break loop
			}
		case <-deadline:
			break loop
		}
	}
	if !sawStopped {
		t.Errorf("subscriber never saw RuntimeEventStopped after failed Recover")
	}
}
