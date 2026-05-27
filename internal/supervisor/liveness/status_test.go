package liveness

import "testing"

// QUM-625 M4: Faulted and Stopped become durable on-disk statuses. These
// tests assert the post-M4 bijection and projection behavior. They FAIL today
// because Status() collapses Faulted/Stopped to "", LivenessFromStatus does
// not decode them, and From() ignores the durable disk statuses.

func TestStatus_DurableFaultedStopped(t *testing.T) {
	if got := (State{Liveness: Faulted}).Status(); got != "faulted" {
		t.Errorf("State{Faulted}.Status() = %q, want %q", got, "faulted")
	}
	if got := (State{Liveness: Stopped}).Status(); got != "stopped" {
		t.Errorf("State{Stopped}.Status() = %q, want %q", got, "stopped")
	}
}

func TestLivenessFromStatus_FaultedStopped(t *testing.T) {
	if got, ok := LivenessFromStatus("faulted"); got != Faulted || !ok {
		t.Errorf("LivenessFromStatus(%q) = (%v, %v), want (%v, true)", "faulted", got, ok, Faulted)
	}
	if got, ok := LivenessFromStatus("stopped"); got != Stopped || !ok {
		t.Errorf("LivenessFromStatus(%q) = (%v, %v), want (%v, true)", "stopped", got, ok, Stopped)
	}
}

func TestStatusBijection_AllResting(t *testing.T) {
	resting := []AgentLiveness{
		Running,
		Faulted,
		Stopped,
		Suspended,
		ResumeFailed,
		Killed,
		Retiring,
		Retired,
	}
	for _, l := range resting {
		t.Run(l.String(), func(t *testing.T) {
			status := (State{Liveness: l}).Status()
			got, ok := LivenessFromStatus(status)
			if !ok {
				t.Fatalf("LivenessFromStatus(%q) ok = false, want true", status)
			}
			if got != l {
				t.Errorf("roundtrip %v -> Status()=%q -> %v, want %v", l, status, got, l)
			}
		})
	}
}

func TestFrom_DurableFaulted(t *testing.T) {
	got := From(Snapshot{DiskStatus: "faulted", Lifecycle: "registered"})
	if got.Liveness != Faulted {
		t.Errorf("From(faulted disk).Liveness = %v, want %v", got.Liveness, Faulted)
	}
}

func TestFrom_DurableStopped(t *testing.T) {
	got := From(Snapshot{DiskStatus: "stopped", Lifecycle: "registered"})
	if got.Liveness != Stopped {
		t.Errorf("From(stopped disk).Liveness = %v, want %v", got.Liveness, Stopped)
	}
}

// TestStatus_TransientsProjectEmpty pins the design contract that transient
// livenesses never project to a durable on-disk status string.
func TestStatus_TransientsProjectEmpty(t *testing.T) {
	transients := []AgentLiveness{Unstarted, Starting, Recovering, Stopping, Resuming}
	for _, tr := range transients {
		t.Run(tr.String(), func(t *testing.T) {
			if got := (State{Liveness: tr}).Status(); got != "" {
				t.Errorf("State{%v}.Status() = %q, want %q", tr, got, "")
			}
		})
	}
}
