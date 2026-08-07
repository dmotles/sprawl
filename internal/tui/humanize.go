package tui

import (
	"fmt"
	"time"
)

// HumanizeSince renders a non-negative duration as a short, agent-readable
// "Xs"/"Xm"/"Xh"/"Xd" string. Negative durations are clamped to 0s (clock
// skew should not produce nonsense).
func HumanizeSince(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		s := int(d.Round(time.Second).Seconds())
		return fmt.Sprintf("%ds", s)
	case d < time.Hour:
		m := int(d.Round(time.Minute).Minutes())
		return fmt.Sprintf("%dm", m)
	case d < 24*time.Hour:
		h := int(d.Round(time.Hour).Hours())
		return fmt.Sprintf("%dh", h)
	default:
		days := int(d / (24 * time.Hour))
		return fmt.Sprintf("%dd", days)
	}
}

// Ago renders how long ago t was, as "15h ago". Empty for a zero t: never
// observed is not an age, and rendering one from the zero time yields
// "489000h ago", which reads as data.
//
// This is the single definition of the age string shared by the MCP status and
// peek tools and the TUI tree, so "15h ago" cannot drift between surfaces.
func Ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return HumanizeSince(now.Sub(t)) + " ago"
}
