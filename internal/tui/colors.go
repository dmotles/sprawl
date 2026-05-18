package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Palette is the semantic color palette for the TUI (QUM-417). Every color
// used by the TUI should be sourced from a Palette role rather than a raw
// `lipgloss.Color("<ansi>")` literal, so the visual language is centralized
// and themable.
//
// Roles:
//   - Primary: user-configurable accent (tracks the accentColor arg to NewTheme).
//   - Accent: secondary accent (notifications, async messages).
//   - Success / Warning / Error / Info: standard semantic signals.
//   - Busy: amber/yellow used for "working but pinned" indicators.
//   - FgBase / FgSubtle / FgMostSubtle: foreground intensities.
//   - BgBase / BgLessVisible: panel and status-bar backgrounds.
//   - System: magenta used for system-injected viewport entries (inbox-drain
//     citations, system notices — QUM-338/QUM-590). Distinct from Primary/Accent
//     by design so the human watching can tell at a glance the system spoke.
type Palette struct {
	Primary       color.Color
	Accent        color.Color
	Success       color.Color
	Warning       color.Color
	Error         color.Color
	Info          color.Color
	Busy          color.Color
	System        color.Color
	FgBase        color.Color
	FgSubtle      color.Color
	FgMostSubtle  color.Color
	BgBase        color.Color
	BgLessVisible color.Color
}

// defaultDarkPalette returns the default dark-terminal palette, with Primary
// bound to the caller-supplied accent. Color values preserve the pre-QUM-417
// pixel output so the migration is a pure refactor.
func defaultDarkPalette(accent color.Color) Palette {
	return Palette{
		Primary:       accent,
		Accent:        lipgloss.Color("39"),  // cyan-blue (notifications)
		Success:       lipgloss.Color("42"),  // green
		Warning:       lipgloss.Color("214"), // amber
		Error:         lipgloss.Color("196"), // red
		Info:          lipgloss.Color("51"),  // cyan
		Busy:          lipgloss.Color("220"), // yellow
		System:        lipgloss.Color("141"), // magenta (system/inbox-drain)
		FgBase:        lipgloss.Color("252"),
		FgSubtle:      lipgloss.Color("245"),
		FgMostSubtle:  lipgloss.Color("240"),
		BgBase:        lipgloss.Color("233"),
		BgLessVisible: lipgloss.Color("236"),
	}
}
