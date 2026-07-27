// Package tui — inline `/`-triggered command suggestion popover (QUM-864).
//
// The popover is a lightweight typeahead widget anchored just above the prompt
// input. It replaces the retired full-screen command palette. Visibility is a
// pure function of the current input text plus a per-entry Esc-dismiss latch,
// so the widget holds almost no state. Like the treeHud (QUM-805) it is
// composited onto the final rendered string — never a modal — so it never gates
// scroll, mouse, or typing.
package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

// Popover geometry (QUM-930). The box is sized to its content and bounded on
// both ends; it is never a fixed-width stub, and content is always truncated
// BEFORE the border is composed so the frame is closed at every width.
const (
	// popoverMaxWidth is a readable upper bound, not a fit-to-content cap. On a
	// 486-column terminal a 486-column popover is as wrong as a 52-column stub:
	// the eye has to travel the whole line to pair a command with its
	// description. 100 sits at the classic 80–100-column readable-prose bound
	// and comfortably fits every description in the registry today (the widest
	// natural row is 98 columns), so nothing elides on a wide terminal.
	popoverMaxWidth = 100

	// popoverFloorWidth keeps a very narrow terminal from collapsing the box to
	// nothing. Below ~24 columns the floor exceeds the composite budget and
	// compositeLeft will clip — unreachable through the app, which shows the
	// too-small fallback below MinTermWidth (40) and never composites the
	// popover there.
	popoverFloorWidth = 20

	// popoverFallbackWidth is the width used before the first WindowSizeMsg,
	// when the terminal width is not yet known. Only reachable from a direct
	// call: View() renders "Initializing..." until the model is ready.
	popoverFallbackWidth = 52

	// popoverGutter is what the composite costs: overlayBottomLeft indents the
	// box by 2 columns (app.go), and a matching 2-column right gutter keeps the
	// box strictly inside the base line so compositeLeft neither collapses the
	// indent nor truncates the border.
	popoverGutter = 4

	// popoverFrame is what lipgloss spends on the box itself: 2 border columns
	// plus 2 padding columns. In lipgloss v2 Width() is the TOTAL frame width,
	// so this is subtracted to get the text budget.
	popoverFrame = 4

	// popoverCursor marks the highlighted row; popoverCursorBlank is its
	// same-width filler on every other row. popoverCursorWidth must stay equal
	// to their display width — popoverWidthFor budgets for it, View renders it.
	// popoverGap separates the name column from the description column.
	popoverCursor      = "› "
	popoverCursorBlank = "  "
	popoverCursorWidth = 2
	popoverGap         = "  "
)

// popoverWidthFor computes the popover's total box width: wide enough for the
// widest `name  description` row, but never past the available terminal width
// (minus the composite gutter) or the readable upper bound, and never below the
// floor.
func popoverWidthFor(matches []commands.Command, termWidth int) int {
	avail := popoverFallbackWidth
	if termWidth > 0 {
		avail = termWidth - popoverGutter
	}

	nameW, descW := popoverColumnWidths(matches)
	natural := popoverFrame + popoverCursorWidth + nameW + len(popoverGap) + descW

	boxWidth := natural
	if avail < boxWidth {
		boxWidth = avail
	}
	if popoverMaxWidth < boxWidth {
		boxWidth = popoverMaxWidth
	}
	if boxWidth < popoverFloorWidth {
		boxWidth = popoverFloorWidth
	}
	return boxWidth
}

// popoverColumnWidths returns the display widths of the widest command name and
// the widest description across matches.
func popoverColumnWidths(matches []commands.Command) (nameW, descW int) {
	for _, c := range matches {
		if w := ansi.StringWidth(c.Name); w > nameW {
			nameW = w
		}
		if w := ansi.StringWidth(c.Description); w > descW {
			descW = w
		}
	}
	return nameW, descW
}

// truncateRow assembles one content row and fits it to exactly contentW display
// columns — padding when short, eliding with a visible "…" when long. The
// elision happens here, on the row text, so the border composed around it is
// always closed. ansi.Truncate is escape- and wide-rune-aware, so a styled
// prefix/name is safe to pass in.
func truncateRow(prefix, name, desc string, contentW int) string {
	if contentW <= 0 {
		return ""
	}
	row := prefix + name
	if desc != "" {
		row += popoverGap + desc
	}
	switch w := ansi.StringWidth(row); {
	case w > contentW:
		row = ansi.Truncate(row, contentW, "…")
		// Truncate can land a column short when a wide rune straddles the cut.
		if pad := contentW - ansi.StringWidth(row); pad > 0 {
			row += strings.Repeat(" ", pad)
		}
	case w < contentW:
		row += strings.Repeat(" ", contentW-w)
	}
	return row
}

// cmdPopover holds the popover's minimal mutable state. Visibility itself is
// derived (see popoverVisible); only the highlight index and the per-entry
// Esc-dismiss latch live here.
type cmdPopover struct {
	theme         *Theme
	width, height int
	highlight     int
	escDismissed  bool
	// compactEnabled gates the capability-tagged /compact command (QUM-865).
	// It is a plain bool (not a closure over the AppModel) so it survives the
	// value-copies bubbletea makes of the model; refreshed whenever the bridge
	// changes (see AppModel.syncPopoverCapabilities).
	compactEnabled bool
}

// capEnabled builds the registry capability predicate from the popover's stored
// capability flags (QUM-865).
func (p cmdPopover) capEnabled(c commands.Capability) bool {
	switch c {
	case commands.CapCompact:
		return p.compactEnabled
	default:
		return false
	}
}

// visible reports whether the popover should show for the given input text. It
// is the single source of truth for visibility: the text must start with '/',
// still be a single whitespace-free token (once a space is typed the user is
// entering args, so the popover hides), match ≥1 registered command available
// under the current backend capabilities, and not be Esc-dismissed for the
// current entry.
func (p cmdPopover) visible(text string) bool {
	if p.escDismissed {
		return false
	}
	if !strings.HasPrefix(text, "/") {
		return false
	}
	if strings.ContainsAny(text, " \t") {
		return false
	}
	return len(p.matches(text)) > 0
}

// matches returns the alphabetical command matches for the leading `/`-token of
// text, with capability-gated commands filtered out unless their capability is
// available (QUM-865). Returns nil when text is not a `/`-prefixed token.
func (p cmdPopover) matches(text string) []commands.Command {
	if !strings.HasPrefix(text, "/") {
		return nil
	}
	return commands.FilterSortedEnabled(strings.TrimPrefix(text, "/"), p.capEnabled)
}

// move adjusts the highlight by delta with wrap-around over n matches.
func (p *cmdPopover) move(delta, n int) {
	if n <= 0 {
		p.highlight = 0
		return
	}
	p.highlight = (p.highlight%n + delta + n) % n
}

// selected returns the currently-highlighted command for text. The highlight is
// defensively reset to the top if out of range. Returns ok=false when nothing
// matches.
func (p *cmdPopover) selected(text string) (commands.Command, bool) {
	matches := p.matches(text)
	if len(matches) == 0 {
		return commands.Command{}, false
	}
	idx := p.highlight
	if idx < 0 || idx >= len(matches) {
		idx = 0
	}
	return matches[idx], true
}

// View renders the popover box for the given text, or "" when not visible.
// maxRows caps the number of command rows shown (0 = unlimited) so the box
// never overpaints the input/status rows on a short terminal; when the match
// list exceeds maxRows a window is shown that keeps the highlighted row visible.
// The box lists each matching command (`name  description`) with the
// highlighted row marked by a `›` cursor. It is sized to its content, bounded by
// the terminal's composite budget and a readable maximum (see popoverWidthFor).
func (p cmdPopover) View(text string, maxRows int) string {
	if !p.visible(text) || p.theme == nil {
		return ""
	}
	if maxRows < 0 {
		return ""
	}
	matches := p.matches(text)

	// Sized from the FULL match list, before the maxRows windowing below, so the
	// box keeps a stable width while the user arrows through the window instead
	// of jittering as rows scroll in and out.
	boxWidth := popoverWidthFor(matches, p.width)
	contentW := boxWidth - popoverFrame
	maxNameLen, _ := popoverColumnWidths(matches)

	hi := p.highlight
	if hi < 0 || hi >= len(matches) {
		hi = 0
	}

	// Window the match list to maxRows, keeping the highlighted row visible.
	if maxRows > 0 && len(matches) > maxRows {
		start := 0
		if hi >= maxRows {
			start = hi - maxRows + 1
		}
		matches = matches[start : start+maxRows]
		hi -= start
	}

	var sb strings.Builder
	for i, c := range matches {
		prefix := popoverCursorBlank
		if i == hi {
			prefix = p.theme.AccentText.Render(popoverCursor)
		}
		// Pad by display width, not rune count: %-*s counts runes, which
		// diverges the moment a command name carries a wide rune.
		name := p.theme.AccentText.Render(c.Name + strings.Repeat(" ", maxNameLen-ansi.StringWidth(c.Name)))
		desc := p.theme.NormalText.Render(c.Description)
		sb.WriteString(truncateRow(prefix, name, desc, contentW))
		if i < len(matches)-1 {
			sb.WriteString("\n")
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(p.theme.Palette.Primary).
		Background(p.theme.Palette.BgBase).
		Padding(0, 1).
		// Width (not MaxWidth): MaxWidth clips the already-bordered output and
		// eats the closing border glyph (QUM-930). Rows are pre-fitted to
		// contentW by truncateRow, so nothing here can wrap or overflow.
		Width(boxWidth).
		Render(sb.String())
	return box
}
