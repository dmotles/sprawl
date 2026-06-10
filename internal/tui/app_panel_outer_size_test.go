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

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TestChatViewport_FillsLayoutSlotNoSlack (QUM-779) pins that the chat
// region's inner viewport is sized to exactly layout.ViewportWidth ×
// layout.ViewportHeight after a WindowSizeMsg — no `-4` slack from the
// stale pre-QUM-661 border reservation. Lipgloss pads under-sized
// content with blank rows at the bottom, so any deficit here surfaces as
// blank rows wedged between the last chat line and the input box.
func TestChatViewport_FillsLayoutSlotNoSlack(t *testing.T) {
	m := newTestAppModel(t)
	const w, h = 120, 30
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	layout := ComputeLayout(w, h, (&app).inputBoxHeight())
	vp := (&app).rootVP()
	if got := vp.Height(); got != layout.ViewportHeight {
		t.Errorf("rootVP.Height() = %d, want layout.ViewportHeight = %d (QUM-779: no -4 slack)",
			got, layout.ViewportHeight)
	}
	if got := vp.Width(); got != layout.ViewportWidth {
		t.Errorf("rootVP.Width() = %d, want layout.ViewportWidth = %d (QUM-779: no -4 slack)",
			got, layout.ViewportWidth)
	}
}

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

// TestRenderPanel_PadsContentToOuter asserts the visible panel hits exactly
// outerW × outerH regardless of how small the content is — lipgloss
// Width/Height pad the content area out to the declared outer dims.
//
// QUM-661: the rounded border was stripped from the panel style so the
// chassis renders terminal-native; the panel frame is now 0 and outer ==
// content area.
func TestRenderPanel_PadsContentToOuter(t *testing.T) {
	m := newTestAppModel(t)

	const outerW, outerH = 24, 6

	style := m.theme.InactiveBorder
	if fh, fv := style.GetHorizontalFrameSize(), style.GetVerticalFrameSize(); fh != 0 || fv != 0 {
		t.Fatalf("expected zero frame on InactiveBorder after QUM-661 chassis port; got h=%d v=%d", fh, fv)
	}

	out := m.renderPanel("hi", outerW, outerH, false)
	lines := strings.Split(out, "\n")

	if len(lines) != outerH {
		t.Fatalf("renderPanel(\"hi\", outerW=%d, outerH=%d) produced %d lines; want exactly %d (QUM-501: outer dims)\n%s",
			outerW, outerH, len(lines), outerH, out)
	}

	// Every visible row must be exactly outerW wide — the panel pads its
	// content out to the declared outer dimensions.
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != outerW {
			t.Errorf("renderPanel line %d width = %d; want %d (QUM-501/QUM-661)\n%s",
				i, w, outerW, out)
		}
	}
}
