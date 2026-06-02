package tui

// QUM-656: top-of-TUI header composing the SPRAWL wordmark (left) with the
// orbital agent tree (right), separated by a dim vertical bar. Width budget
// for the tree pane is reported by HeaderTreeWidth; total row count is
// reported by HeaderHeight. Narrow terminals collapse to a one-line
// gradient "SPRAWL · breadcrumb".

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// HeaderHeight reports how many rows RenderHeader produces at the given
// terminal width. Mirrors WordmarkHeight (3 wide, 1 narrow, 0 at zero width)
// since the tree pane height matches the wordmark's.
func HeaderHeight(width int) int {
	if width <= 0 {
		return 0
	}
	if width < wordmarkNarrowThreshold {
		return 1
	}
	return 3
}

// HeaderTreeWidth returns the cell budget the orbital tree pane gets within
// the header at the given terminal width. Wide layout: termWidth minus the
// wordmark's visible width and the " │ " separator (clamped to a 10-cell
// floor). Narrow layout: nearly the full width, since the breadcrumb shares a
// single line with a small gradient "SPRAWL · " prefix.
func HeaderTreeWidth(width int) int {
	if width <= 0 {
		return 0
	}
	if width < wordmarkNarrowThreshold {
		// Narrow row is "SPRAWL · <breadcrumb>". The orbital renderer will
		// truncate to its budget; reserve the prefix (~10 cells) and floor.
		budget := width - 10
		if budget < 10 {
			budget = 10
		}
		return budget
	}
	wordmarkW := lipgloss.Width(sprawlWordmark[0])
	sepW := lipgloss.Width(" │ ")
	budget := width - wordmarkW - sepW
	if budget < 10 {
		budget = 10
	}
	return budget
}

// RenderHeader composes the wordmark + orbital tree into a single multi-line
// string padded to width. Wide form is 3 rows (`wordmark │ tree`); narrow
// form is a single row (`SPRAWL · breadcrumb`).
func RenderHeader(width int, treeLines []string) string {
	if width <= 0 {
		return ""
	}
	getLine := func(i int) string {
		if i < len(treeLines) {
			return treeLines[i]
		}
		return ""
	}

	if width < wordmarkNarrowThreshold {
		left := gradientLine("SPRAWL")
		sep := headerSep.Render(" · ")
		row := left + sep + getLine(0)
		return padVisible(row, width)
	}

	sep := headerSep.Render(" │ ")
	wordmarkW := lipgloss.Width(sprawlWordmark[0])
	lines := make([]string, 3)
	for i := 0; i < 3; i++ {
		left := gradientLine(sprawlWordmark[i])
		left = padVisible(left, wordmarkW)
		row := left + sep + getLine(i)
		lines[i] = padVisible(row, width)
	}
	return strings.Join(lines, "\n")
}
