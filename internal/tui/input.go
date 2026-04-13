package tui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// InputModel wraps a textinput for the bottom input panel.
type InputModel struct {
	ti    textinput.Model
	theme *Theme
	width int
}

// NewInputModel creates an input model with a placeholder prompt.
func NewInputModel(theme *Theme) InputModel {
	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	return InputModel{
		ti:    ti,
		theme: theme,
	}
}

// Update delegates to the inner textinput.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

// View renders the input field.
func (m InputModel) View() string {
	return m.ti.View()
}

// SetWidth updates the input width.
func (m *InputModel) SetWidth(w int) {
	m.width = w
	m.ti.SetWidth(w)
}

// Focus activates the input for typing.
func (m *InputModel) Focus() tea.Cmd {
	return m.ti.Focus()
}

// Blur deactivates the input.
func (m *InputModel) Blur() {
	m.ti.Blur()
}
