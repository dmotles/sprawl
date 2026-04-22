package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// TreeNode represents a single node in the agent tree.
type TreeNode struct {
	Name              string
	Type              string
	Family            string
	Status            string
	Depth             int
	Unread            int
	LastReportState   string // working, blocked, complete, failure, ""
	LastReportSummary string
}

// TreeModel is the agent tree panel displaying live agent hierarchy.
type TreeModel struct {
	nodes    []TreeNode
	selected int
	width    int
	height   int
	theme    *Theme
}

// NewTreeModel creates an empty tree model.
func NewTreeModel(theme *Theme) TreeModel {
	return TreeModel{
		theme: theme,
	}
}

// SetNodes sets the tree nodes, preserving selection by agent name.
func (m *TreeModel) SetNodes(nodes []TreeNode) {
	// Remember the currently selected agent name.
	var selectedName string
	if m.selected >= 0 && m.selected < len(m.nodes) {
		selectedName = m.nodes[m.selected].Name
	}

	m.nodes = nodes

	// Try to preserve selection by name.
	if selectedName != "" {
		for i, n := range m.nodes {
			if n.Name == selectedName {
				m.selected = i
				return
			}
		}
	}

	// If the selected name was not found, clamp selection.
	if m.selected >= len(m.nodes) {
		if len(m.nodes) > 0 {
			m.selected = len(m.nodes) - 1
		} else {
			m.selected = 0
		}
	}
}

// SelectedAgent returns the name of the currently selected agent.
func (m TreeModel) SelectedAgent() string {
	if m.selected >= 0 && m.selected < len(m.nodes) {
		return m.nodes[m.selected].Name
	}
	return ""
}

// typeIcon returns a bracket icon for the agent type.
func typeIcon(t string) string {
	switch t {
	case "weave":
		return "[W]"
	case "manager":
		return "[M]"
	case "engineer":
		return "[E]"
	case "researcher":
		return "[R]"
	default:
		return "[?]"
	}
}

// Update handles key messages for tree navigation.
func (m TreeModel) Update(msg tea.Msg) (TreeModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.Code {
		case tea.KeyUp, 'k':
			if m.selected > 0 {
				m.selected--
			}
		case tea.KeyDown, 'j':
			if m.selected < len(m.nodes)-1 {
				m.selected++
			}
		case tea.KeyEnter:
			if m.selected >= 0 && m.selected < len(m.nodes) {
				name := m.nodes[m.selected].Name
				return m, func() tea.Msg {
					return AgentSelectedMsg{Name: name}
				}
			}
		}
	}
	return m, nil
}

// View renders the tree panel.
func (m TreeModel) View() string {
	if len(m.nodes) == 0 {
		return m.theme.PlaceholderStyle.Render("No agents running.")
	}

	var b strings.Builder
	for i, node := range m.nodes {
		if i >= m.height && m.height > 0 {
			break
		}

		indent := strings.Repeat("  ", node.Depth)
		icon := typeIcon(node.Type)
		dot := m.theme.ReportDot(node.LastReportState)
		var line string
		if node.LastReportSummary != "" {
			line = fmt.Sprintf("%s%s %s %s — %s", indent, dot, icon, node.Name, node.LastReportSummary)
		} else {
			line = fmt.Sprintf("%s%s %s %s (%s)", indent, dot, icon, node.Name, node.Status)
		}
		if node.Unread > 0 {
			line += fmt.Sprintf(" (%d)", node.Unread)
		}

		if i == m.selected {
			b.WriteString(m.theme.SelectedItem.Render(fmt.Sprintf("> %s", line)))
		} else {
			b.WriteString(m.theme.NormalText.Render(fmt.Sprintf("  %s", line)))
		}
		if i < len(m.nodes)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// SetSize updates the tree panel dimensions.
func (m *TreeModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// PrependWeaveRoot inserts a synthetic weave node at depth 0 and shifts all
// existing nodes down by one depth level, returning the combined slice. The
// rootUnread count is attached to the synthesized weave row so the tree can
// render an unread badge on the root when weave's maildir has unread mail
// (QUM-205 unread-counter subpoint + QUM-311 inbox notifier).
func PrependWeaveRoot(nodes []TreeNode, status string, rootUnread int) []TreeNode {
	weave := TreeNode{
		Name:   "weave",
		Type:   "weave",
		Status: status,
		Depth:  0,
		Unread: rootUnread,
	}
	result := make([]TreeNode, 0, len(nodes)+1)
	result = append(result, weave)
	for _, n := range nodes {
		n.Depth++
		result = append(result, n)
	}
	return result
}

// buildTreeNodes converts a flat list of AgentInfo into a depth-ordered tree.
func buildTreeNodes(agents []supervisor.AgentInfo, unread map[string]int) []TreeNode {
	if len(agents) == 0 {
		return nil
	}

	if unread == nil {
		unread = make(map[string]int)
	}

	// Build a set of known agent names.
	nameSet := make(map[string]bool, len(agents))
	for _, a := range agents {
		nameSet[a.Name] = true
	}

	// Build parent -> children map.
	children := make(map[string][]supervisor.AgentInfo)
	var roots []supervisor.AgentInfo
	for _, a := range agents {
		if a.Parent == "" || !nameSet[a.Parent] {
			roots = append(roots, a)
		} else {
			children[a.Parent] = append(children[a.Parent], a)
		}
	}

	// Sort children alphabetically.
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool {
			return children[k][i].Name < children[k][j].Name
		})
	}
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].Name < roots[j].Name
	})

	// DFS to build ordered nodes.
	var result []TreeNode
	var dfs func(a supervisor.AgentInfo, depth int)
	dfs = func(a supervisor.AgentInfo, depth int) {
		result = append(result, TreeNode{
			Name:              a.Name,
			Type:              a.Type,
			Family:            a.Family,
			Status:            a.Status,
			Depth:             depth,
			Unread:            unread[a.Name],
			LastReportState:   a.LastReportState,
			LastReportSummary: a.LastReportSummary,
		})
		for _, child := range children[a.Name] {
			dfs(child, depth+1)
		}
	}

	for _, root := range roots {
		dfs(root, 0)
	}

	return result
}
