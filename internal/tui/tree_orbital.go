package tui

// QUM-733 5a: horizontal-wrapped pill list, replacing the orbital-pill
// scaffolding (QUM-656). Each agent renders as a `<name> <glyph>` pill in
// agentNames() order — weave first, then DFS children. Pills wrap to
// additional rows when the joined width exceeds HeaderTreeWidth.
//
// Open-Q decision (header row budget on 8+ agents): cap at WordmarkHeight
// (3 rows wide / 1 row narrow). Excess pills are truncated with an
// ellipsis on the trailing row. Rationale: keeps HeaderHeight a function
// of width alone, preserves ComputeLayout's signature, and the /tree
// modal (5b) is the deep-dive surface for many-agent scenarios. The spec
// allowed an implementer-time cap if documented (per QUM-733 issue body).

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
	// StateDormant is the QUM-788 projection for agents whose disk Status is
	// "complete" — terminal-but-revivable. Distinct dim style so it does not
	// collide with StateIdle (gray) or StateFailure (red).
	StateDormant
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
	case "dormant":
		return StateDormant
	}
	return StateIdle
}

// treeWorkPulseStyle returns the foreground style for a working pill at the
// given pulse phase, cycling treeWorkPulseFrames (modulo, guarding negatives).
// QUM-806.
func treeWorkPulseStyle(phase int) lipgloss.Style {
	n := len(treeWorkPulseFrames)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(treeWorkPulseFrames[((phase%n)+n)%n]))
}

// anyWorkingPill reports whether any node renders as a working pill (derived
// state StateWorking). Used to gate the QUM-806 pulse tick: it is armed only
// while ≥1 working pill exists and goes silent otherwise. The weave root is
// StateRoot (never working), so the root's own turn never arms the pulse.
func anyWorkingPill(nodes []TreeNode, now time.Time) bool {
	for _, n := range nodes {
		if TreeNodeAgentState(n, now) == StateWorking {
			return true
		}
	}
	return false
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
	case StateDormant:
		return "◌"
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
	case StateDormant:
		return treeDormantStyle
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
	// QUM-788: dormant-revivable agents (Status="complete"). Desaturated
	// info-blue with Faint(true) so the pill reads as ambient/at-rest but
	// distinct from StateIdle gray (#71717A) and StateFailure red.
	treeDormantStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#60A5FA")).Faint(true)

	// QUM-806: the "breathing" brightness ramp for working pills. A working
	// pill cycles through these amber shades (dim → normal → bright → normal)
	// driven by AppModel.treePulseFrame, approximating a gentle fade-in/out
	// (terminals can't true-alpha-fade). Frame 1 and 3 are the same "normal"
	// anchor (== treeWorkStyle's #FBBF24) so the static and animated baselines
	// match. Cadence/vocabulary aligns with the QUM-796 sparkle (amber accent,
	// ~700ms cycle at 175ms/step) rather than inventing a third animation
	// language.
	treeWorkPulseFrames = []string{
		"#B45309", // dim
		"#FBBF24", // normal (matches treeWorkStyle)
		"#FDE68A", // bright
		"#FBBF24", // normal
	}

	// selReverseStyle styles the selected agent as a reverse-video cyan pill.
	selReverseStyle = lipgloss.NewStyle().
			Reverse(true).
			Foreground(lipgloss.Color("#0B0B12")).
			Background(lipgloss.Color("#22D3EE")).
			Bold(true).
			Padding(0, 1)

	// pillSep is the dim foreground style used for the two-space separator
	// between adjacent pills in the horizontal list (QUM-733 5a).
	pillSep = lipgloss.NewStyle().Foreground(lipgloss.Color("#3F3F46"))
)

// pillSeparator is the dim two-space rendered between adjacent pills.
const pillSeparatorWidth = 2

// OrbitalHeight reports how many rows RenderTreeOrbital will produce at the
// given width. The renderer pads to WordmarkHeight rows at the width so the
// invariant `OrbitalHeight(W, nodes) == len(RenderTreeOrbital(nodes, "", W, 0))`
// holds regardless of topology. The `nodes` parameter is retained for
// signature stability (callers still pass it through).
func OrbitalHeight(width int, nodes []TreeNode) int {
	_ = nodes
	if width <= 0 {
		return 0
	}
	return WordmarkHeight(width)
}

// RenderTreeOrbital returns the horizontal-wrapped pill list. width is the
// cell budget within the header (NOT the full terminal width). Each line is
// right-padded to that width via padVisible; truncation uses ansi.Truncate
// when a pill would exceed the row budget.
//
// QUM-733 5a: pills follow agentNames() order — the caller (AppModel) passes
// in nodes already in this order (PrependWeaveRoot + DFS).
//
// QUM-806: pulsePhase drives the working-pill brightness "breathe". Non-working
// and selected pills ignore it (selected pills keep their reverse-video style).
func RenderTreeOrbital(nodes []TreeNode, selected string, width, pulsePhase int) []string {
	if width <= 0 {
		return nil
	}
	rowCap := WordmarkHeight(width)
	if rowCap < 1 {
		rowCap = 1
	}
	now := time.Now()

	type pill struct {
		styled string
		plainW int // visible cell width of the unstyled label (incl. selReverseStyle padding)
	}
	pills := make([]pill, 0, len(nodes))
	for _, n := range nodes {
		st := TreeNodeAgentState(n, now)
		label := n.Name + " " + stateGlyph(st)
		var styled string
		plainW := lipgloss.Width(label)
		switch {
		case selected != "" && n.Name == selected:
			styled = selReverseStyle.Render(label)
			// Padding(0,1) on either side → +2 visible cells.
			plainW += 2
		case st == StateWorking:
			// QUM-806: working pills breathe via the shared pulse phase.
			styled = treeWorkPulseStyle(pulsePhase).Render(label)
		default:
			styled = stateStyle(st).Render(label)
		}
		// QUM-559: surface the per-agent unread badge as a dim `(N)` suffix
		// outside the pill so the e2e regex `weave[^│]*\([1-9]` matches.
		if n.Unread > 0 {
			badge := fmt.Sprintf(" (%d)", n.Unread)
			styled += pillSep.Render(badge)
			plainW += lipgloss.Width(badge)
		}
		pills = append(pills, pill{styled: styled, plainW: plainW})
	}

	sepStyled := pillSep.Render("  ")

	// Greedy wrap into rows up to (rowCap-1) rows. The final row absorbs all
	// remaining pills and lets ansi.Truncate clip with `…`.
	rendered := make([]string, 0, rowCap)
	flush := func(items []string) {
		if len(items) == 0 {
			rendered = append(rendered, strings.Repeat(" ", width))
			return
		}
		line := strings.Join(items, sepStyled)
		line = ansi.Truncate(line, width, "…")
		rendered = append(rendered, padVisible(line, width))
	}

	var (
		rowItems []string
		rowWidth int
	)
	for i, p := range pills {
		// On the final allowed row, keep packing without wrapping so
		// ansi.Truncate can clip with `…` for overflow.
		if len(rendered) == rowCap-1 {
			rowItems = append(rowItems, p.styled)
			continue
		}
		needed := p.plainW
		if len(rowItems) > 0 {
			needed += pillSeparatorWidth
		}
		if len(rowItems) > 0 && rowWidth+needed > width {
			flush(rowItems)
			rowItems = nil
			rowWidth = 0
			if len(rendered) == rowCap-1 {
				rowItems = append(rowItems, p.styled)
				continue
			}
			needed = p.plainW
		}
		rowItems = append(rowItems, p.styled)
		rowWidth += needed
		_ = i
	}
	if len(rowItems) > 0 {
		flush(rowItems)
	}

	// Pad to rowCap with blank rows so callers can rely on a stable height
	// at this width.
	for len(rendered) < rowCap {
		rendered = append(rendered, strings.Repeat(" ", width))
	}
	return rendered
}
