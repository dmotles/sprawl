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
//   - LastReportState working/complete/blocked/failure → matching state
//   - default → StateIdle
func TreeNodeAgentState(n TreeNode) AgentState {
	if n.FaultClass != "" {
		return StateFailure
	}
	if n.Type == "weave" {
		return StateRoot
	}
	switch n.LastReportState {
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
func RenderTreeOrbital(nodes []TreeNode, selected string, width int) []string {
	if width <= 0 {
		return nil
	}
	narrow := width < wordmarkNarrowThreshold

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

	renderToken := func(n TreeNode) string {
		st := TreeNodeAgentState(n)
		label := n.Name + " " + stateGlyph(st)
		if selected != "" && n.Name == selected {
			return selReverseStyle.Render(label)
		}
		return stateStyle(st).Render(label)
	}

	renderRootLine := func(g rootGroup) string {
		rootState := TreeNodeAgentState(g.root)
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
				gcLabel := c.Name + " " + stateGlyph(TreeNodeAgentState(c))
				var gcStyled string
				if selected != "" && c.Name == selected {
					gcStyled = selReverseStyle.Render(gcLabel)
				} else {
					gcStyled = stateStyle(TreeNodeAgentState(c)).Render(gcLabel)
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
			rootState := TreeNodeAgentState(g.root)
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

	// Wide: up to 3 lines, one per root.
	out := make([]string, 3)
	for i := 0; i < 3; i++ {
		if i < len(roots) {
			line := renderRootLine(roots[i])
			line = ansi.Truncate(line, width, "…")
			out[i] = padVisible(line, width)
		} else {
			out[i] = strings.Repeat(" ", width)
		}
	}
	return out
}
