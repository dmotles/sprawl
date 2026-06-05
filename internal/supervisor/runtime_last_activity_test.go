package supervisor

import (
	"testing"
	"time"
)

// QUM-665: AgentRuntime.LastActivityAt must delegate to the live handle's
// LastActivityAt() probe (mirroring InTurn). When no handle is
// registered, the accessor returns the zero time.
func TestAgentRuntime_LastActivityAt_ZeroWhenNoHandle(t *testing.T) {
	rt := &AgentRuntime{}
	got := rt.LastActivityAt()
	if !got.IsZero() {
		t.Errorf("LastActivityAt() with no handle = %v, want zero", got)
	}
}

// QUM-665: lastActivityProbe is a named optional sub-interface analogous to
// turnProbe. Production handle types (*unifiedHandle and
// *WeaveRuntimeHandle) must satisfy it so AgentRuntime.LastActivityAt can
// type-assert against them without a hard compile-time dependency.
func TestLastActivityProbe_SatisfiedByProductionHandles(t *testing.T) {
	var _ lastActivityProbe = (*unifiedHandle)(nil)
	var _ lastActivityProbe = (*WeaveRuntimeHandle)(nil)
}

// Compile-time sanity: the probe's only method has the expected signature.
func TestLastActivityProbe_MethodSignature(t *testing.T) {
	var p lastActivityProbe = &fakeLastActivityHandle{}
	got := p.LastActivityAt()
	_ = got               // any time.Time value is fine; we just want the method to compile.
	var _ time.Time = got //nolint:staticcheck // QF1011: explicit type assertion is the point of the test
}
