package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestConfirmModel(t *testing.T) ConfirmModel {
	t.Helper()
	theme := NewTheme("212")
	return NewConfirmModel(&theme)
}

func TestConfirmModel_InitiallyHidden(t *testing.T) {
	m := newTestConfirmModel(t)
	if m.Visible() {
		t.Error("NewConfirmModel should not be visible initially")
	}
}

func TestConfirmModel_ShowAndHide(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	if !m.Visible() {
		t.Error("Show() should make dialog visible")
	}
	m.Hide()
	if m.Visible() {
		t.Error("Hide() should make dialog hidden")
	}
}

func TestConfirmModel_YConfirms(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	m.SetSize(80, 24)

	msg := tea.KeyPressMsg{Code: 'y'}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("pressing 'y' should return a cmd")
	}
	result := cmd()
	confirmResult, ok := result.(ConfirmResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want ConfirmResultMsg", result)
	}
	if !confirmResult.Confirmed {
		t.Error("pressing 'y' should produce Confirmed=true")
	}
}

func TestConfirmModel_NDismisses(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	m.SetSize(80, 24)

	msg := tea.KeyPressMsg{Code: 'n'}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("pressing 'n' should return a cmd")
	}
	result := cmd()
	confirmResult, ok := result.(ConfirmResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want ConfirmResultMsg", result)
	}
	if confirmResult.Confirmed {
		t.Error("pressing 'n' should produce Confirmed=false")
	}
}

func TestConfirmModel_OtherKeysSwallowed(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	m.SetSize(80, 24)

	keys := []tea.KeyPressMsg{
		{Code: 'a'},
		{Code: tea.KeyEnter},
		{Code: tea.KeyTab},
		{Code: tea.KeyEscape},
	}
	for _, msg := range keys {
		_, cmd := m.Update(msg)
		if cmd != nil {
			t.Errorf("key %v should not produce a cmd, got non-nil", msg.Code)
		}
	}
}

func TestConfirmModel_ViewContainsQuitText(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	m.SetSize(80, 24)

	view := stripANSI(m.View())
	if !strings.Contains(view, "Quit") {
		t.Errorf("visible dialog should contain 'Quit', got:\n%s", view)
	}
	if !strings.Contains(view, "y") && !strings.Contains(view, "n") {
		t.Errorf("visible dialog should contain y/n instructions, got:\n%s", view)
	}
}

func TestConfirmModel_ViewEmptyWhenHidden(t *testing.T) {
	m := newTestConfirmModel(t)
	view := m.View()
	if view != "" {
		t.Errorf("hidden dialog View() should be empty, got:\n%q", view)
	}
}

func TestConfirmModel_UppercaseYConfirms(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	m.SetSize(80, 24)

	msg := tea.KeyPressMsg{Code: 'Y'}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("pressing 'Y' should return a cmd")
	}
	result := cmd()
	confirmResult, ok := result.(ConfirmResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want ConfirmResultMsg", result)
	}
	if !confirmResult.Confirmed {
		t.Error("pressing 'Y' should produce Confirmed=true")
	}
}

func TestConfirmModel_UppercaseNDismisses(t *testing.T) {
	m := newTestConfirmModel(t)
	m.Show()
	m.SetSize(80, 24)

	msg := tea.KeyPressMsg{Code: 'N'}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("pressing 'N' should return a cmd")
	}
	result := cmd()
	confirmResult, ok := result.(ConfirmResultMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want ConfirmResultMsg", result)
	}
	if confirmResult.Confirmed {
		t.Error("pressing 'N' should produce Confirmed=false")
	}
}
