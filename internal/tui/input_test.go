package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestInputModel(t *testing.T) InputModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewInputModel(&theme)
}

func TestInputModel_InitialState(t *testing.T) {
	m := newTestInputModel(t)
	view := m.View()
	if len(strings.TrimSpace(view)) == 0 {
		t.Error("View() should not be empty initially")
	}
}

func TestInputModel_SetWidth(t *testing.T) {
	m := newTestInputModel(t)
	// Should not panic.
	m.SetWidth(60)
}

func TestInputModel_FocusBlur(t *testing.T) {
	m := newTestInputModel(t)
	// Should not panic.
	_ = m.Focus()
	m.Blur()
}

// --- New tests for Enter key submission ---

func TestInputModel_EnterWithText_EmitsSubmitMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Set value directly (textinput may not process individual key chars in tests)
	m.ti.SetValue("hello")

	// Press Enter
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	// Should produce a cmd that yields SubmitMsg
	if cmd == nil {
		t.Fatal("Enter with text should return a cmd")
	}
	msg := cmd()
	submitMsg, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want SubmitMsg", msg)
	}
	if submitMsg.Text != "hello" {
		t.Errorf("SubmitMsg.Text = %q, want %q", submitMsg.Text, "hello")
	}

	// Input should be cleared after submit
	if m.ti.Value() != "" {
		t.Error("input should be cleared after Enter submission")
	}
}

func TestInputModel_EnterEmpty_NoSubmitMsg(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Press Enter with no text
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// Should not produce a SubmitMsg (either nil cmd, or cmd that returns nil)
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("Enter with empty input should not produce SubmitMsg")
		}
	}
}

func TestInputModel_DisabledIgnoresInput(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()
	m.SetDisabled(true)

	// Try typing while disabled
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'a'})
	m = updated

	// Try Enter while disabled
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	// Should not produce a submit command while disabled
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Error("disabled input should not produce SubmitMsg on Enter")
		}
	}
}

func TestInputModel_SetDisabledTrue(t *testing.T) {
	m := newTestInputModel(t)
	m.SetDisabled(true)
	if !m.disabled {
		t.Error("disabled should be true after SetDisabled(true)")
	}
}

func TestInputModel_SetDisabledFalse(t *testing.T) {
	m := newTestInputModel(t)
	m.SetDisabled(true)
	m.SetDisabled(false)
	if m.disabled {
		t.Error("disabled should be false after SetDisabled(false)")
	}
}
