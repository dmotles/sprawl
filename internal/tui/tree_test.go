package tui

import (
	"strings"
	"testing"
	"time"

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

func TestTreeModel_OrbitalLines_ReturnsRenderedTree(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, InTurn: true},
	})
	m.SetSelected("finn")

	lines := m.OrbitalLines(100, 0)
	if got, want := len(lines), 3; got != want {
		t.Fatalf("len(lines) = %d, want %d", got, want)
	}
	joined := stripAnsi(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "weave") {
		t.Errorf("expected 'weave' in OrbitalLines output, got:\n%s", joined)
	}
	if !strings.Contains(joined, "finn") {
		t.Errorf("expected 'finn' in OrbitalLines output, got:\n%s", joined)
	}

	// Selection should drive the pill: same selReverseStyle substring as in
	// tree_orbital_test.go must appear in the raw (un-stripped) output.
	raw := strings.Join(lines, "\n")
	expectedPill := lipgloss.NewStyle().
		Reverse(true).
		Foreground(lipgloss.Color("#0B0B12")).
		Background(lipgloss.Color("#22D3EE")).
		Bold(true).
		Padding(0, 1).
		Render("finn ⚙")
	if !strings.Contains(raw, expectedPill) {
		t.Errorf("expected selReverseStyle 'finn ⚙' pill in OrbitalLines output after SetSelected; raw:\n%q", raw)
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

// Regression test: when weave.json exists in .sprawl/agents/, the status
// response includes a weave AgentInfo entry (Parent: ""). buildTreeNodes
// places it at depth 0 as a root, and PrependWeaveRoot adds a *second*
// synthetic weave node — resulting in a duplicate. The fix should filter out
// the root agent before passing agents to buildTreeNodes so that weave
// appears exactly once after PrependWeaveRoot.
func TestBuildTreeNodes_ExcludesRootWeave(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "weave", Type: "weave", Family: "", Parent: "", Status: "active"},
		{Name: "tower", Type: "manager", Family: "tower", Parent: "weave", Status: "active"},
		{Name: "finn", Type: "engineer", Family: "tower", Parent: "tower", Status: "active"},
		{Name: "scout", Type: "researcher", Family: "scout", Parent: "weave", Status: "active"},
	}
	unread := map[string]int{
		"weave": 2,
		"finn":  1,
	}

	// Simulate the full flow: filter out root agent, build tree, prepend root.
	// This mirrors tickAgentsCmd which filters before calling buildTreeNodes.
	var filtered []supervisor.AgentInfo
	for _, a := range agents {
		if a.Name != "weave" {
			filtered = append(filtered, a)
		}
	}
	nodes := buildTreeNodes(filtered, unread)
	nodes = PrependWeaveRoot(nodes, "active", unread["weave"])

	// Count how many times "weave" appears in the final node list.
	weaveCount := 0
	for _, n := range nodes {
		if n.Name == "weave" {
			weaveCount++
		}
	}

	if weaveCount != 1 {
		t.Errorf("weave appears %d time(s) in final tree, want exactly 1 (duplicate weave bug)", weaveCount)
		for i, n := range nodes {
			t.Logf("  nodes[%d] = {Name:%q, Depth:%d, Type:%q}", i, n.Name, n.Depth, n.Type)
		}
	}

	// The single weave node should be at depth 0.
	if nodes[0].Name != "weave" || nodes[0].Depth != 0 {
		t.Errorf("nodes[0] = {Name:%q, Depth:%d}, want {Name:weave, Depth:0}", nodes[0].Name, nodes[0].Depth)
	}

	// Child agents should still be present and correctly nested.
	childNames := make(map[string]bool)
	for _, n := range nodes {
		if n.Name != "weave" {
			childNames[n.Name] = true
		}
	}
	for _, expected := range []string{"tower", "finn", "scout"} {
		if !childNames[expected] {
			t.Errorf("expected child %q in tree, not found", expected)
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

func TestBuildTreeNodes_PropagatesCostField(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "alice", Type: "engineer", Status: "active", TotalCostUsd: 0.05},
	}
	nodes := buildTreeNodes(agents, nil)
	if len(nodes) != 1 {
		t.Fatalf("len = %d", len(nodes))
	}
	if nodes[0].TotalCostUsd != 0.05 {
		t.Errorf("TotalCostUsd = %f, want 0.05", nodes[0].TotalCostUsd)
	}
}

func TestBuildTreeNodes_PropagatesReportFields(t *testing.T) {
	agents := []supervisor.AgentInfo{
		{Name: "alice", Type: "engineer", Status: "active", LastReportState: "working", LastReportMessage: "in flight"},
	}
	nodes := buildTreeNodes(agents, nil)
	if len(nodes) != 1 {
		t.Fatalf("len = %d", len(nodes))
	}
	if nodes[0].LastReportState != "working" {
		t.Errorf("LastReportState = %q", nodes[0].LastReportState)
	}
	if nodes[0].LastReportMessage != "in flight" {
		t.Errorf("LastReportMessage = %q", nodes[0].LastReportMessage)
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

// --- QUM-665: DeriveIconState liveness-first precedence ---

// QUM-722: TreeNode.Liveness drives Paused/Died icon variants.

func TestDeriveIconState_PausedReturnsPaused(t *testing.T) {
	n := TreeNode{Liveness: "paused"}
	got := DeriveIconState(n, time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC))
	if got != "paused" {
		t.Errorf("DeriveIconState(paused) = %q, want %q", got, "paused")
	}
}

func TestDeriveIconState_DiedReturnsDied(t *testing.T) {
	n := TreeNode{Liveness: "died"}
	got := DeriveIconState(n, time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC))
	if got != "died" {
		t.Errorf("DeriveIconState(died) = %q, want %q", got, "died")
	}
}

// QUM-722: theme renders distinct glyphs for Paused (⏸) and Died (✗).
// Mirrors the ReportDot test pattern (theme_test.go) — pin the dot/glyph
// per icon state.
func TestTheme_ReportDot_PausedAndDied(t *testing.T) {
	theme := NewTheme("")
	paused := theme.ReportDot("paused")
	if !strings.Contains(paused, "⏸") {
		t.Errorf("ReportDot(paused) = %q, want it to contain ⏸", paused)
	}
	died := theme.ReportDot("died")
	if !strings.Contains(died, "✗") {
		t.Errorf("ReportDot(died) = %q, want it to contain ✗", died)
	}
}

// TestDeriveIconState_Mapping locks the precedence rules for the new icon
// derivation: process-alive=false → idle; else in_autonomous_turn → working;
// else recent activity (within RecentActivityWindow) → working; else fall
// back to last_report_state for blocked/complete/failure; else idle.
// "working" self-reports are NO LONGER special-cased — only liveness drives
// the working state.
func TestDeriveIconState_Mapping(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		node TreeNode
		want string
	}{
		{
			name: "working: in_autonomous_turn beats stale blocked report",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          true,
				LastActivityAt:  time.Time{},
				LastReportState: "blocked",
			},
			want: "working",
		},
		{
			name: "working: recent activity beats stale blocked report (QUM-665 repro)",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  now.Add(-1 * time.Second),
				LastReportState: "blocked",
			},
			want: "working",
		},
		{
			name: "working: recent activity beats stale working report",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  now.Add(-500 * time.Millisecond),
				LastReportState: "working",
			},
			want: "working",
		},
		{
			name: "idle: working self-report no longer special-cased (no recent activity)",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  now.Add(-2 * RecentActivityWindow),
				LastReportState: "working",
			},
			want: "idle",
		},
		{
			name: "blocked: idle by liveness, blocked self-report",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  time.Time{},
				LastReportState: "blocked",
			},
			want: "blocked",
		},
		{
			name: "blocked: idle by liveness with old activity, blocked self-report",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  now.Add(-2 * RecentActivityWindow),
				LastReportState: "blocked",
			},
			want: "blocked",
		},
		{
			name: "complete: idle by liveness, complete self-report",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  time.Time{},
				LastReportState: "complete",
			},
			want: "complete",
		},
		{
			name: "failure: idle by liveness, failure self-report",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  time.Time{},
				LastReportState: "failure",
			},
			want: "failure",
		},
		{
			name: "idle: no signal at all",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  time.Time{},
				LastReportState: "",
			},
			want: "idle",
		},
		{
			name: "idle: process dead beats any other signal",
			node: TreeNode{
				ProcessAlive:    boolPtr(false),
				InTurn:          true,
				LastActivityAt:  now,
				LastReportState: "working",
			},
			want: "idle",
		},
		{
			name: "weave row fallback: nil ProcessAlive + no signals + empty report",
			node: TreeNode{
				ProcessAlive:    nil,
				InTurn:          false,
				LastActivityAt:  time.Time{},
				LastReportState: "",
			},
			want: "idle",
		},
		{
			name: "weave row fallback: nil ProcessAlive routes through report state when no activity",
			node: TreeNode{
				ProcessAlive:    nil,
				InTurn:          false,
				LastActivityAt:  time.Time{},
				LastReportState: "blocked",
			},
			want: "blocked",
		},
		{
			name: "boundary: exactly at RecentActivityWindow counts as not-recent",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  now.Add(-RecentActivityWindow),
				LastReportState: "",
			},
			want: "idle",
		},
		{
			name: "boundary: just inside RecentActivityWindow counts as recent",
			node: TreeNode{
				ProcessAlive:    boolPtr(true),
				InTurn:          false,
				LastActivityAt:  now.Add(-(RecentActivityWindow - 1*time.Millisecond)),
				LastReportState: "blocked",
			},
			want: "working",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveIconState(tc.node, now)
			if got != tc.want {
				t.Errorf("DeriveIconState() = %q, want %q\n  node = %+v", got, tc.want, tc.node)
			}
		})
	}
}

// TestBuildTreeNodes_PropagatesLivenessFields asserts the liveness fields on
// AgentInfo (InTurn, LastActivityAt, ProcessAlive) flow verbatim
// into the TreeNode produced by buildTreeNodes. Without this, DeriveIconState
// would always see zero-values and the QUM-665 fix would never engage.
func TestBuildTreeNodes_PropagatesLivenessFields(t *testing.T) {
	alive := true
	ts := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	agents := []supervisor.AgentInfo{
		{
			Name:           "alice",
			Type:           "engineer",
			Status:         "active",
			ProcessAlive:   &alive,
			InTurn:         true,
			LastActivityAt: ts,
		},
	}
	nodes := buildTreeNodes(agents, nil)
	if len(nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.ProcessAlive == nil || *n.ProcessAlive != true {
		t.Errorf("TreeNode.ProcessAlive = %v, want *bool→true", n.ProcessAlive)
	}
	if !n.InTurn {
		t.Errorf("TreeNode.InTurn = false, want true")
	}
	if !n.LastActivityAt.Equal(ts) {
		t.Errorf("TreeNode.LastActivityAt = %v, want %v", n.LastActivityAt, ts)
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

// --- QUM-692: tree liveness fix ---
//
// TODO(QUM-692): the implementer should rename TreeNode.InTurn and
// AgentInfo.InTurn to InTurn (and Session.InTurn() to
// InTurn()) as part of the fix. When that rename lands, update the field
// references in the tests below from InTurn → InTurn.

// TestDeriveIconState_InTurnTrue_AlwaysWorking_RegardlessOfActivityWindow
// asserts the contract for the renamed InTurn flag (QUM-692): when the flag
// is true the node must render as "working" no matter how stale
// LastActivityAt is. Regression guard for the case where the only signal we
// have is a still-open backend turn (no recent tool-use activity yet).
func TestDeriveIconState_InTurnTrue_AlwaysWorking_RegardlessOfActivityWindow(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	node := TreeNode{
		ProcessAlive:    boolPtr(true),
		InTurn:          true,
		LastActivityAt:  now.Add(-1 * time.Hour),
		LastReportState: "",
	}
	if got := DeriveIconState(node, now); got != "working" {
		t.Errorf("DeriveIconState() = %q, want \"working\" (InTurn=true must trump stale activity)", got)
	}
}

// TestDeriveIconState_RecentActivityWindow_30Seconds asserts that QUM-692
// bumps RecentActivityWindow from 2s to 30s: a node whose last activity was
// 15s ago must still render as "working" via the fallback path.
func TestDeriveIconState_RecentActivityWindow_30Seconds(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)

	if RecentActivityWindow < 30*time.Second {
		t.Errorf("RecentActivityWindow = %v, want >= 30s (QUM-692)", RecentActivityWindow)
	}

	node := TreeNode{
		ProcessAlive:    boolPtr(true),
		InTurn:          false,
		LastActivityAt:  now.Add(-15 * time.Second),
		LastReportState: "",
	}
	if got := DeriveIconState(node, now); got != "working" {
		t.Errorf("DeriveIconState() = %q, want \"working\" (15s old activity must be within the 30s window)", got)
	}
}

// TestDeriveIconState_Idle_NoInTurn_NoRecentActivity is a baseline regression
// guard: with no InTurn signal, no recent activity, and no self-report,
// DeriveIconState must return "idle". Pins the negative case so the wider
// 30s window doesn't accidentally make idle nodes look working.
func TestDeriveIconState_Idle_NoInTurn_NoRecentActivity(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	node := TreeNode{
		ProcessAlive:    boolPtr(true),
		InTurn:          false,
		LastActivityAt:  now.Add(-5 * time.Minute),
		LastReportState: "",
	}
	if got := DeriveIconState(node, now); got != "idle" {
		t.Errorf("DeriveIconState() = %q, want \"idle\"", got)
	}
}

// TestBuildTreeNodes_ManagerInheritsInTurn_FromDescendant asserts the
// transitive rollup: a manager with any descendant whose InTurn=true must
// itself be rendered as InTurn=true so the manager row shows "working" in
// the tree. Without this, an active engineer under an idle-looking manager
// makes the user think the family stack is dead.
func TestBuildTreeNodes_ManagerInheritsInTurn_FromDescendant(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	agents := []supervisor.AgentInfo{
		{
			Name:           "tower",
			Type:           "manager",
			Family:         "tower",
			Parent:         "",
			Status:         "active",
			ProcessAlive:   boolPtr(true),
			InTurn:         false,
			LastActivityAt: time.Time{},
		},
		{
			Name:           "finn",
			Type:           "engineer",
			Family:         "tower",
			Parent:         "tower",
			Status:         "active",
			ProcessAlive:   boolPtr(true),
			InTurn:         true,
			LastActivityAt: now,
		},
	}

	nodes := buildTreeNodes(agents, nil)
	if len(nodes) != 2 {
		t.Fatalf("len(nodes) = %d, want 2", len(nodes))
	}

	var tower *TreeNode
	for i := range nodes {
		if nodes[i].Name == "tower" {
			tower = &nodes[i]
			break
		}
	}
	if tower == nil {
		t.Fatal("tower node not found in buildTreeNodes output")
	}
	if !tower.InTurn {
		t.Errorf("tower.InTurn = false, want true (transitive rollup from finn)")
	}
	if got := DeriveIconState(*tower, now); got != "working" {
		t.Errorf("DeriveIconState(tower) = %q, want \"working\" (manager should show working when descendant is in turn)", got)
	}
}

// TestBuildTreeNodes_ManagerStaysIdle_NoActiveDescendants asserts the negative
// case of the rollup: a manager whose descendants are all idle must remain
// idle. Guards against an over-eager rollup that paints every manager as
// working unconditionally.
func TestBuildTreeNodes_ManagerStaysIdle_NoActiveDescendants(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	agents := []supervisor.AgentInfo{
		{
			Name:           "tower",
			Type:           "manager",
			Family:         "tower",
			Parent:         "",
			Status:         "active",
			ProcessAlive:   boolPtr(true),
			InTurn:         false,
			LastActivityAt: time.Time{},
		},
		{
			Name:           "finn",
			Type:           "engineer",
			Family:         "tower",
			Parent:         "tower",
			Status:         "active",
			ProcessAlive:   boolPtr(true),
			InTurn:         false,
			LastActivityAt: time.Time{},
		},
		{
			Name:           "oak",
			Type:           "engineer",
			Family:         "tower",
			Parent:         "tower",
			Status:         "idle",
			ProcessAlive:   boolPtr(true),
			InTurn:         false,
			LastActivityAt: time.Time{},
		},
	}

	nodes := buildTreeNodes(agents, nil)
	var tower *TreeNode
	for i := range nodes {
		if nodes[i].Name == "tower" {
			tower = &nodes[i]
			break
		}
	}
	if tower == nil {
		t.Fatal("tower node not found")
	}
	if tower.InTurn {
		t.Errorf("tower.InTurn = true, want false (no descendant is in turn)")
	}
	if got := DeriveIconState(*tower, now); got != "idle" {
		t.Errorf("DeriveIconState(tower) = %q, want \"idle\"", got)
	}
}
