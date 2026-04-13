package tui

import (
	"strings"
	"testing"
)

func newTestStatusBarModel(t *testing.T) StatusBarModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewStatusBarModel(&theme, "myrepo", "v1.0.0", 3)
}

func TestStatusBar_ContainsRepoName(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "myrepo") {
		t.Errorf("View() should contain repo name 'myrepo', got:\n%s", view)
	}
}

func TestStatusBar_ContainsVersion(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "v1.0.0") {
		t.Errorf("View() should contain version 'v1.0.0', got:\n%s", view)
	}
}

func TestStatusBar_ContainsAgentCount(t *testing.T) {
	m := newTestStatusBarModel(t)
	m.SetWidth(80)
	view := m.View()
	if !strings.Contains(view, "3") {
		t.Errorf("View() should contain agent count '3', got:\n%s", view)
	}
}

func TestStatusBar_SetWidth(t *testing.T) {
	m := newTestStatusBarModel(t)
	// Should not panic.
	m.SetWidth(120)
}
