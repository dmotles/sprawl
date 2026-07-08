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
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/tui/commands"
)

// popoverMaxWidth caps the popover box width so a long description never
// overflows a narrow terminal.
const popoverMaxWidth = 52

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
// highlighted row marked by a `›` cursor, bounded to popoverMaxWidth columns.
func (p cmdPopover) View(text string, maxRows int) string {
	if !p.visible(text) || p.theme == nil {
		return ""
	}
	if maxRows < 0 {
		return ""
	}
	matches := p.matches(text)

	boxWidth := popoverMaxWidth
	if p.width > 0 && p.width-4 < boxWidth {
		boxWidth = p.width - 4
	}
	if boxWidth < 20 {
		boxWidth = 20
	}

	maxNameLen := 0
	for _, c := range matches {
		if len(c.Name) > maxNameLen {
			maxNameLen = len(c.Name)
		}
	}

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
		prefix := "  "
		if i == hi {
			prefix = p.theme.AccentText.Render("› ")
		}
		name := p.theme.AccentText.Render(fmt.Sprintf("%-*s", maxNameLen, c.Name))
		desc := p.theme.NormalText.Render("  " + c.Description)
		sb.WriteString(prefix)
		sb.WriteString(name)
		sb.WriteString(desc)
		if i < len(matches)-1 {
			sb.WriteString("\n")
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(p.theme.Palette.Primary).
		Background(p.theme.Palette.BgBase).
		Padding(0, 1).
		MaxWidth(boxWidth).
		Render(sb.String())
	return box
}
