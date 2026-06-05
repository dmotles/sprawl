package supervisor

import "testing"

// TestRuntimeHandleProbes is the QUM-613 guard for the sub-interface
// refactor that promotes inline duck-typed assertions on RuntimeHandle
// into named unexported sub-interfaces. The test asserts that the
// production handle types satisfy the new probe interfaces at runtime.
// Uses typed-nil pointers (no construction) since real handles need
// complex deps.
func TestRuntimeHandleProbes(t *testing.T) {
	t.Run("terminalFaultProbe/unifiedHandle", func(t *testing.T) {
		if _, ok := any((*unifiedHandle)(nil)).(terminalFaultProbe); !ok {
			t.Fatalf("*unifiedHandle does not satisfy terminalFaultProbe")
		}
	})
	t.Run("terminalFaultProbe/WeaveRuntimeHandle", func(t *testing.T) {
		if _, ok := any((*WeaveRuntimeHandle)(nil)).(terminalFaultProbe); !ok {
			t.Fatalf("*WeaveRuntimeHandle does not satisfy terminalFaultProbe")
		}
	})
	t.Run("stopWaitTimeoutProbe/unifiedHandle", func(t *testing.T) {
		if _, ok := any((*unifiedHandle)(nil)).(stopWaitTimeoutProbe); !ok {
			t.Fatalf("*unifiedHandle does not satisfy stopWaitTimeoutProbe")
		}
	})
	t.Run("turnProbe/unifiedHandle", func(t *testing.T) {
		if _, ok := any((*unifiedHandle)(nil)).(turnProbe); !ok {
			t.Fatalf("*unifiedHandle does not satisfy turnProbe")
		}
	})
	t.Run("turnProbe/WeaveRuntimeHandle", func(t *testing.T) {
		if _, ok := any((*WeaveRuntimeHandle)(nil)).(turnProbe); !ok {
			t.Fatalf("*WeaveRuntimeHandle does not satisfy turnProbe")
		}
	})
	t.Run("terminalFaultInjectorProbe/unifiedHandle", func(t *testing.T) {
		if _, ok := any((*unifiedHandle)(nil)).(terminalFaultInjectorProbe); !ok {
			t.Fatalf("*unifiedHandle does not satisfy terminalFaultInjectorProbe")
		}
	})
}
