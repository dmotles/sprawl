package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// hudTestApp returns a ready AppModel with a supervisor and one child agent
// (so agentNames() has >= 2 entries and Ctrl+N actually cycles).
func hudTestApp(t *testing.T) AppModel {
	t.Helper()
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)
	app.childNodes = []TreeNode{{Name: "tower", Type: "manager"}}
	app.treeSeeded = true
	app.rebuildTree()
	return app
}

func toastTexts(app AppModel) []string {
	var out []string
	for _, tt := range app.toasts.Toasts() {
		out = append(out, tt.Text)
	}
	return out
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestApp_CtrlN_ShowsHUDAndArmsTick(t *testing.T) {
	app := hudTestApp(t)
	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.treeHud.visible {
		t.Error("Ctrl+N should make the tree HUD visible")
	}
	if cmd == nil {
		t.Error("Ctrl+N should return a command batch (cycle + fade tick)")
	}
}

func TestApp_CtrlP_ShowsHUD(t *testing.T) {
	app := hudTestApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.treeHud.visible {
		t.Error("Ctrl+P should make the tree HUD visible")
	}
}

func TestApp_TreeHudTimer_StaleGenDoesNotHide_CurrentGenHides(t *testing.T) {
	app := hudTestApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	cur := app.treeHud.gen

	// A stale (older) generation timer must not hide the HUD.
	updated, _ = app.Update(treeHudTimerMsg{Gen: cur - 1})
	app = updated.(AppModel)
	if !app.treeHud.visible {
		t.Error("stale-generation timer must not hide the HUD")
	}

	// The current-generation timer hides it.
	updated, _ = app.Update(treeHudTimerMsg{Gen: cur})
	app = updated.(AppModel)
	if app.treeHud.visible {
		t.Error("current-generation timer should hide the HUD")
	}
}

func TestApp_AgentTreeMsg_SpawnFiresToastAndFlash(t *testing.T) {
	app := hudTestApp(t)
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{
		{Name: "tower", Type: "manager"},
		{Name: "finn", Type: "engineer"},
	}})
	app = updated.(AppModel)
	if !containsSubstr(toastTexts(app), "spawned: finn") {
		t.Errorf("expected a 'spawned: finn' toast, got %v", toastTexts(app))
	}
	if app.treeHud.changes["finn"] != hudChangeSpawned {
		t.Errorf("expected finn flagged spawned in HUD, got %v", app.treeHud.changes["finn"])
	}
	if !app.treeHud.visible {
		t.Error("spawn should make the HUD visible")
	}
}

func TestApp_AgentTreeMsg_RetireFiresToastAndFlash(t *testing.T) {
	app := hudTestApp(t)
	// prev childNodes already has tower; remove it.
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{}})
	app = updated.(AppModel)
	if !containsSubstr(toastTexts(app), "retired: tower") {
		t.Errorf("expected a 'retired: tower' toast, got %v", toastTexts(app))
	}
	if app.treeHud.changes["tower"] != hudChangeRetired {
		t.Errorf("expected tower flagged retired in HUD, got %v", app.treeHud.changes["tower"])
	}
	// The retired node is gone from m.tree.nodes (rebuildTree dropped it), so
	// the HUD must render it as a struck-through ghost row in the live View —
	// not just record the change-map entry.
	view := app.View().Content
	if !strings.Contains(stripANSI(view), "tower") {
		t.Errorf("retired agent should still appear (ghost row) in the rendered HUD")
	}
	if !strikeRe.MatchString(view) {
		t.Errorf("retired ghost row should render struck-through in the live View")
	}
}

func TestApp_AgentTreeMsg_FirstSeedDoesNotFlash(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)
	// treeSeeded is false on a fresh app.
	updated, _ = app.Update(AgentTreeMsg{Nodes: []TreeNode{{Name: "tower", Type: "manager"}}})
	app = updated.(AppModel)
	if len(toastTexts(app)) != 0 {
		t.Errorf("first AgentTreeMsg should not flash spawn toasts, got %v", toastTexts(app))
	}
	if app.treeHud.visible {
		t.Error("first AgentTreeMsg should not show the HUD")
	}
	if !app.treeSeeded {
		t.Error("treeSeeded should be true after the first AgentTreeMsg")
	}
}

func TestApp_AgentTreeMsg_ShowTreeSuppressesHUDButToastStillFires(t *testing.T) {
	app := hudTestApp(t)
	app.showTree = true
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{
		{Name: "tower", Type: "manager"},
		{Name: "finn", Type: "engineer"},
	}})
	app = updated.(AppModel)
	if app.treeHud.visible {
		t.Error("HUD must be suppressed while the /tree modal is open")
	}
	if !containsSubstr(toastTexts(app), "spawned: finn") {
		t.Errorf("toast should still fire while /tree is open, got %v", toastTexts(app))
	}
}

func TestApp_AgentTreeMsg_SpawnAndRetireTogether(t *testing.T) {
	app := hudTestApp(t)
	// prev = {tower}; next = {finn} → tower retired, finn spawned.
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{
		{Name: "finn", Type: "engineer"},
	}})
	app = updated.(AppModel)
	tt := toastTexts(app)
	if !containsSubstr(tt, "spawned: finn") || !containsSubstr(tt, "retired: tower") {
		t.Errorf("expected both spawn+retire toasts, got %v", tt)
	}
	if app.treeHud.changes["finn"] != hudChangeSpawned {
		t.Errorf("finn should be flagged spawned, got %v", app.treeHud.changes["finn"])
	}
	if app.treeHud.changes["tower"] != hudChangeRetired {
		t.Errorf("tower should be flagged retired, got %v", app.treeHud.changes["tower"])
	}
}

// The HUD is suppressed ONLY by m.showTree, NOT by other modals (it is not in
// anyModalUp()). A spawn while a non-tree modal is open must still show it.
func TestApp_AgentTreeMsg_OtherModalDoesNotSuppressHUD(t *testing.T) {
	app := hudTestApp(t)
	app.showHelp = true
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{
		{Name: "tower", Type: "manager"},
		{Name: "finn", Type: "engineer"},
	}})
	app = updated.(AppModel)
	if !app.treeHud.visible {
		t.Error("HUD should still flash on spawn while a non-tree modal (help) is open")
	}
}

// QUM-769 guard: toggling HUD visibility must NOT invalidate the chat panel or
// composed-string render cache — the HUD is an overlay applied after the cache.
func TestApp_HUDOverlayDoesNotInvalidateChatCache(t *testing.T) {
	app := hudTestApp(t)
	_ = app.View()
	vpBefore := app.cache.viewport
	composedKeyBefore := app.cache.composedKey

	app.treeHud.visible = true
	app.treeHud.changes = map[string]hudChangeKind{"tower": hudChangeSpawned}
	_ = app.View()

	if app.cache.viewport != vpBefore {
		t.Error("chat/viewport panel cache changed when HUD visibility toggled (QUM-769 regression)")
	}
	if app.cache.composedKey != composedKeyBefore {
		t.Error("composed render cache key changed when HUD visibility toggled (QUM-769 regression)")
	}
}
