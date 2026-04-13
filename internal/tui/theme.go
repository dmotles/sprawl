package tui

import (
	"charm.land/lipgloss/v2"
)

const (
	defaultAccentColor = "colour39"
	backgroundColor    = "233"
	dimColor           = "240"
)

// Theme holds all Lip Gloss styles for the TUI, parameterized by accent color.
type Theme struct {
	AccentColor    string
	Background     lipgloss.Style
	ActiveBorder   lipgloss.Style
	InactiveBorder lipgloss.Style
	AccentText     lipgloss.Style
	NormalText     lipgloss.Style
	StatusBar      lipgloss.Style
	SelectedItem   lipgloss.Style
}

// NewTheme constructs a Theme with the given accent color.
// If accentColor is empty, a default is used.
func NewTheme(accentColor string) Theme {
	if accentColor == "" {
		accentColor = defaultAccentColor
	}

	accent := lipgloss.Color(accentColor)
	bg := lipgloss.Color(backgroundColor)
	dim := lipgloss.Color(dimColor)

	return Theme{
		AccentColor: accentColor,
		Background: lipgloss.NewStyle().
			Background(bg),
		ActiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Background(bg),
		InactiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(dim).
			Background(bg),
		AccentText: lipgloss.NewStyle().
			Foreground(accent).
			Background(bg),
		NormalText: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(bg),
		StatusBar: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236")).
			Padding(0, 1),
		SelectedItem: lipgloss.NewStyle().
			Foreground(accent).
			Bold(true).
			Background(bg),
	}
}
