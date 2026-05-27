package liveness

import "fmt"

// AgentLiveness is the unified lifecycle state of an agent.
type AgentLiveness int

const (
	Unstarted AgentLiveness = iota
	Starting
	Running
	Faulted
	Recovering
	Stopping
	Stopped
	Suspended
	Resuming
	ResumeFailed
	Killed
	Retiring
	Retired
)

// String renders the liveness as a lowercase token.
func (l AgentLiveness) String() string {
	switch l {
	case Unstarted:
		return "unstarted"
	case Starting:
		return "starting"
	case Running:
		return "running"
	case Faulted:
		return "faulted"
	case Recovering:
		return "recovering"
	case Stopping:
		return "stopping"
	case Stopped:
		return "stopped"
	case Suspended:
		return "suspended"
	case Resuming:
		return "resuming"
	case ResumeFailed:
		return "resume_failed"
	case Killed:
		return "killed"
	case Retiring:
		return "retiring"
	case Retired:
		return "retired"
	default:
		return fmt.Sprintf("unknown(%d)", int(l))
	}
}

// State pairs a liveness with the Running·AutonomousTurn sub-state bool.
type State struct {
	Liveness         AgentLiveness
	InAutonomousTurn bool
}

// String renders the state, distinguishing the autonomous-turn sub-state.
// The middle char in the autonomous form is U+00B7 MIDDLE DOT.
func (s State) String() string {
	if s.Liveness == Running && s.InAutonomousTurn {
		return "running·autonomous-turn"
	}
	return s.Liveness.String()
}

// ProcessAlive reports whether the agent's process is expected to be running.
// Both Running and Running·AutonomousTurn count as alive (invariant 6).
func ProcessAlive(s State) bool {
	return s.Liveness == Running
}
