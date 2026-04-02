package memory

import "time"

// BudgetConfig controls the size budget for the context blob.
// Section priority ordering is implicit: Active State > Timeline > Recent Sessions.
// When the budget is exceeded, lower-priority sections are truncated first.
type BudgetConfig struct {
	MaxTotalChars   int
	MaxSessionChars int
}

// TimelineCompressionConfig controls thresholds for timeline compression.
type TimelineCompressionConfig struct {
	WeeklySummaryAge  time.Duration
	MonthlySummaryAge time.Duration
	MaxEntries        int
	MaxSizeChars      int
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
	}
}

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
