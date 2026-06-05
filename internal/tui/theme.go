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
	Palette Palette
	// ActiveBorder / InactiveBorder are kept for caller-side symmetry but
	// QUM-661 stripped their rounded border + Palette.BgBase fill so the
	// chassis renders terminal-native. They are now zero-frame, no-bg
	// styles; the rename/removal is deferred to the QUM-655 sweep.
	//
	// Note: the Theme.Background field was removed in QUM-661 — it was
	// unreferenced outside the theme smoke test.
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
	// ThinkingText is the dim/zinc foreground used for the ✻ thinking…
	// transient marker (QUM-677 S7 v3). Darker than FgMostSubtle so the
	// marker reads as ambient/ephemeral and does not compete with assistant
	// text for the user's attention.
	ThinkingText lipgloss.Style
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

	// QUM-664: stub fields for visual-identity spike port. Zero-value styles
	// until the implementer phase wires Palette.UserPrompt / Palette.InputBar
	// into NewTheme.
	UserPromptText lipgloss.Style
	InputBarStyle  lipgloss.Style
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

	// QUM-661: chassis styles intentionally omit .Background(...) so the
	// host terminal's bg shows through. The only style that still paints a
	// bg is StatusBar (Palette.BgLessVisible — the deliberate redesign
	// anchor). Palette.BgBase remains in use by modal overlays
	// (palette.go / help.go / confirm.go / shorthelp.go) so floating
	// surfaces still read as elevated against the chassis.
	return Theme{
		AccentColor:      accentColor,
		Palette:          pal,
		ActiveBorder:     lipgloss.NewStyle(),
		InactiveBorder:   lipgloss.NewStyle(),
		AccentText:       lipgloss.NewStyle().Foreground(pal.Primary),
		NormalText:       lipgloss.NewStyle().Foreground(pal.FgBase),
		ErrorText:        lipgloss.NewStyle().Foreground(pal.Error),
		SystemText:       lipgloss.NewStyle().Foreground(pal.System),
		ThinkingText:     lipgloss.NewStyle().Foreground(lipgloss.Color("#52525B")).Italic(true),
		NotificationText: lipgloss.NewStyle().Foreground(pal.Accent),
		InterruptText:    lipgloss.NewStyle().Foreground(pal.Warning),
		StatusChangeText: lipgloss.NewStyle().Foreground(pal.FgSubtle),
		// No Padding — StatusBarModel.View manages its own left/right spacing
		// inside `line` and sets `.Width(m.width)`. Adding Padding here makes
		// the rendered width m.width+2 which wraps the trailing right-side
		// segments onto a second line at most terminal widths.
		StatusBar: lipgloss.NewStyle().
			Foreground(pal.FgBase).
			Background(pal.BgLessVisible),
		SelectedItem:      lipgloss.NewStyle().Foreground(pal.Primary).Bold(true),
		PlaceholderStyle:  lipgloss.NewStyle().Foreground(pal.FgMostSubtle).Faint(true),
		ReportDotWorking:  lipgloss.NewStyle().Foreground(pal.Success),
		ReportDotBlocked:  lipgloss.NewStyle().Foreground(pal.Busy),
		ReportDotFailure:  lipgloss.NewStyle().Foreground(pal.Error),
		ReportDotComplete: lipgloss.NewStyle().Foreground(pal.Info),
		ReportDotIdle:     lipgloss.NewStyle().Foreground(pal.FgMostSubtle),
		// QUM-664: visual-identity spike — bold bright-blue chevron + grey
		// vertical bar gutter sourced from the semantic palette.
		UserPromptText: lipgloss.NewStyle().Foreground(pal.UserPrompt).Bold(true),
		InputBarStyle:  lipgloss.NewStyle().Foreground(pal.InputBar),
	}
}
