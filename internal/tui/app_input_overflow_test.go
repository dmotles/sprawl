package tui

// QUM-448 — regression test for input-panel vertical overflow.
//
// When typing or pasting grows the textarea past the default 3-row input
// height, AppModel.resizePanels() must run again so the cached
// tree/viewport/activity sub-models shrink to match the new layout. Without
// that propagation the cached panels keep rendering at their pre-grow height
// and JoinVertical(mainRow, inputView, status) overflows the terminal —
// status bar (and eventually the bottom of the input box itself) gets
// clipped off the bottom of the screen.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestPaste_DoesNotOverflowTerminal exercises the PasteMsg path. A large
// paste grows the textarea to its MaxHeight (10 inner rows + 2 border = 12
// outer). On a 24-row terminal the composed View must still fit.
func TestPaste_DoesNotOverflowTerminal(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	app := updated.(AppModel)
	focusInputPanel(&app)

	big := strings.Repeat("paste-line\n", 30)
	updated, _ = app.Update(tea.PasteMsg{Content: big})
	app = updated.(AppModel)

	v := app.View()
	lines := strings.Split(strings.TrimRight(v.Content, "\n"), "\n")
	const termHeight = 24
	if got := len(lines); got > termHeight {
		t.Fatalf("rendered %d lines after paste, terminal height is %d — input growth pushed cached panels off-screen (QUM-448)\n%s",
			got, termHeight, v.Content)
	}
}

// TestKeyTypingGrowth_DoesNotOverflowTerminal exercises the PanelInput
// keypress path. Pressing Enter several times grows the textarea the same
// way newlines do; the cached panels must shrink each time.
func TestKeyTypingGrowth_DoesNotOverflowTerminal(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	app := updated.(AppModel)
	focusInputPanel(&app)

	// Insert content via key events that go through the PanelInput branch
	// of delegateKey (app.go ~1626). We send Shift+Enter which the
	// textarea inserts as a literal newline (Enter alone is captured by
	// the app as submit).
	for i := 0; i < 15; i++ {
		updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
		app = updated.(AppModel)
	}

	v := app.View()
	lines := strings.Split(strings.TrimRight(v.Content, "\n"), "\n")
	const termHeight = 24
	if got := len(lines); got > termHeight {
		t.Fatalf("rendered %d lines after typing, terminal height is %d — input growth pushed cached panels off-screen (QUM-448)\n%s",
			got, termHeight, v.Content)
	}
}
