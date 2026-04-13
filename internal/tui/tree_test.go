package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestTreeModel(t *testing.T) TreeModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewTreeModel(&theme)
}

func TestTreeModel_InitialSelection(t *testing.T) {
	m := newTestTreeModel(t)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0", m.selected)
	}
}

func TestTreeModel_NavigateDown(t *testing.T) {
	m := newTestTreeModel(t)
	if len(m.items) < 2 {
		t.Skip("need at least 2 items for navigation test")
	}
	msg := tea.KeyPressMsg{Code: tea.KeyDown}
	m, _ = m.Update(msg)
	if m.selected != 1 {
		t.Errorf("selected = %d, want 1 after down key", m.selected)
	}
}

func TestTreeModel_NavigateUp(t *testing.T) {
	m := newTestTreeModel(t)
	if len(m.items) < 2 {
		t.Skip("need at least 2 items for navigation test")
	}
	// Move down first, then up.
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	m, _ = m.Update(down)
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	m, _ = m.Update(up)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0 after up key", m.selected)
	}
}

func TestTreeModel_BoundsCheckTop(t *testing.T) {
	m := newTestTreeModel(t)
	msg := tea.KeyPressMsg{Code: tea.KeyUp}
	m, _ = m.Update(msg)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0 (should not go negative)", m.selected)
	}
}

func TestTreeModel_BoundsCheckBottom(t *testing.T) {
	m := newTestTreeModel(t)
	if len(m.items) == 0 {
		t.Skip("need items for bounds check")
	}
	last := len(m.items) - 1
	// Navigate to the last item.
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	for i := 0; i < len(m.items)+5; i++ {
		m, _ = m.Update(down)
	}
	if m.selected != last {
		t.Errorf("selected = %d, want %d (should not exceed last item)", m.selected, last)
	}
}

func TestTreeModel_ViewContainsItems(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	view := m.View()
	for _, item := range m.items {
		if !strings.Contains(view, item) {
			t.Errorf("View() should contain item %q, got:\n%s", item, view)
		}
	}
}

func TestTreeModel_SetSize(t *testing.T) {
	m := newTestTreeModel(t)
	// Should not panic.
	m.SetSize(30, 15)
	if m.width != 30 {
		t.Errorf("width = %d, want 30", m.width)
	}
	if m.height != 15 {
		t.Errorf("height = %d, want 15", m.height)
	}
}
