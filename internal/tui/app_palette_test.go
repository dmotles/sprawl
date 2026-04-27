package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func readyApp(t *testing.T) AppModel {
	t.Helper()
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(AppModel)
}

func readyAppWithBridge(t *testing.T, b *Bridge) AppModel {
	t.Helper()
	m := newTestAppModelWithBridge(t, b)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(AppModel)
}

func TestAppModel_OpenPaletteMsg_OpensWhenIdle(t *testing.T) {
	app := readyApp(t)
	updated, _ := app.Update(OpenPaletteMsg{})
	app = updated.(AppModel)
	if !app.showPalette {
		t.Error("OpenPaletteMsg should set showPalette=true when idle")
	}
	if !app.palette.Visible() {
		t.Error("palette should be visible after OpenPaletteMsg")
	}
}

func TestAppModel_OpenPaletteMsg_GatedWhenInputDisabled(t *testing.T) {
	app := readyApp(t)
	app.input.SetDisabled(true)
	updated, _ := app.Update(OpenPaletteMsg{})
	app = updated.(AppModel)
	if app.showPalette {
		t.Error("OpenPaletteMsg must be no-op when input disabled")
	}
}

func TestAppModel_OpenPaletteMsg_GatedWhenConfirmActive(t *testing.T) {
	app := readyApp(t)
	// Trigger confirm via Ctrl-C.
	u, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = u.(AppModel)
	u, _ = app.Update(OpenPaletteMsg{})
	app = u.(AppModel)
	if app.showPalette {
		t.Error("OpenPaletteMsg must be no-op when showConfirm")
	}
}

func TestAppModel_OpenPaletteMsg_GatedWhenHelpActive(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = u.(AppModel)
	if !app.showHelp {
		t.Fatal("setup: F1 should open help")
	}
	u, _ = app.Update(OpenPaletteMsg{})
	app = u.(AppModel)
	if app.showPalette {
		t.Error("OpenPaletteMsg must be no-op when showHelp")
	}
}

func TestAppModel_ClosePaletteMsg_Closes(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(OpenPaletteMsg{})
	app = u.(AppModel)
	u, _ = app.Update(ClosePaletteMsg{})
	app = u.(AppModel)
	if app.showPalette {
		t.Error("ClosePaletteMsg should clear showPalette")
	}
	if app.palette.Visible() {
		t.Error("palette should be hidden after ClosePaletteMsg")
	}
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
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)
	app := readyAppWithBridge(t, b)

	template := "INJECTED PROMPT"
	updated, cmd := app.Update(InjectPromptMsg{Template: template})
	app = updated.(AppModel)

	if app.turnState != TurnThinking {
		t.Errorf("turnState after InjectPromptMsg = %v, want TurnThinking", app.turnState)
	}
	// QUM-340: input is no longer disabled by turn state — the user can keep
	// typing while the injected /handoff prompt is in flight, and a fresh
	// Enter would queue into pendingSubmit. Verify the bar is unaffected.
	if app.input.disabled {
		t.Error("input must not be disabled by turn-state after QUM-340")
	}
	if cmd == nil {
		t.Fatal("InjectPromptMsg must return a cmd that calls bridge.SendMessage")
	}
	// Viewport must NOT contain the template as a user message (would blow up viewport for 2KB templates).
	for _, m := range app.viewportFor("weave").GetMessages() {
		if m.Type == MessageUser && m.Content == template {
			t.Error("InjectPromptMsg must not AppendUserMessage with the template content")
		}
	}
	// Viewport should have a status line indicating dispatch.
	foundStatus := false
	for _, m := range app.viewportFor("weave").GetMessages() {
		if m.Type == MessageStatus && (strings.Contains(m.Content, "/handoff") || strings.Contains(m.Content, "dispatched")) {
			foundStatus = true
			break
		}
	}
	if !foundStatus {
		t.Error("InjectPromptMsg should append a status line (containing '/handoff' or 'dispatched') to the viewport")
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
	ms := newMockSession()
	ctx := context.Background()
	b := NewBridge(ctx, ms)
	app := readyAppWithBridge(t, b)
	app.setTurnState(TurnThinking)

	_, cmd := app.Update(InjectPromptMsg{Template: "x"})
	if cmd != nil {
		t.Error("InjectPromptMsg must be no-op when turnState != TurnIdle")
	}
}

func TestAppModel_SessionRestartingMsg_ForceClosesPalette(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(OpenPaletteMsg{})
	app = u.(AppModel)
	if !app.showPalette {
		t.Fatal("setup: palette should be open")
	}
	u, _ = app.Update(SessionRestartingMsg{Reason: "handoff"})
	app = u.(AppModel)
	if app.showPalette {
		t.Error("SessionRestartingMsg must force-close the palette")
	}
}

func TestAppModel_KeysRouteToPaletteWhenVisible(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(OpenPaletteMsg{})
	app = u.(AppModel)

	// Typing 'h' should go to palette filter, not input or panel cycling.
	u, _ = app.Update(tea.KeyPressMsg{Code: 'h'})
	app = u.(AppModel)
	if app.palette.filter != "h" {
		t.Errorf("palette filter = %q, want %q — keys should route to palette while visible",
			app.palette.filter, "h")
	}
}

// --- QUM-279: Agent switching via keybindings + /switch ---

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
	if cmd != nil {
		if msg := cmd(); msg != nil {
			u2, _ := app.Update(msg)
			app = u2.(AppModel)
		}
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
	app.activePanel = PanelInput
	app.updateFocus()
	app = pressKeyAndApply(t, app, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if app.observedAgent != "finn" {
		t.Errorf("Ctrl+N from input panel: observedAgent = %q, want finn", app.observedAgent)
	}
}

func TestAppModel_CtrlN_IgnoredWhenPaletteOpen(t *testing.T) {
	app := appWithAgents(t)
	u, _ := app.Update(OpenPaletteMsg{})
	app = u.(AppModel)
	u, _ = app.Update(tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	app = u.(AppModel)
	if app.observedAgent != "weave" {
		t.Errorf("Ctrl+N with palette open: observedAgent = %q, want weave (ignored)", app.observedAgent)
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

func TestAppModel_OpenPaletteMsg_PopulatesAgentsList(t *testing.T) {
	app := appWithAgents(t)
	u, _ := app.Update(OpenPaletteMsg{})
	app = u.(AppModel)
	if !app.showPalette {
		t.Fatal("setup: palette should be open")
	}
	// Palette must know about the current agents so /switch can filter them.
	got := app.palette.agents
	want := []string{"weave", "finn", "ghost"}
	if len(got) != len(want) {
		t.Fatalf("palette.agents = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("palette.agents[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestAppModel_TabDoesNotCyclePanelWhenPaletteVisible(t *testing.T) {
	app := readyApp(t)
	u, _ := app.Update(OpenPaletteMsg{})
	app = u.(AppModel)

	initialPanel := app.activePanel
	u, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = u.(AppModel)
	if app.activePanel != initialPanel {
		t.Errorf("Tab should NOT cycle panels when palette visible; activePanel changed from %d to %d",
			initialPanel, app.activePanel)
	}
}
