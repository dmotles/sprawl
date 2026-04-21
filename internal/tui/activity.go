package tui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
)

// activityAnsiRe strips ANSI escape sequences for the padLine over-width
// fallback. Kept local (not exported); the test helper in testutil_test.go
// is a separate, test-only copy.
var activityAnsiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// ActivityPanelModel renders a tail of ActivityEntry records for the currently
// observed agent (QUM-296). It is a display-only panel: no focus, no input.
// The panel is sized by SetSize and refreshed by SetEntries; the App delivers
// fresh data via ActivityTickMsg on each tick (§4.6 item 1, §4.4).
type ActivityPanelModel struct {
	theme   *Theme
	agent   string
	entries []agentloop.ActivityEntry
	width   int
	height  int
}

// NewActivityPanelModel constructs an empty panel bound to the given theme.
func NewActivityPanelModel(theme *Theme) ActivityPanelModel {
	return ActivityPanelModel{theme: theme}
}

// SetSize sets the inner content dimensions (without border).
func (m *ActivityPanelModel) SetSize(w, h int) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	m.width = w
	m.height = h
}

// SetAgent updates the observed-agent label. Entries are NOT cleared — the
// caller is expected to dispatch a fresh ActivityTickMsg shortly after.
func (m *ActivityPanelModel) SetAgent(name string) {
	m.agent = name
}

// SetEntries replaces the current tail. Oldest-first.
func (m *ActivityPanelModel) SetEntries(entries []agentloop.ActivityEntry) {
	m.entries = entries
}

// View renders the panel contents. Output lines are padded/truncated to
// m.width; total line count is at most m.height.
func (m ActivityPanelModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	headerLines := m.renderHeader()
	bodyHeight := m.height - len(headerLines)
	if bodyHeight < 1 {
		// Not enough room for a body; just return the header clipped.
		return strings.Join(headerLines[:m.height], "\n")
	}

	var body []string
	if len(m.entries) == 0 {
		body = []string{m.padLine(m.theme.PlaceholderStyle.Render("No activity yet."))}
	} else {
		// Tail the entries to fit bodyHeight.
		start := 0
		if len(m.entries) > bodyHeight {
			start = len(m.entries) - bodyHeight
		}
		for _, e := range m.entries[start:] {
			body = append(body, m.renderEntry(e))
		}
	}

	// Pad body to fill available height so the border stays clean.
	for len(body) < bodyHeight {
		body = append(body, m.padLine(""))
	}

	return strings.Join(append(headerLines, body...), "\n")
}

func (m ActivityPanelModel) renderHeader() []string {
	label := "Activity"
	if m.agent != "" {
		label = fmt.Sprintf("Activity — %s", m.agent)
	}
	styled := m.theme.AccentText.Render(label)
	return []string{m.padLine(styled)}
}

// renderEntry builds one panel line for an ActivityEntry: "HH:MM:SS <glyph> <body>".
func (m ActivityPanelModel) renderEntry(e agentloop.ActivityEntry) string {
	ts := e.TS.Format("15:04:05")
	tsStyled := m.theme.PlaceholderStyle.Render(ts)

	glyph, bodyStyle := kindStyle(m.theme, e.Kind)
	glyphStyled := bodyStyle.Render(glyph)

	body := entryBody(e)
	// Compute visible width budget: width - len(ts) - 1 - visible(glyph) - 1.
	budget := m.width - len(ts) - 1 - glyphVisibleWidth(glyph) - 1
	if budget < 4 {
		budget = 4
	}
	body = truncateRunes(body, budget)
	bodyStyled := bodyStyle.Render(body)

	line := tsStyled + " " + glyphStyled + " " + bodyStyled
	return m.padLine(line)
}

// entryBody returns the human-readable body for a given entry kind.
func entryBody(e agentloop.ActivityEntry) string {
	switch e.Kind {
	case "tool_use":
		// Summary already starts with the tool name; fall back to Tool field
		// if Summary is empty.
		if e.Summary != "" {
			return e.Summary
		}
		return e.Tool
	default:
		return e.Summary
	}
}

// kindStyle returns the glyph + lipgloss style used to render an entry of the
// given kind. Styles are keyed off the active theme so recoloring propagates.
func kindStyle(theme *Theme, kind string) (string, lipgloss.Style) {
	switch kind {
	case "tool_use":
		return "▶", theme.AccentText
	case "assistant_text":
		return "✎", theme.NormalText
	case "result":
		return "•", theme.PlaceholderStyle
	case "system":
		return "·", theme.PlaceholderStyle
	case "rate_limit":
		return "!", lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	default:
		return "·", theme.NormalText
	}
}

// padLine renders a single line padded/truncated to the panel's content width.
// The visible width of the input may already include ANSI escapes, so we
// measure via lipgloss.Width.
func (m ActivityPanelModel) padLine(s string) string {
	w := lipgloss.Width(s)
	if w > m.width {
		// Best-effort: fall back to a rune-truncated, style-less version.
		return truncateRunes(stripANSIString(s), m.width)
	}
	if w < m.width {
		return s + strings.Repeat(" ", m.width-w)
	}
	return s
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// glyphVisibleWidth returns the terminal width of a glyph; single-rune
// Unicode glyphs (our panel vocabulary) count as 1. lipgloss.Width is correct
// in general, but a string of one printable rune is always width 1 here.
func glyphVisibleWidth(glyph string) int {
	// lipgloss.Width handles the general case safely.
	w := lipgloss.Width(glyph)
	if w <= 0 {
		return 1
	}
	return w
}

// stripANSIString is a small helper for the over-width fallback. It mirrors
// the testutil stripANSI but is available in non-test code.
func stripANSIString(s string) string {
	return activityAnsiRe.ReplaceAllString(s, "")
}
