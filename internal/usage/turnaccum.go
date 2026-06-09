package usage

import "github.com/dmotles/sprawl/internal/protocol"

// TurnAccumulator sums assistant-frame usage across one turn.
type TurnAccumulator struct {
	usage   protocol.Usage
	model   string
	hasData bool
}

// Absorb folds a single assistant frame's usage + model into the accumulator.
// Tokens are summed; the latest non-empty model wins.
func (a *TurnAccumulator) Absorb(u protocol.Usage, model string) {
	a.usage.InputTokens += u.InputTokens
	a.usage.OutputTokens += u.OutputTokens
	a.usage.CacheReadInputTokens += u.CacheReadInputTokens
	a.usage.CacheCreationInputTokens += u.CacheCreationInputTokens
	if model != "" {
		a.model = model
	}
	a.hasData = true
}

// Reset zeroes the accumulator.
func (a *TurnAccumulator) Reset() {
	a.usage = protocol.Usage{}
	a.model = ""
	a.hasData = false
}

// HasData reports whether the accumulator has absorbed any frame since
// construction or last Reset.
func (a *TurnAccumulator) HasData() bool { return a.hasData }

// Usage returns the summed token block accumulated so far.
func (a *TurnAccumulator) Usage() protocol.Usage { return a.usage }

// Model returns the last non-empty model string absorbed.
func (a *TurnAccumulator) Model() string { return a.model }
