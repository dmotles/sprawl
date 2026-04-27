package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	defaultAccentColor = "39"
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
	// ErrorText is the red foreground used for failure indicators (e.g. the
	// ✗ glyph and result preview on a failed tool call — QUM-336).
	ErrorText        lipgloss.Style
	StatusBar        lipgloss.Style
	SelectedItem     lipgloss.Style
	PlaceholderStyle lipgloss.Style

	// Per-agent report status chip colors (docs/designs/messaging-overhaul.md §4.7).
	ReportDotWorking  lipgloss.Style
	ReportDotBlocked  lipgloss.Style
	ReportDotFailure  lipgloss.Style
	ReportDotComplete lipgloss.Style
	ReportDotIdle     lipgloss.Style
}

// ReportDot returns the colored "●" for the given report state. Empty or
// unknown states render as the grey idle dot.
func (t *Theme) ReportDot(state string) string {
	const dot = "●"
	switch state {
	case "working":
		return t.ReportDotWorking.Render(dot)
	case "blocked":
		return t.ReportDotBlocked.Render(dot)
	case "failure":
		return t.ReportDotFailure.Render(dot)
	case "complete":
		return t.ReportDotComplete.Render(dot)
	default:
		return t.ReportDotIdle.Render(dot)
	}
}

// NewTheme constructs a Theme with the given accent color.
// If accentColor is empty, a default is used.
func NewTheme(accentColor string) Theme {
	if accentColor == "" {
		accentColor = defaultAccentColor
	}
	// Lip Gloss v2 doesn't understand tmux color names like "colour141".
	// Strip the prefix so we pass just the ANSI number (e.g., "141").
	accentColor = strings.TrimPrefix(accentColor, "colour")

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
		ErrorText: lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Background(bg),
		// No Padding — StatusBarModel.View manages its own left/right spacing
		// inside `line` and sets `.Width(m.width)`. Adding Padding here makes
		// the rendered width m.width+2 which wraps the trailing "? Help" onto
		// a second line at most terminal widths.
		StatusBar: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("236")),
		SelectedItem: lipgloss.NewStyle().
			Foreground(accent).
			Bold(true).
			Background(bg),
		PlaceholderStyle: lipgloss.NewStyle().
			Foreground(dim).
			Faint(true).
			Background(bg),
		ReportDotWorking:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Background(bg),  // green
		ReportDotBlocked:  lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Background(bg), // yellow
		ReportDotFailure:  lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Background(bg), // red
		ReportDotComplete: lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Background(bg),  // cyan
		ReportDotIdle:     lipgloss.NewStyle().Foreground(dim).Background(bg),                   // grey
	}
}
