package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ErrorDialogModel renders a centered modal dialog when the Claude subprocess
// crashes or encounters a fatal error. It offers the user 'r' to restart or
// 'q' to quit.
type ErrorDialogModel struct {
	err    error
	theme  *Theme
	width  int
	height int
}

// NewErrorDialog creates an ErrorDialogModel for the given error.
func NewErrorDialog(theme *Theme, err error) ErrorDialogModel {
	return ErrorDialogModel{
		err:   err,
		theme: theme,
	}
}

// SetSize updates the available area for centering the dialog.
func (m *ErrorDialogModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// Update handles key presses: 'r' to restart, 'q' to quit.
func (m ErrorDialogModel) Update(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.Code {
	case 'r':
		return func() tea.Msg { return RestartSessionMsg{} }
	case 'q':
		return tea.Quit
	}
	return nil
}

// View renders the error dialog box.
func (m ErrorDialogModel) View() string {
	dialogWidth := 50
	if m.width > 0 && m.width < dialogWidth+4 {
		dialogWidth = m.width - 4
	}
	if dialogWidth < 20 {
		dialogWidth = 20
	}

	title := "Session Error"
	errText := fmt.Sprintf("%v", m.err)
	hints := "[r] restart  [q] quit"

	content := fmt.Sprintf(
		"%s\n\n%s\n\n%s",
		title,
		errText,
		hints,
	)

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.theme.AccentColor)).
		Padding(1, 2).
		Width(dialogWidth).
		Align(lipgloss.Center)

	box := boxStyle.Render(content)

	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}
