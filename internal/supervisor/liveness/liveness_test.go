package liveness

import (
	"errors"
	"strings"
	"testing"
)

func TestAgentLiveness_String(t *testing.T) {
	cases := []struct {
		name string
		in   AgentLiveness
		want string
	}{
		{"unstarted", Unstarted, "unstarted"},
		{"starting", Starting, "starting"},
		{"running", Running, "running"},
		{"faulted", Faulted, "faulted"},
		{"recovering", Recovering, "recovering"},
		{"stopping", Stopping, "stopping"},
		{"stopped", Stopped, "stopped"},
		{"suspended", Suspended, "suspended"},
		{"resuming", Resuming, "resuming"},
		{"resume_failed", ResumeFailed, "resume_failed"},
		{"killed", Killed, "killed"},
		{"retiring", Retiring, "retiring"},
		{"retired", Retired, "retired"},
		{"unknown", AgentLiveness(99), "unknown(99)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Errorf("AgentLiveness(%d).String() = %q, want %q", int(tc.in), got, tc.want)
			}
		})
	}

	stateCases := []struct {
		name string
		in   State
		want string
	}{
		{"running_autonomous", State{Liveness: Running, InTurn: true}, "running·autonomous-turn"},
		{"running_plain", State{Liveness: Running, InTurn: false}, "running"},
	}
	for _, tc := range stateCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Errorf("State.String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCanTransition_Legal(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
	}{
		{"T1_unstarted_to_starting", State{Liveness: Unstarted}, State{Liveness: Starting}},
		{"T2_starting_to_running", State{Liveness: Starting}, State{Liveness: Running}},
		{"T3_starting_to_faulted", State{Liveness: Starting}, State{Liveness: Faulted}},
		{"T4_running_to_autonomous", State{Liveness: Running, InTurn: false}, State{Liveness: Running, InTurn: true}},
		{"T5_autonomous_to_running", State{Liveness: Running, InTurn: true}, State{Liveness: Running, InTurn: false}},
		{"T6a_running_to_faulted", State{Liveness: Running, InTurn: false}, State{Liveness: Faulted}},
		{"T6b_autonomous_to_faulted", State{Liveness: Running, InTurn: true}, State{Liveness: Faulted}},
		{"T7_running_to_stopping", State{Liveness: Running, InTurn: false}, State{Liveness: Stopping}},
		{"T8_stopping_to_stopped", State{Liveness: Stopping}, State{Liveness: Stopped}},
		{"T9_faulted_to_recovering", State{Liveness: Faulted}, State{Liveness: Recovering}},
		{"T10_recovering_to_running", State{Liveness: Recovering}, State{Liveness: Running, InTurn: false}},
		{"T11_recovering_to_faulted", State{Liveness: Recovering}, State{Liveness: Faulted}},
		{"T12_running_to_suspended", State{Liveness: Running, InTurn: false}, State{Liveness: Suspended}},
		{"T12_stopped_to_suspended", State{Liveness: Stopped}, State{Liveness: Suspended}},
		{"T12_faulted_to_suspended", State{Liveness: Faulted}, State{Liveness: Suspended}},
		{"T13_suspended_to_resuming", State{Liveness: Suspended}, State{Liveness: Resuming}},
		{"T14_resuming_to_running", State{Liveness: Resuming}, State{Liveness: Running, InTurn: false}},
		{"T15_resuming_to_resumefailed", State{Liveness: Resuming}, State{Liveness: ResumeFailed}},
		{"T16_running_to_killed", State{Liveness: Running, InTurn: false}, State{Liveness: Killed}},
		{"T16_faulted_to_killed", State{Liveness: Faulted}, State{Liveness: Killed}},
		{"T16_stopped_to_killed", State{Liveness: Stopped}, State{Liveness: Killed}},
		{"T16_suspended_to_killed", State{Liveness: Suspended}, State{Liveness: Killed}},
		{"T16_resumefailed_to_killed", State{Liveness: ResumeFailed}, State{Liveness: Killed}},
		{"T17_running_to_retiring", State{Liveness: Running, InTurn: false}, State{Liveness: Retiring}},
		{"T17_faulted_to_retiring", State{Liveness: Faulted}, State{Liveness: Retiring}},
		{"T17_stopped_to_retiring", State{Liveness: Stopped}, State{Liveness: Retiring}},
		{"T17_suspended_to_retiring", State{Liveness: Suspended}, State{Liveness: Retiring}},
		{"T17_resumefailed_to_retiring", State{Liveness: ResumeFailed}, State{Liveness: Retiring}},
		{"T18_retiring_to_retired", State{Liveness: Retiring}, State{Liveness: Retired}},
		{"T19_resumefailed_to_recovering", State{Liveness: ResumeFailed}, State{Liveness: Recovering}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !CanTransition(tc.from, tc.to) {
				t.Errorf("CanTransition(%v, %v) = false, want true", tc.from, tc.to)
			}
		})
	}
}

func TestCanTransition_Illegal(t *testing.T) {
	cases := []struct {
		name string
		from State
		to   State
	}{
		{"inv1_no_unstarted_to_running", State{Liveness: Unstarted}, State{Liveness: Running}},
		{"inv2_no_unstarted_to_faulted", State{Liveness: Unstarted}, State{Liveness: Faulted}},
		{"inv3_no_faulted_to_stopped", State{Liveness: Faulted}, State{Liveness: Stopped}},
		{"inv5_killed_to_running", State{Liveness: Killed}, State{Liveness: Running}},
		{"inv5_killed_to_retiring", State{Liveness: Killed}, State{Liveness: Retiring}},
		{"inv5_killed_to_killed", State{Liveness: Killed}, State{Liveness: Killed}},
		{"inv5_retired_to_running", State{Liveness: Retired}, State{Liveness: Running}},
		{"inv5_retired_to_recovering", State{Liveness: Retired}, State{Liveness: Recovering}},
		{"inv4_autonomousturn_not_sink", State{Liveness: Running, InTurn: true}, State{Liveness: Stopping}},
		{"t12_unstarted_to_suspended_illegal", State{Liveness: Unstarted}, State{Liveness: Suspended}},
		{"running_noop_same_substate", State{Liveness: Running, InTurn: false}, State{Liveness: Running, InTurn: false}},
		{"bad_bit_on_nonrunning", State{Liveness: Running, InTurn: false}, State{Liveness: Faulted, InTurn: true}},
		{"stopped_to_running_direct", State{Liveness: Stopped}, State{Liveness: Running}},
		{"inv4_autonomousturn_to_killed", State{Liveness: Running, InTurn: true}, State{Liveness: Killed}},
		{"inv4_autonomousturn_to_retiring", State{Liveness: Running, InTurn: true}, State{Liveness: Retiring}},
		{"inv1_faulted_to_running_direct", State{Liveness: Faulted}, State{Liveness: Running}},
		{"inv1_suspended_to_running_direct", State{Liveness: Suspended}, State{Liveness: Running}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if CanTransition(tc.from, tc.to) {
				t.Errorf("CanTransition(%v, %v) = true, want false", tc.from, tc.to)
			}
		})
	}
}

func TestValidate_WrapsSentinel(t *testing.T) {
	t.Run("legal_returns_nil", func(t *testing.T) {
		if err := Validate(State{Liveness: Unstarted}, State{Liveness: Starting}); err != nil {
			t.Errorf("Validate(legal) = %v, want nil", err)
		}
	})

	t.Run("illegal_wraps_sentinel_and_names_states", func(t *testing.T) {
		err := Validate(State{Liveness: Faulted}, State{Liveness: Stopped})
		if err == nil {
			t.Fatalf("Validate(illegal) = nil, want non-nil error")
		}
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("errors.Is(err, ErrIllegalTransition) = false, want true; err=%v", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "faulted") {
			t.Errorf("err.Error() = %q, want it to contain %q", msg, "faulted")
		}
		if !strings.Contains(msg, "stopped") {
			t.Errorf("err.Error() = %q, want it to contain %q", msg, "stopped")
		}
	})
}

func TestFrom_MapsFromToday(t *testing.T) {
	cases := []struct {
		name string
		in   Snapshot
		want State
	}{
		{"unstarted_from_registered", Snapshot{Lifecycle: "registered"}, State{Liveness: Unstarted}},
		{"running_idle", Snapshot{Lifecycle: "started", RuntimeState: "idle"}, State{Liveness: Running, InTurn: false}},
		{"running_autonomous_via_turnactive", Snapshot{Lifecycle: "started", RuntimeState: "turn-active"}, State{Liveness: Running, InTurn: true}},
		{"running_autonomous_via_bool", Snapshot{Lifecycle: "started", RuntimeState: "idle", InTurn: true}, State{Liveness: Running, InTurn: true}},
		{"faulted_beats_running", Snapshot{Lifecycle: "started", RuntimeState: "idle", TerminalErr: true}, State{Liveness: Faulted}},
		{"stopping_from_interrupting", Snapshot{Lifecycle: "started", RuntimeState: "interrupting"}, State{Liveness: Stopping}},
		{"stopped_nonfault", Snapshot{Lifecycle: "stopped"}, State{Liveness: Stopped}},
		{"suspended_from_disk", Snapshot{DiskStatus: "suspended"}, State{Liveness: Suspended}},
		{"resume_failed_from_disk", Snapshot{DiskStatus: "resume_failed"}, State{Liveness: ResumeFailed}},
		{"killed_from_lifecycle", Snapshot{Lifecycle: "killed"}, State{Liveness: Killed}},
		{"killed_from_disk", Snapshot{DiskStatus: "killed"}, State{Liveness: Killed}},
		{"retiring_from_disk", Snapshot{DiskStatus: "retiring"}, State{Liveness: Retiring}},
		{"retired_from_lifecycle", Snapshot{Lifecycle: "retired"}, State{Liveness: Retired}},
		{"retired_from_disk", Snapshot{DiskStatus: "retired"}, State{Liveness: Retired}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := From(tc.in); got != tc.want {
				t.Errorf("From(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFrom_Precedence(t *testing.T) {
	cases := []struct {
		name string
		in   Snapshot
		want State
	}{
		{"terminalErr_wins_over_started", Snapshot{Lifecycle: "started", RuntimeState: "turn-active", TerminalErr: true}, State{Liveness: Faulted}},
		{"killed_disk_wins_over_started", Snapshot{Lifecycle: "started", DiskStatus: "killed"}, State{Liveness: Killed}},
		{"done_ignored_falls_through", Snapshot{Lifecycle: "started", RuntimeState: "idle", DiskStatus: "done"}, State{Liveness: Running, InTurn: false}},
		{"problem_ignored", Snapshot{Lifecycle: "stopped", DiskStatus: "problem"}, State{Liveness: Stopped}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := From(tc.in); got != tc.want {
				t.Errorf("From(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestFrom_NeverYieldsTransients(t *testing.T) {
	snaps := []Snapshot{
		{Lifecycle: "registered"},
		{Lifecycle: "started", RuntimeState: "idle"},
		{Lifecycle: "started", RuntimeState: "turn-active"},
		{Lifecycle: "started", RuntimeState: "idle", InTurn: true},
		{Lifecycle: "started", RuntimeState: "idle", TerminalErr: true},
		{Lifecycle: "started", RuntimeState: "interrupting"},
		{Lifecycle: "stopped"},
		{DiskStatus: "suspended"},
		{DiskStatus: "resume_failed"},
		{Lifecycle: "killed"},
		{DiskStatus: "killed"},
		{DiskStatus: "retiring"},
		{Lifecycle: "retired"},
		{DiskStatus: "retired"},
		{Lifecycle: "started", DiskStatus: "killed"},
		{Lifecycle: "stopped", DiskStatus: "problem"},
	}
	transients := map[AgentLiveness]string{
		Starting:   "Starting",
		Recovering: "Recovering",
		Resuming:   "Resuming",
	}
	for _, snap := range snaps {
		got := From(snap)
		if name, bad := transients[got.Liveness]; bad {
			t.Errorf("From(%+v).Liveness = %s, want a non-transient state", snap, name)
		}
	}
}

func TestProcessAlive(t *testing.T) {
	cases := []struct {
		name string
		in   State
		want bool
	}{
		{"unstarted", State{Liveness: Unstarted}, false},
		{"starting", State{Liveness: Starting}, false},
		{"running", State{Liveness: Running}, true},
		{"running_autonomous", State{Liveness: Running, InTurn: true}, true},
		{"faulted", State{Liveness: Faulted}, false},
		{"recovering", State{Liveness: Recovering}, false},
		{"stopping", State{Liveness: Stopping}, false},
		{"stopped", State{Liveness: Stopped}, false},
		{"suspended", State{Liveness: Suspended}, false},
		{"resuming", State{Liveness: Resuming}, false},
		{"resume_failed", State{Liveness: ResumeFailed}, false},
		{"killed", State{Liveness: Killed}, false},
		{"retiring", State{Liveness: Retiring}, false},
		{"retired", State{Liveness: Retired}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProcessAlive(tc.in); got != tc.want {
				t.Errorf("ProcessAlive(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestStatus_Projection(t *testing.T) {
	cases := []struct {
		name string
		in   State
		want string
	}{
		{"running", State{Liveness: Running}, "active"},
		{"running_autonomous", State{Liveness: Running, InTurn: true}, "active"},
		{"suspended", State{Liveness: Suspended}, "suspended"},
		{"resume_failed", State{Liveness: ResumeFailed}, "resume_failed"},
		{"killed", State{Liveness: Killed}, "killed"},
		{"retiring", State{Liveness: Retiring}, "retiring"},
		{"retired", State{Liveness: Retired}, "retired"},
		{"stopped", State{Liveness: Stopped}, "stopped"},
		{"faulted", State{Liveness: Faulted}, "faulted"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Status(); got != tc.want {
				t.Errorf("State{%v}.Status() = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLivenessFromStatus(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		want   AgentLiveness
		wantOK bool
	}{
		{"suspended", "suspended", Suspended, true},
		{"resume_failed", "resume_failed", ResumeFailed, true},
		{"killed", "killed", Killed, true},
		{"retired", "retired", Retired, true},
		{"retiring", "retiring", Retiring, true},
		{"active", "active", Running, true},
		{"running", "running", Running, true},
		{"faulted", "faulted", Faulted, true},
		{"stopped", "stopped", Stopped, true},
		{"done", "done", 0, false},
		{"problem", "problem", 0, false},
		{"garbage", "garbage", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LivenessFromStatus(tc.in)
			if ok != tc.wantOK {
				t.Errorf("LivenessFromStatus(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			// When wantOK is false the returned liveness must be the zero
			// value (Unstarted). Asserting this unconditionally closes a
			// false-green window where an impl could return (Running, false)
			// for an unrecognized status like "done".
			if got != tc.want {
				t.Errorf("LivenessFromStatus(%q) liveness = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestLivenessFromStatus_RoundtripSubset(t *testing.T) {
	for _, s := range []string{"suspended", "resume_failed", "killed", "retired", "retiring"} {
		t.Run(s, func(t *testing.T) {
			l, ok := LivenessFromStatus(s)
			if !ok {
				t.Fatalf("LivenessFromStatus(%q) ok = false, want true", s)
			}
			if got := (State{Liveness: l}).Status(); got != s {
				t.Errorf("roundtrip: State{%v}.Status() = %q, want %q", l, got, s)
			}
		})
	}
}
