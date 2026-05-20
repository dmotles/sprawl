package supervisor

import (
	"context"
	"testing"
	"time"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
)

// ctxRecordingStarter is a RuntimeStarter that records the ctx it was
// invoked with so the QUM-606 R1 test can prove AgentRuntime.Recover
// passes a non-cancellable ctx down to the starter.
type ctxRecordingStarter struct {
	startCalls   int
	lastCtx      context.Context
	sessionMaker func(call int) *runtimeTestSession
}

func (s *ctxRecordingStarter) Start(ctx context.Context, spec RuntimeStartSpec) (RuntimeHandle, error) {
	s.startCalls++
	s.lastCtx = ctx
	if s.sessionMaker != nil {
		return s.sessionMaker(s.startCalls), nil
	}
	return &runtimeTestSession{
		sessionID: "sess-" + spec.Name,
		caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
	}, nil
}

// TestRuntime_Recover_DoesNotKillNewHandleOnCallerCtxCancel pins QUM-606
// R1's structural contract: the ctx threaded into starter.Start by
// AgentRuntime.Recover must NOT be the caller's ctx, because production
// realStarter.Start binds the subprocess to that ctx via
// exec.CommandContext — forwarding the caller's ctx would SIGKILL the
// new claude subprocess the moment the MCP toolRecover return cancels
// the ctx.
//
// Proof shape: invoke Recover with a cancellable callerCtx, then cancel
// it; the ctx the starter observed must NOT be Done. That can only hold
// if Recover handed a detached ctx (context.Background or similar) to
// starter.Start.
func TestRuntime_Recover_DoesNotKillNewHandleOnCallerCtxCancel(t *testing.T) {
	// Per-call sessions so the recover path gets a fresh handle distinct
	// from the initial one.
	starter := &ctxRecordingStarter{
		sessionMaker: func(call int) *runtimeTestSession {
			return &runtimeTestSession{
				sessionID: "sess-alice",
				caps:      backendpkg.Capabilities{SupportsInterrupt: true, SupportsResume: true},
			}
		},
	}
	rt := NewAgentRuntime(AgentRuntimeConfig{
		SprawlRoot: "/repo",
		Agent:      testAgentState("alice"),
		Starter:    starter,
	})
	// Shorten the post-Start health probe timeout so the test runs fast
	// even though our test session never emits a non-init protocol frame.
	prevTimeout := recoverHealthProbeTimeout
	recoverHealthProbeTimeout = 100 * time.Millisecond
	t.Cleanup(func() { recoverHealthProbeTimeout = prevTimeout })

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	// Poison the live handle so Recover proceeds past the
	// IsTerminallyFaulted gate.
	rt.mu.RLock()
	first := rt.handle.(*runtimeTestSession)
	rt.mu.RUnlock()
	first.terminallyFaulted = true

	callerCtx, callerCancel := context.WithCancel(context.Background())
	if err := rt.Recover(callerCtx); err != nil {
		callerCancel()
		t.Fatalf("Recover: %v", err)
	}
	// Cancel the caller's ctx AFTER Recover returns and assert the ctx
	// observed by the starter is NOT Done. If Recover had forwarded the
	// caller's ctx, callerCancel() would Done() starter.lastCtx too.
	callerCancel()

	if starter.startCalls < 2 {
		t.Fatalf("starter.startCalls = %d, want >= 2 (initial + recover)", starter.startCalls)
	}
	select {
	case <-starter.lastCtx.Done():
		t.Fatalf("starter ctx Done after caller ctx cancel — Recover forwarded the caller ctx (QUM-606 R1 regression)")
	default:
		// Expected: starter received a detached ctx.
	}
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
	starter := &ctxRecordingStarter{
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

	if err := rt.Start(context.Background()); err != nil {
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
