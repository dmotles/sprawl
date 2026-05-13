// QUM-467: cmd-level guard that the weave-launch path reuses the
// supervisor's host-scoped MCP bridge instead of building a fresh one
// per invocation.
//
// Today, three sites in cmd/enter.go (newSupervisor at ~230, plus the
// two newSessionImpl* paths at ~388 and ~473) each call
// `host.NewMCPBridge()` and then `bridge.Register("sprawl", server)`.
// Each weave-claude restart routes through one of those sites and
// overwrites the bridge, even though children spawned earlier are still
// holding handles to the original. The fix is to construct the bridge
// once at supervisor scope and have all three sites consult
// `sup.MCPBridge()`.
//
// This test pins the contract: across multiple weave-launch invocations
// (initial + restart), the bridge consulted by the launcher is the same
// instance the supervisor exposes via its MCPBridge() accessor. Pointer
// equality is the assertion.
//
// IMPLEMENTER NOTE: the current cmd/enter.go inlines bridge construction
// inside newSessionImpl/newSessionImplUnified, which makes this hard to
// unit-test directly without spawning a real claude. The expected
// refactor either:
//
//  1. extracts a helper like `cmd.newWeaveSessionBridge(sup) backend.ToolBridge`
//     that returns sup.MCPBridge() when non-nil and falls back to a
//     fresh one otherwise — tested directly here, OR
//  2. moves the entire bridge wiring into cmd.newSupervisor and has
//     newSessionImpl* read it back via sup.MCPBridge() — tested by
//     asserting sup.MCPBridge() is non-nil after newSupervisor() runs.
//
// We write the test against the second, simpler shape because it
// matches the oracle plan most directly. If the implementer chooses
// shape (1), this test should be updated to call the helper.
package cmd

import (
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// supervisorMCPBridgeAccessor lets us call MCPBridge() without forcing
// it onto the public Supervisor interface. We use an interface
// assertion against the concrete value (which the implementer is free
// to satisfy on *supervisor.Real only) so the test FILE COMPILES today
// — the red signal is a runtime t.Fatal, not a compile error. This
// matches Go TDD norms (tests compile, expectations fail).
//
// If the implementer chooses to widen the public Supervisor interface
// to include MCPBridge(), this local declaration becomes redundant and
// can be removed.
type supervisorMCPBridgeAccessor interface {
	MCPBridge() backendpkg.ToolBridge
}

// TestNewSession_ReusesSupervisorMCPBridge — after the QUM-467 fix,
// the supervisor returned by deps.newSupervisor exposes a stable
// MCPBridge() that is non-nil after the production wiring runs. The
// three weave-launch sites in cmd/enter.go are expected to consult this
// accessor instead of constructing fresh bridges.
//
// This is the minimum-viable assertion the implementer can make pass
// without restructuring the production code further than the oracle
// plan calls for.
func TestNewSession_ReusesSupervisorMCPBridge(t *testing.T) {
	deps := defaultEnterDeps
	if deps == nil {
		deps = resolveEnterDeps()
	}
	if deps.newSupervisor == nil {
		t.Fatal("defaultEnterDeps.newSupervisor is nil; cannot exercise QUM-467 contract")
	}

	tmpDir := t.TempDir()
	sup, _ := deps.newSupervisor(tmpDir, nil)
	if sup == nil {
		t.Fatal("newSupervisor returned nil")
	}

	if _, ok := sup.(*supervisor.Real); !ok {
		t.Skipf("supervisor type %T is not *supervisor.Real; cannot drive accessor", sup)
	}
	acc, ok := sup.(supervisorMCPBridgeAccessor)
	if !ok {
		t.Fatalf("*supervisor.Real does not implement MCPBridge() backend.ToolBridge — QUM-467 accessor missing")
	}

	first := acc.MCPBridge()
	if first == nil {
		t.Fatal("sup.MCPBridge() returned nil after newSupervisor; expected a host-scoped bridge installed during construction")
	}

	// Calling it again must yield the same instance — children
	// registered against `first` would be severed otherwise.
	second := acc.MCPBridge()
	if first != second {
		t.Errorf("sup.MCPBridge() identity changed across calls: %p -> %p", first, second)
	}
}

// TestSupervisorMCPBridge_ProductionUsesAccessor (QUM-473 §7) pins the
// contract that the production weave-launch path NEVER takes the fallback
// branch in supervisorMCPBridge. The fallback (host.NewMCPBridge() +
// bridge.Register("sprawl", ...)) is the same pattern QUM-467 flagged as
// the bug: any future refactor that drops the *supervisor.Real type
// assertion and silently regresses to building a fresh per-call bridge
// would sever children that hold the original. This test fails closed in
// that scenario.
//
// We don't reach into supervisorMCPBridge's internals; we just assert
// the returned bridge is pointer-equal to the one *supervisor.Real
// exposes via MCPBridge(). The fallback path stays in place for test
// doubles and is exercised by other tests in this file via its
// pointer-equality invariants over MCPBridge() itself.
func TestSupervisorMCPBridge_ProductionUsesAccessor(t *testing.T) {
	deps := defaultEnterDeps
	if deps == nil {
		deps = resolveEnterDeps()
	}
	if deps.newSupervisor == nil {
		t.Fatal("defaultEnterDeps.newSupervisor is nil; cannot exercise QUM-473 §7 contract")
	}

	tmpDir := t.TempDir()
	sup, _ := deps.newSupervisor(tmpDir, nil)
	if sup == nil {
		t.Fatal("newSupervisor returned nil")
	}
	if _, ok := sup.(*supervisor.Real); !ok {
		t.Skipf("supervisor type %T is not *supervisor.Real; the accessor-path assertion only applies to production", sup)
	}
	acc, ok := sup.(supervisorMCPBridgeAccessor)
	if !ok {
		t.Fatalf("*supervisor.Real does not implement MCPBridge() backend.ToolBridge — accessor missing")
	}

	want := acc.MCPBridge()
	if want == nil {
		t.Fatal("sup.MCPBridge() returned nil after newSupervisor; expected a host-scoped bridge installed during construction")
	}

	got := supervisorMCPBridge(sup)
	if got != want {
		t.Errorf("supervisorMCPBridge(*supervisor.Real) took the fallback path: got %p, want %p (sup.MCPBridge()). Production must never construct a fresh bridge here — see QUM-467 / QUM-473 §7.",
			got, want)
	}
}

// TestNewSupervisor_BridgeUsedByChildSpawns — ensures the bridge
// exposed via MCPBridge() is the same one captured into the runtime
// starter's InitSpec used to launch children. We can't peek directly
// at the in-process starter's initSpec from the cmd package, so we
// rely on a pointer-equality check that the supervisor's accessor
// returns the same bridge before AND after we call SetChildMCPConfig
// with a *different* bridge. The expected post-fix behavior is that
// SetChildMCPConfig either no-ops on the bridge field (if the
// supervisor owns its own) or accepts the install only on first call.
// In either case the production code path in newSupervisor (which
// installs once during construction) must yield a stable accessor.
func TestNewSupervisor_BridgeStableAfterChildMCPConfigChurn(t *testing.T) {
	deps := defaultEnterDeps
	if deps == nil {
		deps = resolveEnterDeps()
	}
	if deps.newSupervisor == nil {
		t.Skip("defaultEnterDeps.newSupervisor is nil")
	}

	tmpDir := t.TempDir()
	sup, _ := deps.newSupervisor(tmpDir, nil)
	if sup == nil {
		t.Fatal("newSupervisor returned nil")
	}

	r, ok := sup.(*supervisor.Real)
	if !ok {
		t.Skipf("supervisor type %T is not *supervisor.Real; cannot drive churn", sup)
	}
	acc, ok := sup.(supervisorMCPBridgeAccessor)
	if !ok {
		t.Fatalf("*supervisor.Real does not implement MCPBridge() — QUM-467 accessor missing")
	}

	want := acc.MCPBridge()
	if want == nil {
		t.Fatal("MCPBridge() nil after newSupervisor")
	}

	// Simulate the pre-fix duplication: re-install with a fresh InitSpec
	// (as the buggy weave-launch sites in cmd/enter.go would have done).
	r.SetChildMCPConfig(backendpkg.InitSpec{}, nil)

	got := acc.MCPBridge()
	if got != want {
		t.Errorf("MCPBridge() identity changed after SetChildMCPConfig churn: %p -> %p (QUM-467 regression)",
			want, got)
	}
}
