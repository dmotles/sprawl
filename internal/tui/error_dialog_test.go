package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestErrorDialog(t *testing.T, err error) ErrorDialogModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewErrorDialog(&theme, err)
}

func TestErrorDialog_View_ContainsErrorText(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("subprocess crashed"))
	d.SetSize(80, 24)
	view := stripANSI(d.View())
	if !strings.Contains(view, "subprocess crashed") {
		t.Errorf("View() should contain error text, got:\n%s", view)
	}
}

func TestErrorDialog_View_ShowsKeyHints(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("some error"))
	d.SetSize(80, 24)
	view := stripANSI(d.View())
	if !strings.Contains(view, "[r]") {
		t.Errorf("View() should show '[r]' key hint, got:\n%s", view)
	}
	if !strings.Contains(view, "[q]") {
		t.Errorf("View() should show '[q]' key hint, got:\n%s", view)
	}
	if !strings.Contains(view, "restart") {
		t.Errorf("View() should show 'restart' hint, got:\n%s", view)
	}
	if !strings.Contains(view, "quit") {
		t.Errorf("View() should show 'quit' hint, got:\n%s", view)
	}
}

func TestErrorDialog_View_ShowsTitle(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("some error"))
	d.SetSize(80, 24)
	view := stripANSI(d.View())
	if !strings.Contains(view, "Error") {
		t.Errorf("View() should contain a title with 'Error', got:\n%s", view)
	}
}

func TestErrorDialog_Update_R_ReturnsRestartCmd(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("crash"))
	cmd := d.Update(tea.KeyPressMsg{Code: 'r'})
	if cmd == nil {
		t.Fatal("pressing 'r' should return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(RestartSessionMsg); !ok {
		t.Errorf("pressing 'r' should produce RestartSessionMsg, got %T", msg)
	}
}

func TestErrorDialog_Update_Q_ReturnsQuit(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("crash"))
	cmd := d.Update(tea.KeyPressMsg{Code: 'q'})
	if cmd == nil {
		t.Fatal("pressing 'q' should return a cmd")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("pressing 'q' should produce QuitMsg, got %T", msg)
	}
}

func TestErrorDialog_Update_OtherKey_ReturnsNil(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("crash"))
	cmd := d.Update(tea.KeyPressMsg{Code: 'x'})
	if cmd != nil {
		t.Errorf("pressing 'x' should return nil cmd, got non-nil")
	}
}

func TestErrorDialog_View_WithoutSetSize(t *testing.T) {
	d := newTestErrorDialog(t, fmt.Errorf("no size set"))
	// Don't call SetSize — exercises the fallback path
	view := stripANSI(d.View())
	if !strings.Contains(view, "no size set") {
		t.Errorf("View() without SetSize should still render error text, got:\n%s", view)
	}
}
