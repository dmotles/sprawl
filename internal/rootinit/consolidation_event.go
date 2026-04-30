package rootinit

import "time"

// ConsolidationEvent carries progress from the background consolidation
// pipeline. Consumers (cmd/enter.go) translate these into TUI messages.
type ConsolidationEvent struct {
	Phase    string        // human-readable phase label (empty when Done)
	Done     bool          // true when the pipeline has finished
	Err      error         // non-nil on failure (only meaningful when Done)
	Duration time.Duration // wall-clock duration (only meaningful when Done)
}

// sendConsolidationEvent sends an event to the channel without blocking.
// No-op if ch is nil.
func sendConsolidationEvent(ch chan<- ConsolidationEvent, ev ConsolidationEvent) {
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
	}
}
