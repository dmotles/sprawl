package liveness

// Snapshot is the raw multi-source view of an agent used to derive a unified
// liveness State.
type Snapshot struct {
	Lifecycle        string // registered|started|stopped|killed|retired
	RuntimeState     string // idle|turn-active|interrupting|stopped
	TerminalErr      bool
	InAutonomousTurn bool
	DiskStatus       string // active|running|suspended|killed|retired|retiring|done|resume_failed
}

// Local mirrors of the input string vocabularies. These are intentionally
// duplicated rather than imported from internal/state, internal/runtime, or
// internal/supervisor to keep this package a pure, dependency-free leaf.
const (
	lifecycleStarted = "started"
	lifecycleStopped = "stopped"
	lifecycleKilled  = "killed"
	lifecycleRetired = "retired"

	runtimeTurnActive   = "turn-active"
	runtimeInterrupting = "interrupting"

	diskKilled       = "killed"
	diskRetired      = "retired"
	diskRetiring     = "retiring"
	diskSuspended    = "suspended"
	diskResumeFailed = "resume_failed"
	diskFaulted      = "faulted"
	diskStopped      = "stopped"
)

// From projects a raw Snapshot onto a unified liveness State.
//
// Precedence is load-bearing — fault must beat "started" so a crashed-but-
// still-marked-started agent surfaces as Faulted (QUM-606). The order is:
//
//  1. Terminal/operator durable states (killed/retired/retiring).
//  2. Fault beats Running (TerminalErr -> Faulted).
//  3. Cross-process disk resting states (resume_failed/suspended).
//  4. Stop-in-flight (RuntimeState interrupting -> Stopping).
//  5. Started/live (-> Running, with autonomous-turn sub-state).
//  6. Deliberately stopped lifecycle.
//  7. Default -> Unstarted.
//
// "done"/"problem" are NOT liveness signals and are deliberately ignored.
// From never returns a transient state (Starting/Recovering/Resuming).
func From(s Snapshot) State {
	// 1. Terminal/operator durable states.
	if s.Lifecycle == lifecycleKilled || s.DiskStatus == diskKilled {
		return State{Liveness: Killed}
	}
	if s.Lifecycle == lifecycleRetired || s.DiskStatus == diskRetired {
		return State{Liveness: Retired}
	}
	if s.DiskStatus == diskRetiring {
		return State{Liveness: Retiring}
	}

	// 2. Fault beats Running. A durable on-disk "faulted" status is honored
	// here too so a crash recorded across processes survives.
	if s.TerminalErr || s.DiskStatus == diskFaulted {
		return State{Liveness: Faulted}
	}

	// 3. Cross-process disk resting states.
	if s.DiskStatus == diskResumeFailed {
		return State{Liveness: ResumeFailed}
	}
	if s.DiskStatus == diskSuspended {
		return State{Liveness: Suspended}
	}

	// A durable on-disk "stopped" status wins over a stale Lifecycle:
	// placed after the other disk resting states but before the
	// Lifecycle-started branch.
	if s.DiskStatus == diskStopped {
		return State{Liveness: Stopped}
	}

	// 4. Stop-in-flight.
	if s.RuntimeState == runtimeInterrupting {
		return State{Liveness: Stopping}
	}

	// 5. Started/live.
	if s.Lifecycle == lifecycleStarted {
		if s.RuntimeState == runtimeTurnActive || s.InAutonomousTurn {
			return State{Liveness: Running, InAutonomousTurn: true}
		}
		return State{Liveness: Running}
	}

	// 6. Deliberately stopped.
	if s.Lifecycle == lifecycleStopped {
		return State{Liveness: Stopped}
	}

	// 7. Default.
	return State{Liveness: Unstarted}
}
