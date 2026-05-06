package tui

// QUM-483 — regression tests for tall-tree overflow.
//
// renderPanel applies lipgloss .Width(w).Height(h), but lipgloss treats
// Height as a *minimum*: when the inner content is taller than h, the
// rendered output grows past h instead of being clamped. The same is true
// for Width when content has long lines. When the agent tree grows tall
// (many nodes, or nodes with long LastReportMessage), the tree panel
// renders taller than its declared Height, the composed View overflows the
// terminal, and the input bar is pushed off the bottom of the screen.
//
// The fix is to also call .MaxWidth(w).MaxHeight(h) so lipgloss truncates
// the render to the declared size.

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRenderPanel_ClampsHeightWhenContentExceeds is a direct unit test of
// renderPanel: feed it overflowing content and assert the bordered output
// stays at exactly (w + horizontalFrame, h + verticalFrame) — i.e. the
// caller's declared content size plus the border, never larger.
//
// In lipgloss v2, Width/Height set the *content* size while MaxWidth/
// MaxHeight clamp the *outer* (post-border) render. The fix adds the
// border frame to the Max* values so well-sized content is unaffected and
// overflow is truncated rather than allowed to grow the panel.
func TestRenderPanel_ClampsHeightWhenContentExceeds(t *testing.T) {
	m := newTestAppModel(t)

	const w, h = 10, 3
	// The rounded border style adds 2 rows + 2 cols of frame.
	const wantOuterW, wantOuterH = w + 2, h + 2

	// Height clamp: 20 lines of content into an h-tall content area.
	tall := strings.Repeat("x\n", 20)
	out := m.renderPanel(tall, w, h, false)
	gotLines := len(strings.Split(out, "\n"))
	if gotLines > wantOuterH {
		t.Errorf("renderPanel(content=20 rows, h=%d) returned %d lines; want <= %d (lipgloss Height is min-not-max — needs MaxHeight clamp, QUM-483)\n%s",
			h, gotLines, wantOuterH, out)
	}

	// Width clamp: a single very long line into a w-wide content area.
	wide := strings.Repeat("y", 80)
	out = m.renderPanel(wide, w, h, false)
	maxW := 0
	for _, ln := range strings.Split(out, "\n") {
		if lw := lipgloss.Width(ln); lw > maxW {
			maxW = lw
		}
	}
	if maxW > wantOuterW {
		t.Errorf("renderPanel(content=80-wide line, w=%d) max line width = %d; want <= %d (needs MaxWidth clamp, QUM-483)\n%s",
			w, maxW, wantOuterW, out)
	}
}
