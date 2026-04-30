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
	m.ta.SetValue("hello")

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
	if m.ta.Value() != "" {
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

// --- Tests for QUM-381: multi-line textarea migration ---

// TestInputModel_ShiftEnterInsertsNewline verifies that shift+enter inserts a
// newline into the input rather than submitting. With the current textinput
// implementation this will FAIL because textinput does not handle shift+enter
// as newline insertion. After the textarea migration it should pass.
func TestInputModel_ShiftEnterInsertsNewline(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Seed the input with some text.
	m.ta.SetValue("hello")

	// Send shift+enter — should insert a newline, not submit.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	m = updated

	// Shift+enter must NOT produce a SubmitMsg.
	if cmd != nil {
		msg := cmd()
		if _, ok := msg.(SubmitMsg); ok {
			t.Fatal("shift+enter should not produce SubmitMsg")
		}
	}

	// The value should now contain a newline.
	val := m.ta.Value()
	if !strings.Contains(val, "\n") {
		t.Errorf("after shift+enter, value should contain a newline, got %q", val)
	}
}

// TestInputModel_EnterStillSubmitsMultiLine verifies that pressing Enter
// submits even when the input contains multi-line text. The SubmitMsg.Text
// should preserve the full multi-line content.
func TestInputModel_EnterStillSubmitsMultiLine(t *testing.T) {
	m := newTestInputModel(t)
	_ = m.Focus()

	// Seed multi-line content directly.
	m.ta.SetValue("line1\nline2")

	updated, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated

	if cmd == nil {
		t.Fatal("Enter with multi-line text should return a cmd")
	}
	msg := cmd()
	submitMsg, ok := msg.(SubmitMsg)
	if !ok {
		t.Fatalf("Enter cmd returned %T, want SubmitMsg", msg)
	}
	expected := "line1\nline2"
	if submitMsg.Text != expected {
		t.Errorf("SubmitMsg.Text = %q, want %q", submitMsg.Text, expected)
	}
}
