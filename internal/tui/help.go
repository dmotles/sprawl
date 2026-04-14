package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// HelpModel renders a centered overlay listing all TUI keybindings.
type HelpModel struct {
	theme  *Theme
	width  int
	height int
}

// NewHelpModel creates a help overlay model.
func NewHelpModel(theme *Theme) HelpModel {
	return HelpModel{theme: theme}
}

// SetSize updates the available area for centering the overlay.
func (m *HelpModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// View renders the help overlay as a centered box with keybindings.
func (m HelpModel) View() string {
	bindings := [][2]string{
		{"? / F1", "Toggle help"},
		{"Tab / Shift+Tab", "Cycle panel focus"},
		{"Up / Down", "Navigate agent tree"},
		{"Enter", "Select agent"},
		{"PgUp / PgDn", "Scroll output"},
		{"Ctrl+C", "Quit"},
		{"Esc", "Dismiss help"},
	}

	// Find max key width for alignment.
	maxKeyWidth := 0
	for _, b := range bindings {
		if len(b[0]) > maxKeyWidth {
			maxKeyWidth = len(b[0])
		}
	}

	keyStyle := m.theme.AccentText
	descStyle := m.theme.NormalText
	titleStyle := m.theme.AccentText.Bold(true)

	var lines []string
	lines = append(lines, titleStyle.Render("Keybindings"))
	lines = append(lines, "")

	for _, b := range bindings {
		key := keyStyle.Render(fmt.Sprintf("%-*s", maxKeyWidth, b[0]))
		desc := descStyle.Render("  " + b[1])
		lines = append(lines, key+desc)
	}

	content := strings.Join(lines, "\n")

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(m.theme.AccentColor)).
		Background(lipgloss.Color(backgroundColor)).
		Padding(1, 2)

	box := boxStyle.Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
