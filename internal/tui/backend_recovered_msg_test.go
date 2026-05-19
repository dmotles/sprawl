package tui

import (
	"strings"
	"testing"
)

// QUM-601: after the supervisor reports a successful in-place recovery for an
// agent, the TUI receives a BackendFaultClearedMsg. The handler must:
//  1. Delete the per-agent fault sticker from m.faults (so the FAULT badge
//     stops showing).
//  2. Append a status banner to the root viewport that mentions both the
//     agent name and the word "recovered".
//  3. Rebuild the tree so the FaultClass on the matching TreeNode is empty.
//     (Viewport history is intentionally NOT cleared — operator forensic
//     trail.)

func TestAppModel_BackendFaultClearedMsg_DeletesFaultEntry(t *testing.T) {
	app := newTestAppModel(t)

	// Seed a fault for alice via the BackendFaultMsg handler so we test
	// against the same internal map shape the production path uses.
	updated, _ := app.Update(BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled",
		NextAction: "recover",
	})
	app = updated.(AppModel)
	if _, ok := app.faults["alice"]; !ok {
		t.Fatalf("pre-clear: faults[alice] missing; faults=%v", app.faults)
	}

	updated, _ = app.Update(BackendFaultClearedMsg{Agent: "alice"})
	app = updated.(AppModel)

	if _, ok := app.faults["alice"]; ok {
		t.Errorf("faults[alice] still present after BackendFaultClearedMsg; faults=%v", app.faults)
	}
}

func TestAppModel_BackendFaultClearedMsg_AppendsBanner(t *testing.T) {
	app := newTestAppModel(t)
	updated, _ := app.Update(BackendFaultMsg{
		Agent:      "alice",
		Class:      "HangTimeout",
		Reason:     "stalled",
		NextAction: "recover",
	})
	app = updated.(AppModel)

	updated, _ = app.Update(BackendFaultClearedMsg{Agent: "alice"})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "alice") {
		t.Errorf("viewport should mention agent name on recovery; got:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "recovered") {
		t.Errorf("viewport should mention 'recovered' on recovery; got:\n%s", view)
	}
}

func TestAppModel_BackendFaultClearedMsg_RebuildsTreeWithoutFaultClass(t *testing.T) {
	app := newTestAppModel(t)

	// Seed an agent tree with alice + a fault on alice.
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
		NextAction: "recover",
	})
	app = updated.(AppModel)
	// Confirm pre-condition: alice has FaultClass set on the tree node.
	var pre *TreeNode
	for i := range app.tree.nodes {
		if app.tree.nodes[i].Name == "alice" {
			n := app.tree.nodes[i]
			pre = &n
			break
		}
	}
	if pre == nil || pre.FaultClass != "HangTimeout" {
		t.Fatalf("pre-clear: alice node FaultClass = %+v, want HangTimeout", pre)
	}

	updated, _ = app.Update(BackendFaultClearedMsg{Agent: "alice"})
	app = updated.(AppModel)

	var post *TreeNode
	for i := range app.tree.nodes {
		if app.tree.nodes[i].Name == "alice" {
			n := app.tree.nodes[i]
			post = &n
			break
		}
	}
	if post == nil {
		t.Fatalf("alice node missing from tree after BackendFaultClearedMsg; nodes=%+v", app.tree.nodes)
	}
	if post.FaultClass != "" {
		t.Errorf("alice node FaultClass = %q after recovery; want empty", post.FaultClass)
	}
}
