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
	if !app.input.disabled {
		t.Error("input should be disabled after InjectPromptMsg")
	}
	if cmd == nil {
		t.Fatal("InjectPromptMsg must return a cmd that calls bridge.SendMessage")
	}
	// Viewport must NOT contain the template as a user message (would blow up viewport for 2KB templates).
	for _, m := range app.viewport.GetMessages() {
		if m.Type == MessageUser && m.Content == template {
			t.Error("InjectPromptMsg must not AppendUserMessage with the template content")
		}
	}
	// Viewport should have a status line indicating dispatch.
	foundStatus := false
	for _, m := range app.viewport.GetMessages() {
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
