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

// TestLivenessFromStatus_PausedAndDiedAreRecognised — QUM-1260.
//
// Both were absent from the switch, so both decoded to (0, false) and
// RecoverAgents' accept-set skipped them via its unrecognised-status branch.
// For `paused` that produced the RIGHT behaviour for the WRONG reason: the
// explicit `lv == liveness.Paused` guard in RecoverAgents, added so "a future
// projection tweak can't silently regress this contract" (QUM-723), was dead
// code, and the contract it documents was being upheld by a default arm that
// knows nothing about pausing. For `died` it produced the wrong behaviour
// outright: a crash survivor whose subprocess exit sprawl happened to observe
// before dying itself is stamped `died` and then never auto-resumes on any
// later `sprawl enter` — measured in the paused-persistence P2 row, where the
// same "simulated crash" was observed leaving `active`, `suspended`, `paused`
// and `died` on different runs depending on the kernel's reap order.
//
// This change is behaviour-PRESERVING by construction and that is the point:
// paused and died were skipped as unrecognised and are now skipped as
// recognised-and-excluded. What it buys is that the exclusions are readable,
// that QUM-723's guard is live code again, and that whether `died` SHOULD be
// excluded becomes a question someone can see and answer rather than an
// accident of a default arm. That question is tracked separately; nothing here
// widens the accept-set.
func TestLivenessFromStatus_PausedAndDiedAreRecognised(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   AgentLiveness
	}{
		{"paused", Paused},
		{"died", Died},
	} {
		got, ok := LivenessFromStatus(tc.status)
		if !ok {
			t.Errorf("LivenessFromStatus(%q) not recognised; a status the product writes must not decode through the unknown branch", tc.status)
			continue
		}
		if got != tc.want {
			t.Errorf("LivenessFromStatus(%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// TestLivenessFromStatus_UnknownStillUnrecognised is the negative control for
// the test above: adding cases must not turn the switch into a function that
// accepts anything. Without it, `return Suspended, true` for every input would
// satisfy every assertion above.
func TestLivenessFromStatus_UnknownStillUnrecognised(t *testing.T) {
	for _, s := range []string{"", "banana", "Paused", "DIED", "pause"} {
		if got, ok := LivenessFromStatus(s); ok {
			t.Errorf("LivenessFromStatus(%q) = (%v, true), want not recognised", s, got)
		}
	}
}
