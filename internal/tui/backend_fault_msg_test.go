package tui

import (
	"strings"
	"testing"
)

// QUM-602: when the supervisor emits a backend fault for an agent, the TUI
// must (a) render a viewport banner naming the agent + class + next-action
// hint, and (b) tag the agent's tree row so the operator can spot the faulted
// runtime at a glance.

func TestAppModel_BackendFaultMsg_AppendsBanner(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "backend: reader hang timeout (no frames within HangTimeout)",
		NextAction: "retire+respawn",
	})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "backend fault on alice") {
		t.Errorf("viewport should mention agent name; got:\n%s", view)
	}
	if !strings.Contains(view, "HangTimeout") {
		t.Errorf("viewport should mention fault class; got:\n%s", view)
	}
	if !strings.Contains(view, "retire+respawn") {
		t.Errorf("viewport should mention next-action hint; got:\n%s", view)
	}
}

func TestAppModel_BackendFaultMsg_StoresFaultForAgent(t *testing.T) {
	app := newTestAppModel(t)

	// Seed the tree with an "alice" child row by dispatching an
	// AgentTreeMsg with a single node matching the fault's agent name.
	treeMsg := AgentTreeMsg{
		Nodes: []TreeNode{
			{Name: "alice", Type: "engineer", Status: "active", Depth: 1},
		},
	}
	updated, _ := app.Update(treeMsg)
	app = updated.(AppModel)

	updated, _ = app.Update(BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled",
		NextAction: "retire+respawn",
	})
	app = updated.(AppModel)

	// Re-dispatching the same tree msg must not lose the fault sticker —
	// the AppModel stores faults in a side map keyed by agent name and
	// re-applies them on every rebuildTree.
	updated, _ = app.Update(treeMsg)
	app = updated.(AppModel)

	var found *TreeNode
	for i := range app.tree.nodes {
		if app.tree.nodes[i].Name == "alice" {
			n := app.tree.nodes[i]
			found = &n
			break
		}
	}
	if found == nil {
		t.Fatalf("tree.nodes did not contain alice; got %+v", app.tree.nodes)
	}
	if found.FaultClass != "HangTimeout" {
		t.Errorf("alice node FaultClass = %q, want %q", found.FaultClass, "HangTimeout")
	}
}

func TestTreeView_RendersFaultIndicator(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(80, 20)
	m.SetNodes([]TreeNode{
		{Name: "alice", Type: "engineer", Status: "active", Depth: 0, FaultClass: "HangTimeout"},
	})

	view := stripAnsi(m.View())
	if !strings.Contains(view, "[FAULT:HangTimeout]") {
		t.Errorf("View() should contain '[FAULT:HangTimeout]'; got:\n%s", view)
	}
}

func TestTreeNodes_NoFaultClass_NoFaultBadge(t *testing.T) {
	m := newTestTreeModel(t)
	m.SetSize(80, 20)
	m.SetNodes([]TreeNode{
		{Name: "bob", Type: "engineer", Status: "active", Depth: 0},
	})

	view := stripAnsi(m.View())
	if strings.Contains(view, "[FAULT") {
		t.Errorf("View() should NOT contain a fault badge when FaultClass is empty; got:\n%s", view)
	}
}
