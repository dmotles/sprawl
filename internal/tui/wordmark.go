package tui

// QUM-646: SPRAWL wordmark — ported from the dmotles/tui-chat-spike branch
// (internal/tuichat/header.go, commit cf2619b). Renders a 3-line thin
// box-drawing wordmark with a cyan→purple horizontal gradient at the top of
// the TUI; falls back to a single-line gradient "SPRAWL" when the terminal
// is narrower than wordmarkNarrowThreshold columns.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// sprawlWordmark is the 3-line wordmark, ~40 cols wide. Each letter is 3
// cells wide (W is 5) with 4-space inter-letter gaps.
var sprawlWordmark = []string{
	"╭─╮    ╭─╮    ╭─╮    ╭─╮    ╷ ╷ ╷    ╷  ",
	"╰─╮    ├─╯    ├┬╯    ├─┤    │ │ │    │  ",
	"╰─╯    ╵      ╵╰╴    ╵ ╵    ╰─┴─╯    ╰─╴",
}

const (
	// gradStart / gradEnd are the cyan→purple endpoints applied left→right
	// across non-space runes of each wordmark line. Tailwind cyan-400 /
	// purple-500.
	gradStart = "#22D3EE"
	gradEnd   = "#A855F7"

	// wordmarkNarrowThreshold is the terminal-width cutoff below which the
	// banner collapses to a single-line gradient "SPRAWL". The wide form is
	// ~40 cols itself; the threshold gives breathing room for the tree pane
	// border etc.
	wordmarkNarrowThreshold = 70
)

// WordmarkHeight reports how many rows RenderWordmark will produce at the
// given terminal width. Zero width yields zero rows so callers can subtract
// safely on degenerate sizes.
func WordmarkHeight(width int) int {
	if width <= 0 {
		return 0
	}
	if width < wordmarkNarrowThreshold {
		return 1
	}
	return len(sprawlWordmark)
}

// RenderWordmark returns the SPRAWL wordmark padded to the given terminal
// width. Wide (>=wordmarkNarrowThreshold) returns the 3-line box-drawing
// glyphs with a cyan→purple gradient; narrow returns a single-line
// gradient "SPRAWL". Width 0 returns the empty string.
func RenderWordmark(width int) string {
	if width <= 0 {
		return ""
	}
	if width < wordmarkNarrowThreshold {
		return padVisible(gradientLine("SPRAWL"), width)
	}
	lines := make([]string, len(sprawlWordmark))
	for i, glyph := range sprawlWordmark {
		lines[i] = padVisible(gradientLine(glyph), width)
	}
	return strings.Join(lines, "\n")
}

// gradientLine renders s with a left→right cyan→purple hex color lerp across
// non-space runes; spaces are left uncolored to preserve the background.
func gradientLine(s string) string {
	sr, sg, sb := hexToRGB(gradStart)
	er, eg, eb := hexToRGB(gradEnd)
	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		return ""
	}
	var b strings.Builder
	for i, r := range runes {
		if r == ' ' {
			b.WriteRune(r)
			continue
		}
		var t float64
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		rv := lerp(sr, er, t)
		gv := lerp(sg, eg, t)
		bv := lerp(sb, eb, t)
		hex := fmt.Sprintf("#%02X%02X%02X", rv, gv, bv)
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Bold(true).Render(string(r)))
	}
	return b.String()
}

func hexToRGB(s string) (int, int, int) {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return 255, 255, 255
	}
	var r, g, b int
	_, _ = fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b)
	return r, g, b
}

func lerp(a, b int, t float64) int {
	v := float64(a) + (float64(b)-float64(a))*t
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return int(v + 0.5)
}

// padVisible right-pads s with spaces so its visible (terminal-cell) width
// matches width. If s is already at or beyond width, it is returned as-is.
func padVisible(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}
