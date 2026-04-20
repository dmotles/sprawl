package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// InputModel wraps a textinput for the bottom input panel.
type InputModel struct {
	ti       textinput.Model
	theme    *Theme
	width    int
	disabled bool
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

// Update handles key events: Enter submits, disabled blocks all input.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if m.disabled {
			return m, nil
		}
		// Intercept `/` as the very first character of an empty input: open
		// the command palette rather than inserting the literal slash. `/`
		// mid-text falls through and is inserted by textinput normally.
		if keyMsg.Code == '/' && m.ti.Value() == "" {
			return m, func() tea.Msg { return OpenPaletteMsg{} }
		}
		if keyMsg.Code == tea.KeyEnter {
			text := strings.TrimSpace(m.ti.Value())
			if text != "" {
				m.ti.SetValue("")
				return m, func() tea.Msg { return SubmitMsg{Text: text} }
			}
			return m, nil
		}
	}
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

// SetDisabled enables or disables the input.
func (m *InputModel) SetDisabled(disabled bool) {
	m.disabled = disabled
	if disabled {
		m.ti.Placeholder = "Thinking..."
	} else {
		m.ti.Placeholder = "Type a message..."
	}
}
