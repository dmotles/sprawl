package tui

// QUM-733 5b/5c: TreeModalModel renders the full agent tree as a centered
// modal overlay. Opened via the `/tree` palette command (ToggleTreeMsg).
//
// Chrome mirrors HelpModel / ConfirmModel: RoundedBorder,
// BorderForeground = theme.Palette.Primary, Background = theme.Palette.BgBase,
// Padding(1,2), centered via lipgloss.Place.
//
// Content: vertical agent tree with 2-space-per-depth indentation. Each row
// shows:
//   - `>` left marker when the row matches the observed agent (selected ≠ cursor).
//   - `›` accent marker when the row matches the cursor (Up/Down navigation).
//   - Status dot (theme.ReportDot via DeriveIconState).
//   - Type icon (typeIcon).
//   - Name (rendered with selReverseStyle pill when observed).
//   - Family/type chip [eng]/[mgr]/[res] dim-italic; weave omitted.
//   - Cost tag `[$0.0000]` when TotalCostUsd > 0.
//   - `(status)` or `— <last_report_message>`.
//
// Keys: ↑/↓ move cursor; Enter emits AgentSelectedMsg and hides the modal
// (visibility sync at the AppModel layer clears showTree — QUM-733 hotfix:
// do NOT batch a ToggleTreeMsg here, that re-toggles showTree back to true);
// Esc dismisses; all others swallowed.

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TreeModalModel is the agent-tree modal overlay (QUM-733 5b).
type TreeModalModel struct {
	theme    *Theme
	width    int
	height   int
	visible  bool
	nodes    []TreeNode
	observed string
	cursor   int
}

// NewTreeModalModel constructs a hidden tree-modal model.
func NewTreeModalModel(theme *Theme) TreeModalModel {
	return TreeModalModel{theme: theme}
}

// SetSize updates the centering area.
func (m *TreeModalModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// SetNodes refreshes the tree contents and observed-agent marker. Cursor is
// clamped to the new node count and reset to the observed agent's row when
// possible (so opening the modal puts the cursor where the user is looking).
func (m *TreeModalModel) SetNodes(nodes []TreeNode, observed string) {
	m.nodes = nodes
	m.observed = observed
	// Position cursor on the observed agent's row when it exists.
	m.cursor = 0
	for i, n := range nodes {
		if n.Name == observed {
			m.cursor = i
			break
		}
	}
}

// Show makes the modal visible.
func (m *TreeModalModel) Show() { m.visible = true }

// Hide hides the modal.
func (m *TreeModalModel) Hide() { m.visible = false }

// Visible reports whether the modal is currently showing.
func (m TreeModalModel) Visible() bool { return m.visible }

// Cursor returns the current cursor index (exposed for tests).
func (m TreeModalModel) Cursor() int { return m.cursor }

// Update handles key events while the modal is visible. Emits:
//   - AgentSelectedMsg on Enter (visibility cleared locally; AppModel syncs
//     showTree from Visible()). Do NOT batch ToggleTreeMsg here — that
//     re-flips showTree back to true (QUM-733 hotfix).
//   - No cmd on Esc (dismiss; AppModel syncs visibility).
//   - No cmd on ↑ / ↓ (cursor mutation).
//   - All other keys swallowed.
func (m TreeModalModel) Update(msg tea.KeyPressMsg) (TreeModalModel, tea.Cmd) {
	if !m.visible {
		return m, nil
	}
	switch msg.Code {
	case tea.KeyEscape:
		m.visible = false
		return m, nil
	case tea.KeyEnter:
		if m.cursor < 0 || m.cursor >= len(m.nodes) {
			m.visible = false
			return m, nil
		}
		name := m.nodes[m.cursor].Name
		m.visible = false
		return m, sendMsgCmd(AgentSelectedMsg{Name: name})
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case tea.KeyDown:
		if m.cursor < len(m.nodes)-1 {
			m.cursor++
		}
		return m, nil
	}
	return m, nil
}

// typeChip returns the dim-italic family chip rendered after an agent name.
// Mapping: manager→mgr, engineer→eng, researcher→res, weave→omitted (root
// is already obvious).
func (m TreeModalModel) typeChip(t string) string {
	var label string
	switch t {
	case "manager":
		label = "mgr"
	case "engineer":
		label = "eng"
	case "researcher":
		label = "res"
	default:
		return ""
	}
	style := lipgloss.NewStyle().Foreground(m.theme.Palette.FgMostSubtle).Italic(true)
	return style.Render("[" + label + "]")
}

// View renders the modal as a centered box. Returns empty when hidden.
func (m TreeModalModel) View() string {
	if !m.visible {
		return ""
	}

	now := time.Now()
	var lines []string
	title := m.theme.AccentText.Bold(true).Render("Agent Tree")
	lines = append(lines, title)
	lines = append(lines, "")

	if len(m.nodes) == 0 {
		lines = append(lines, m.theme.PlaceholderStyle.Render("No agents running."))
	} else {
		for i, n := range m.nodes {
			lines = append(lines, m.renderRow(n, i, now))
		}
	}
	lines = append(lines,
		"",
		m.theme.NormalText.Render("↑↓ navigate • Enter select • Esc close"),
	)

	content := strings.Join(lines, "\n")

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Palette.Primary).
		Background(m.theme.Palette.BgBase).
		Padding(1, 2)

	box := boxStyle.Render(content)
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return box
}

func (m TreeModalModel) renderRow(n TreeNode, idx int, now time.Time) string {
	indent := strings.Repeat("  ", n.Depth)

	// Left marker: `>` for observed, `›` for cursor, two spaces otherwise.
	// When observed == cursor, the cursor marker wins (so the user knows
	// they're looking at the observed row).
	var marker string
	switch {
	case idx == m.cursor:
		marker = m.theme.AccentText.Render("› ")
	case n.Name == m.observed:
		marker = m.theme.AccentText.Render("> ")
	default:
		marker = "  "
	}

	dot := m.theme.ReportDot(DeriveIconState(n, now))
	icon := typeIcon(n.Type)

	var name string
	if n.Name == m.observed {
		name = selReverseStyle.Render(n.Name)
	} else {
		name = m.theme.NormalText.Render(n.Name)
	}

	chip := m.typeChip(n.Type)

	var cost string
	if n.TotalCostUsd > 0 {
		cost = m.theme.NormalText.Render(fmt.Sprintf(" [$%.4f]", n.TotalCostUsd))
	}

	var tail string
	if n.LastReportMessage != "" {
		tail = m.theme.NormalText.Render(" — " + n.LastReportMessage)
	} else {
		status := n.Status
		if status == "" {
			status = "unknown"
		}
		tail = m.theme.PlaceholderStyle.Render(" (" + status + ")")
	}

	parts := []string{marker, indent, dot, " ", icon, " ", name}
	if chip != "" {
		parts = append(parts, " ", chip)
	}
	if cost != "" {
		parts = append(parts, cost)
	}
	parts = append(parts, tail)
	return strings.Join(parts, "")
}
