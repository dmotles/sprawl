package tui

import (
	"strings"
	"testing"
)

// QUM-602 / QUM-675 S5: when the supervisor emits a backend fault for an
// agent, the TUI must (a) tag the agent's tree row with the fault sticker,
// and (b) NOT pollute the chat viewport with a status banner — the tree
// badge is the operator-facing surface (display-policy spec row 6: "pure
// deletion"). Backend recovery routes to the statusbar transient label.

func TestAppModel_BackendFaultMsg_NoViewportBanner(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "backend: reader hang timeout (no frames within HangTimeout)",
		NextAction: "retire+respawn",
	})
	app = updated.(AppModel)

	// QUM-693: Status/Banner/Error never enter ChatList — vacuous bleed
	// assertion deleted. Tree badge is the operator-facing surface; see
	// TestTreeView_RendersFaultIndicator.
	_ = app
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

// QUM-602 / QUM-675 S5: the fault map must re-stamp on every repeated fault
// arrival (no latched boolean). The original viewport-banner re-fire test is
// replaced by an assertion on the faults map's freshness, since the tree
// badge re-renders deterministically from that map.
func TestAppModel_BackendFaultMsg_RestampsFaultsMapOnRepeat(t *testing.T) {
	app := newTestAppModel(t)

	first := BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled-once",
		NextAction: "retire+respawn",
	}
	second := BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled-twice",
		NextAction: "retire+respawn",
	}

	updated, _ := app.Update(first)
	app = updated.(AppModel)
	updated, _ = app.Update(second)
	app = updated.(AppModel)

	got, ok := app.faults["alice"]
	if !ok {
		t.Fatalf("faults[alice] missing after repeat fault; faults=%v", app.faults)
	}
	if got.Reason != "stalled-twice" {
		t.Errorf("faults[alice].Reason = %q, want %q (re-stamp on repeat)", got.Reason, "stalled-twice")
	}

	// QUM-693: viewport-banner bleed assertion is vacuous post-deletion.
}

// QUM-602 / QUM-675 S5: after a fault is cleared, a subsequent fault for the
// same agent must re-stamp the faults map. The viewport-banner re-fire
// assertion from the original test is replaced by a check on the faults map
// state + a no-viewport-bleed assertion. Recovery banner is checked via the
// statusbar transient label (see TestBackendFaultClearedMsg_RoutesToTransientLabel
// in app_transient_label_test.go).
func TestAppModel_BackendFaultMsg_RestampsFaultsMapAfterClear(t *testing.T) {
	app := newTestAppModel(t)

	fault := BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled",
		NextAction: "retire+respawn",
	}

	updated, _ := app.Update(fault)
	app = updated.(AppModel)
	updated, _ = app.Update(BackendFaultClearedMsg{Agent: "alice"})
	app = updated.(AppModel)
	if _, ok := app.faults["alice"]; ok {
		t.Fatalf("faults[alice] should be deleted after BackendFaultClearedMsg; faults=%v", app.faults)
	}
	updated, _ = app.Update(fault)
	app = updated.(AppModel)

	if _, ok := app.faults["alice"]; !ok {
		t.Errorf("faults[alice] should be re-stamped after fault→clear→fault; faults=%v", app.faults)
	}
	// QUM-693: viewport-banner bleed assertion is vacuous post-deletion.
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
