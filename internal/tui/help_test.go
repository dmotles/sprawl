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
		"Cycle panel focus",
		"Navigate agent tree",
		"Select agent",
		"Scroll output",
		"Quit",
		"Dismiss help",
	}
	for _, exp := range expected {
		if !strings.Contains(view, exp) {
			t.Errorf("View() should contain %q, got:\n%s", exp, view)
		}
	}
}

func TestHelpModel_View_ContainsKeyLabels(t *testing.T) {
	m := newTestHelpModel(t)
	m.SetSize(80, 24)
	view := stripANSI(m.View())

	keys := []string{"F1", "Tab", "Shift+Tab", "Enter", "PgUp", "PgDn", "Ctrl+C", "Esc"}
	for _, key := range keys {
		if !strings.Contains(view, key) {
			t.Errorf("View() should contain key label %q, got:\n%s", key, view)
		}
	}
}

// --- App-level help overlay tests ---

func TestAppModel_QuestionMarkTogglesHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Press ? to open help.
	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	if !app.showHelp {
		t.Error("showHelp should be true after pressing '?'")
	}

	// Press ? again to close help.
	updated, _ = app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	if app.showHelp {
		t.Error("showHelp should be false after pressing '?' again")
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

	// Open help.
	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
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

	// Open help.
	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	panelBefore := app.activePanel

	// Tab should be swallowed (panel should not change).
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = updated.(AppModel)
	if app.activePanel != panelBefore {
		t.Errorf("activePanel changed from %d to %d while help is shown; Tab should be swallowed", panelBefore, app.activePanel)
	}
}

func TestAppModel_CtrlCShowsConfirmWithHelpOpen(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Open help.
	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
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

	// Open help.
	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
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

	// Open and close help.
	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	v := app.View()
	content := stripANSI(v.Content)
	if strings.Contains(content, "Keybindings") {
		t.Errorf("View() after dismissing help should NOT contain 'Keybindings', got:\n%s", content)
	}
}
