package tui

// QUM-656: orbital-pill agent tree, ported from the dmotles/tui-chat-spike
// branch (internal/tuichat/tree.go treeOrbitalCore). Roots render as a left
// anchor (`<root> ──●`) and children orbit out to the right as inline
// `name glyph` tokens, separated by dim two-space gaps. Grandchildren
// hang from their parent with a leading `↳ `. The currently selected agent
// renders as a reverse-video cyan pill.

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// AgentState drives glyph + color selection for a tree node.
type AgentState int

const (
	StateRoot AgentState = iota
	StateWorking
	StateIdle
	StateDone
	StateBlocked
	StateFailure
)

// TreeNodeAgentState classifies a TreeNode into an AgentState for rendering.
// Rules (highest priority first):
//   - FaultClass != "" → StateFailure (overrides everything)
//   - Type == "weave"  → StateRoot
//   - Otherwise delegate to DeriveIconState (liveness-first per QUM-665).
//
// `now` is passed in so the helper remains pure/testable.
func TreeNodeAgentState(n TreeNode, now time.Time) AgentState {
	if n.FaultClass != "" {
		return StateFailure
	}
	if n.Type == "weave" {
		return StateRoot
	}
	switch DeriveIconState(n, now) {
	case "working":
		return StateWorking
	case "complete":
		return StateDone
	case "blocked":
		return StateBlocked
	case "failure":
		return StateFailure
	}
	return StateIdle
}

// stateGlyph returns the short status glyph for an AgentState.
func stateGlyph(s AgentState) string {
	switch s {
	case StateRoot:
		return "●"
	case StateWorking:
		return "⚙"
	case StateIdle:
		return "⏳"
	case StateDone:
		return "✓"
	case StateBlocked:
		return "⏸"
	case StateFailure:
		return "✗"
	}
	return "·"
}

// stateStyle returns the lipgloss style for an AgentState. Colors mirror the
// spike's palette (cool purple root, amber working, neutral idle, green done,
// orange blocked, red failure).
func stateStyle(s AgentState) lipgloss.Style {
	switch s {
	case StateRoot:
		return treeRootStyle
	case StateWorking:
		return treeWorkStyle
	case StateIdle:
		return treeIdleStyle
	case StateDone:
		return treeDoneStyle
	case StateBlocked:
		return treeBlockedStyle
	case StateFailure:
		return treeFailureStyle
	}
	return treeIdleStyle
}

var (
	treeRootStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	treeWorkStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24"))
	treeIdleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#71717A"))
	treeDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
	treeBlockedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	treeFailureStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))

	// selReverseStyle styles the selected agent as a reverse-video cyan pill.
	// Exact SGR shape matters — tree_orbital_test.go matches against a lipgloss
	// style with identical attributes.
	selReverseStyle = lipgloss.NewStyle().
			Reverse(true).
			Foreground(lipgloss.Color("#0B0B12")).
			Background(lipgloss.Color("#22D3EE")).
			Bold(true).
			Padding(0, 1)

	// headerSep is the dim foreground style used for orbital scaffolding
	// (the ` ──● ` anchor, two-space token separator, `↳ ` grandchild prefix,
	// and the narrow-mode `·` / `→` breadcrumb glyphs).
	headerSep = lipgloss.NewStyle().Foreground(lipgloss.Color("#3F3F46"))
)

// OrbitalHeight reports how many rows RenderTreeOrbital will produce at the
// given terminal width. Zero width yields zero rows so callers can subtract
// safely on degenerate sizes; widths below the narrow threshold collapse to a
// single breadcrumb line; wide widths get three orbital rows (one per root).
func OrbitalHeight(width int) int {
	if width <= 0 {
		return 0
	}
	if width < wordmarkNarrowThreshold {
		return 1
	}
	return 3
}

// RenderTreeOrbital returns the orbital-pill rendering of the tree. width is
// the cell budget the tree must fit within (NOT the full terminal width).
// Lines are right-padded to that width via padVisible and truncated via
// ansi.Truncate so total visible width matches exactly.
//
// QUM-679 / QUM-686: when the sole depth-0 root is weave and any of its
// depth-1 children is a manager, the renderer pivots ONLY the managers into
// their own row anchors below weave. Weave keeps row 0 with any non-manager
// depth-1 children (engineers, researchers) rendered inline as orbits; each
// manager gets its own row with that manager's depth-2 descendants rebased to
// inline orbits. Descendants of a non-manager weave child stay attached to
// weave's row as grandchildren (↳ prefix).
//
// QUM-686 fix: the original QUM-679 implementation discarded weave entirely
// and promoted every depth-1 child (including researchers/engineers) into a
// separate top-level row, which produced `ghost · tower` instead of the spike
// layout `weave ──◉ ghost ✓ / tower ──◉` for the weave+researcher+manager
// session shape.
func RenderTreeOrbital(nodes []TreeNode, selected string, width int) []string {
	if width <= 0 {
		return nil
	}
	narrow := width < wordmarkNarrowThreshold
	now := time.Now()

	// Group nodes by root. A root is any node at depth 0; subsequent nodes at
	// deeper depths attach to the most-recent root with monotonically-deeper
	// indices walked depth-first.
	type rootGroup struct {
		root     TreeNode
		children []TreeNode // depth >= 1, in original DFS order
	}
	var roots []rootGroup
	var cur *rootGroup
	for _, n := range nodes {
		if n.Depth == 0 {
			roots = append(roots, rootGroup{root: n})
			cur = &roots[len(roots)-1]
			continue
		}
		if cur == nil {
			// Orphaned non-root before any root — synthesize a pseudo-root so
			// it still renders somewhere.
			roots = append(roots, rootGroup{root: n})
			cur = &roots[len(roots)-1]
			continue
		}
		cur.children = append(cur.children, n)
	}

	// QUM-686: pivot ONLY the manager children of weave into their own row
	// anchors below weave. Weave keeps row 0; non-manager depth-1 children
	// (researchers, engineers) stay inline on weave's row. Depth-2 descendants
	// of a pivoted manager are rebased to depth 1 so they render as inline
	// orbits of the manager. Depth-2 descendants of a non-manager direct
	// weave child stay attached to weave as grandchildren (↳ glyph).
	if len(roots) == 1 && roots[0].root.Type == "weave" && hasManagerChild(roots[0].children) {
		w := roots[0]
		newWeave := rootGroup{root: w.root}
		var managerRows []rootGroup
		// pcurManager tracks the most-recent depth-1 manager so its depth-2
		// descendants land on that manager's row. Nil when the most-recent
		// depth-1 child was a non-manager (engineers/researchers): their
		// depth-2 descendants stay on weave's row as grandchildren.
		var pcurManager *rootGroup
		for _, c := range w.children {
			if c.Depth == 1 {
				if c.Type == "manager" {
					managerRows = append(managerRows, rootGroup{root: c})
					pcurManager = &managerRows[len(managerRows)-1]
				} else {
					// Non-manager direct child of weave: render inline on
					// weave's row.
					newWeave.children = append(newWeave.children, c)
					pcurManager = nil
				}
				continue
			}
			// Depth >= 2: attach to the most-recent depth-1 ancestor.
			if pcurManager != nil {
				// Rebase depth so renderRootLine's token-vs-↳ classifier
				// (Depth == 1 → inline token) treats the manager's direct
				// engineers as orbits, not grandchildren.
				cc := c
				cc.Depth = c.Depth - 1
				pcurManager.children = append(pcurManager.children, cc)
			} else {
				// Grandchild of weave via a non-manager intermediary —
				// keep depth as-is so it renders with the ↳ glyph.
				newWeave.children = append(newWeave.children, c)
			}
		}
		roots = append([]rootGroup{newWeave}, managerRows...)
	}

	renderToken := func(n TreeNode) string {
		st := TreeNodeAgentState(n, now)
		label := n.Name + " " + stateGlyph(st)
		if selected != "" && n.Name == selected {
			return selReverseStyle.Render(label)
		}
		return stateStyle(st).Render(label)
	}

	renderRootLine := func(g rootGroup) string {
		rootState := TreeNodeAgentState(g.root, now)
		var b strings.Builder
		// QUM-657: with children, the trailing ──● anchor stands in for the
		// root's status glyph; with no children the anchor would dangle, so
		// we append the glyph directly to the root name and skip the anchor.
		hasChildren := len(g.children) > 0
		rootLabel := g.root.Name
		if !hasChildren {
			rootLabel = g.root.Name + " " + stateGlyph(rootState)
		}
		if selected != "" && g.root.Name == selected {
			b.WriteString(selReverseStyle.Render(g.root.Name + " " + stateGlyph(rootState)))
		} else {
			b.WriteString(stateStyle(rootState).Render(rootLabel))
		}
		if !hasChildren {
			// Surface unread badge even without children, then stop.
			if g.root.Unread > 0 {
				b.WriteString(" " + headerSep.Render(fmt.Sprintf("(%d)", g.root.Unread)))
			}
			return b.String()
		}
		b.WriteString(headerSep.Render(" ──● "))
		// QUM-559: surface a root's unread maildir count as a dim `(N) ` badge
		// right after the anchor. e2e-tests/notify-tui.sh asserts this badge
		// appears on the weave row after a maildir delivery.
		if g.root.Unread > 0 {
			b.WriteString(headerSep.Render(fmt.Sprintf("(%d) ", g.root.Unread)))
		}

		var tokens []string
		for _, c := range g.children {
			if c.Depth == 1 {
				tokens = append(tokens, renderToken(c))
			} else {
				// Grandchild (depth >= 2): prefix with ↳ and render as a
				// dim-prefixed token after its parent.
				gcLabel := c.Name + " " + stateGlyph(TreeNodeAgentState(c, now))
				var gcStyled string
				if selected != "" && c.Name == selected {
					gcStyled = selReverseStyle.Render(gcLabel)
				} else {
					gcStyled = stateStyle(TreeNodeAgentState(c, now)).Render(gcLabel)
				}
				tokens = append(tokens, headerSep.Render("↳ ")+gcStyled)
			}
		}
		b.WriteString(strings.Join(tokens, headerSep.Render("  ")))
		return b.String()
	}

	if narrow {
		// Breadcrumb: per-root chains joined with " · ", root→child joined with
		// " → ". Only render the first child of each root (single-line budget).
		var chunks []string
		for _, g := range roots {
			rootState := TreeNodeAgentState(g.root, now)
			var rs string
			if selected != "" && g.root.Name == selected {
				rs = selReverseStyle.Render(g.root.Name + " " + stateGlyph(rootState))
			} else {
				rs = stateStyle(rootState).Render(g.root.Name)
			}
			chunk := rs
			if g.root.Unread > 0 {
				chunk += " " + headerSep.Render(fmt.Sprintf("(%d)", g.root.Unread))
			}
			if len(g.children) > 0 {
				chunk += headerSep.Render(" → ") + renderToken(g.children[0])
			}
			chunks = append(chunks, chunk)
		}
		line := strings.Join(chunks, headerSep.Render(" · "))
		if line == "" {
			return []string{strings.Repeat(" ", width)}
		}
		line = ansi.Truncate(line, width, "…")
		return []string{padVisible(line, width)}
	}

	// Wide: up to 3 lines, one per root. QUM-686: weave's Unread badge is
	// already surfaced by renderRootLine directly (weave is now on row 0
	// instead of being replaced by manager rows). If post-pivot root count
	// exceeds 3, the 3rd row is augmented with a `+K more` indicator that
	// names the overflowed managers.
	out := make([]string, 3)
	for i := 0; i < 3; i++ {
		if i < len(roots) {
			line := renderRootLine(roots[i])
			// QUM-679: overflow indicator on the last visible row.
			if i == 2 && len(roots) > 3 {
				extras := make([]TreeNode, 0, len(roots)-3)
				for _, r := range roots[3:] {
					extras = append(extras, r.root)
				}
				line += headerSep.Render(overflowSuffix(extras))
			}
			line = ansi.Truncate(line, width, "…")
			out[i] = padVisible(line, width)
		} else {
			out[i] = strings.Repeat(" ", width)
		}
	}
	return out
}

// hasManagerChild reports whether any child has Type == "manager". Used by
// RenderTreeOrbital to decide whether to pivot weave's children into separate
// rows (QUM-679).
func hasManagerChild(children []TreeNode) bool {
	for _, c := range children {
		if c.Type == "manager" {
			return true
		}
	}
	return false
}

// overflowSuffix builds the trailing `  +K more (name, name, …)` annotation
// used when more pivoted manager rows exist than the 3-row header budget.
func overflowSuffix(extras []TreeNode) string {
	if len(extras) == 0 {
		return ""
	}
	// Collect depth-1 (manager / direct-child) names only — descendants don't
	// count as additional rows.
	var names []string
	for _, n := range extras {
		if n.Depth <= 1 {
			names = append(names, n.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	const nameCap = 3
	shown := names
	more := 0
	if len(shown) > nameCap {
		more = len(shown) - nameCap
		shown = shown[:nameCap]
	}
	joined := strings.Join(shown, ", ")
	if more > 0 {
		return fmt.Sprintf("  +%d more (%s, …)", len(names), joined)
	}
	return fmt.Sprintf("  +%d more (%s)", len(names), joined)
}
