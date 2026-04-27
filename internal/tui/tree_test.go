package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

func newTestTreeModel(t *testing.T) TreeModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewTreeModel(&theme)
}

func TestTreeModel_InitialSelection(t *testing.T) {
	m := newTestTreeModel(t)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0", m.selected)
	}
}

func TestTreeModel_NavigateDown(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())
	if len(m.nodes) < 2 {
		t.Skip("need at least 2 nodes for navigation test")
	}
	msg := tea.KeyPressMsg{Code: tea.KeyDown}
	m, _ = m.Update(msg)
	if m.selected != 1 {
		t.Errorf("selected = %d, want 1 after down key", m.selected)
	}
}

func TestTreeModel_NavigateUp(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())
	if len(m.nodes) < 2 {
		t.Skip("need at least 2 nodes for navigation test")
	}
	// Move down first, then up.
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	m, _ = m.Update(down)
	up := tea.KeyPressMsg{Code: tea.KeyUp}
	m, _ = m.Update(up)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0 after up key", m.selected)
	}
}

func TestTreeModel_NavigateDownWithJ(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())
	msg := tea.KeyPressMsg{Code: 'j'}
	m, _ = m.Update(msg)
	if m.selected != 1 {
		t.Errorf("selected = %d, want 1 after 'j' key", m.selected)
	}
}

func TestTreeModel_NavigateUpWithK(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())
	// Move down first, then up via 'k'.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j'})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'k'})
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0 after 'k' key", m.selected)
	}
}

func TestTreeModel_BoundsCheckTop(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())
	msg := tea.KeyPressMsg{Code: tea.KeyUp}
	m, _ = m.Update(msg)
	if m.selected != 0 {
		t.Errorf("selected = %d, want 0 (should not go negative)", m.selected)
	}
}

func TestTreeModel_BoundsCheckBottom(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())
	if len(m.nodes) == 0 {
		t.Skip("need nodes for bounds check")
	}
	last := len(m.nodes) - 1
	// Navigate to the last item.
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	for i := 0; i < len(m.nodes)+5; i++ {
		m, _ = m.Update(down)
	}
	if m.selected != last {
		t.Errorf("selected = %d, want %d (should not exceed last item)", m.selected, last)
	}
}

func TestTreeModel_ViewContainsItems(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	m.SetNodes(newTestTreeNodes())
	view := m.View()
	for _, node := range m.nodes {
		if !strings.Contains(view, node.Name) {
			t.Errorf("View() should contain node name %q, got:\n%s", node.Name, view)
		}
	}
}

func TestTreeModel_SetSize(t *testing.T) {
	m := newTestTreeModel(t)
	// Should not panic.
	m.SetSize(30, 15)
	if m.width != 30 {
		t.Errorf("width = %d, want 30", m.width)
	}
	if m.height != 15 {
		t.Errorf("height = %d, want 15", m.height)
	}
}

// --- Tests for QUM-200 5c: Agent Tree Panel + Observation ---

func TestTreeModel_SetSelected(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes(newTestTreeNodes())

	m.SetSelected("finn")
	if got := m.SelectedAgent(); got != "finn" {
		t.Errorf("SelectedAgent() = %q after SetSelected(\"finn\"), want %q", got, "finn")
	}

	// Setting a name not in the list is a no-op (selection unchanged).
	m.SetSelected("nonexistent")
	if got := m.SelectedAgent(); got != "finn" {
		t.Errorf("SelectedAgent() = %q after SetSelected(\"nonexistent\"), want %q (unchanged)", got, "finn")
	}
}

func newTestTreeNodes() []TreeNode {
	return []TreeNode{
		{Name: "weave", Type: "weave", Status: "active", Depth: 0, Unread: 2},
		{Name: "tower", Type: "manager", Status: "active", Depth: 1},
		{Name: "finn", Type: "engineer", Status: "active", Depth: 2, Unread: 1},
		{Name: "oak", Type: "engineer", Status: "idle", Depth: 2},
		{Name: "scout", Type: "researcher", Status: "active", Depth: 1},
	}
}

func TestTreeNode_TypeIcon(t *testing.T) {
	tests := []struct {
		nodeType string
		want     string
	}{
		{"weave", "[W]"},
		{"manager", "[M]"},
		{"engineer", "[E]"},
		{"researcher", "[R]"},
		{"unknown", "[?]"},
	}
	for _, tc := range tests {
		t.Run(tc.nodeType, func(t *testing.T) {
			got := typeIcon(tc.nodeType)
			if got != tc.want {
				t.Errorf("typeIcon(%q) = %q, want %q", tc.nodeType, got, tc.want)
			}
		})
	}
}

func TestTreeModel_SetNodes(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	nodes := newTestTreeNodes()

	m.SetNodes(nodes)

	if len(m.nodes) != len(nodes) {
		t.Fatalf("len(nodes) = %d, want %d", len(m.nodes), len(nodes))
	}
	for i, n := range m.nodes {
		if n.Name != nodes[i].Name {
			t.Errorf("nodes[%d].Name = %q, want %q", i, n.Name, nodes[i].Name)
		}
	}

	// View should render without panic and contain agent names.
	view := m.View()
	if !strings.Contains(view, "weave") {
		t.Errorf("View() should contain 'weave' after SetNodes, got:\n%s", view)
	}
}

func TestTreeModel_SetNodes_PreservesSelection(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)

	// Set initial nodes and select "finn" (index 2).
	nodes := newTestTreeNodes()
	m.SetNodes(nodes)
	m.selected = 2 // finn

	// Update nodes with a different order — finn is now at index 3.
	reordered := []TreeNode{
		{Name: "weave", Type: "weave", Status: "active", Depth: 0},
		{Name: "tower", Type: "manager", Status: "active", Depth: 1},
		{Name: "scout", Type: "researcher", Status: "active", Depth: 1},
		{Name: "finn", Type: "engineer", Status: "active", Depth: 2},
		{Name: "oak", Type: "engineer", Status: "idle", Depth: 2},
	}
	m.SetNodes(reordered)

	// Selection should follow "finn" to its new index.
	if m.selected != 3 {
		t.Errorf("selected = %d, want 3 (should preserve selection by name 'finn')", m.selected)
	}
}

func TestTreeModel_SetNodes_Empty(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)

	m.SetNodes(newTestTreeNodes())
	m.SetNodes(nil)

	if len(m.nodes) != 0 {
		t.Errorf("len(nodes) = %d after SetNodes(nil), want 0", len(m.nodes))
	}
	if m.selected != 0 {
		t.Errorf("selected = %d after empty SetNodes, want 0", m.selected)
	}

	// View should render a placeholder on empty nodes.
	view := m.View()
	if !strings.Contains(view, "No agents running") {
		t.Errorf("empty tree View() should contain placeholder text, got: %q", view)
	}
}

func TestTreeModel_ViewRendersTypeIcons(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	m.SetNodes(newTestTreeNodes())

	view := m.View()
	for _, icon := range []string{"[W]", "[M]", "[E]", "[R]"} {
		if !strings.Contains(view, icon) {
			t.Errorf("View() should contain type icon %q, got:\n%s", icon, view)
		}
	}
}

func TestTreeModel_ViewRendersIndentation(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	m.SetNodes(newTestTreeNodes())

	view := m.View()
	lines := strings.Split(view, "\n")

	// Find lines containing agents at different depths.
	// Depth 0 = "weave" (no indent), Depth 1 = "tower" (indented), Depth 2 = "finn" (more indented).
	var weaveLine, towerLine, finnLine string
	for _, line := range lines {
		stripped := stripAnsi(line)
		if strings.Contains(stripped, "weave") {
			weaveLine = stripped
		}
		if strings.Contains(stripped, "tower") {
			towerLine = stripped
		}
		if strings.Contains(stripped, "finn") {
			finnLine = stripped
		}
	}

	if weaveLine == "" || towerLine == "" || finnLine == "" {
		t.Fatalf("could not find weave/tower/finn lines in view:\n%s", view)
	}

	// tower (depth 1) should have more leading space than weave (depth 0).
	weaveIndent := len(weaveLine) - len(strings.TrimLeft(weaveLine, " "))
	towerIndent := len(towerLine) - len(strings.TrimLeft(towerLine, " "))
	finnIndent := len(finnLine) - len(strings.TrimLeft(finnLine, " "))

	if towerIndent <= weaveIndent {
		t.Errorf("tower indent (%d) should be greater than weave indent (%d)", towerIndent, weaveIndent)
	}
	if finnIndent <= towerIndent {
		t.Errorf("finn indent (%d) should be greater than tower indent (%d)", finnIndent, towerIndent)
	}
}

// stripAnsi removes ANSI escape sequences from a string for assertion purposes.
func stripAnsi(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		if r == '\033' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEscape = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func TestTreeModel_ViewRendersUnreadBadge(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	m.SetNodes(newTestTreeNodes()) // weave has Unread: 2, finn has Unread: 1

	view := m.View()
	// Unread badge should show the count for weave (2) and finn (1).
	if !strings.Contains(view, "(2)") {
		t.Errorf("View() should contain unread badge '(2)' for weave, got:\n%s", view)
	}
	if !strings.Contains(view, "(1)") {
		t.Errorf("View() should contain unread badge '(1)' for finn, got:\n%s", view)
	}
}

func TestTreeModel_ViewOmitsZeroUnread(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0, Unread: 0},
	}
	m.SetNodes(nodes)

	view := m.View()
	if strings.Contains(view, "(0)") {
		t.Errorf("View() should not contain '(0)' badge for zero unread, got:\n%s", view)
	}
}

func TestTreeModel_EnterEmitsAgentSelectedMsg(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	m.SetNodes(newTestTreeNodes())
	m.selected = 1 // tower

	enterMsg := tea.KeyPressMsg{Code: tea.KeyEnter}
	_, cmd := m.Update(enterMsg)
	if cmd == nil {
		t.Fatal("Enter key should return a cmd")
	}
	result := cmd()
	sel, ok := result.(AgentSelectedMsg)
	if !ok {
		t.Fatalf("cmd() returned %T, want AgentSelectedMsg", result)
	}
	if sel.Name != "tower" {
		t.Errorf("AgentSelectedMsg.Name = %q, want %q", sel.Name, "tower")
	}
}

func TestTreeModel_SelectedAgent(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(40, 20)
	m.SetNodes(newTestTreeNodes())

	m.selected = 0
	if got := m.SelectedAgent(); got != "weave" {
		t.Errorf("SelectedAgent() = %q, want %q", got, "weave")
	}

	m.selected = 2
	if got := m.SelectedAgent(); got != "finn" {
		t.Errorf("SelectedAgent() = %q, want %q", got, "finn")
	}
}

func TestBuildTreeNodes_Hierarchy(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "weave", Type: "weave", Family: "", Parent: "", Status: "active"},
		{Name: "tower", Type: "manager", Family: "tower", Parent: "weave", Status: "active"},
		{Name: "finn", Type: "engineer", Family: "tower", Parent: "tower", Status: "active"},
		{Name: "oak", Type: "engineer", Family: "tower", Parent: "tower", Status: "idle"},
		{Name: "scout", Type: "researcher", Family: "scout", Parent: "weave", Status: "active"},
	}
	unread := map[string]int{
		"weave": 3,
		"finn":  1,
	}

	nodes := buildTreeNodes(agents, unread)

	if len(nodes) != 5 {
		t.Fatalf("len(nodes) = %d, want 5", len(nodes))
	}

	// Verify root.
	if nodes[0].Name != "weave" || nodes[0].Depth != 0 {
		t.Errorf("nodes[0] = {Name:%q, Depth:%d}, want {Name:weave, Depth:0}", nodes[0].Name, nodes[0].Depth)
	}
	if nodes[0].Unread != 3 {
		t.Errorf("nodes[0].Unread = %d, want 3", nodes[0].Unread)
	}

	// Children of weave should be depth 1.
	if nodes[1].Depth != 1 {
		t.Errorf("nodes[1].Depth = %d, want 1 (child of weave)", nodes[1].Depth)
	}

	// Children of tower should be depth 2.
	foundFinn := false
	for _, n := range nodes {
		if n.Name == "finn" {
			foundFinn = true
			if n.Depth != 2 {
				t.Errorf("finn.Depth = %d, want 2", n.Depth)
			}
			if n.Unread != 1 {
				t.Errorf("finn.Unread = %d, want 1", n.Unread)
			}
		}
	}
	if !foundFinn {
		t.Error("finn not found in nodes")
	}
}

func TestTreeModel_ReportChipRendering(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(80, 10)
	m.SetNodes([]TreeNode{
		{Name: "alice", Type: "engineer", Status: "active", LastReportState: "working", LastReportSummary: "writing tests"},
		{Name: "bob", Type: "engineer", Status: "active", LastReportState: "blocked", LastReportSummary: "awaiting review"},
		{Name: "carol", Type: "engineer", Status: "done", LastReportState: "complete", LastReportSummary: "merged PR"},
		{Name: "dave", Type: "engineer", Status: "problem", LastReportState: "failure", LastReportSummary: "tests fail"},
		{Name: "eve", Type: "engineer", Status: "active"}, // no report → idle
	})
	view := m.View()

	// Dot glyph appears for every row (once per node).
	if n := strings.Count(view, "●"); n != 5 {
		t.Errorf("expected 5 dots (one per node), got %d; view:\n%s", n, view)
	}
	// When a summary is present, it replaces the "(status)" rendering.
	if !strings.Contains(view, "alice — writing tests") {
		t.Errorf("working chip summary missing; view:\n%s", view)
	}
	if !strings.Contains(view, "bob — awaiting review") {
		t.Errorf("blocked chip summary missing; view:\n%s", view)
	}
	if !strings.Contains(view, "carol — merged PR") {
		t.Errorf("complete chip summary missing; view:\n%s", view)
	}
	if !strings.Contains(view, "dave — tests fail") {
		t.Errorf("failure chip summary missing; view:\n%s", view)
	}
	// No-report node falls back to "(status)".
	if !strings.Contains(view, "eve (active)") {
		t.Errorf("idle fallback missing for eve; view:\n%s", view)
	}
}

// QUM-324: a LastReportSummary longer than the tree panel's inner width must
// not bleed past the right border. Every rendered row — including the
// ellipsised long-summary row — must fit inside m.width display cells.
func TestTreeModel_ViewClipsLongSummary(t *testing.T) {
	const panelWidth = 40
	m := newTestTreeModel(t)
	m.SetSize(panelWidth, 10)
	m.SetNodes([]TreeNode{
		{
			Name:              "alice",
			Type:              "engineer",
			Status:            "active",
			LastReportState:   "working",
			LastReportSummary: strings.Repeat("abcdefghij", 200), // 2000 chars
		},
	})

	view := m.View()
	lines := strings.Split(view, "\n")
	for _, line := range lines {
		if w := lipgloss.Width(line); w > panelWidth {
			t.Errorf("rendered line width %d exceeds panel width %d: %q", w, panelWidth, line)
		}
	}
	// The view must render exactly one physical line per logical node — no
	// soft-wrap into extra rows. This is the invariant that keeps the tree
	// panel from overflowing its border's Height budget (which would push the
	// input box off the bottom of the screen — QUM-324 follow-up).
	if got, want := len(lines), len(m.nodes); got != want {
		t.Errorf("view has %d physical lines, want %d (one per node); view:\n%s", got, want, view)
	}
	// Sanity: the row should still contain the agent name, proving the clip
	// didn't just eat the whole line.
	if !strings.Contains(stripAnsi(view), "alice") {
		t.Errorf("expected 'alice' in clipped view, got:\n%s", view)
	}
	// And it should end with the ellipsis — the summary is well past the
	// truncation point.
	if !strings.Contains(view, "…") {
		t.Errorf("expected ellipsis in clipped long-summary row, got:\n%s", view)
	}
}

// QUM-324: a multi-line summary must render as a single row so the tree
// can't push past its configured height, and the panel-width invariant still
// holds on the (now single-line) output.
func TestTreeModel_ViewStripsMultilineSummary(t *testing.T) {
	const panelWidth = 80
	m := newTestTreeModel(t)
	m.SetSize(panelWidth, 5)
	m.SetNodes([]TreeNode{
		{
			Name:              "alice",
			Type:              "engineer",
			Status:            "active",
			LastReportState:   "working",
			LastReportSummary: "line1\nline2\nline3",
		},
	})

	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 1 {
		t.Errorf("multi-line summary should render as a single row, got %d:\n%s", len(lines), view)
	}
	stripped := stripAnsi(view)
	for _, frag := range []string{"line1", "line2", "line3"} {
		if !strings.Contains(stripped, frag) {
			t.Errorf("expected fragment %q in flattened summary, got: %q", frag, stripped)
		}
	}
	if strings.ContainsAny(stripped, "\n\r\t") {
		t.Errorf("flattened summary still contains newline/tab: %q", stripped)
	}
}

// QUM-324: the selected-row prefix ("> ") eats the same two cells as the
// normal-row prefix ("  "), so both rows must fit the panel width when a
// long summary is present.
func TestTreeModel_ViewClipsLongSummary_SelectedRow(t *testing.T) {
	const panelWidth = 30
	m := newTestTreeModel(t)
	m.SetSize(panelWidth, 10)
	m.SetNodes([]TreeNode{
		{Name: "bob", Type: "engineer", Status: "active"},
		{
			Name:              "alice",
			Type:              "engineer",
			Status:            "active",
			LastReportState:   "working",
			LastReportSummary: strings.Repeat("x", 500),
		},
	})
	m.selected = 1 // select the row with the long summary

	view := m.View()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > panelWidth {
			t.Errorf("rendered line width %d exceeds panel width %d: %q", w, panelWidth, line)
		}
	}
}

func TestTreeModel_ReportChip_ColorsDiffer(t *testing.T) {
	// The five state classes should each yield a distinct ANSI-coded dot.
	theme := NewTheme("colour212")
	seen := map[string]bool{}
	for _, s := range []string{"working", "blocked", "failure", "complete", "", "bogus"} {
		rendered := theme.ReportDot(s)
		if rendered == "" || !strings.Contains(rendered, "●") {
			t.Errorf("ReportDot(%q) did not render the dot glyph: %q", s, rendered)
		}
		seen[rendered] = true
	}
	// working/blocked/failure/complete/idle should be 5 distinct values.
	// "" and "bogus" fall through to idle.
	if len(seen) != 5 {
		t.Errorf("expected 5 distinct rendered dots, got %d (%v)", len(seen), seen)
	}
}

func TestBuildTreeNodes_PropagatesReportFields(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "alice", Type: "engineer", Status: "active", LastReportState: "working", LastReportSummary: "in flight"},
	}
	nodes := buildTreeNodes(agents, nil)
	if len(nodes) != 1 {
		t.Fatalf("len = %d", len(nodes))
	}
	if nodes[0].LastReportState != "working" {
		t.Errorf("LastReportState = %q", nodes[0].LastReportState)
	}
	if nodes[0].LastReportSummary != "in flight" {
		t.Errorf("LastReportSummary = %q", nodes[0].LastReportSummary)
	}
}

func TestBuildTreeNodes_Empty(t *testing.T) {
	nodes := buildTreeNodes(nil, nil)
	if len(nodes) != 0 {
		t.Errorf("len(nodes) = %d for empty input, want 0", len(nodes))
	}
}

func TestBuildTreeNodes_OrphanedParent(t *testing.T) {
	// Agent references a parent that doesn't exist in the list.
	agents := []supervisor.AgentInfo{
		{Name: "finn", Type: "engineer", Parent: "ghost", Status: "active"},
	}
	nodes := buildTreeNodes(agents, nil)

	// Should still produce a node (likely at depth 0 since parent is missing).
	if len(nodes) == 0 {
		t.Fatal("buildTreeNodes should handle orphaned parent gracefully, got 0 nodes")
	}
	if nodes[0].Name != "finn" {
		t.Errorf("nodes[0].Name = %q, want %q", nodes[0].Name, "finn")
	}
}

// --- Tests for QUM-235: PrependWeaveRoot ---

func TestPrependWeaveRoot_EmptyChildren(t *testing.T) {
	result := PrependWeaveRoot(nil, "idle", 0)

	if len(result) != 1 {
		t.Fatalf("PrependWeaveRoot(nil) returned %d nodes, want 1", len(result))
	}
	if result[0].Name != "weave" {
		t.Errorf("result[0].Name = %q, want %q", result[0].Name, "weave")
	}
}

func TestPrependWeaveRoot_ShiftsChildDepths(t *testing.T) {
	children := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
		{Name: "finn", Type: "engineer", Status: "active", Depth: 1},
	}

	result := PrependWeaveRoot(children, "idle", 0)

	// Result has weave + 2 children = 3 nodes.
	if len(result) != 3 {
		t.Fatalf("len(result) = %d, want 3", len(result))
	}
	// tower was depth 0, should now be depth 1.
	if result[1].Depth != 1 {
		t.Errorf("result[1].Depth = %d, want 1 (tower shifted by 1)", result[1].Depth)
	}
	// finn was depth 1, should now be depth 2.
	if result[2].Depth != 2 {
		t.Errorf("result[2].Depth = %d, want 2 (finn shifted by 1)", result[2].Depth)
	}
}

func TestPrependWeaveRoot_PreservesChildOrder(t *testing.T) {
	children := []TreeNode{
		{Name: "alpha", Type: "manager", Status: "active", Depth: 0},
		{Name: "beta", Type: "engineer", Status: "active", Depth: 1},
		{Name: "gamma", Type: "engineer", Status: "idle", Depth: 1},
	}

	result := PrependWeaveRoot(children, "idle", 0)

	if len(result) != 4 {
		t.Fatalf("len(result) = %d, want 4", len(result))
	}
	// First node is always weave.
	if result[0].Name != "weave" {
		t.Errorf("result[0].Name = %q, want %q", result[0].Name, "weave")
	}
	// Children should preserve order.
	if result[1].Name != "alpha" {
		t.Errorf("result[1].Name = %q, want %q", result[1].Name, "alpha")
	}
	if result[2].Name != "beta" {
		t.Errorf("result[2].Name = %q, want %q", result[2].Name, "beta")
	}
	if result[3].Name != "gamma" {
		t.Errorf("result[3].Name = %q, want %q", result[3].Name, "gamma")
	}
}

func TestPrependWeaveRoot_DoesNotMutateInput(t *testing.T) {
	children := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
		{Name: "finn", Type: "engineer", Status: "active", Depth: 1},
	}
	originalDepths := []int{children[0].Depth, children[1].Depth}

	PrependWeaveRoot(children, "idle", 0)

	// Original slice must not be mutated.
	if children[0].Depth != originalDepths[0] {
		t.Errorf("children[0].Depth mutated: got %d, want %d", children[0].Depth, originalDepths[0])
	}
	if children[1].Depth != originalDepths[1] {
		t.Errorf("children[1].Depth mutated: got %d, want %d", children[1].Depth, originalDepths[1])
	}
}

func TestPrependWeaveRoot_StatusReflected(t *testing.T) {
	result := PrependWeaveRoot(nil, "thinking", 0)

	if result[0].Status != "thinking" {
		t.Errorf("result[0].Status = %q, want %q", result[0].Status, "thinking")
	}
}

func TestPrependWeaveRoot_WeaveUnreadReflected(t *testing.T) {
	// QUM-205 / QUM-311: the synthesized weave row carries the caller-supplied
	// unread count so the tree can render an unread badge on the root.
	result := PrependWeaveRoot(nil, "idle", 3)

	if result[0].Unread != 3 {
		t.Errorf("result[0].Unread = %d, want 3 (rootUnread arg should propagate)", result[0].Unread)
	}
}

func TestPrependWeaveRoot_WeaveUnreadZeroByDefault(t *testing.T) {
	result := PrependWeaveRoot(nil, "idle", 0)

	if result[0].Unread != 0 {
		t.Errorf("result[0].Unread = %d, want 0 when rootUnread=0", result[0].Unread)
	}
}

func TestPrependWeaveRoot_WeaveIsDepthZero(t *testing.T) {
	children := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	result := PrependWeaveRoot(children, "idle", 0)

	if result[0].Depth != 0 {
		t.Errorf("result[0].Depth = %d, want 0 (weave always at depth 0)", result[0].Depth)
	}
}
