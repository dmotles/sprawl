// Package tui — transient corner-anchored agent-tree HUD (QUM-805).
//
// The HUD is a read-only, top-right overlay that appears momentarily when the
// user cycles the observed agent (Ctrl+N / Ctrl+P) or when the agent tree
// changes (an agent spawns or retires). It auto-hides 3 seconds after the last
// trigger via a generation-guarded tea.Tick (no global ticker — the fade tick
// runs only while the HUD is visible). It is NOT a modal: it is composited onto
// the final rendered string like a toast, so it never gates mouse/scroll/typing
// and never participates in the chat-panel render cache (QUM-769).
package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// retiredGhostGlyph prefixes a synthetic ghost row for a just-retired agent
// that is no longer in the tree node set.
const retiredGhostGlyph = "·"

// hudChangeKind classifies a spawn/retire flash highlight in the HUD.
type hudChangeKind int

const (
	// hudChangeNone is the absence of a flash highlight.
	hudChangeNone hudChangeKind = iota
	// hudChangeSpawned highlights a newly-spawned agent (green).
	hudChangeSpawned
	// hudChangeRetired highlights a just-retired agent (dim + strikethrough).
	hudChangeRetired
)

// treeHud holds the transient HUD's visibility + fade-generation state.
// gen is bumped on every trigger; a fade timer armed with generation G hides
// the HUD only if G still matches gen when it fires, so a newer trigger
// supersedes (resets) any in-flight timer.
type treeHud struct {
	visible bool
	gen     uint64
	changes map[string]hudChangeKind
}

// triggerNav marks the HUD visible for a navigation (Ctrl+N/P) event and bumps
// the fade generation, returning the new generation token for the timer. It
// deliberately does NOT clear pending spawn/retire flashes — a navigation
// immediately following a spawn must extend (not wipe) the flash.
func (h *treeHud) triggerNav() uint64 {
	h.visible = true
	h.gen++
	return h.gen
}

// triggerChange marks the HUD visible for a spawn/retire flash, records the
// change for the named agent, bumps the fade generation, and returns the new
// generation token.
func (h *treeHud) triggerChange(name string, kind hudChangeKind) uint64 {
	h.visible = true
	if h.changes == nil {
		h.changes = make(map[string]hudChangeKind)
	}
	h.changes[name] = kind
	h.gen++
	return h.gen
}

// expire hides the HUD iff gen matches the current generation (i.e. no newer
// trigger arrived since this timer was armed). On a hide it also clears the
// flash set. Returns true when it hid the HUD.
func (h *treeHud) expire(gen uint64) bool {
	if gen != h.gen {
		return false
	}
	h.visible = false
	h.changes = nil
	return true
}

// diffTreeNodes reports which agents were added (spawned) and removed (retired)
// between prev and next, compared by Name. Full TreeNode values are returned so
// callers can read Type for the spawn toast label.
func diffTreeNodes(prev, next []TreeNode) (added, removed []TreeNode) {
	prevSet := make(map[string]struct{}, len(prev))
	for _, n := range prev {
		prevSet[n.Name] = struct{}{}
	}
	nextSet := make(map[string]struct{}, len(next))
	for _, n := range next {
		nextSet[n.Name] = struct{}{}
	}
	for _, n := range next {
		if _, ok := prevSet[n.Name]; !ok {
			added = append(added, n)
		}
	}
	for _, n := range prev {
		if _, ok := nextSet[n.Name]; !ok {
			removed = append(removed, n)
		}
	}
	return added, removed
}

var (
	// hudSpawnedStyle flashes a newly-spawned agent green.
	hudSpawnedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399")).Bold(true)
	// hudRetiredStyle flashes a just-retired agent dim + struck through.
	hudRetiredStyle = lipgloss.NewStyle().Faint(true).Strikethrough(true)
	// hudObservedStyle marks the currently-observed agent. Reverse video (no
	// padding, so it never widens the row past the panel's inner width).
	hudObservedStyle = lipgloss.NewStyle().Reverse(true).Bold(true)
)

// hudRowStyle selects the lipgloss style for a HUD tree row. Spawn/retire
// flashes take precedence over the observed highlight so a freshly-changed
// node is unmistakable.
func hudRowStyle(theme *Theme, kind hudChangeKind, observed bool) lipgloss.Style {
	switch kind {
	case hudChangeSpawned:
		return hudSpawnedStyle
	case hudChangeRetired:
		return hudRetiredStyle
	}
	if observed {
		return hudObservedStyle
	}
	if theme != nil {
		return theme.NormalText
	}
	return lipgloss.NewStyle()
}

// renderTreeHud renders the compact mini-tree panel: one row per node
// (`<indent><glyph> <name>`), the observed agent highlighted and any
// spawn/retire flash colorized. A just-retired agent is already gone from
// nodes by the time the HUD renders (rebuildTree runs first), so it is drawn
// as a synthetic dim+strikethrough "ghost" row for the flash window. Returns
// "" when there is nothing to render. The box is bounded to maxW columns and
// maxH rows (border included).
func renderTreeHud(h treeHud, nodes []TreeNode, observed string, theme *Theme, maxW, maxH int) string {
	if maxW <= 0 || maxH <= 0 {
		return ""
	}
	now := time.Now()
	innerW := maxW - 4 // 2 border cells + 2 padding cells
	if innerW < 1 {
		innerW = 1
	}
	maxRows := maxH - 2 // top + bottom border
	if maxRows < 1 {
		maxRows = 1
	}

	rendered := make(map[string]bool, len(nodes))
	rows := make([]string, 0, maxRows)
	// Note: when len(nodes) > maxRows the tail is silently dropped (no "+N
	// more" indicator) — acceptable for a transient v1 HUD; the /tree modal is
	// the deep-dive surface.
	for _, n := range nodes {
		if len(rows) >= maxRows {
			break
		}
		indent := strings.Repeat("  ", n.Depth)
		glyph := stateGlyph(TreeNodeAgentState(n, now))
		label := clipTreeRow(fmt.Sprintf("%s%s %s", indent, glyph, n.Name), innerW)
		kind := hudChangeNone
		if h.changes != nil {
			kind = h.changes[n.Name]
		}
		rows = append(rows, hudRowStyle(theme, kind, n.Name == observed).Render(label))
		rendered[n.Name] = true
	}

	// QUM-805: synthetic ghost rows for just-retired agents no longer present
	// in nodes, so the dim+strikethrough flash is actually visible. Sorted for
	// deterministic ordering across the (unordered) change map.
	if len(h.changes) > 0 {
		ghosts := make([]string, 0, len(h.changes))
		for name, kind := range h.changes {
			if kind == hudChangeRetired && !rendered[name] {
				ghosts = append(ghosts, name)
			}
		}
		sort.Strings(ghosts)
		for _, name := range ghosts {
			if len(rows) >= maxRows {
				break
			}
			label := clipTreeRow(fmt.Sprintf("%s %s", retiredGhostGlyph, name), innerW)
			rows = append(rows, hudRowStyle(theme, hudChangeRetired, false).Render(label))
		}
	}

	if len(rows) == 0 {
		return ""
	}

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if theme != nil && theme.Palette.Primary != nil {
		box = box.BorderForeground(theme.Palette.Primary)
	}
	return box.Render(strings.Join(rows, "\n"))
}

// overlayTopRight composites panel onto base, right-anchored, starting at
// anchorRow. It preserves every base line's visible width and never writes into
// the bottom two rows (reserved for the input/status bar, mirroring the toast
// Overlay contract). Panel rows that would fall outside the writable region are
// clipped rather than overflowing.
func overlayTopRight(base, panel string, anchorRow int) string {
	if panel == "" {
		return base
	}
	lines := strings.Split(base, "\n")
	const bottomReserve = 2
	maxRow := len(lines) - bottomReserve
	for i, pl := range strings.Split(panel, "\n") {
		row := anchorRow + i
		if row < 0 || row >= len(lines) || row >= maxRow {
			continue
		}
		lines[row] = compositeRight(lines[row], pl)
	}
	return strings.Join(lines, "\n")
}

// compositeRight overlays box flush against the right edge of line, preserving
// line's visible width and the left base segment outside the box footprint.
func compositeRight(line, box string) string {
	lineW := ansi.StringWidth(line)
	boxW := ansi.StringWidth(box)
	if boxW >= lineW {
		return ansi.Truncate(box, lineW, "")
	}
	leftW := lineW - boxW
	leftPart := ansi.Truncate(line, leftW, "")
	if lw := ansi.StringWidth(leftPart); lw < leftW {
		leftPart += strings.Repeat(" ", leftW-lw)
	}
	return leftPart + box
}
