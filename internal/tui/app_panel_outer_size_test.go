package tui

// QUM-501 — renderPanel(w, h) treats w/h as *outer* (post-border) dimensions.
//
// Before QUM-501, callers passed layout slot dimensions minus a manual
// `-2` for the border frame at every call site. The new contract: w/h
// are the full bordered panel size, and lipgloss v2 sizes the content
// area inside the border for us.
//
// These tests pin the outer-sizing invariant: regardless of content
// size, the bordered panel comes out at exactly w x h.

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRenderPanel_OuterSizeMatchesDeclared asserts that for non-overflowing
// content, the rendered panel is exactly outerH lines and exactly outerW
// columns wide on every line. Covers both active and inactive border styles
// so a regression where the two styles drift in frame size is caught.
func TestRenderPanel_OuterSizeMatchesDeclared(t *testing.T) {
	m := newTestAppModel(t)

	const outerW, outerH = 20, 5

	for _, active := range []bool{false, true} {
		active := active
		name := "inactive"
		if active {
			name = "active"
		}
		t.Run(name, func(t *testing.T) {
			out := m.renderPanel("hello", outerW, outerH, active)
			lines := strings.Split(out, "\n")

			if len(lines) != outerH {
				t.Errorf("renderPanel(\"hello\", outerW=%d, outerH=%d, active=%v) produced %d lines; want exactly %d (QUM-501: w/h are outer dims)\n%s",
					outerW, outerH, active, len(lines), outerH, out)
			}

			maxW := 0
			for _, ln := range lines {
				if lw := lipgloss.Width(ln); lw > maxW {
					maxW = lw
				}
			}
			if maxW != outerW {
				t.Errorf("renderPanel(\"hello\", outerW=%d, outerH=%d, active=%v) max line width = %d; want exactly %d (QUM-501: w/h are outer dims)\n%s",
					outerW, outerH, active, maxW, outerW, out)
			}
		})
	}
}

// TestRenderPanel_DerivesContentFromFrame asserts the visible bordered
// panel hits exactly outerW × outerH regardless of how small the content
// is — the content area inside the border is derived from the style's
// frame size by lipgloss. The frame here comes from
// m.theme.InactiveBorder (active=false).
func TestRenderPanel_DerivesContentFromFrame(t *testing.T) {
	m := newTestAppModel(t)

	const outerW, outerH = 24, 6

	style := m.theme.InactiveBorder
	frameH := style.GetHorizontalFrameSize()
	frameV := style.GetVerticalFrameSize()
	if frameH == 0 || frameV == 0 {
		t.Fatalf("expected non-zero frame on InactiveBorder; got h=%d v=%d", frameH, frameV)
	}

	out := m.renderPanel("hi", outerW, outerH, false)
	lines := strings.Split(out, "\n")

	if len(lines) != outerH {
		t.Fatalf("renderPanel(\"hi\", outerW=%d, outerH=%d) produced %d lines; want exactly %d (QUM-501: outer dims include border)\n%s",
			outerW, outerH, len(lines), outerH, out)
	}

	// Every visible row — top border, middle rows, bottom border — must be
	// exactly outerW wide. A middle row (lines[1]) exercises the content
	// derivation: its width equals border-edge + content-area + border-edge,
	// where content-area was derived as outerW - frameH.
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != outerW {
			t.Errorf("renderPanel line %d width = %d; want %d (QUM-501: renderPanel must derive content = outer - frame, frameH=%d)\n%s",
				i, w, outerW, frameH, out)
		}
	}
}
