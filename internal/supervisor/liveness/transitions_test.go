package liveness

import "testing"

// QUM-722: legal edges for Pausing/Paused/Died (T20–T29).
//
// T20  Running     → Pausing      pause requested
// T21  Pausing     → Paused       turn boundary observed → clean exit
// T22  Pausing     → Killed       pause_timeout fired
// T23  Pausing     → Faulted      backend fault during wait
// T25  Paused      → Killed       (extends T16 source set)
// T26  Paused      → Retiring     (extends T17 source set)
// T27  Running/Recovering/Resuming → Died (handle.Done close, !faulted && !expectingExit)
// T29  Died        → Killed/Retiring (extends T16/T17 source set)

func TestCanTransition_T20_RunningToPausing(t *testing.T) {
	if !CanTransition(State{Liveness: Running}, State{Liveness: Pausing}) {
		t.Errorf("Running → Pausing should be legal (T20)")
	}
}

func TestCanTransition_T21_PausingToPaused(t *testing.T) {
	if !CanTransition(State{Liveness: Pausing}, State{Liveness: Paused}) {
		t.Errorf("Pausing → Paused should be legal (T21)")
	}
}

func TestCanTransition_T22_PausingToKilled(t *testing.T) {
	if !CanTransition(State{Liveness: Pausing}, State{Liveness: Killed}) {
		t.Errorf("Pausing → Killed should be legal (T22 — pause timeout)")
	}
}

func TestCanTransition_T23_PausingToFaulted(t *testing.T) {
	if !CanTransition(State{Liveness: Pausing}, State{Liveness: Faulted}) {
		t.Errorf("Pausing → Faulted should be legal (T23 — fault during pause wait)")
	}
}

func TestCanTransition_T25_PausedToKilled(t *testing.T) {
	if !CanTransition(State{Liveness: Paused}, State{Liveness: Killed}) {
		t.Errorf("Paused → Killed should be legal (T25)")
	}
}

func TestCanTransition_T26_PausedToRetiring(t *testing.T) {
	if !CanTransition(State{Liveness: Paused}, State{Liveness: Retiring}) {
		t.Errorf("Paused → Retiring should be legal (T26)")
	}
}

func TestCanTransition_T27_RunningToDied(t *testing.T) {
	if !CanTransition(State{Liveness: Running}, State{Liveness: Died}) {
		t.Errorf("Running → Died should be legal (T27 — unexpected exit)")
	}
}

func TestCanTransition_T27_RecoveringToDied(t *testing.T) {
	if !CanTransition(State{Liveness: Recovering}, State{Liveness: Died}) {
		t.Errorf("Recovering → Died should be legal (T27)")
	}
}

func TestCanTransition_T27_ResumingToDied(t *testing.T) {
	if !CanTransition(State{Liveness: Resuming}, State{Liveness: Died}) {
		t.Errorf("Resuming → Died should be legal (T27)")
	}
}

func TestCanTransition_T29_DiedToKilled(t *testing.T) {
	if !CanTransition(State{Liveness: Died}, State{Liveness: Killed}) {
		t.Errorf("Died → Killed should be legal (T29)")
	}
}

func TestCanTransition_T29_DiedToRetiring(t *testing.T) {
	if !CanTransition(State{Liveness: Died}, State{Liveness: Retiring}) {
		t.Errorf("Died → Retiring should be legal (T29)")
	}
}

// Negative cases — Died is not a resurrection target; Paused is not a sink
// that can hop back to Running directly.

func TestCanTransition_DiedToRunning_Illegal(t *testing.T) {
	if CanTransition(State{Liveness: Died}, State{Liveness: Running}) {
		t.Errorf("Died → Running must be illegal (no resurrection)")
	}
}

func TestCanTransition_DiedToPausing_Illegal(t *testing.T) {
	if CanTransition(State{Liveness: Died}, State{Liveness: Pausing}) {
		t.Errorf("Died → Pausing must be illegal")
	}
}

func TestCanTransition_PausedToRunning_Illegal(t *testing.T) {
	// Resuming a paused agent goes through wake (re-emit Suspended/Resuming),
	// not a direct Paused → Running edge.
	if CanTransition(State{Liveness: Paused}, State{Liveness: Running}) {
		t.Errorf("Paused → Running direct edge must be illegal")
	}
}

func TestCanTransition_PausingFromIdle_Illegal(t *testing.T) {
	// Only Running (idle or autonomous-turn) may enter Pausing.
	if CanTransition(State{Liveness: Stopped}, State{Liveness: Pausing}) {
		t.Errorf("Stopped → Pausing must be illegal")
	}
	if CanTransition(State{Liveness: Suspended}, State{Liveness: Pausing}) {
		t.Errorf("Suspended → Pausing must be illegal")
	}
}
