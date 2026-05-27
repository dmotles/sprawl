package supervisor

import (
	"context"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/supervisor/liveness"
)

// M1 process_alive reachability map (QUM-622, R4 gate).
//
// Real.Status feeds liveness.From(...) only the inputs M1 actually populates:
// the runtime's Lifecycle, its terminal-fault probe (IsTerminallyFaulted), and
// its InAutonomousTurn() probe. RuntimeState and DiskStatus are left empty in
// M1. Given those inputs, process_alive can only ever resolve to:
//
//	Unstarted (registered, never started) -> nil   (absent / unknown)
//	Running                               -> true
//	Running·AutonomousTurn                -> true
//	Faulted (faulted-but-started window)  -> false  (QUM-606)
//	Stopped                               -> false
//	Killed                                -> false
//	Retired                               -> false
//
// The projectable *transient* states — Stopping / Starting / Recovering — are
// NOT reachable from a unit test under M1, because they require RuntimeState
// (e.g. "interrupting") which M1 does not feed into the projection. They only
// become reachable once a later slice plumbs RuntimeState through. Therefore
// the representative transient/unknown -> nil guard is the Unstarted case,
// already covered by TestStatus_ProcessAliveTriStateComesFromRuntimeKnowledge's
// "unknown-agent". We deliberately do NOT fabricate a RuntimeState here to
// force a transient: that would test a code path M1 does not exercise.

// QUM-606 regression guard (M1 / QUM-622): a runtime whose backend session has
// been terminally faulted — but whose RuntimeHandle has NOT yet been torn down
// (Lifecycle still == started) — must report process_alive == false in
// Real.Status. Today Real.Status keys process_alive off Lifecycle alone, so it
// lies "true" during this faulted-but-not-stopped window. After M1, Status must
// derive process_alive from the liveness projection, which folds in the
// handle's IsTerminallyFaulted() probe.
func TestStatus_FaultedStartedRuntimeReportsProcessAliveFalse(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	agent := testAgentState("faulted-agent")
	saveTestAgent(t, tmpDir, agent)

	rt := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      agent,
		Starter: &runtimeTestStarter{
			session: &runtimeTestSession{
				sessionID:         "sess-faulted",
				caps:              backendpkg.Capabilities{SupportsInterrupt: true},
				terminallyFaulted: true,
				// doneCh nil: no watchHandleExit fires, Lifecycle stays started.
			},
		},
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	// Sanity: the runtime is still in the "lie window" — handle alive,
	// Lifecycle started, but the session underneath is terminally faulted.
	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Fatalf("precondition: Lifecycle = %q, want %q (need faulted-but-started window)", got, liveness.Running)
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	byName := make(map[string]AgentInfo, len(agents))
	for _, info := range agents {
		byName[info.Name] = info
	}

	info := byName["faulted-agent"]
	if info.ProcessAlive == nil {
		t.Fatalf("faulted-agent ProcessAlive = nil, want non-nil false (QUM-606)")
	}
	if *info.ProcessAlive {
		t.Fatalf("faulted-agent ProcessAlive = true, want false: a terminally faulted runtime must report process_alive=false even while its handle is still up (QUM-606)")
	}
}

// Sibling positive case (M1 / QUM-622): a healthy started runtime must report
// process_alive == true. Passes both today and after M1; guards against a
// false-negative regression where the liveness projection would wrongly mark a
// live runtime dead.
func TestStatus_RunningRuntimeReportsProcessAliveTrue(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	agent := testAgentState("running-agent")
	saveTestAgent(t, tmpDir, agent)

	rt := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      agent,
		Starter: &runtimeTestStarter{
			session: &runtimeTestSession{
				sessionID:         "sess-running",
				caps:              backendpkg.Capabilities{SupportsInterrupt: true},
				terminallyFaulted: false,
			},
		},
	})
	if err := rt.Start(); err != nil {
		t.Fatalf("runtime start: %v", err)
	}

	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Fatalf("precondition: Lifecycle = %q, want %q", got, liveness.Running)
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	byName := make(map[string]AgentInfo, len(agents))
	for _, info := range agents {
		byName[info.Name] = info
	}

	info := byName["running-agent"]
	if info.ProcessAlive == nil {
		t.Fatalf("running-agent ProcessAlive = nil, want non-nil true")
	}
	if !*info.ProcessAlive {
		t.Fatalf("running-agent ProcessAlive = false, want true: a healthy started runtime must report process_alive=true")
	}
}

// Gap 1 (M1 / QUM-622): a started runtime whose handle reports
// InAutonomousTurn()==true projects to liveness Running·AutonomousTurn, which
// must still yield process_alive == true. Passes today (Lifecycle=started ->
// true) but guards that the M1 liveness-projection rewrite keeps the
// autonomous sub-state alive rather than collapsing it to nil/false.
func TestStatus_AutonomousTurnRuntimeReportsProcessAliveTrue(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	agent := testAgentState("autonomous-agent")
	saveTestAgent(t, tmpDir, agent)

	// fakeInAutonomousTurnHandle (peek_inautonomousturn_test.go) implements the
	// optional InAutonomousTurn() bool probe that AgentRuntime.InAutonomousTurn
	// type-asserts against. RegisterRootRuntime AttachHandles it, so the
	// runtime's Lifecycle becomes started with a live handle reporting
	// autonomy==true.
	h := &fakeInAutonomousTurnHandle{
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		sessionID: "sess-autonomous",
		autonomy:  true,
	}
	rt, err := sup.RegisterRootRuntime("autonomous-agent", h, agent)
	if err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	if got := rt.Snapshot().Liveness; got != liveness.Running {
		t.Fatalf("precondition: Lifecycle = %q, want %q", got, liveness.Running)
	}
	if !rt.InAutonomousTurn() {
		t.Fatalf("precondition: InAutonomousTurn() = false, want true (handle reports autonomy)")
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	byName := make(map[string]AgentInfo, len(agents))
	for _, info := range agents {
		byName[info.Name] = info
	}

	info := byName["autonomous-agent"]
	if info.ProcessAlive == nil {
		t.Fatalf("autonomous-agent ProcessAlive = nil, want non-nil true")
	}
	if !*info.ProcessAlive {
		t.Fatalf("autonomous-agent ProcessAlive = false, want true: a runtime in an autonomous turn must report process_alive=true")
	}
}

// Gap 2 (M1 / QUM-622): a retired agent projects to liveness Retired, which
// must yield process_alive == false. Mirrors the tri-state test's
// "stopped-agent" construction (Status drives Lifecycle via SyncAgentState),
// using Status="retired" so Lifecycle=retired. Passes today (retired -> false
// already) and after M1.
func TestStatus_RetiredRuntimeReportsProcessAliveFalse(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)

	agent := testAgentState("retired-agent")
	agent.Status = "retired"
	saveTestAgent(t, tmpDir, agent)

	rt := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
		SprawlRoot: tmpDir,
		Agent:      agent,
	})
	rt.SyncAgentState(agent)

	if got := rt.Snapshot().Liveness; got != liveness.Retired {
		t.Fatalf("precondition: Lifecycle = %q, want %q", got, liveness.Retired)
	}

	agents, err := sup.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}

	byName := make(map[string]AgentInfo, len(agents))
	for _, info := range agents {
		byName[info.Name] = info
	}

	info := byName["retired-agent"]
	if info.ProcessAlive == nil {
		t.Fatalf("retired-agent ProcessAlive = nil, want non-nil false")
	}
	if *info.ProcessAlive {
		t.Fatalf("retired-agent ProcessAlive = true, want false: a retired agent must report process_alive=false")
	}
}

// Unit test for the new (*AgentRuntime).IsTerminallyFaulted accessor
// (M1 / QUM-622). Mirrors the optional-interface probe pattern in
// peek_inautonomousturn_test.go: the accessor delegates to the live handle's
// IsTerminallyFaulted() probe and defaults to false when there is no handle or
// the handle lacks the probe.
func TestAgentRuntime_IsTerminallyFaulted(t *testing.T) {
	t.Run("faulted handle reports true", func(t *testing.T) {
		sup, tmpDir := newTestSupervisor(t)
		agent := testAgentState("faulted-handle")
		saveTestAgent(t, tmpDir, agent)

		rt := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: tmpDir,
			Agent:      agent,
			Starter: &runtimeTestStarter{
				session: &runtimeTestSession{
					sessionID:         "sess-faulted-handle",
					caps:              backendpkg.Capabilities{SupportsInterrupt: true},
					terminallyFaulted: true,
				},
			},
		})
		if err := rt.Start(); err != nil {
			t.Fatalf("runtime start: %v", err)
		}

		if !rt.IsTerminallyFaulted() {
			t.Errorf("IsTerminallyFaulted() = false, want true (handle reports faulted)")
		}
	})

	t.Run("healthy handle reports false", func(t *testing.T) {
		sup, tmpDir := newTestSupervisor(t)
		agent := testAgentState("healthy-handle")
		saveTestAgent(t, tmpDir, agent)

		rt := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: tmpDir,
			Agent:      agent,
			Starter: &runtimeTestStarter{
				session: &runtimeTestSession{
					sessionID:         "sess-healthy-handle",
					caps:              backendpkg.Capabilities{SupportsInterrupt: true},
					terminallyFaulted: false,
				},
			},
		})
		if err := rt.Start(); err != nil {
			t.Fatalf("runtime start: %v", err)
		}

		if rt.IsTerminallyFaulted() {
			t.Errorf("IsTerminallyFaulted() = true, want false (handle reports healthy)")
		}
	})

	t.Run("handle lacking the probe reports false", func(t *testing.T) {
		sup, tmpDir := newTestSupervisor(t)
		saveTestAgent(t, tmpDir, testAgentState("probeless"))

		h := &fakeRootHandle{caps: backendpkg.Capabilities{SupportsInterrupt: true}}
		rt, err := sup.RegisterRootRuntime("probeless", h, nil)
		if err != nil {
			t.Fatalf("RegisterRootRuntime: %v", err)
		}

		if rt.IsTerminallyFaulted() {
			t.Errorf("IsTerminallyFaulted() = true, want false (handle lacks IsTerminallyFaulted probe)")
		}
	})

	t.Run("no handle reports false", func(t *testing.T) {
		sup, tmpDir := newTestSupervisor(t)
		agent := testAgentState("never-started")
		saveTestAgent(t, tmpDir, agent)

		// Registered but never Started: no live handle.
		rt := sup.runtimeRegistry.Ensure(AgentRuntimeConfig{
			SprawlRoot: tmpDir,
			Agent:      agent,
		})

		if got := rt.Snapshot().Liveness; got == liveness.Running {
			t.Fatalf("precondition: Lifecycle = %q, want non-started (no live handle)", got)
		}
		if rt.IsTerminallyFaulted() {
			t.Errorf("IsTerminallyFaulted() = true, want false (no live handle)")
		}
	})
}
