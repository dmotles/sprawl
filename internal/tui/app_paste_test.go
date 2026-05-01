package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-430: bracketed-paste support for the TUI input bar.
//
// AppModel.Update should route tea.PasteMsg to the embedded InputModel so
// pasted content (including embedded newlines) is inserted verbatim into the
// input textarea. Paste must only be accepted when the input bar is the
// effective focus: observing the root agent, input panel active, no
// modal/palette open.

// containsSubmitMsg drains a tea.Cmd (recursively expanding tea.BatchMsg)
// and returns true if any produced message is a SubmitMsg.
func containsSubmitMsg(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	for _, msg := range collectBatchMsgs(t, cmd) {
		if _, ok := msg.(SubmitMsg); ok {
			return true
		}
	}
	return false
}

// focusInputPanel mirrors how the input panel becomes active in real usage:
// activePanel set to PanelInput followed by updateFocus() so the embedded
// textarea is actually focused.
func focusInputPanel(m *AppModel) {
	m.activePanel = PanelInput
	m.updateFocus()
}

func TestAppModel_PasteMsg_MultilineInsertedVerbatim(t *testing.T) {
	m := newTestAppModel(t)
	focusInputPanel(&m)

	updated, cmd := m.Update(tea.PasteMsg{Content: "line1\nline2\nline3"})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	got := app.input.Value()
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("input.Value() after paste = %q, want %q", got, want)
	}

	if containsSubmitMsg(t, cmd) {
		t.Error("paste should not produce SubmitMsg")
	}
}

func TestAppModel_PasteMsg_IgnoredWhenTreeFocused(t *testing.T) {
	m := newTestAppModel(t)
	// Default startPanel is PanelTree when bridge is nil; assert it explicitly
	// so this test fails loudly if that default ever changes.
	m.activePanel = PanelTree
	m.updateFocus()
	prior := m.input.Value()

	updated, cmd := m.Update(tea.PasteMsg{Content: "tree-browse paste\nshould be ignored"})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	if app.input.Value() != prior {
		t.Errorf("input.Value() = %q, want unchanged %q (tree focused)", app.input.Value(), prior)
	}
	if cmd != nil {
		t.Errorf("paste with tree focused should return nil cmd, got non-nil")
	}
}

func TestAppModel_PasteMsg_IgnoredWhenPaletteOpen(t *testing.T) {
	m := newTestAppModel(t)
	m.showPalette = true
	prior := m.input.Value()

	updated, cmd := m.Update(tea.PasteMsg{Content: "hello\nworld"})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	if app.input.Value() != prior {
		t.Errorf("input.Value() = %q, want unchanged %q (palette open)", app.input.Value(), prior)
	}
	if cmd != nil {
		t.Errorf("paste with palette open should return nil cmd, got non-nil")
	}
}

func TestAppModel_PasteMsg_IgnoredWhenObservingChildAgent(t *testing.T) {
	m := newTestAppModel(t)
	m.observedAgent = "some-child"
	if m.observedAgent == m.rootAgent {
		t.Fatalf("test setup: observedAgent must differ from rootAgent")
	}
	prior := m.input.Value()

	updated, _ := m.Update(tea.PasteMsg{Content: "nope"})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	if app.input.Value() != prior {
		t.Errorf("input.Value() = %q, want unchanged %q (observing child)", app.input.Value(), prior)
	}
}

func TestAppModel_PasteMsg_IgnoredWhenModalOpen(t *testing.T) {
	cases := []struct {
		name string
		set  func(*AppModel)
	}{
		{"showHelp", func(m *AppModel) { m.showHelp = true }},
		{"showConfirm", func(m *AppModel) { m.showConfirm = true }},
		{"showError", func(m *AppModel) { m.showError = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestAppModel(t)
			tc.set(&m)
			prior := m.input.Value()

			updated, _ := m.Update(tea.PasteMsg{Content: "ignored\ncontent"})
			app, ok := updated.(AppModel)
			if !ok {
				t.Fatalf("Update returned %T, want AppModel", updated)
			}

			if app.input.Value() != prior {
				t.Errorf("input.Value() = %q, want unchanged %q (%s open)", app.input.Value(), prior, tc.name)
			}
		})
	}
}

func TestAppModel_PasteMsg_PreservesTrailingNewline(t *testing.T) {
	m := newTestAppModel(t)
	focusInputPanel(&m)

	updated, _ := m.Update(tea.PasteMsg{Content: "abc\n"})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	got := app.input.Value()
	want := "abc\n"
	if got != want {
		t.Errorf("input.Value() = %q, want %q (trailing newline must be preserved)", got, want)
	}
}
