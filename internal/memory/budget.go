package memory

import "time"

// BudgetConfig controls the size budget for the context blob.
// Section priority ordering is implicit: Active State > Persistent Knowledge > Timeline > Recent Sessions.
// When the budget is exceeded, lower-priority sections are truncated first.
type BudgetConfig struct {
	MaxTotalChars   int
	MaxSessionChars int
}

// TimelineCompressionConfig controls thresholds for timeline compression.
//
// Model, InvokeTimeout, MaxPromptChars, and OverlapSessions govern the
// LLM invocation used by Consolidate (QUM-284/285/286). Zero values fall
// back to DefaultTimelineCompressionConfig.
type TimelineCompressionConfig struct {
	WeeklySummaryAge  time.Duration
	MonthlySummaryAge time.Duration
	MaxEntries        int
	MaxSizeChars      int

	// Model is the Claude model name passed to the invoker. Empty means
	// "let the claude CLI pick" (typically the user's default).
	Model string
	// InvokeTimeout bounds the single claude -p call. Zero falls back to
	// DefaultInvokeTimeout.
	InvokeTimeout time.Duration
	// MaxPromptChars caps the total prompt size (header + existing
	// timeline + session bodies). Zero falls back to DefaultMaxConsolidationPromptChars.
	MaxPromptChars int
	// OverlapSessions is the number of sessions older than the most recent
	// timeline entry that are still included in the prompt, as overlap
	// context. Zero falls back to DefaultOverlapSessions.
	OverlapSessions int
}

// DefaultBudgetConfig returns a BudgetConfig with sensible defaults.
func DefaultBudgetConfig() BudgetConfig {
	return BudgetConfig{
		MaxTotalChars:   10000,
		MaxSessionChars: 2000,
	}
}

// DefaultTimelineCompressionConfig returns a TimelineCompressionConfig with sensible defaults.
func DefaultTimelineCompressionConfig() TimelineCompressionConfig {
	return TimelineCompressionConfig{
		WeeklySummaryAge:  30 * 24 * time.Hour,
		MonthlySummaryAge: 90 * 24 * time.Hour,
		MaxEntries:        200,
		MaxSizeChars:      50000,
		Model:             DefaultMemoryModel,
		InvokeTimeout:     DefaultInvokeTimeout,
		MaxPromptChars:    DefaultMaxConsolidationPromptChars,
		OverlapSessions:   DefaultOverlapSessions,
	}
}

// DefaultMemoryModel is the Claude model used for memory distillation
// (consolidation + persistent-knowledge updates). Distillation is a
// structured bullet-extraction task — sonnet handles it well without the
// latency/cost of opus. Users can override via .sprawl/config.yaml
// `memory_model` or by constructing a non-default config.
const DefaultMemoryModel = "sonnet"

// DefaultInvokeTimeout bounds each individual claude -p call made by the
// memory pipeline. Picked to cover slow-prompt + slow-network worst cases
// while still recovering from genuinely stuck invocations.
const DefaultInvokeTimeout = 120 * time.Second

// DefaultMaxConsolidationPromptChars caps the byte length of the prompt
// sent to the consolidator. This is larger than the output budget
// (MaxSizeChars=50000) because the prompt also contains the full existing
// timeline plus overlap session bodies.
const DefaultMaxConsolidationPromptChars = 120000

// DefaultOverlapSessions is the number of sessions older than the most
// recent timeline entry that are still fed to the consolidator as context.
// Two is enough to let the model see cross-session themes without
// re-feeding the full history.
const DefaultOverlapSessions = 2

// MeasureBytes returns the byte count of s.
// This measures byte count, not Unicode rune count. Byte count is sufficient
// for budget estimation. Can be swapped to utf8.RuneCountInString() later if needed.
func MeasureBytes(s string) int {
	return len(s)
}

const truncationNote = "\n[...truncated]"

// TruncateWithNote truncates s to maxChars bytes, appending a truncation note.
// If s is within the limit, it is returned unchanged. If maxChars is less than
// the length of the truncation note, s is truncated to maxChars with no note
// appended (best-effort truncation).
func TruncateWithNote(s string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if len(s) <= maxChars {
		return s
	}
	if maxChars < len(truncationNote) {
		return s[:maxChars]
	}
	return s[:maxChars-len(truncationNote)] + truncationNote
}
