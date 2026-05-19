// QUM-600: UnifiedRuntime.StopWithOptions exposes a SkipPoliteInterrupt
// knob so the abandon-retire path can skip the unconditional
// Session.Interrupt issued during Stop. Today Stop always issues a polite
// Interrupt before cancelling the loop; when retire was invoked with
// abandon=true the caller does not want to wait on that interrupt at all —
// it just wants the process to die.
//
// These tests are RED until the implementer adds:
//   - type StopOptions struct { SkipPoliteInterrupt bool }
//   - (rt *UnifiedRuntime) StopWithOptions(ctx, StopOptions) error
//   - and refactors (rt *UnifiedRuntime) Stop to delegate to StopWithOptions
//     with default options (SkipPoliteInterrupt=false).
package runtime

import (
	"context"
	"testing"
	"time"
)

// TestUnifiedRuntime_StopWithOptions_SkipPoliteInterrupt pins the QUM-600
// contract: when SkipPoliteInterrupt is true, Stop must NOT call
// Session.Interrupt at all (the abandon path is teardown-only).
func TestUnifiedRuntime_StopWithOptions_SkipPoliteInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !waitForState(t, rt, StateIdle, 1*time.Second) {
		t.Fatalf("not idle before Stop; state=%v", rt.State())
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rt.StopWithOptions(stopCtx, StopOptions{SkipPoliteInterrupt: true}); err != nil {
		t.Fatalf("StopWithOptions: %v", err)
	}

	if got := mock.interruptCount(); got != 0 {
		t.Errorf("Session.Interrupt count = %d, want 0 (QUM-600: SkipPoliteInterrupt must skip Stop's Interrupt forward)", got)
	}
	if got := rt.State(); got != StateStopped {
		t.Errorf("State after StopWithOptions = %v, want StateStopped", got)
	}
}

// TestUnifiedRuntime_StopWithOptions_DefaultCallsPoliteInterrupt pins the
// default-options behaviour: StopOptions{} (SkipPoliteInterrupt=false) must
// still call Session.Interrupt — same semantics as the legacy Stop.
func TestUnifiedRuntime_StopWithOptions_DefaultCallsPoliteInterrupt(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !waitForState(t, rt, StateIdle, 1*time.Second) {
		t.Fatalf("not idle before Stop; state=%v", rt.State())
	}

	if err := rt.StopWithOptions(context.Background(), StopOptions{}); err != nil {
		t.Fatalf("StopWithOptions: %v", err)
	}

	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt count = %d, want 1 (default StopOptions{} must still call Interrupt)", got)
	}
}

// TestUnifiedRuntime_Stop_DelegatesToStopWithOptions sanity-checks that
// the legacy Stop entrypoint still forwards Session.Interrupt — i.e. it
// is implemented as StopWithOptions(ctx, StopOptions{}).
func TestUnifiedRuntime_Stop_DelegatesToStopWithOptions(t *testing.T) {
	mock := &mockUnifiedSession{}
	rt := New(RuntimeConfig{Name: "x", Session: mock})

	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !waitForState(t, rt, StateIdle, 1*time.Second) {
		t.Fatalf("not idle before Stop; state=%v", rt.State())
	}

	if err := rt.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got := mock.interruptCount(); got != 1 {
		t.Errorf("Session.Interrupt count after plain Stop = %d, want 1 (default path unchanged)", got)
	}
}
