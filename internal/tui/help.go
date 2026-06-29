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
		{"F1", "Toggle help"},
		{"Ctrl+N / Ctrl+P", "Cycle observed agent"},
		{"Ctrl+O", "Toggle expand tool-call inputs and outputs"},
		{"Ctrl+V", "Toggle validate-output popup (while merge validate running)"},
		{"/switch <name>", "Switch agent (fuzzy match)"},
		{"PgUp / PgDn / Home / End", "Scroll output"},
		{"Ctrl+C", "Clear input / Quit if empty"},
		{"Shift+Enter", "Insert newline in input"},
		{"Alt+Enter / Ctrl+J", "Insert newline in input"},
		{`Trailing \ + Enter`, "Insert newline (line continuation)"},
		{"Esc (queued msg)", "Reload into input if empty; else clear queue (refuse to clobber)"},
		{"Ctrl+G (weave)", "Send all queued messages now (flush)"},
		{"Ctrl+U (weave)", "Recall queued messages into input for editing"},
		{"Type + Enter (weave busy)", "Queues the message (shown as a pending bubble)"},
		{"Up / Down", "Navigate input history (or scroll output when input empty)"},
		{"Ctrl+R", "Reverse-search input history"},
		{"Esc (search)", "Cancel reverse search"},
		{"Esc", "Dismiss help / clear queue / interrupt turn"},
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
		BorderForeground(m.theme.Palette.Primary).
		Background(m.theme.Palette.BgBase).
		Padding(1, 2)

	box := boxStyle.Render(content)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}
