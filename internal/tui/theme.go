package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	defaultAccentColor = "39"
)

// Theme holds all Lip Gloss styles for the TUI, parameterized by accent color.
type Theme struct {
	AccentColor string
	// Palette exposes the semantic color roles (QUM-417). Call sites that
	// need a raw color should reach for Palette.<Role> rather than a raw
	// `lipgloss.Color("<ansi>")` literal.
	Palette        Palette
	Background     lipgloss.Style
	ActiveBorder   lipgloss.Style
	InactiveBorder lipgloss.Style
	AccentText     lipgloss.Style
	NormalText     lipgloss.Style
	// ErrorText is the red foreground used for failure indicators (e.g. the
	// ✗ glyph and result preview on a failed tool call — QUM-336).
	ErrorText lipgloss.Style
	// SystemText is the foreground used for system-injected viewport entries
	// (the mail-glyph inbox-drain rendering — QUM-338). Distinct from accent
	// (cyan) and error (red) so the human watching can tell at a glance the
	// system spoke, not the user.
	SystemText lipgloss.Style
	// NotificationText is the foreground used for async-class
	// `<system-notification>` viewport entries (QUM-557). Distinct from
	// SystemText (magenta/inbox-drain) and AccentText (cyan/user input)
	// so live and replay paths both render notifications identically.
	NotificationText lipgloss.Style
	// InterruptText is the foreground used for interrupt-class
	// `<system-notification>` entries — bodies starting with `[interrupt]`
	// (QUM-557). Amber to signal "act soon" without screaming-red.
	InterruptText lipgloss.Style
	// StatusChangeText is the foreground used for `type="status_change"`
	// `<system-notification>` entries (QUM-562). Dim grey to read as a muted
	// state-change pin, visually distinct from NotificationText cyan
	// (message-async) and InterruptText amber (message-interrupt).
	StatusChangeText lipgloss.Style
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

	pal := defaultDarkPalette(lipgloss.Color(accentColor))
	bg := pal.BgBase

	return Theme{
		AccentColor: accentColor,
		Palette:     pal,
		Background: lipgloss.NewStyle().
			Background(bg),
		ActiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(pal.Primary).
			Background(bg),
		InactiveBorder: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(pal.FgMostSubtle).
			Background(bg),
		AccentText: lipgloss.NewStyle().
			Foreground(pal.Primary).
			Background(bg),
		NormalText: lipgloss.NewStyle().
			Foreground(pal.FgBase).
			Background(bg),
		ErrorText: lipgloss.NewStyle().
			Foreground(pal.Error).
			Background(bg),
		// SystemText keeps its magenta hue (no Palette role yet — distinct
		// from Primary/Accent by design per QUM-338).
		SystemText: lipgloss.NewStyle().
			Foreground(lipgloss.Color("141")).
			Background(bg),
		NotificationText: lipgloss.NewStyle().
			Foreground(pal.Accent).
			Background(bg),
		InterruptText: lipgloss.NewStyle().
			Foreground(pal.Warning).
			Background(bg),
		StatusChangeText: lipgloss.NewStyle().
			Foreground(pal.FgSubtle).
			Background(bg),
		// No Padding — StatusBarModel.View manages its own left/right spacing
		// inside `line` and sets `.Width(m.width)`. Adding Padding here makes
		// the rendered width m.width+2 which wraps the trailing "? Help" onto
		// a second line at most terminal widths.
		StatusBar: lipgloss.NewStyle().
			Foreground(pal.FgBase).
			Background(pal.BgLessVisible),
		SelectedItem: lipgloss.NewStyle().
			Foreground(pal.Primary).
			Bold(true).
			Background(bg),
		PlaceholderStyle: lipgloss.NewStyle().
			Foreground(pal.FgMostSubtle).
			Faint(true).
			Background(bg),
		ReportDotWorking:  lipgloss.NewStyle().Foreground(pal.Success).Background(bg),
		ReportDotBlocked:  lipgloss.NewStyle().Foreground(pal.Busy).Background(bg),
		ReportDotFailure:  lipgloss.NewStyle().Foreground(pal.Error).Background(bg),
		ReportDotComplete: lipgloss.NewStyle().Foreground(pal.Info).Background(bg),
		ReportDotIdle:     lipgloss.NewStyle().Foreground(pal.FgMostSubtle).Background(bg),
	}
}
