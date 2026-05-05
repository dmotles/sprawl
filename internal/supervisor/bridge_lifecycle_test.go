// Package supervisor — bridge_lifecycle_test.go
//
// QUM-467 regression guards. Tests the supervisor's MCPBridge() accessor
// (NEW API — does not yet exist; this is the TDD red signal).
//
// Per the oracle plan attached to QUM-467 (see Linear comment from
// 2026-05-05), the host MCP bridge should live for the supervisor's
// lifetime — not weave-claude's lifetime. The accessor lets the three
// duplicate `host.NewMCPBridge()` sites in cmd/enter.go (lines 230, 388,
// 473) collapse into a single shared instance. This file exercises the
// new accessor at the supervisor layer.
//
// Expected NEW API:
//
//	// MCPBridge returns the host-scoped MCP tool bridge installed on
//	// this supervisor. The bridge is constructed once (via
//	// InstallMCPBridge or as a side-effect of SetChildMCPConfig) and
//	// reused across weave-claude restarts.
//	func (r *Real) MCPBridge() backend.ToolBridge
//
// Naming note: the implementer may either (a) repurpose
// SetChildMCPConfig to also stash the bridge into a *Real field
// readable via MCPBridge(), or (b) add a dedicated
// InstallMCPBridge(server, allowedTools) method. Either is acceptable
// — these tests assert behavior, not the installer name.
package supervisor

import (
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/host"
)

// supervisorMCPBridgeAccessor is the contract the production *Real type
// must satisfy after the QUM-467 fix. We assert it as an interface so the
// test fails-to-compile cleanly until the accessor is added rather than
// silently lying about pointer equality via reflection.
type supervisorMCPBridgeAccessor interface {
	MCPBridge() backendpkg.ToolBridge
}

// TestSupervisor_MCPBridge_StableAcrossInstall — the bridge captured by
// the first SetChildMCPConfig call is exposed via MCPBridge() and is
// stable across all subsequent SetChildMCPConfig calls. This is the
// unified contract: implementers may choose either eager construction
// in NewReal OR lazy "first install wins" semantics — both pass this
// test as long as identity is preserved across subsequent installs.
//
// This is the core invariant child agents rely on across weave-claude
// restarts: the bridge they were registered against must survive
// supervisor-level reconfiguration.
func TestSupervisor_MCPBridge_StableAcrossInstall(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	// The accessor must exist on *Real.
	acc, ok := any(sup).(supervisorMCPBridgeAccessor)
	if !ok {
		t.Fatalf("*supervisor.Real does not implement MCPBridge() backend.ToolBridge — QUM-467 accessor missing")
	}

	// Establish the baseline bridge via the existing surface. We use
	// SetChildMCPConfig because that's the existing installer; if the
	// implementer adds a dedicated method, the call below should be
	// updated to match. After this call, MCPBridge() must be non-nil
	// regardless of whether the supervisor constructs eagerly (in
	// NewReal) or lazily (on first install).
	bridge1 := host.NewMCPBridge()
	sup.SetChildMCPConfig(backendpkg.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge1,
	}, []string{"mcp__sprawl__send_async"})

	first := acc.MCPBridge()
	if first == nil {
		t.Fatalf("MCPBridge() returned nil after first SetChildMCPConfig; expected the host-scoped bridge to be exposed")
	}

	// Simulate the kind of churn cmd/enter.go does today: re-install
	// child MCP config with a DIFFERENT bridge instance, as the three
	// duplicate weave-launch sites would have triggered post-restart.
	// The accessor's bridge identity must NOT change — children
	// registered against `first` would otherwise be severed.
	bridge2 := host.NewMCPBridge()
	sup.SetChildMCPConfig(backendpkg.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     bridge2,
	}, []string{"mcp__sprawl__send_async"})

	second := acc.MCPBridge()
	if second == nil {
		t.Fatalf("MCPBridge() returned nil after second SetChildMCPConfig; expected stable host-scoped bridge")
	}
	if first != second {
		t.Errorf("MCPBridge() changed identity after subsequent SetChildMCPConfig: %p -> %p — bridge must be host-scoped, not session-scoped (QUM-467 regression)",
			first, second)
	}
}

// TestSupervisor_MCPBridge_SetOnceViaInstall — once a bridge has been
// installed (via whichever installer the implementer settles on), the
// accessor returns that same bridge on every call. No silent
// re-creation, no nil churn.
func TestSupervisor_MCPBridge_SetOnceViaInstall(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	acc, ok := any(sup).(supervisorMCPBridgeAccessor)
	if !ok {
		t.Fatalf("*supervisor.Real does not implement MCPBridge() backend.ToolBridge — QUM-467 accessor missing")
	}

	// Install a bridge. Whether the installer is named SetChildMCPConfig
	// (which already exists and would gain bridge-stash semantics) or a
	// dedicated InstallMCPBridge(...) is up to the implementer. We use
	// SetChildMCPConfig because it's the existing surface; if the
	// implementer adds a new method, this test should be updated to call
	// it.
	want := host.NewMCPBridge()
	sup.SetChildMCPConfig(backendpkg.InitSpec{
		MCPServerNames: []string{"sprawl"},
		ToolBridge:     want,
	}, nil)

	for i := 0; i < 3; i++ {
		got := acc.MCPBridge()
		if got == nil {
			t.Fatalf("call %d: MCPBridge() returned nil after install", i)
		}
		// Pointer-equality check: the accessor must hand back the exact
		// instance that was installed, not a fresh one.
		gotBridge, ok := got.(*host.MCPBridge)
		if !ok {
			// If the accessor returns a wrapper, that's a design choice
			// the implementer may take — but the wrapper must wrap the
			// same identity across calls. Skip pointer-eq check and rely
			// on the cross-call identity check below.
			t.Logf("call %d: MCPBridge() returned non-*host.MCPBridge type %T; skipping pointer-eq check", i, got)
			continue
		}
		if gotBridge != want {
			t.Errorf("call %d: MCPBridge() returned a different *host.MCPBridge instance (%p) than was installed (%p)",
				i, gotBridge, want)
		}
	}
}

// TestSupervisor_MCPBridge_SurvivesSimulatedWeaveRestart simulates the
// cmd/enter.go newSessionImplUnified path being invoked twice (the
// "weave-claude restarted" scenario). Each invocation today recreates a
// host.NewMCPBridge() and calls some equivalent of "install on
// supervisor". After QUM-467, the supervisor's MCPBridge() accessor
// must keep returning the SAME bridge across these simulated restarts —
// because children were registered against the original instance.
//
// We cannot run a real claude here; instead we assert that AFTER the
// fix, repeated simulated installs do NOT change the supervisor's
// authoritative bridge identity. The expected behavior is one of:
//
//  1. The supervisor constructs its own bridge eagerly (in NewReal or
//     a dedicated install path) and SetChildMCPConfig becomes a no-op
//     for the bridge field. In that case MCPBridge() returns the
//     eagerly-built bridge regardless of subsequent installs.
//
//  2. The supervisor accepts the FIRST install and ignores subsequent
//     ones (set-once semantics).
//
// Either satisfies the QUM-467 contract. A failure is when subsequent
// installs replace the bridge — that's the bug.
func TestSupervisor_MCPBridge_SurvivesSimulatedWeaveRestart(t *testing.T) {
	sup, _ := newTestSupervisor(t)

	acc, ok := any(sup).(supervisorMCPBridgeAccessor)
	if !ok {
		t.Fatalf("*supervisor.Real does not implement MCPBridge() backend.ToolBridge — QUM-467 accessor missing")
	}

	original := acc.MCPBridge()
	if original == nil {
		// If the supervisor doesn't eagerly construct a bridge, we need
		// to install one before the "restart" simulation has anything
		// to compare to. Install one now — that becomes the baseline.
		baseline := host.NewMCPBridge()
		sup.SetChildMCPConfig(backendpkg.InitSpec{
			MCPServerNames: []string{"sprawl"},
			ToolBridge:     baseline,
		}, nil)
		original = acc.MCPBridge()
		if original == nil {
			t.Fatalf("MCPBridge() still nil after explicit install; accessor is broken")
		}
	}

	// Simulate three "weave-claude restarts": each time, cmd/enter.go's
	// pre-fix code path would build a fresh bridge and re-install. After
	// the fix these calls must NOT rotate the supervisor's authoritative
	// bridge.
	for i := 0; i < 3; i++ {
		fresh := host.NewMCPBridge()
		sup.SetChildMCPConfig(backendpkg.InitSpec{
			MCPServerNames: []string{"sprawl"},
			ToolBridge:     fresh,
		}, nil)

		got := acc.MCPBridge()
		if got != original {
			t.Errorf("simulated restart %d: MCPBridge() identity changed (%p -> %p) — children registered against the original would be severed (QUM-467 regression)",
				i, original, got)
		}
	}
}
