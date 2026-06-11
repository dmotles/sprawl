package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// containsSparkle reports whether the (ANSI-stripped) text contains any
// sparkle glyph from the animation cycle.
func containsSparkle(s string) bool {
	plain := stripANSI(s)
	for _, g := range sparkleGlyphs {
		if strings.Contains(plain, g) {
			return true
		}
	}
	return false
}

// viewLines renders the view, strips ANSI, splits into lines, and trims a
// single trailing blank line produced by a final join newline.
func viewLines(v tea.View) []string {
	lines := strings.Split(stripANSI(v.Content), "\n")
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// TestSparkleTick_AdvancesFrame asserts a sparkleTickMsg increments the frame
// counter and re-arms the tick (self-perpetuating).
func TestSparkleTick_AdvancesFrame(t *testing.T) {
	app := resizedApp(t, 200, 60)
	before := app.sparkleFrame
	next, cmd := app.Update(sparkleTickMsg{})
	app = next.(AppModel)
	if app.sparkleFrame != before+1 {
		t.Errorf("sparkleFrame = %d, want %d", app.sparkleFrame, before+1)
	}
	if cmd == nil {
		t.Error("sparkleTickMsg handler must re-arm the tick (non-nil cmd)")
	}
}

// TestSparkle_VisibleAboveInput_WhenRootNonIdle asserts the sparkle row is
// drawn directly above the prompt input bar when the root agent is non-idle.
func TestSparkle_VisibleAboveInput_WhenRootNonIdle(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.turnState = TurnStreaming
	lines := viewLines(app.View())

	inputIdx := -1
	for i, ln := range lines {
		if strings.Contains(ln, "▌") {
			inputIdx = i
			break
		}
	}
	if inputIdx <= 0 {
		t.Fatalf("could not locate input bar row (▌) in view:\n%s", strings.Join(lines, "\n"))
	}
	if !containsSparkle(lines[inputIdx-1]) {
		t.Errorf("expected sparkle glyph on the row above the input bar (index %d);\n got %q\n--- view ---\n%s",
			inputIdx-1, lines[inputIdx-1], strings.Join(lines, "\n"))
	}
}

// TestSparkle_HiddenWhenRootIdle asserts no sparkle glyph appears when the
// root agent is idle.
func TestSparkle_HiddenWhenRootIdle(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.turnState = TurnIdle
	if containsSparkle(app.View().Content) {
		t.Errorf("sparkle glyph should be hidden when root is idle;\n view:\n%s", stripANSI(app.View().Content))
	}
}

// TestSparkle_ChildFooterWhenObservedChildWorking asserts the sparkle renders
// as the bottom footer row of an observed child pane when that child is
// working, and is absent when the child is idle.
func TestSparkle_ChildFooterWhenObservedChildWorking(t *testing.T) {
	working := func(t *testing.T, inTurn bool) tea.View {
		t.Helper()
		m := newTestAppModel(t)
		m.childNodes = []TreeNode{{Name: "kid", Type: "researcher", InTurn: inTurn}}
		m.observedAgent = "kid"
		_ = m.viewportFor("kid")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
		app := updated.(AppModel)
		return app.View()
	}

	// Working child: sparkle present on the footer row (row above status bar).
	lines := viewLines(working(t, true))
	if len(lines) < 2 {
		t.Fatalf("view too short: %d lines", len(lines))
	}
	footer := lines[len(lines)-2]
	if !containsSparkle(footer) {
		t.Errorf("expected sparkle on child pane footer (row above status);\n footer = %q\n--- view ---\n%s",
			footer, strings.Join(lines, "\n"))
	}

	// Idle child: no sparkle anywhere.
	if containsSparkle(working(t, false).Content) {
		t.Error("sparkle should be absent for an idle observed child")
	}
}

// TestSparkle_ChildFooter_RecentActivityPath pins the AC as written
// (DeriveIconState == "working") rather than the InTurn proxy: a child with
// InTurn=false but recent LastActivityAt is "working" and must show a sparkle.
func TestSparkle_ChildFooter_RecentActivityPath(t *testing.T) {
	m := newTestAppModel(t)
	m.childNodes = []TreeNode{{Name: "kid", LastActivityAt: time.Now()}}
	m.observedAgent = "kid"
	_ = m.viewportFor("kid")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app := updated.(AppModel)

	lines := viewLines(app.View())
	if len(lines) < 2 {
		t.Fatalf("view too short: %d lines", len(lines))
	}
	if !containsSparkle(lines[len(lines)-2]) {
		t.Errorf("expected sparkle for child working via recent-activity path;\n footer = %q", lines[len(lines)-2])
	}
}

// TestSparkle_HeightExact_NoOverflow asserts the reserved sparkle row keeps
// the composed view height exactly equal to the terminal height across sizes,
// for both root (non-idle) and child (working) panes.
func TestSparkle_HeightExact_NoOverflow(t *testing.T) {
	sizes := []struct{ w, h int }{{80, 24}, {200, 60}, {40, 15}}
	for _, sz := range sizes {
		// Root non-idle.
		app := resizedApp(t, sz.w, sz.h)
		app.turnState = TurnStreaming
		if got := lipgloss.Height(app.View().Content); got != sz.h {
			t.Errorf("root non-idle %dx%d: view height = %d, want %d", sz.w, sz.h, got, sz.h)
		}

		// Child working.
		m := newTestAppModel(t)
		m.childNodes = []TreeNode{{Name: "kid", InTurn: true}}
		m.observedAgent = "kid"
		_ = m.viewportFor("kid")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: sz.w, Height: sz.h})
		capp := updated.(AppModel)
		if got := lipgloss.Height(capp.View().Content); got != sz.h {
			t.Errorf("child working %dx%d: view height = %d, want %d", sz.w, sz.h, got, sz.h)
		}
	}
}

// TestSparkleTick_DoesNotInvalidateViewportCache is the QUM-769 guard: a frame
// tick must not invalidate the viewport / mainRow render caches (only the
// cheap composed row may change).
func TestSparkleTick_DoesNotInvalidateViewportCache(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.turnState = TurnStreaming // sparkle visible, so it animates per frame
	_ = app.View()

	vpBefore := app.cache.viewport
	vpKeyBefore := app.cache.viewportKey
	mainRowBefore := app.cache.mainRow
	composedKeyBefore := app.cache.composedKey
	contentBefore := app.View().Content

	next, _ := app.Update(sparkleTickMsg{})
	app = next.(AppModel)
	contentAfter := app.View().Content

	// The expensive caches (viewport panel + the joined main row) must be
	// preserved across a frame tick — that's the whole QUM-769 point.
	if app.cache.viewport != vpBefore {
		t.Error("viewport cache changed after sparkle tick (QUM-769 violation)")
	}
	if app.cache.viewportKey != vpKeyBefore {
		t.Error("viewport cache key changed after sparkle tick (QUM-769 violation)")
	}
	if app.cache.mainRow != mainRowBefore {
		t.Error("mainRow cache changed after sparkle tick (QUM-769 violation)")
	}
	// But the (cheap) composed cache MUST change, otherwise the sparkle would
	// freeze on one frame and never animate.
	if app.cache.composedKey == composedKeyBefore {
		t.Error("composed cache key did not change after sparkle tick — sparkle would not animate")
	}
	if contentAfter == contentBefore {
		t.Error("rendered content identical across sparkle tick — sparkle is not animating")
	}
}

// TestSparkle_ComposedEqualsUncached_WithSparkle asserts the cached fast-path
// compose is byte-identical to the uncached lipgloss oracle when the sparkle
// row is present, for both root and child panes.
func TestSparkle_ComposedEqualsUncached_WithSparkle(t *testing.T) {
	// Root non-idle.
	app := resizedApp(t, 200, 60)
	app.turnState = TurnStreaming
	if cached, uncached := app.View().Content, app.viewUncached().Content; cached != uncached {
		t.Errorf("root: cached View() != viewUncached() with sparkle present")
	}

	// Child working.
	m := newTestAppModel(t)
	m.childNodes = []TreeNode{{Name: "kid", InTurn: true}}
	m.observedAgent = "kid"
	_ = m.viewportFor("kid")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	capp := updated.(AppModel)
	if cached, uncached := capp.View().Content, capp.viewUncached().Content; cached != uncached {
		t.Errorf("child: cached View() != viewUncached() with sparkle present")
	}
}
