package liveness

// Local mirrors of the on-disk status string vocabulary. Intentionally
// duplicated rather than imported from internal/state to keep this package a
// pure, dependency-free leaf.
const (
	statusFaulted = "faulted"
	statusStopped = "stopped"
)

// Status projects a State onto the on-disk status string, or "" when the
// state has no persisted status representation. As of M4 (QUM-625) the
// resting livenesses — including Stopped and Faulted — each map to a durable
// on-disk status string; only the transient states (Unstarted/Starting/
// Recovering/Stopping/Resuming) collapse to "".
func (s State) Status() string {
	switch s.Liveness {
	case Running:
		return "active"
	case Faulted:
		return statusFaulted
	case Stopped:
		return statusStopped
	case Suspended:
		return "suspended"
	case ResumeFailed:
		return "resume_failed"
	case Killed:
		return "killed"
	case Retiring:
		return "retiring"
	case Retired:
		return "retired"
	default:
		return ""
	}
}

// LivenessFromStatus maps an on-disk status string back to a liveness,
// reporting whether the status is recognized. As of M4 (QUM-625) the durable
// "faulted"/"stopped" statuses decode back to their livenesses. Unrecognized
// strings (including "done"/"problem") return (0, false).
func LivenessFromStatus(status string) (AgentLiveness, bool) {
	switch status {
	case statusFaulted:
		return Faulted, true
	case statusStopped:
		return Stopped, true
	case "suspended":
		return Suspended, true
	case "resume_failed":
		return ResumeFailed, true
	case "killed":
		return Killed, true
	case "retired":
		return Retired, true
	case "retiring":
		return Retiring, true
	case "active":
		return Running, true
	case "running": // legacy alias
		return Running, true
	default:
		return 0, false
	}
}
