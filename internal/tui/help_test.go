package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestHelpModel(t *testing.T) HelpModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewHelpModel(&theme)
}

func TestHelpModel_View_ContainsTitle(t *testing.T) {
	m := newTestHelpModel(t)
	m.SetSize(80, 24)
	view := stripANSI(m.View())
	if !strings.Contains(view, "Keybindings") {
		t.Errorf("View() should contain 'Keybindings', got:\n%s", view)
	}
}

func TestHelpModel_View_ContainsAllBindings(t *testing.T) {
	m := newTestHelpModel(t)
	m.SetSize(80, 24)
	view := stripANSI(m.View())

	expected := []string{
		"Toggle help",
		"Cycle observed agent",
		"Scroll output",
		"Quit",
		"Dismiss help",
		// QUM-845: message-queueing shortcuts.
		"Send all queued messages now (flush)",
		"Recall queued messages into input",
		"Queues the message",
	}
	for _, exp := range expected {
		if !strings.Contains(view, exp) {
			t.Errorf("View() should contain %q, got:\n%s", exp, view)
		}
	}
}

// QUM-845: the queueing shortcuts must be annotated weave-only so they aren't
// implied to work while observing a child.
func TestHelpModel_View_QueueingShortcutsAreWeaveOnly(t *testing.T) {
	m := newTestHelpModel(t)
	m.SetSize(80, 24)
	view := stripANSI(m.View())

	for _, exp := range []string{"(weave)", "(weave busy)"} {
		if !strings.Contains(view, exp) {
			t.Errorf("View() should annotate queueing shortcuts with %q, got:\n%s", exp, view)
		}
	}
}

func TestHelpModel_View_ContainsKeyLabels(t *testing.T) {
	m := newTestHelpModel(t)
	m.SetSize(80, 24)
	view := stripANSI(m.View())

	keys := []string{"F1", "PgUp", "PgDn", "Ctrl+C", "Esc", "Ctrl+G", "Ctrl+U"}
	for _, key := range keys {
		if !strings.Contains(view, key) {
			t.Errorf("View() should contain key label %q, got:\n%s", key, view)
		}
	}
}

// --- App-level help overlay tests ---

// QUM-695: `?` is no longer wired to help. The canonical help key is F1
// (see TestAppModel_F1TogglesHelp below).
func TestAppModel_QuestionMarkDoesNotToggleHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	if app.showHelp {
		t.Error("? should NOT open help post-QUM-695")
	}
}

func TestAppModel_F1TogglesHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)
	if !app.showHelp {
		t.Error("showHelp should be true after pressing F1")
	}

	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)
	if app.showHelp {
		t.Error("showHelp should be false after pressing F1 again")
	}
}

func TestAppModel_EscDismissesHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Open help via F1.
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)

	// Press Esc to dismiss.
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.showHelp {
		t.Error("showHelp should be false after pressing Esc")
	}
}

func TestAppModel_HelpSwallowsKeys(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Open help via F1 (post-QUM-695 the canonical help key).
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)
	if !app.showHelp {
		t.Fatal("setup: help should be open")
	}
	priorInput := app.input.Value()

	// QUM-695: Tab no longer cycles panels — assert the help overlay
	// swallows it instead of letting the input textarea consume it.
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = updated.(AppModel)
	if app.input.Value() != priorInput {
		t.Errorf("Tab should be swallowed by help overlay, input changed from %q to %q", priorInput, app.input.Value())
	}
	if !app.showHelp {
		t.Error("help overlay should remain open after Tab")
	}
}

func TestAppModel_CtrlCShowsConfirmWithHelpOpen(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Open help via F1.
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)

	// Ctrl+C should show confirm dialog (not quit directly).
	updated, _ = app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.showConfirm {
		t.Error("Ctrl+C with help open should show confirm dialog")
	}
}

func TestAppModel_ViewShowsHelpOverlay(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Open help via F1.
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)

	v := app.View()
	content := stripANSI(v.Content)
	if !strings.Contains(content, "Keybindings") {
		t.Errorf("View() with help open should contain 'Keybindings', got:\n%s", content)
	}
}

func TestAppModel_ViewHidesHelpWhenDismissed(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Open and close help via F1 → Esc.
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	v := app.View()
	content := stripANSI(v.Content)
	if strings.Contains(content, "Keybindings") {
		t.Errorf("View() after dismissing help should NOT contain 'Keybindings', got:\n%s", content)
	}
}
