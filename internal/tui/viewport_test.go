package tui

import (
	"strings"
	"testing"
)

func newTestViewportModel(t *testing.T) ViewportModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewViewportModel(&theme)
}

func TestViewportModel_InitialContent(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	view := m.View()
	if len(strings.TrimSpace(view)) == 0 {
		t.Error("View() should not be empty initially")
	}
}

func TestViewportModel_SetContent(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.SetContent("hello world test content")
	view := m.View()
	if !strings.Contains(view, "hello world test content") {
		t.Errorf("View() should contain set content, got:\n%s", view)
	}
}

func TestViewportModel_SetSize(t *testing.T) {
	m := newTestViewportModel(t)
	// Should not panic.
	m.SetSize(80, 30)
}
