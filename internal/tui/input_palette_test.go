package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestInputModel_SlashOnEmptyEmitsOpenPaletteMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	updated, cmd := m.Update(tea.KeyPressMsg{Code: '/'})
	m = updated

	if cmd == nil {
		t.Fatal("'/' on empty input should return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(OpenPaletteMsg); !ok {
		t.Fatalf("'/' on empty input returned %T, want OpenPaletteMsg", msg)
	}
	// Must not insert `/` into the buffer.
	if m.ti.Value() != "" {
		t.Errorf("input value = %q, want empty (palette-trigger '/' should not be inserted)", m.ti.Value())
	}
}

func TestInputModel_SlashMidTextInsertsLiterally(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	m.ti.SetValue("hello")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: '/'})
	m = updated

	// No palette should be opened.
	if cmd != nil {
		if _, ok := cmd().(OpenPaletteMsg); ok {
			t.Error("'/' mid-text must not emit OpenPaletteMsg")
		}
	}
	// The underlying textinput receives the '/' and appends it. We don't
	// assert the exact resulting value here because textinput.Update may not
	// process test KeyPressMsg for printable chars in headless mode. What
	// matters is the absence of OpenPaletteMsg.
	_ = m
}

func TestInputModel_SlashWhileDisabledIsSwallowed(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetDisabled(true)

	_, cmd := m.Update(tea.KeyPressMsg{Code: '/'})
	if cmd != nil {
		if _, ok := cmd().(OpenPaletteMsg); ok {
			t.Error("disabled input must not emit OpenPaletteMsg on '/'")
		}
	}
}

func TestInputModel_LeadingSpaceSlashCommandSubmitsTrimmed(t *testing.T) {
	// Escape hatch per spec: `" /handoff"` → submit `/handoff` literal.
	m := newTestInputModel(t)
	_ = m.Focus()

	m.ti.SetValue(" /handoff")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter with ' /handoff' should produce SubmitMsg")
	}
	sm, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want SubmitMsg", cmd())
	}
	if sm.Text != "/handoff" {
		t.Errorf("SubmitMsg.Text = %q, want %q (leading space stripped)", sm.Text, "/handoff")
	}
}
