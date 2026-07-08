package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// NOTE (QUM-864): the full-screen command palette was deleted and replaced by
// the inline popover (see app_cmdpopover_test.go). The palette-specific tests
// that lived here (OpenPaletteMsg gating, key/Tab routing to the palette,
// agent-list population) were removed with it. The command-dispatch messages
// they exercised (PaletteQuitMsg, ToggleHelpMsg, InjectPromptMsg, ShowUsageMsg)
// survive and are still covered below; the shared readyApp helpers also live
// here.

func readyApp(t *testing.T) AppModel {
	t.Helper()
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(AppModel)
}

func readyAppWithBridge(t *testing.T, b SessionBackend) AppModel {
	t.Helper()
	m := newTestAppModelWithBridge(t, b)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(AppModel)
}

func TestAppModel_PaletteQuitMsg_SetsQuittingAndReturnsQuit(t *testing.T) {
	app := readyApp(t)
	updated, cmd := app.Update(PaletteQuitMsg{})
	app = updated.(AppModel)
	if !app.quitting {
		t.Error("PaletteQuitMsg should set quitting=true")
	}
	if cmd == nil {
		t.Fatal("PaletteQuitMsg should return a quit cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd = %T, want tea.QuitMsg", cmd())
	}
}

func TestAppModel_ToggleHelpMsg_TogglesShowHelp(t *testing.T) {
	app := readyApp(t)
	if app.showHelp {
		t.Fatal("setup: help should start hidden")
	}
	u, _ := app.Update(ToggleHelpMsg{})
	app = u.(AppModel)
	if !app.showHelp {
		t.Error("ToggleHelpMsg should show help")
	}
	u, _ = app.Update(ToggleHelpMsg{})
	app = u.(AppModel)
	if app.showHelp {
		t.Error("ToggleHelpMsg should toggle back to hidden")
	}
}

func TestAppModel_InjectPromptMsg_SendsToBridgeWithoutAppendingUserMessage(t *testing.T) {
	ms := newFakeSessionBackend()
	b := ms
	app := readyAppWithBridge(t, b)

	template := "INJECTED PROMPT"
	updated, cmd := app.Update(InjectPromptMsg{Template: template})
	app = updated.(AppModel)

	if app.turnState != TurnThinking {
		t.Errorf("turnState after InjectPromptMsg = %v, want TurnThinking", app.turnState)
	}
	// QUM-340/828: input is no longer disabled by turn state — the user can keep
	// typing while the injected /handoff prompt is in flight, and a fresh Enter
	// writes straight to the CLI stdin queue. Verify the bar is unaffected.
	if app.input.disabled {
		t.Error("input must not be disabled by turn-state after QUM-340")
	}
	if cmd == nil {
		t.Fatal("InjectPromptMsg must return a cmd that calls bridge.SendMessage")
	}
	// Viewport must NOT contain the template as a user message (would blow up viewport for 2KB templates).
	for _, it := range app.viewportFor("weave").ChatList().Items() {
		if u, ok := it.(*UserItem); ok && u.Text() == template {
			t.Error("InjectPromptMsg must not AppendUserMessage with the template content")
		}
	}
	// QUM-675 S5: dispatch status now lives on the statusbar transient label.
	bar := stripAnsi(app.statusBar.View())
	if !strings.Contains(bar, "/handoff") && !strings.Contains(bar, "dispatched") {
		t.Errorf("InjectPromptMsg should set a transient status label (containing '/handoff' or 'dispatched'); got: %s", bar)
	}

	// Execute the cmd — should resolve to UserMessageSentMsg via the bridge.
	msg := cmd()
	if _, ok := msg.(UserMessageSentMsg); !ok {
		t.Errorf("cmd returned %T, want UserMessageSentMsg (bridge.SendMessage path)", msg)
	}
}

func TestAppModel_InjectPromptMsg_NoopWhenBridgeNil(t *testing.T) {
	app := readyApp(t) // no bridge
	updated, cmd := app.Update(InjectPromptMsg{Template: "x"})
	app = updated.(AppModel)
	if cmd != nil {
		t.Error("InjectPromptMsg with nil bridge should return nil cmd")
	}
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle (no-op)", app.turnState)
	}
}

func TestAppModel_InjectPromptMsg_NoopWhenTurnBusy(t *testing.T) {
	ms := newFakeSessionBackend()
	b := ms
	app := readyAppWithBridge(t, b)
	app.setTurnState(TurnThinking)

	_, cmd := app.Update(InjectPromptMsg{Template: "x"})
	if cmd != nil {
		t.Error("InjectPromptMsg must be no-op when turnState != TurnIdle")
	}
}

// --- QUM-279: Agent switching via keybindings ---

func appWithAgents(t *testing.T) AppModel {
	t.Helper()
	app := readyApp(t)
	// Feed an agent tree: weave (root, injected by rebuildTree) + finn + ghost.
	app.childNodes = []TreeNode{
		{Name: "finn", Type: "engineer", Status: "active", Depth: 0},
		{Name: "ghost", Type: "researcher", Status: "active", Depth: 0},
	}
	app.rebuildTree()
	return app
}

// pressKeyAndApply sends a KeyPressMsg, executes any returned cmd, and feeds
// the resulting msg back into the app. Useful for keys whose effect is
// expressed via a command (e.g. Ctrl+N emits AgentSelectedMsg via cmd).
func pressKeyAndApply(t *testing.T, app AppModel, key tea.KeyPressMsg) AppModel {
	t.Helper()
	u, cmd := app.Update(key)
	app = u.(AppModel)
	// Unwrap tea.BatchMsg (QUM-805: Ctrl+N/P now batches the cycle cmd with the
	// HUD fade tick) so the AgentSelectedMsg still reaches Update.
	for _, msg := range collectBatchMsgs(t, cmd) {
		u2, _ := app.Update(msg)
		app = u2.(AppModel)
	}
	return app
}

func TestAppModel_CtrlNCyclesObservedAgentForward(t *testing.T) {
	app := appWithAgents(t)
	if app.observedAgent != "weave" {
		t.Fatalf("setup: observedAgent = %q, want weave", app.observedAgent)
	}
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if app.observedAgent != "finn" {
		t.Errorf("Ctrl+N: observedAgent = %q, want finn", app.observedAgent)
	}
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if app.observedAgent != "ghost" {
		t.Errorf("Ctrl+N x2: observedAgent = %q, want ghost", app.observedAgent)
	}
	// Wrap-around.
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if app.observedAgent != "weave" {
		t.Errorf("Ctrl+N x3 (wrap): observedAgent = %q, want weave", app.observedAgent)
	}
}

func TestAppModel_CtrlPCyclesObservedAgentBackward(t *testing.T) {
	app := appWithAgents(t)
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if app.observedAgent != "ghost" {
		t.Errorf("Ctrl+P from weave: observedAgent = %q, want ghost (wraps)", app.observedAgent)
	}
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if app.observedAgent != "finn" {
		t.Errorf("Ctrl+P x2: observedAgent = %q, want finn", app.observedAgent)
	}
}

func TestAppModel_CtrlN_FiresGloballyFromInputPanel(t *testing.T) {
	app := appWithAgents(t)
	app.updateFocus()
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if app.observedAgent != "finn" {
		t.Errorf("Ctrl+N from input panel: observedAgent = %q, want finn", app.observedAgent)
	}
}

func TestAppModel_CtrlN_NoopWithOnlyOneAgent(t *testing.T) {
	app := readyApp(t) // only weave synthesized
	u, _ := app.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	app = u.(AppModel)
	if app.observedAgent != "weave" {
		t.Errorf("Ctrl+N with only weave: observedAgent = %q, want weave", app.observedAgent)
	}
}

func TestAppModel_SwitchAgentMsgSwitchesViaAgentSelected(t *testing.T) {
	app := appWithAgents(t)
	// Reuse the existing AgentSelectedMsg path.
	u, _ := app.Update(AgentSelectedMsg{Name: "finn"})
	app = u.(AppModel)
	if app.observedAgent != "finn" {
		t.Errorf("observedAgent after AgentSelectedMsg{finn} = %q, want finn", app.observedAgent)
	}
}

// --- QUM-721: /usage modal wiring at the AppModel level ---

func TestAppModel_ShowUsageMsg_OpensModal(t *testing.T) {
	app := readyApp(t)
	updated, _ := app.Update(ShowUsageMsg{})
	app = updated.(AppModel)
	if !app.showUsage {
		t.Error("ShowUsageMsg should set showUsage=true")
	}
	if !app.usageModal.Visible() {
		t.Error("usage modal should be visible after ShowUsageMsg")
	}
}

func TestAppModel_DismissUsageMsg_ClosesModal(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(ShowUsageMsg{})
	app = u.(AppModel)
	u, _ = app.Update(DismissUsageMsg{})
	app = u.(AppModel)
	if app.showUsage {
		t.Error("DismissUsageMsg should clear showUsage")
	}
	if app.usageModal.Visible() {
		t.Error("usage modal should be hidden after DismissUsageMsg")
	}
}

func TestAppModel_KeysRouteToUsageModalWhenVisible(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(ShowUsageMsg{})
	app = u.(AppModel)
	priorInput := app.input.Value()
	u, _ = app.Update(tea.KeyPressMsg{Code: 'x'})
	app = u.(AppModel)
	if app.input.Value() != priorInput {
		t.Errorf("key 'x' while usage modal visible should be consumed by modal; input changed from %q to %q",
			priorInput, app.input.Value())
	}
}

func TestAppModel_QInUsageModalDismisses(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(ShowUsageMsg{})
	app = u.(AppModel)
	// Pressing 'q' (no modifier) should route to modal and dismiss it.
	u, cmd := app.Update(tea.KeyPressMsg{Code: 'q'})
	app = u.(AppModel)
	if cmd != nil {
		if msg := cmd(); msg != nil {
			u2, _ := app.Update(msg)
			app = u2.(AppModel)
		}
	}
	if app.showUsage {
		t.Error("q while usage modal visible should dismiss it")
	}
}

func TestAppModel_ShowUsageMsg_GatedWhenAnotherModalUp(t *testing.T) {
	app := readyApp(t)
	// Open help first.
	u, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = u.(AppModel)
	if !app.showHelp {
		t.Fatal("setup: F1 should open help")
	}
	u, _ = app.Update(ShowUsageMsg{})
	app = u.(AppModel)
	if app.showUsage {
		t.Error("ShowUsageMsg must be no-op when another modal (help) is already up")
	}
}
