package tui

import "charm.land/lipgloss/v2"

// QUM-796: animated "sparkle" activity indicator. A single row that cycles a
// small glyph in the accent color whenever an agent is non-idle — shown above
// the prompt input for the root agent and as a footer for observed child
// panes. The frame counter lives on AppModel (advanced by an independent
// ~250ms tick) so animating it never invalidates the chat render cache
// (QUM-769); these helpers are pure.

// sparkleGlyphs is the animation cycle, mirroring the Claude CLI activity
// glyph set.
var sparkleGlyphs = []string{"✶", "✢", "✽"}

// sparkleGlyph returns the glyph for the given frame counter, wrapping the
// cycle. Guards against negative counters.
func sparkleGlyph(frame int) string {
	n := len(sparkleGlyphs)
	return sparkleGlyphs[((frame%n)+n)%n]
}

// sparkleWordForTurn maps a turn state to the short dim status word rendered
// next to the glyph. Idle/Complete render no word.
func sparkleWordForTurn(s TurnState) string {
	switch s {
	case TurnThinking:
		return "thinking…"
	case TurnStreaming:
		return "running…"
	default:
		return ""
	}
}

// renderSparkle renders the single-row indicator: the animated glyph in the
// accent color at Faint, optionally followed by a dim status word.
func renderSparkle(theme *Theme, frame int, word string) string {
	glyph := lipgloss.NewStyle().
		Foreground(theme.Palette.Primary).
		Faint(true).
		Render(sparkleGlyph(frame))
	if word == "" {
		return glyph
	}
	return glyph + " " + theme.PlaceholderStyle.Render(word)
}
