package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-733 5b/5c: TreeModalModel is a centered modal that renders the full
// vertical agent tree with cursor navigation (↑/↓) and Enter-to-select
// dispatch. Esc dismisses. The modal is opened via the `/tree` palette
// command (ToggleTreeMsg).

func newTestTreeModal(t *testing.T) TreeModalModel {
	t.Helper()
	theme := NewTheme("colour212")
	m := NewTreeModalModel(&theme)
	m.SetSize(120, 40)
	return m
}

func TestTreeModal_InitiallyHidden(t *testing.T) {
	m := newTestTreeModal(t)
	if m.Visible() {
		t.Error("new TreeModal should be hidden")
	}
	if m.View() != "" {
		t.Error("hidden TreeModal.View() should be empty")
	}
}

func TestTreeModal_ShowAndHide(t *testing.T) {
	m := newTestTreeModal(t)
	m.Show()
	if !m.Visible() {
		t.Error("Show() should set visible")
	}
	m.Hide()
	if m.Visible() {
		t.Error("Hide() should clear visible")
	}
}

func TestTreeModal_View_ContainsAgentRows(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0, Status: "active"},
		{Name: "finn", Type: "engineer", Depth: 1, Status: "working"},
		{Name: "ghost", Type: "researcher", Depth: 1, Status: "idle"},
	}, "weave")
	m.Show()
	out := stripAnsi(m.View())
	for _, want := range []string{"weave", "finn", "ghost"} {
		if !strings.Contains(out, want) {
			t.Errorf("modal view should contain %q, got:\n%s", want, out)
		}
	}
}

func TestTreeModal_View_RendersTypeFamilyChip(t *testing.T) {
	// 5c: family/type chip after the name. Mapping: engineer→eng,
	// researcher→res, manager→mgr, weave→omitted.
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
		{Name: "ghost", Type: "researcher", Depth: 1},
		{Name: "tower", Type: "manager", Depth: 1},
	}, "weave")
	m.Show()
	out := stripAnsi(m.View())
	if !strings.Contains(out, "[eng]") {
		t.Errorf("engineer row should show [eng] chip; got:\n%s", out)
	}
	if !strings.Contains(out, "[res]") {
		t.Errorf("researcher row should show [res] chip; got:\n%s", out)
	}
	if !strings.Contains(out, "[mgr]") {
		t.Errorf("manager row should show [mgr] chip; got:\n%s", out)
	}
	// Weave row must NOT carry a type chip — it's redundant at root.
	weaveIdx := strings.Index(out, "weave")
	if weaveIdx < 0 {
		t.Fatal("weave row missing")
	}
	weaveLine := out[weaveIdx:]
	if nl := strings.IndexByte(weaveLine, '\n'); nl > 0 {
		weaveLine = weaveLine[:nl]
	}
	for _, chip := range []string{"[eng]", "[mgr]", "[res]"} {
		if strings.Contains(weaveLine, chip) {
			t.Errorf("weave line must not contain chip %q; got: %q", chip, weaveLine)
		}
	}
}

func TestTreeModal_View_CostTagOmittedWhenZero(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, TotalCostUsd: 0},
	}, "weave")
	m.Show()
	out := stripAnsi(m.View())
	if strings.Contains(out, "[$") {
		t.Errorf("zero-cost agent should not render a cost tag; got:\n%s", out)
	}
}

func TestTreeModal_View_CostTagWhenNonZero(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1, TotalCostUsd: 0.0042},
	}, "weave")
	m.Show()
	out := stripAnsi(m.View())
	if !strings.Contains(out, "[$0.0042]") {
		t.Errorf("non-zero cost should render '[$0.0042]'; got:\n%s", out)
	}
}

func TestTreeModal_View_SelectedObservedDistinctFromCursor(t *testing.T) {
	// 5c: selected (observed) ≠ cursor. Selected gets a `>` left marker;
	// cursor gets the AccentText `›` highlight. Both visible at once when
	// they differ.
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
		{Name: "ghost", Type: "researcher", Depth: 1},
	}, "weave") // observed = weave
	m.Show()
	// Move cursor to "ghost" (idx 2).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	out := stripAnsi(m.View())
	lines := strings.Split(out, "\n")

	findLine := func(name string) string {
		for _, l := range lines {
			if strings.Contains(l, name) {
				return l
			}
		}
		return ""
	}
	weaveLine := findLine("weave")
	ghostLine := findLine("ghost")
	if weaveLine == "" || ghostLine == "" {
		t.Fatalf("missing rows. lines:\n%s", out)
	}
	// Observed (weave) carries a `>` left marker.
	if !strings.Contains(weaveLine, ">") {
		t.Errorf("observed row 'weave' should carry '>' marker; got: %q", weaveLine)
	}
	// Cursor (ghost) carries the `›` accent marker, distinct from `>`.
	if !strings.Contains(ghostLine, "›") {
		t.Errorf("cursor row 'ghost' should carry '›' marker; got: %q", ghostLine)
	}
}

func TestTreeModal_UpDownMovesCursor(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
		{Name: "ghost", Type: "researcher", Depth: 1},
	}, "weave")
	m.Show()
	if got := m.Cursor(); got != 0 {
		t.Errorf("initial cursor = %d, want 0", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.Cursor(); got != 1 {
		t.Errorf("after Down: cursor = %d, want 1", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.Cursor(); got != 2 {
		t.Errorf("after 2nd Down: cursor = %d, want 2", got)
	}
	// Saturates at last row.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if got := m.Cursor(); got != 2 {
		t.Errorf("Down at end should saturate at 2, got %d", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := m.Cursor(); got != 1 {
		t.Errorf("after Up: cursor = %d, want 1", got)
	}
}

func TestTreeModal_EnterEmitsAgentSelectedMsg(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}, "weave")
	m.Show()
	// Cursor onto finn.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter should return a command")
	}
	if m.Visible() {
		t.Error("Enter should dismiss the modal")
	}
	// The cmd emits AgentSelectedMsg{Name:"finn"}. QUM-733 hotfix: must NOT
	// also emit ToggleTreeMsg — the AppModel-level visibility sync handles
	// closing the modal, and a re-batched ToggleTreeMsg would re-open it.
	msg := cmd()
	got := flattenMsgs(msg)
	var sawAgent, sawToggle bool
	for _, in := range got {
		if a, ok := in.(AgentSelectedMsg); ok && a.Name == "finn" {
			sawAgent = true
		}
		if _, ok := in.(ToggleTreeMsg); ok {
			sawToggle = true
		}
	}
	if !sawAgent {
		t.Errorf("expected AgentSelectedMsg{Name:\"finn\"} in cmd output; got: %#v", got)
	}
	if sawToggle {
		t.Errorf("Enter must NOT emit ToggleTreeMsg (QUM-733 hotfix): cmd output = %#v", got)
	}
}

func TestTreeModal_EscDismisses(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
	}, "weave")
	m.Show()
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Visible() {
		t.Error("Esc should dismiss modal")
	}
	if cmd != nil {
		// Either nil or a ToggleTreeMsg-emitting cmd is acceptable; the
		// visibility flip is the load-bearing contract.
		_ = cmd()
	}
}

func TestTreeModal_OtherKeysSwallowed(t *testing.T) {
	m := newTestTreeModal(t)
	m.SetNodes([]TreeNode{
		{Name: "weave", Type: "weave", Depth: 0},
		{Name: "finn", Type: "engineer", Depth: 1},
	}, "weave")
	m.Show()
	before := m.Cursor()
	m, cmd := m.Update(tea.KeyPressMsg{Code: 'x'})
	if cmd != nil {
		// Swallow → no cmd. Tolerant: nil expected.
		t.Errorf("non-nav key 'x' should be swallowed (nil cmd), got %v", cmd)
	}
	if got := m.Cursor(); got != before {
		t.Errorf("non-nav key 'x' must not move cursor: before=%d after=%d", before, got)
	}
	if !m.Visible() {
		t.Error("non-nav key must not dismiss modal")
	}
}

// flattenMsgs unwraps tea.BatchMsg into a flat slice for assertion. Plain
// messages are returned as a single-element slice.
func flattenMsgs(msg tea.Msg) []tea.Msg {
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			if c == nil {
				continue
			}
			out = append(out, flattenMsgs(c())...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// --- App-level integration tests ---

func TestAppModel_ToggleTreeMsg_OpensAndClosesModal(t *testing.T) {
	app := readyApp(t)
	if app.showTree {
		t.Fatal("setup: tree modal should start hidden")
	}
	u, _ := app.Update(ToggleTreeMsg{})
	app = u.(AppModel)
	if !app.showTree {
		t.Error("ToggleTreeMsg should open the tree modal")
	}
	u, _ = app.Update(ToggleTreeMsg{})
	app = u.(AppModel)
	if app.showTree {
		t.Error("Second ToggleTreeMsg should close the tree modal")
	}
}

func TestAppModel_ToggleTreeMsg_NoopWhileHigherPriorityModalUp(t *testing.T) {
	app := readyApp(t)
	// Force help open (higher priority).
	app.showHelp = true
	u, _ := app.Update(ToggleTreeMsg{})
	app = u.(AppModel)
	if app.showTree {
		t.Error("ToggleTreeMsg must be a no-op when a higher-priority modal is up")
	}
}

func TestAppModel_anyModalUp_IncludesShowTree(t *testing.T) {
	app := readyApp(t)
	if app.anyModalUp() {
		t.Fatal("setup: no modals up")
	}
	app.showTree = true
	if !app.anyModalUp() {
		t.Error("anyModalUp() must include showTree")
	}
}

func TestAppModel_TreeModal_EnterDispatchesAgentSelected(t *testing.T) {
	app := readyApp(t)
	// Seed tree with a child agent so cursor has somewhere to move.
	app.childNodes = []TreeNode{
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	app.rebuildTree()
	u, _ := app.Update(ToggleTreeMsg{})
	app = u.(AppModel)
	if !app.showTree {
		t.Fatal("tree modal should be open")
	}
	// Move cursor down to finn.
	u, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = u.(AppModel)
	// Press Enter.
	u, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = u.(AppModel)
	if app.showTree {
		t.Error("Enter must dismiss the tree modal")
	}
	if cmd == nil {
		t.Fatal("Enter should return a command")
	}
	msgs := flattenMsgs(cmd())
	sawSel := false
	for _, m := range msgs {
		if a, ok := m.(AgentSelectedMsg); ok && a.Name == "finn" {
			sawSel = true
		}
	}
	if !sawSel {
		t.Errorf("expected AgentSelectedMsg{Name:\"finn\"}; got %#v", msgs)
	}
}

func TestAppModel_TreeModal_EscDismissesWithoutSelection(t *testing.T) {
	app := readyApp(t)
	app.childNodes = []TreeNode{
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	app.rebuildTree()
	u, _ := app.Update(ToggleTreeMsg{})
	app = u.(AppModel)
	prevObserved := app.observedAgent
	u, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = u.(AppModel)
	if app.showTree {
		t.Error("Esc must dismiss the tree modal")
	}
	if app.observedAgent != prevObserved {
		t.Errorf("Esc must not change observedAgent: was %q now %q", prevObserved, app.observedAgent)
	}
}

// QUM-733 5b: Ctrl+T must NOT open the tree modal — that binding stays on
// toast dismiss-all (QUM-649).
func TestAppModel_CtrlT_DoesNotOpenTreeModal(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	app = u.(AppModel)
	if app.showTree {
		t.Error("Ctrl+T must not open the tree modal (reserved for toast dismiss-all)")
	}
}

// QUM-733 hotfix regression: pressing Enter in the tree modal must close the
// modal and KEEP it closed after the returned cmd's batched messages are fed
// back through AppModel.Update. The prior bug batched a ToggleTreeMsg into
// the Enter cmd; after AppModel synced showTree=false from Visible(), the
// trailing ToggleTreeMsg re-opened the modal on the same observed agent.
func TestAppModel_TreeModal_EnterDoesNotReopenAfterCmdReplay(t *testing.T) {
	app := readyApp(t)
	app.childNodes = []TreeNode{
		{Name: "finn", Type: "engineer", Depth: 1},
	}
	app.rebuildTree()
	// Open the modal.
	u, _ := app.Update(ToggleTreeMsg{})
	app = u.(AppModel)
	if !app.showTree {
		t.Fatal("setup: tree modal should be open")
	}
	// Move cursor down to finn.
	u, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = u.(AppModel)
	// Press Enter.
	u, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	app = u.(AppModel)
	if app.showTree {
		t.Fatal("Enter must close the tree modal immediately")
	}
	if cmd == nil {
		t.Fatal("Enter should return a command")
	}
	// Re-feed every batched message back through AppModel.Update — this
	// simulates the bubbletea runtime delivering the batched cmds. After
	// replay, showTree must remain false (no spurious re-toggle).
	for _, msg := range flattenMsgs(cmd()) {
		u, _ = app.Update(msg)
		app = u.(AppModel)
	}
	if app.showTree {
		t.Error("tree modal re-opened after Enter cmd replay (QUM-733 regression)")
	}
}
