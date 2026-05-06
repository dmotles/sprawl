package tui

// QUM-483 / QUM-501 — regression tests for tall-tree overflow and outer-sizing.
//
// renderPanel applies lipgloss .Width(w).Height(h), but lipgloss treats
// Height as a *minimum*: when the inner content is taller than h, the
// rendered output grows past h instead of being clamped. The same is true
// for Width when content has long lines. When the agent tree grows tall
// (many nodes, or nodes with long LastReportMessage), the tree panel
// renders taller than its declared Height, the composed View overflows the
// terminal, and the input bar is pushed off the bottom of the screen.
//
// Per QUM-501, w/h passed to renderPanel are *outer* (post-border)
// dimensions. lipgloss v2 treats Width/Height as outer when a border is
// set, so the panel renders at exactly w x h with the content area
// derived inside the border.

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRenderPanel_ClampsHeightWhenContentExceeds is a direct unit test of
// renderPanel: feed it overflowing content and assert the bordered output
// stays at exactly the declared outer (w, h) — never larger.
//
// Under QUM-501, w and h are outer dimensions: rendering must satisfy
// `len(lines) <= h` and `max line width <= w` regardless of content size.
func TestRenderPanel_ClampsHeightWhenContentExceeds(t *testing.T) {
	m := newTestAppModel(t)

	// Outer dims: full bordered panel size requested by caller.
	const wantOuterW, wantOuterH = 12, 5

	// Height clamp: 20 lines of content into an outerH-tall panel.
	tall := strings.Repeat("x\n", 20)
	out := m.renderPanel(tall, wantOuterW, wantOuterH, false)
	gotLines := len(strings.Split(out, "\n"))
	if gotLines > wantOuterH {
		t.Errorf("renderPanel(content=20 rows, outerH=%d) returned %d lines; want <= %d (lipgloss Height is min-not-max — needs MaxHeight clamp on outer dim, QUM-483/501)\n%s",
			wantOuterH, gotLines, wantOuterH, out)
	}

	// Width clamp: a single very long line into an outerW-wide panel.
	wide := strings.Repeat("y", 80)
	out = m.renderPanel(wide, wantOuterW, wantOuterH, false)
	maxW := 0
	for _, ln := range strings.Split(out, "\n") {
		if lw := lipgloss.Width(ln); lw > maxW {
			maxW = lw
		}
	}
	if maxW > wantOuterW {
		t.Errorf("renderPanel(content=80-wide line, outerW=%d) max line width = %d; want <= %d (needs MaxWidth clamp on outer dim, QUM-483/501)\n%s",
			wantOuterW, maxW, wantOuterW, out)
	}
}
