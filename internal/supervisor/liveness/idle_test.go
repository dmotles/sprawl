package liveness

import "testing"

// QUM-1186 (D2): "idle" is the new on-disk resting token for an agent whose
// runtime was reclaimed for inactivity. It projects onto Suspended — it is a
// disk-only REFINEMENT of suspended, recording WHY the teardown happened, not
// a new liveness.
//
// The round-trip is deliberately lossy in one direction: "idle" decodes to
// Suspended, but Suspended still encodes back to "suspended". See
// TestStatus_SuspendedStillEncodesSuspended for why that asymmetry is load
// bearing rather than an oversight.

func TestFrom_IdleDiskStatusProjectsSuspended(t *testing.T) {
	in := Snapshot{DiskStatus: "idle"}
	want := State{Liveness: Suspended}
	if got := From(in); got != want {
		t.Errorf("From(%+v) = %v, want %v", in, got, want)
	}
}

func TestLivenessFromStatus_Idle(t *testing.T) {
	got, ok := LivenessFromStatus("idle")
	if !ok || got != Suspended {
		t.Errorf("LivenessFromStatus(%q) = (%v, %v), want (%v, true)", "idle", got, ok, Suspended)
	}
}

// TestStatus_SuspendedStillEncodesSuspended is the NEGATIVE control on the
// round-trip, direction: it must stay quiet. Making State{Suspended}.Status()
// return "idle" would be the "obvious" way to close the bijection, and it
// would silently rewrite every shutdown-suspended agent on disk to idle. The
// asymmetry is the design.
func TestStatus_SuspendedStillEncodesSuspended(t *testing.T) {
	if got := (State{Liveness: Suspended}).Status(); got != "suspended" {
		t.Errorf("State{Suspended}.Status() = %q, want %q — idle must NOT become the encode target", got, "suspended")
	}
}

// TestFrom_IdleDoesNotOutrankTerminalStates pins precedence. "idle" is a
// resting refinement, so anything the projection treats as terminal or
// faulted must still beat it. Without this, a reaped-then-killed agent could
// surface as merely suspended and the operator would never see the kill.
func TestFrom_IdleDoesNotOutrankTerminalStates(t *testing.T) {
	// A durable fault recorded alongside a stale idle token must win.
	in := Snapshot{DiskStatus: "idle", TerminalErr: true}
	if got := From(in); got.Liveness != Faulted {
		t.Errorf("From(%+v).Liveness = %v, want %v (fault must beat idle)", in, got.Liveness, Faulted)
	}
	// Lifecycle-killed must win over a stale idle disk token.
	in = Snapshot{DiskStatus: "idle", Lifecycle: "killed"}
	if got := From(in); got.Liveness != Killed {
		t.Errorf("From(%+v).Liveness = %v, want %v (killed must beat idle)", in, got.Liveness, Killed)
	}
}
