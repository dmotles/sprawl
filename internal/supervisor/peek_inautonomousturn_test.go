package supervisor

import (
	"context"
	"testing"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/state"
)

// fakeInAutonomousTurnHandle is a RuntimeHandle that additionally exposes
// InAutonomousTurn() bool. Mirrors fakeRootHandle (supervisor_test.go) plus
// the optional-interface method that Real.Peek is expected to type-assert
// against (QUM-585).
type fakeInAutonomousTurnHandle struct {
	caps      backendpkg.Capabilities
	sessionID string
	autonomy  bool
	doneCh    chan struct{}
}

func (h *fakeInAutonomousTurnHandle) Interrupt(context.Context) error       { return nil }
func (h *fakeInAutonomousTurnHandle) Wake() error                           { return nil }
func (h *fakeInAutonomousTurnHandle) WakeForDelivery() error                { return nil }
func (h *fakeInAutonomousTurnHandle) ForceInterruptDelivery() error         { return nil }
func (h *fakeInAutonomousTurnHandle) Stop(context.Context) error            { return nil }
func (h *fakeInAutonomousTurnHandle) SessionID() string                     { return h.sessionID }
func (h *fakeInAutonomousTurnHandle) Capabilities() backendpkg.Capabilities { return h.caps }
func (h *fakeInAutonomousTurnHandle) Done() <-chan struct{}                 { return h.doneCh }
func (h *fakeInAutonomousTurnHandle) InAutonomousTurn() bool                { return h.autonomy }

// QUM-585: Real.Peek must populate PeekResult.InAutonomousTurn by querying
// the target agent's registered runtime handle. When the handle reports
// true, the field must be true.
func TestReal_Peek_InAutonomousTurn_TrueWhenHandleReportsTrue(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})

	h := &fakeInAutonomousTurnHandle{
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		sessionID: "sess-ratz",
		autonomy:  true,
	}
	if _, err := sup.RegisterRootRuntime("ratz", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	got, err := sup.Peek(context.Background(), "ratz", 10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if !got.InAutonomousTurn {
		t.Errorf("InAutonomousTurn = false, want true (handle reports true)")
	}
}

// QUM-585: when the handle reports false, the field must be false.
func TestReal_Peek_InAutonomousTurn_FalseWhenHandleReportsFalse(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ratz",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})

	h := &fakeInAutonomousTurnHandle{
		caps:      backendpkg.Capabilities{SupportsInterrupt: true},
		sessionID: "sess-ratz",
		autonomy:  false,
	}
	if _, err := sup.RegisterRootRuntime("ratz", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	got, err := sup.Peek(context.Background(), "ratz", 10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.InAutonomousTurn {
		t.Errorf("InAutonomousTurn = true, want false (handle reports false)")
	}
}

// QUM-585: when a runtime is registered but its handle does NOT implement
// the optional InAutonomousTurn() interface, Peek must default the field to
// false (no panic, no error). Locks the optional-interface contract so a
// future change to a hard cast would be caught.
func TestReal_Peek_InAutonomousTurn_FalseWhenHandleLacksMethod(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "stub",
		Type:   "engineer",
		Parent: "weave",
		Status: "active",
	})

	h := &fakeRootHandle{caps: backendpkg.Capabilities{SupportsInterrupt: true}}
	if _, err := sup.RegisterRootRuntime("stub", h, nil); err != nil {
		t.Fatalf("RegisterRootRuntime: %v", err)
	}

	got, err := sup.Peek(context.Background(), "stub", 10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.InAutonomousTurn {
		t.Errorf("InAutonomousTurn = true, want false (handle lacks optional method)")
	}
}

// QUM-585: when no runtime is registered for the agent (only on-disk state
// exists), Peek must still succeed and report InAutonomousTurn=false rather
// than erroring.
func TestReal_Peek_InAutonomousTurn_FalseWhenNoRuntimeRegistered(t *testing.T) {
	sup, tmpDir := newTestSupervisor(t)
	saveTestAgent(t, tmpDir, &state.AgentState{
		Name:   "ghost",
		Type:   "researcher",
		Parent: "weave",
		Status: "active",
	})

	got, err := sup.Peek(context.Background(), "ghost", 10)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if got.InAutonomousTurn {
		t.Errorf("InAutonomousTurn = true, want false (no runtime registered)")
	}
}
