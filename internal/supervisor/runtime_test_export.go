package supervisor

// Test-only seams exported for use by external test packages (notably
// internal/tui). Production code MUST NOT depend on the symbols in this file.
//
// The exported AttachUnifiedRuntimeForTest helper requires a non-nil
// testing.TB. Production code does not (and should not) import the standard
// "testing" package, so this argument acts as a strong convention gate
// against accidental production calls — mirroring the stdlib convention
// (e.g. httptest helpers expect test plumbing). See QUM-439.

import (
	"context"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
)

// testExportUnifiedHandle is a minimal RuntimeHandle that exposes a UnifiedRuntime
// via the unifiedRuntimeProvider interface so AgentRuntime.UnifiedRuntime()
// resolves in tests without spawning a real backend session.
type testExportUnifiedHandle struct {
	rt *runtimepkg.UnifiedRuntime
}

func (h *testExportUnifiedHandle) Interrupt(_ context.Context) error { return nil }
func (h *testExportUnifiedHandle) Wake() error                       { return nil }
func (h *testExportUnifiedHandle) InterruptDelivery() error          { return nil }
func (h *testExportUnifiedHandle) Stop(_ context.Context) error      { return nil }
func (h *testExportUnifiedHandle) SessionID() string                 { return "" }
func (h *testExportUnifiedHandle) Capabilities() backendpkg.Capabilities {
	return backendpkg.Capabilities{}
}
func (h *testExportUnifiedHandle) UnifiedRuntime() *runtimepkg.UnifiedRuntime { return h.rt }

// AttachUnifiedRuntimeForTest installs a fake unified-handle on rt so that
// rt.UnifiedRuntime() returns urt. Test-only: requires a non-nil testing.TB
// so production code (which does not import "testing") cannot reach this
// seam by accident.
func AttachUnifiedRuntimeForTest(tb testing.TB, rt *AgentRuntime, urt *runtimepkg.UnifiedRuntime) {
	if tb == nil {
		panic("supervisor.AttachUnifiedRuntimeForTest: testing.TB must be non-nil; this is a test-only seam")
	}
	tb.Helper()
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.handle = &testExportUnifiedHandle{rt: urt}
	rt.snapshot.Lifecycle = RuntimeLifecycleStarted
}
