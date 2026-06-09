package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// shortBinding is one key/hint pair shown in the short-help row.
type shortBinding struct {
	Key  string
	Hint string
}

// ShortHelpState is the set of inputs that determine which bindings the
// short-help row should advertise on the next render (QUM-420).
//
// It is intentionally a plain value type with no behaviour — callers build
// one from AppModel state and pass it to shortHelpBindings or
// ShortHelpModel.SetState.
type ShortHelpState struct {
	TurnState   TurnState
	InputEmpty  bool
	HasQueued   bool
	PaletteOpen bool
}

// shortHelpBindings returns the bindings to render for the given state. The
// result is ordered: state-specific bindings first, then the always-on
// pair (F1: help, ctrl+c: clear/quit). QUM-695 collapsed the focus/select
// branches — the input panel is the sole keystroke recipient.
func shortHelpBindings(s ShortHelpState) []shortBinding {
	bindings := make([]shortBinding, 0, 5)

	switch {
	case s.PaletteOpen:
		// Palette-open is exclusive: only palette navigation bindings show.
		bindings = append(bindings,
			shortBinding{Key: "↑↓/tab", Hint: "navigate"},
			shortBinding{Key: "enter", Hint: "run"},
			shortBinding{Key: "esc", Hint: "close"},
		)

	case s.TurnState == TurnStreaming || s.TurnState == TurnThinking:
		// QUM-630: queued + streaming means esc preempts (interrupt & send)
		// and ctrl+c recalls the queued msg into the prompt.
		if s.HasQueued {
			bindings = append(bindings,
				shortBinding{Key: "esc", Hint: "interrupt & send"},
				shortBinding{Key: "F1", Hint: "help"},
				shortBinding{Key: "ctrl+c", Hint: "edit"},
			)
			if len(bindings) > 5 {
				bindings = bindings[:5]
			}
			return bindings
		}
		// Streaming/thinking precedence: esc means interrupt.
		bindings = append(bindings, shortBinding{Key: "esc", Hint: "interrupt"})

	case s.HasQueued:
		// QUM-630: queued + idle. Esc sends the queued msg; ctrl+c recalls.
		bindings = append(bindings,
			shortBinding{Key: "esc", Hint: "send queued"},
			shortBinding{Key: "F1", Hint: "help"},
			shortBinding{Key: "ctrl+c", Hint: "edit"},
		)
		if len(bindings) > 5 {
			bindings = bindings[:5]
		}
		return bindings

	default:
		if s.InputEmpty {
			bindings = append(bindings, shortBinding{Key: "/", Hint: "commands"})
		}
	}

	// Always-on bindings, appended last.
	bindings = append(bindings,
		shortBinding{Key: "F1", Hint: "help"},
		shortBinding{Key: "ctrl+c", Hint: "clear/quit"},
	)

	if len(bindings) > 5 {
		bindings = bindings[:5]
	}
	return bindings
}

// ShortHelpModel renders the single-line short-help row (QUM-420).
//
// It is a passive view: callers set width and state on it before View() is
// invoked. No Bubble Tea Update is needed.
type ShortHelpModel struct {
	theme *Theme
	width int
	state ShortHelpState
}

// NewShortHelpModel constructs a ShortHelpModel bound to the given theme.
// The model starts at width=0; callers must call SetWidth before View() to
// produce a sized line.
func NewShortHelpModel(theme *Theme) ShortHelpModel {
	return ShortHelpModel{theme: theme}
}

// SetWidth installs the target terminal width.
func (m *ShortHelpModel) SetWidth(w int) {
	if w < 0 {
		w = 0
	}
	m.width = w
}

// SetState installs the next ShortHelpState.
func (m *ShortHelpModel) SetState(s ShortHelpState) {
	m.state = s
}

// View renders the short-help row as a single line padded (and truncated)
// to the configured width.
func (m ShortHelpModel) View() string {
	if m.width <= 0 {
		return ""
	}

	bindings := shortHelpBindings(m.state)

	var style lipgloss.Style
	if m.theme != nil {
		style = lipgloss.NewStyle().
			Foreground(m.theme.Palette.FgMostSubtle).
			Background(m.theme.Palette.BgBase).
			Faint(true)
	} else {
		style = lipgloss.NewStyle().Faint(true)
	}

	parts := make([]string, 0, len(bindings))
	for _, b := range bindings {
		parts = append(parts, b.Key+": "+b.Hint)
	}
	line := strings.Join(parts, " • ")
	rendered := style.Render(line)

	// Guard against widths narrower than the rendered content: truncate
	// before padding so PlaceHorizontal never emits a newline.
	if ansi.StringWidth(rendered) > m.width {
		rendered = ansi.Truncate(rendered, m.width, "…")
	}
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Left, rendered)
}
