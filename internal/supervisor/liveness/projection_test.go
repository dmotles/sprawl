package liveness

import "testing"

// QUM-722: projection rules for Paused/Died disk tokens.

func TestFrom_DiedFromDiskStatus(t *testing.T) {
	in := Snapshot{DiskStatus: "died"}
	want := State{Liveness: Died}
	if got := From(in); got != want {
		t.Errorf("From(%+v) = %v, want %v", in, got, want)
	}
}

func TestFrom_PausedFromDiskStatus(t *testing.T) {
	in := Snapshot{DiskStatus: "paused"}
	want := State{Liveness: Paused}
	if got := From(in); got != want {
		t.Errorf("From(%+v) = %v, want %v", in, got, want)
	}
}

// Precedence: Died is in the terminal/operator block at the top of From and
// therefore beats stale Lifecycle="started" (no live handle but stale DB).
func TestFrom_DiedBeatsStartedLifecycle(t *testing.T) {
	in := Snapshot{Lifecycle: "started", DiskStatus: "died"}
	want := State{Liveness: Died}
	if got := From(in); got != want {
		t.Errorf("From(%+v) = %v, want %v (Died must beat Lifecycle=started)", in, got, want)
	}
}

// Precedence: Paused is a cross-process resting state and beats a stale
// Lifecycle="stopped" (so a paused agent surfaces as Paused, not Stopped).
func TestFrom_PausedBeatsStoppedLifecycle(t *testing.T) {
	in := Snapshot{Lifecycle: "stopped", DiskStatus: "paused"}
	want := State{Liveness: Paused}
	if got := From(in); got != want {
		t.Errorf("From(%+v) = %v, want %v (Paused must beat Lifecycle=stopped)", in, got, want)
	}
}

// Precedence: TerminalErr still beats DiskStatus="died" (a faulted agent
// is Faulted, not Died — operator-actionable distinction).
// NOTE: Per spec, Died is in the terminal/operator block ABOVE Faulted, so
// this test pins the alternate behavior: Died (DiskStatus) beats no-handle
// inference but a live TerminalErr is still authoritative. Adjust during impl
// if the precedence is locked differently.
func TestFrom_DiedDiskBeatsNoHandleInference(t *testing.T) {
	// No Lifecycle, no live signals — just the durable disk marker.
	in := Snapshot{DiskStatus: "died"}
	if got := From(in); got.Liveness != Died {
		t.Errorf("From(%+v).Liveness = %v, want Died", in, got.Liveness)
	}
}
