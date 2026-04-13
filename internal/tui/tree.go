package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// TreeModel is the agent tree panel with placeholder data.
type TreeModel struct {
	items    []string
	selected int
	width    int
	height   int
	theme    *Theme
}

// NewTreeModel creates a tree model with placeholder agent names.
func NewTreeModel(theme *Theme) TreeModel {
	return TreeModel{
		items: []string{
			"weave (root)",
			"tower",
			"  finn",
			"  oak",
			"scout",
		},
		theme: theme,
	}
}

// Update handles key messages for tree navigation.
func (m TreeModel) Update(msg tea.Msg) (TreeModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.Code {
		case tea.KeyUp:
			if m.selected > 0 {
				m.selected--
			}
		case tea.KeyDown:
			if m.selected < len(m.items)-1 {
				m.selected++
			}
		}
	}
	return m, nil
}

// View renders the tree panel.
func (m TreeModel) View() string {
	if len(m.items) == 0 {
		return ""
	}

	var b strings.Builder
	for i, item := range m.items {
		if i >= m.height && m.height > 0 {
			break
		}
		if i == m.selected {
			b.WriteString(m.theme.SelectedItem.Render(fmt.Sprintf("> %s", item)))
		} else {
			b.WriteString(m.theme.NormalText.Render(fmt.Sprintf("  %s", item)))
		}
		if i < len(m.items)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// SetSize updates the tree panel dimensions.
func (m *TreeModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}
