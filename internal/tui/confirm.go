package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ConfirmModel renders a centered confirmation dialog that accepts y/n input.
type ConfirmModel struct {
	theme   *Theme
	width   int
	height  int
	visible bool
	message string
}

// NewConfirmModel constructs a confirm dialog with the given theme.
func NewConfirmModel(theme *Theme) ConfirmModel {
	return ConfirmModel{
		theme:   theme,
		message: "Quit? This will stop all agents. [y/n]",
	}
}

// Show makes the dialog visible.
func (m *ConfirmModel) Show() { m.visible = true }

// Hide makes the dialog hidden.
func (m *ConfirmModel) Hide() { m.visible = false }

// Visible returns whether the dialog is showing.
func (m ConfirmModel) Visible() bool { return m.visible }

// SetSize updates the available area for centering.
func (m *ConfirmModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// Update handles y/n input while the dialog is visible.
func (m ConfirmModel) Update(msg tea.KeyPressMsg) (ConfirmModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	key := unicode.ToLower(msg.Code)
	switch key {
	case 'y':
		return m, func() tea.Msg { return ConfirmResultMsg{Confirmed: true} }
	case 'n':
		return m, func() tea.Msg { return ConfirmResultMsg{Confirmed: false} }
	}
	return m, nil
}

// View renders the centered dialog box. Returns empty string when hidden.
func (m ConfirmModel) View() string {
	if !m.visible {
		return ""
	}

	dialogWidth := 42
	content := m.message

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.theme.AccentColor)).
		Background(lipgloss.Color(backgroundColor)).
		Foreground(lipgloss.Color("252")).
		Padding(1, 2).
		Width(dialogWidth).
		Align(lipgloss.Center).
		Render(content)

	// Center the box in the available area.
	if m.width > 0 && m.height > 0 {
		// Build a full-size background and place the dialog in the center.
		boxLines := strings.Count(box, "\n") + 1
		boxWidth := lipgloss.Width(box)

		// Pad horizontally.
		leftPad := (m.width - boxWidth) / 2
		if leftPad < 0 {
			leftPad = 0
		}
		// Pad vertically.
		topPad := (m.height - boxLines) / 2
		if topPad < 0 {
			topPad = 0
		}

		var sb strings.Builder
		for range topPad {
			sb.WriteString(strings.Repeat(" ", m.width))
			sb.WriteByte('\n')
		}
		for _, line := range strings.Split(box, "\n") {
			sb.WriteString(strings.Repeat(" ", leftPad))
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
		return sb.String()
	}

	return box
}
