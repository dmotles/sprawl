package tui

import (
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

const placeholderContent = `Welcome to Sprawl TUI

This is the output viewport. Agent output will appear here.

Use PgUp/PgDn to scroll through content.
Use Tab/Shift+Tab to switch between panels.
Press Ctrl+C to quit.

---

Waiting for agent activity...`

// ViewportModel wraps a bubbles viewport with theme styling.
type ViewportModel struct {
	vp    viewport.Model
	theme *Theme
}

// NewViewportModel creates a viewport with placeholder content.
func NewViewportModel(theme *Theme) ViewportModel {
	vp := viewport.New()
	vp.SetContent(placeholderContent)
	return ViewportModel{
		vp:    vp,
		theme: theme,
	}
}

// Update delegates to the inner viewport for scroll handling.
func (m ViewportModel) Update(msg tea.Msg) (ViewportModel, tea.Cmd) {
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// View renders the viewport content.
func (m ViewportModel) View() string {
	return m.vp.View()
}

// SetSize updates the viewport dimensions.
func (m *ViewportModel) SetSize(w, h int) {
	m.vp.SetWidth(w)
	m.vp.SetHeight(h)
}

// SetContent replaces the viewport content.
func (m *ViewportModel) SetContent(s string) {
	m.vp.SetContent(s)
}
