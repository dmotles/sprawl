package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/supervisor"
)

func newTestAppModel(t *testing.T) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
}

func newTestAppModelWithBridge(t *testing.T, bridge *Bridge) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", nil)
}

func TestAppModel_InitReturnsNil(t *testing.T) {
	m := newTestAppModel(t)
	cmd := m.Init()
	if cmd != nil {
		t.Errorf("Init() = %v, want nil", cmd)
	}
}

func TestAppModel_NotReadyBeforeResize(t *testing.T) {
	m := newTestAppModel(t)
	if m.ready {
		t.Error("ready should be false before receiving WindowSizeMsg")
	}
}

func TestAppModel_WindowSizeSetsReady(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.WindowSizeMsg{Width: 80, Height: 24}
	updated, _ := m.Update(msg)
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}
	if !app.ready {
		t.Error("ready should be true after WindowSizeMsg")
	}
}

func TestAppModel_ViewBeforeReady(t *testing.T) {
	m := newTestAppModel(t)
	v := m.View()
	if !strings.Contains(v.Content, "Initializing") {
		t.Errorf("View().Content before ready should contain 'Initializing', got:\n%s", v.Content)
	}
}

func TestAppModel_ViewAfterReady(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.WindowSizeMsg{Width: 80, Height: 24}
	updated, _ := m.Update(msg)
	app := updated.(AppModel)
	v := app.View()
	if strings.TrimSpace(v.Content) == "" {
		t.Error("View().Content after ready should not be empty")
	}
	if strings.Contains(v.Content, "Initializing") {
		t.Error("View().Content after ready should not contain 'Initializing'")
	}
}

// QUM-324 follow-up: at narrow widths the tree panel must not soft-wrap its
// rows past its own border's Height, which would push the input box off the
// bottom of the screen. Render the full app View at 40x15 with a nontrivial
// tree population and assert the bottom of the composed content contains the
// input panel's bottom border glyph (╰).
func TestAppModel_ViewKeepsInputBoxAtNarrowWidths(t *testing.T) {
	m := newTestAppModel(t)
	// Seed the tree with enough nodes whose names, combined with the
	// "  dot icon name (status) (unread)" formatting, exceed the tree-panel
	// inner width so the pre-fix code soft-wraps each row into two physical
	// lines — enough wrapped rows to push past the tree panel's declared
	// Height, which drags the input box off the bottom of the screen.
	var nodes []TreeNode
	for i := 0; i < 6; i++ {
		nodes = append(nodes, TreeNode{
			Name: fmt.Sprintf("agent-with-a-longish-name-%d", i), Type: "engineer",
			Status: "active", Unread: 1,
		})
	}
	m.childNodes = nodes
	m.rebuildTree()
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 15})
	app := resized.(AppModel)
	v := app.View()
	lines := strings.Split(v.Content, "\n")
	// Total rendered line count must not exceed the terminal height. If the
	// tree soft-wraps its rows, lipgloss does NOT enforce the panel's
	// Height() — it lets the panel grow taller — so the composed output
	// overflows past the bottom of the terminal and the input box + status
	// bar are clipped off-screen (QUM-324 residual, bug 2 from dmotles
	// 2026-04-22 pane capture).
	const termHeight = 15
	if got := len(lines); got > termHeight {
		t.Errorf("rendered view is %d lines tall, want <= terminal height %d — the tree panel grew past its declared Height and pushed the input box off-screen", got, termHeight)
	}
	// And the input box's bottom border must still be present.
	if !strings.Contains(v.Content, "╰") {
		t.Errorf("View content missing bottom-border glyphs — panel layout collapsed:\n%s", v.Content)
	}
}

func TestAppModel_TabCyclesPanel(t *testing.T) {
	m := newTestAppModel(t)
	// Ensure ready state so panels are active.
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	initial := app.activePanel
	tabMsg := tea.KeyPressMsg{Code: tea.KeyTab}
	updated, _ := app.Update(tabMsg)
	app = updated.(AppModel)
	if app.activePanel == initial {
		t.Errorf("activePanel should change after Tab, got %d both times", app.activePanel)
	}
}

func TestAppModel_ShiftTabCyclesBackward(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Move forward first.
	tabMsg := tea.KeyPressMsg{Code: tea.KeyTab}
	updated, _ := app.Update(tabMsg)
	app = updated.(AppModel)
	afterTab := app.activePanel

	// Shift+Tab should go back.
	shiftTabMsg := tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	updated, _ = app.Update(shiftTabMsg)
	app = updated.(AppModel)
	if app.activePanel == afterTab {
		t.Errorf("activePanel should change after Shift+Tab, stayed at %d", app.activePanel)
	}
}

func TestAppModel_TabWrapsAround(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Press Tab 3 times (number of panels) to cycle back to start.
	panelCount := 3 // PanelTree, PanelViewport, PanelInput
	tabMsg := tea.KeyPressMsg{Code: tea.KeyTab}
	initial := app.activePanel
	for i := 0; i < panelCount; i++ {
		updated, _ := app.Update(tabMsg)
		app = updated.(AppModel)
	}
	if app.activePanel != initial {
		t.Errorf("activePanel = %d after %d Tabs, want %d (should wrap)", app.activePanel, panelCount, initial)
	}
}

func TestAppModel_CtrlCShowsConfirm(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	updated, cmd := m.Update(msg)
	app := updated.(AppModel)
	if !app.showConfirm {
		t.Error("Ctrl+C should set showConfirm to true")
	}
	if cmd != nil {
		t.Error("Ctrl+C should not return a cmd (no immediate quit)")
	}
}

func TestAppModel_ConfirmYQuitsApp(t *testing.T) {
	m := newTestAppModel(t)
	// Show confirm dialog first.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app := updated.(AppModel)

	// Confirm with y.
	updated, cmd := app.Update(ConfirmResultMsg{Confirmed: true})
	app = updated.(AppModel)
	if cmd == nil {
		t.Fatal("ConfirmResultMsg{Confirmed:true} should return a quit cmd")
	}
	result := cmd()
	if _, ok := result.(tea.QuitMsg); !ok {
		t.Errorf("cmd() = %T, want tea.QuitMsg", result)
	}
	if app.showConfirm {
		t.Error("showConfirm should be false after confirmation")
	}
}

func TestAppModel_QuestionMarkOpensHelpOnTreePanel(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.activePanel = PanelTree
	app.updateFocus()

	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	if !app.showHelp {
		t.Error("? on tree panel should toggle help")
	}
}

func TestAppModel_QuestionMarkIgnoredOnInputPanel(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.activePanel = PanelInput
	app.updateFocus()

	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	if app.showHelp {
		t.Error("? on input panel should NOT open help; it should be delegated as text")
	}
}

func TestAppModel_F1OpensHelpOnInputPanel(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.activePanel = PanelInput
	app.updateFocus()

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)
	if !app.showHelp {
		t.Error("F1 should toggle help even on input panel")
	}
}

func TestAppModel_ConfirmNDismisses(t *testing.T) {
	m := newTestAppModel(t)
	// Show confirm dialog.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app := updated.(AppModel)

	// Dismiss with n.
	updated, cmd := app.Update(ConfirmResultMsg{Confirmed: false})
	app = updated.(AppModel)
	if app.showConfirm {
		t.Error("showConfirm should be false after dismissal")
	}
	if cmd != nil {
		t.Error("dismissing confirm should not return a cmd")
	}
}

func TestAppModel_ConfirmSwallowsKeys(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Show confirm dialog.
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	initialPanel := app.activePanel

	// Tab should not change panel while confirm is visible.
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = updated.(AppModel)
	if app.activePanel != initialPanel {
		t.Errorf("Tab should be swallowed when confirm is showing, panel changed from %d to %d", initialPanel, app.activePanel)
	}
}

func TestAppModel_SignalMsgShowsConfirm(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(SignalMsg{})
	app := updated.(AppModel)
	if !app.showConfirm {
		t.Error("SignalMsg should set showConfirm to true")
	}
}

func TestAppModel_DoubleCtrlCIgnored(t *testing.T) {
	m := newTestAppModel(t)
	// First Ctrl+C.
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app := updated.(AppModel)
	if !app.showConfirm {
		t.Fatal("first Ctrl+C should show confirm")
	}

	// Second Ctrl+C should not crash or change state.
	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.showConfirm {
		t.Error("showConfirm should still be true after second Ctrl+C")
	}
	if cmd != nil {
		t.Error("second Ctrl+C should not produce a cmd")
	}
}

func TestAppModel_ViewShowsConfirmOverlay(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	view := stripANSI(app.View().Content)
	if !strings.Contains(view, "Quit") {
		t.Errorf("View should show confirm overlay with 'Quit', got:\n%s", view)
	}
}

// --- Bridge integration tests ---

func TestAppModel_InitWithBridge(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() with bridge should return a cmd, got nil")
	}
}

func TestAppModel_SubmitMsg_SendsViabridge(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)

	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, cmd := app.Update(SubmitMsg{Text: "hello claude"})
	app = updated.(AppModel)

	if cmd == nil {
		t.Error("SubmitMsg should return a cmd to send message via bridge")
	}
	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v after SubmitMsg, want TurnThinking", app.turnState)
	}
}

func TestAppModel_SubmitMsg_EmptyTextIgnored(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)

	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(SubmitMsg{Text: ""})
	if cmd != nil {
		t.Error("empty SubmitMsg should not return a cmd")
	}
}

func TestAppModel_SubmitMsg_NoBridge(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(SubmitMsg{Text: "hello"})
	if cmd != nil {
		t.Error("SubmitMsg with no bridge should not return a cmd")
	}
}

func TestAppModel_AssistantTextMsg_SetsTurnStateStreaming(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(AssistantTextMsg{Text: "some text"})
	app = updated.(AppModel)

	if app.turnState != TurnStreaming {
		t.Errorf("turnState = %v after AssistantTextMsg, want TurnStreaming", app.turnState)
	}
}

func TestAppModel_SessionResultMsg_SetsTurnStateIdle(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionResultMsg{
		Result:     "done",
		NumTurns:   1,
		DurationMs: 100,
	})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after SessionResultMsg, want TurnIdle", app.turnState)
	}
}

func TestAppModel_SessionResultMsg_WithError(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionResultMsg{
		IsError: true,
		Result:  "something went wrong",
	})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after error SessionResultMsg, want TurnIdle", app.turnState)
	}
}

func TestAppModel_SessionErrorMsg_SetsTurnStateIdle(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("connection lost")})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after SessionErrorMsg, want TurnIdle", app.turnState)
	}
}

func TestAppModel_SessionErrorMsg_ShowsDialog_WhenStreaming(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Simulate being mid-stream
	app.turnState = TurnStreaming

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("subprocess crashed")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("showError should be true when SessionErrorMsg received during streaming")
	}
}

func TestAppModel_SessionErrorMsg_NoDialog_WhenIdle(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// turnState is TurnIdle by default
	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("some error")})
	app = updated.(AppModel)

	if app.showError {
		t.Error("showError should be false when SessionErrorMsg received during idle")
	}
}

func TestAppModel_SessionErrorMsg_ShowsDialog_WhenThinking(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.turnState = TurnThinking

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("process died")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("showError should be true when SessionErrorMsg received during thinking")
	}
}

func TestAppModel_ErrorDialog_BlocksKeys(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Set up error dialog state
	app.showError = true
	app.errorDialog = NewErrorDialog(&app.theme, fmt.Errorf("crash"))
	app.errorDialog.SetSize(80, 24)

	initial := app.activePanel
	tabMsg := tea.KeyPressMsg{Code: tea.KeyTab}
	updated, _ := app.Update(tabMsg)
	app = updated.(AppModel)

	if app.activePanel != initial {
		t.Errorf("Tab should not cycle panels when error dialog is shown, panel changed from %d to %d", initial, app.activePanel)
	}
}

func TestAppModel_RestartSessionMsg_ClearsError(t *testing.T) {
	restartCalled := false
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		restartCalled = true
		newMock := newMockSession()
		return NewBridge(context.Background(), newMock), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.showError = true
	app.errorDialog = NewErrorDialog(&app.theme, fmt.Errorf("crash"))

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)

	if app.showError {
		t.Error("showError should be false after RestartSessionMsg")
	}
	app = driveAsyncRestart(t, app, cmd)
	if !restartCalled {
		t.Error("restartFunc should have been called")
	}
	if app.showError {
		t.Error("showError should still be false after successful RestartCompleteMsg")
	}
	if app.restarting {
		t.Error("restarting should be false after RestartCompleteMsg")
	}
}

func TestAppModel_RestartSessionMsg_RestartFails(t *testing.T) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", func() (*Bridge, error) {
		return nil, fmt.Errorf("failed to restart")
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	if !app.showError {
		t.Error("showError should be true when restart fails")
	}
	if app.restarting {
		t.Error("restarting should be false after RestartCompleteMsg")
	}
}

func TestAppModel_ErrorDialog_RendersOverlay(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.showError = true
	app.errorDialog = NewErrorDialog(&app.theme, fmt.Errorf("subprocess crashed"))
	app.errorDialog.SetSize(80, 24)

	v := app.View()
	content := stripANSI(v.Content)
	if !strings.Contains(content, "subprocess crashed") {
		t.Errorf("View() should show error dialog overlay with error text, got:\n%s", content)
	}
}

func TestAppModel_RestartSessionMsg_NoRestartFunc_Quits(t *testing.T) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(RestartSessionMsg{})
	if cmd == nil {
		t.Fatal("RestartSessionMsg with no restartFunc should return quit cmd")
	}
	result := cmd()
	if _, ok := result.(tea.QuitMsg); !ok {
		t.Errorf("RestartSessionMsg with no restartFunc should produce QuitMsg, got %T", result)
	}
}

func TestAppModel_SessionErrorMsg_WhenIdle_AppendsToViewport(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("some transient error")})
	app = updated.(AppModel)

	// Should not show dialog
	if app.showError {
		t.Error("showError should be false for idle error")
	}
	// Error text should be in viewport
	found := false
	for _, entry := range app.viewportFor("weave").messages {
		if entry.Type == MessageError && strings.Contains(entry.Content, "some transient error") {
			found = true
			break
		}
	}
	if !found {
		t.Error("error text should be appended to viewport when error arrives during idle")
	}
}

func TestAppModel_SessionErrorMsg_WhenStreaming_ShowsErrorDialog(t *testing.T) {
	// QUM-340: turn-state no longer drives input.disabled. The error dialog
	// is its own modal — when it's up, the App routes all keys to it, so the
	// "input is unreachable" guarantee is provided by showError, not the
	// disabled flag.
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.turnState = TurnStreaming

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("process died")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("error dialog should be shown after streaming-time SessionErrorMsg")
	}
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after error, want TurnIdle", app.turnState)
	}
}

func TestAppModel_RestartSessionMsg_RestoresIdleState(t *testing.T) {
	// QUM-340: input is no longer disabled by turn-state, so this test now
	// verifies that a successful restart leaves the App in TurnIdle and
	// dismisses the error dialog. The bar is always editable when visible.
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.showError = true

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	if app.showError {
		t.Error("error dialog should be dismissed after successful restart")
	}
	if app.turnState != TurnIdle {
		t.Errorf("turnState should be TurnIdle after restart, got %v", app.turnState)
	}
}

func TestAppModel_RestartSessionMsg_ClosesOldBridge(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.Update(RestartSessionMsg{})

	if !mock.closeCalled {
		t.Error("old bridge session should be closed on restart")
	}
}

func TestAppModel_CtrlC_ShowsConfirmDuringErrorDialog(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.showError = true
	app.errorDialog = NewErrorDialog(&app.theme, fmt.Errorf("crash"))

	msg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	updated, _ := app.Update(msg)
	app = updated.(AppModel)
	if !app.showConfirm {
		t.Error("Ctrl+C during error dialog should show confirm dialog")
	}
}

func TestAppModel_UserMessageSentMsg_ProducesWaitCmd(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// First send a message to set up bridge.events
	sendCmd := bridge.SendMessage("test")
	sendCmd() // sets bridge.events

	updated, cmd := app.Update(UserMessageSentMsg{})
	_ = updated

	if cmd == nil {
		t.Fatal("UserMessageSentMsg should produce a cmd to wait for next event")
	}
}

func TestAppModel_SessionResultMsg_DisplaysResultText(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionResultMsg{
		Result:       "\n\npong",
		IsError:      false,
		DurationMs:   100,
		TotalCostUsd: 0.001,
		NumTurns:     1,
	})
	app = updated.(AppModel)

	// The result text should be displayed as an assistant message in the viewport.
	found := false
	for _, entry := range app.viewportFor("weave").messages {
		if entry.Type == MessageAssistant && strings.Contains(entry.Content, "pong") {
			found = true
			break
		}
	}
	if !found {
		t.Error("SessionResultMsg with non-empty Result should display result text as assistant message in viewport")
	}
}

func TestAppModel_SessionResultMsg_ErrorDoesNotDisplayResultAsAssistant(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionResultMsg{
		Result:  "something went wrong",
		IsError: true,
	})
	app = updated.(AppModel)

	// Error result should NOT be displayed as assistant message
	for _, entry := range app.viewportFor("weave").messages {
		if entry.Type == MessageAssistant {
			t.Error("Error SessionResultMsg should not create an assistant message entry")
		}
	}
}

func TestAppModel_TurnStateMsg_UpdatesTurnState(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(TurnStateMsg{State: TurnThinking})
	app = updated.(AppModel)

	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v, want TurnThinking", app.turnState)
	}
}

// --- Tests for QUM-200 5c: App Model Agent Tree + Observation ---

// mockSupervisor implements supervisor.Supervisor for testing.
type mockSupervisor struct {
	agents    []supervisor.AgentInfo
	statusErr error
}

func (m *mockSupervisor) Spawn(_ context.Context, _ supervisor.SpawnRequest) (*supervisor.AgentInfo, error) {
	return nil, nil
}

func (m *mockSupervisor) Status(_ context.Context) ([]supervisor.AgentInfo, error) {
	return m.agents, m.statusErr
}
func (m *mockSupervisor) Delegate(_ context.Context, _, _ string) error      { return nil }
func (m *mockSupervisor) Message(_ context.Context, _, _, _ string) error    { return nil }
func (m *mockSupervisor) Merge(_ context.Context, _, _ string, _ bool) error { return nil }
func (m *mockSupervisor) Retire(_ context.Context, _ string, _, _, _, _ bool) error {
	return nil
}
func (m *mockSupervisor) Kill(_ context.Context, _ string) error    { return nil }
func (m *mockSupervisor) Shutdown(_ context.Context) error          { return nil }
func (m *mockSupervisor) Handoff(_ context.Context, _ string) error { return nil }
func (m *mockSupervisor) HandoffRequested() <-chan struct{}         { return nil }
func (m *mockSupervisor) PeekActivity(_ context.Context, _ string, _ int) ([]agentloop.ActivityEntry, error) {
	return nil, nil
}

func (m *mockSupervisor) SendAsync(_ context.Context, _, _, _, _ string, _ []string) (*supervisor.SendAsyncResult, error) {
	return &supervisor.SendAsyncResult{}, nil
}

func (m *mockSupervisor) Peek(_ context.Context, _ string, _ int) (*supervisor.PeekResult, error) {
	return &supervisor.PeekResult{}, nil
}

func (m *mockSupervisor) ReportStatus(_ context.Context, _, _, _, _ string) (*supervisor.ReportStatusResult, error) {
	return &supervisor.ReportStatusResult{}, nil
}

func (m *mockSupervisor) SendInterrupt(_ context.Context, _, _, _, _ string) (*supervisor.SendInterruptResult, error) {
	return &supervisor.SendInterruptResult{}, nil
}

func (m *mockSupervisor) MessagesList(_ context.Context, _ string, _ int) (*supervisor.MessagesListResult, error) {
	return &supervisor.MessagesListResult{}, nil
}

func (m *mockSupervisor) MessagesRead(_ context.Context, _ string) (*supervisor.MessagesReadResult, error) {
	return &supervisor.MessagesReadResult{}, nil
}

func (m *mockSupervisor) MessagesArchive(_ context.Context, _ string) (*supervisor.MessagesArchiveResult, error) {
	return &supervisor.MessagesArchiveResult{}, nil
}

func (m *mockSupervisor) MessagesArchiveAll(_ context.Context, _ string) (*supervisor.MessagesArchiveAllResult, error) {
	return &supervisor.MessagesArchiveAllResult{}, nil
}

func (m *mockSupervisor) MessagesPeek(_ context.Context) (*supervisor.MessagesPeekResult, error) {
	return &supervisor.MessagesPeekResult{}, nil
}

func newTestAppModelWithSupervisor(t *testing.T, sup supervisor.Supervisor) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", nil, sup, "/tmp/test-sprawl", nil)
}

func TestAppModel_NewAppModelWithSupervisor(t *testing.T) {
	sup := &mockSupervisor{
		agents: []supervisor.AgentInfo{
			{Name: "weave", Type: "weave", Status: "active"},
		},
	}
	// Should not panic with supervisor and sprawlRoot params.
	m := newTestAppModelWithSupervisor(t, sup)
	_ = m.View()
}

func TestAppModel_AgentTreeMsg_UpdatesTree(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Send 3 child nodes. PrependWeaveRoot adds weave as the permanent root,
	// so the final tree should have 4 nodes total (weave + 3 children).
	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
		{Name: "finn", Type: "engineer", Status: "active", Depth: 1},
		{Name: "oak", Type: "engineer", Status: "idle", Depth: 1},
	}

	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	if len(app.tree.nodes) != 4 {
		t.Errorf("tree.nodes = %d after AgentTreeMsg, want 4 (weave root + 3 children)", len(app.tree.nodes))
	}
	if app.tree.nodes[0].Name != "weave" {
		t.Errorf("tree.nodes[0].Name = %q, want %q", app.tree.nodes[0].Name, "weave")
	}
}

func TestAppModel_AgentSelectedMsg_SwapsViewport(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Set up tree nodes (child agents only — weave is prepended automatically).
	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	// Add a message to the root agent's viewport.
	app.viewportFor("weave").AppendUserMessage("root message")

	// Switch to observing tower.
	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)

	// The observed agent should now be "tower".
	if app.observedAgent != "tower" {
		t.Errorf("observedAgent = %q after selecting tower, want %q", app.observedAgent, "tower")
	}

	// The rendered (observed) viewport should NOT contain the root message
	// while observing tower — tower's vp is independent.
	view := app.observedVP().View()
	if strings.Contains(view, "root message") {
		t.Error("viewport should not show root agent's messages when observing tower")
	}

	// Switch back to weave — root message should be restored.
	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	view = app.observedVP().View()
	if !strings.Contains(view, "root message") {
		t.Errorf("viewport should show root agent's messages after switching back, got:\n%s", view)
	}
}

func TestAppModel_AgentSelectedMsg_MovesTreeCursor(t *testing.T) {
	// QUM-341: AgentSelectedMsg must move the tree panel's `>` cursor to the
	// newly-observed agent's row, so Ctrl+N / Ctrl+P cycling stays in sync
	// with tree-driven selection.
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	// Initially the cursor sits on the synthesized weave root.
	if got := app.tree.SelectedAgent(); got != "weave" {
		t.Fatalf("initial tree.SelectedAgent() = %q, want %q", got, "weave")
	}

	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)

	if got := app.tree.SelectedAgent(); got != "tower" {
		t.Errorf("tree.SelectedAgent() = %q after AgentSelectedMsg{tower}, want %q", got, "tower")
	}

	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	if got := app.tree.SelectedAgent(); got != "weave" {
		t.Errorf("tree.SelectedAgent() = %q after AgentSelectedMsg{weave}, want %q", got, "weave")
	}
}

func TestAppModel_AgentSelectedMsg_HidesInputBarForNonRoot(t *testing.T) {
	// QUM-340: the input bar is hidden entirely while observing a non-root
	// agent (cleaner UX than a disabled-but-visible bar). The viewport
	// reclaims those rows.
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	rootView := app.View().Content
	// Capture the viewport height while observing root for comparison.
	rootViewportH := app.viewportFor("weave").Height()

	// Select a non-root agent.
	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)

	childView := app.View().Content
	if strings.Contains(childView, "ype a message") {
		t.Error("input bar placeholder should not appear in View when observing non-root agent")
	}
	if strings.Count(childView, "╭") >= strings.Count(rootView, "╭") {
		t.Errorf("expected one fewer bordered panel after hiding input bar; root borders=%d child borders=%d",
			strings.Count(rootView, "╭"), strings.Count(childView, "╭"))
	}
	childViewportH := app.viewportFor("tower").Height()
	if childViewportH <= rootViewportH {
		t.Errorf("child viewport should be taller than root viewport after input-bar hide; child=%d root=%d", childViewportH, rootViewportH)
	}
}

func TestAppModel_AgentSelectedMsg_RestoresInputBarOnCycleBack(t *testing.T) {
	// QUM-340: cycling root → child → root re-renders the input bar and
	// snaps the viewport back to its original size.
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	rootViewportH := app.viewportFor("weave").Height()

	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)
	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	view := app.View().Content
	if !strings.Contains(view, "ype a message") {
		t.Error("input bar should be visible again after cycling back to weave")
	}
	weaveH := app.viewportFor("weave").Height()
	if weaveH != rootViewportH {
		t.Errorf("weave viewport height should match original after cycle-back; got %d, want %d", weaveH, rootViewportH)
	}
}

// --- Tests for QUM-235: Weave as permanent root node in agent tree ---

func TestAppModel_WeaveVisibleBeforeFirstTick(t *testing.T) {
	m := newTestAppModel(t)

	// A freshly constructed app should have weave in the tree without any
	// AgentTreeMsg being dispatched.
	found := false
	for _, node := range m.tree.nodes {
		if node.Name == "weave" {
			found = true
			break
		}
	}
	if !found {
		t.Error("freshly constructed AppModel should have weave node in tree before any AgentTreeMsg")
	}
}

func TestAppModel_RootAgentIsWeave(t *testing.T) {
	m := newTestAppModel(t)

	if m.rootAgent != "weave" {
		t.Errorf("rootAgent = %q, want %q", m.rootAgent, "weave")
	}
}

func TestAppModel_AgentTreeMsg_AlwaysHasWeaveRoot(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Send an empty AgentTreeMsg (no child nodes).
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{}})
	app = updated.(AppModel)

	// Even with no children, weave should always appear in the tree.
	if len(app.tree.nodes) == 0 {
		t.Fatal("tree should never be empty after AgentTreeMsg — weave root must always be present")
	}
	if app.tree.nodes[0].Name != "weave" {
		t.Errorf("tree.nodes[0].Name = %q, want %q", app.tree.nodes[0].Name, "weave")
	}
}

func TestAppModel_AgentTreeMsg_WeaveRootIsDepthZero(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	// First node must be weave at depth 0.
	if app.tree.nodes[0].Name != "weave" {
		t.Errorf("tree.nodes[0].Name = %q, want %q", app.tree.nodes[0].Name, "weave")
	}
	if app.tree.nodes[0].Depth != 0 {
		t.Errorf("tree.nodes[0].Depth = %d, want 0 (weave should always be at depth 0)", app.tree.nodes[0].Depth)
	}
}

func TestAppModel_AgentTreeMsg_ChildrenShiftedByOne(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Send nodes where tower is depth 0 and finn is depth 1.
	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
		{Name: "finn", Type: "engineer", Status: "active", Depth: 1},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	// tree should be: weave(0), tower(1), finn(2)
	if len(app.tree.nodes) != 3 {
		t.Fatalf("len(tree.nodes) = %d, want 3 (weave + tower + finn)", len(app.tree.nodes))
	}
	// tower (originally depth 0) should now be depth 1.
	if app.tree.nodes[1].Name != "tower" {
		t.Errorf("tree.nodes[1].Name = %q, want %q", app.tree.nodes[1].Name, "tower")
	}
	if app.tree.nodes[1].Depth != 1 {
		t.Errorf("tree.nodes[1].Depth = %d, want 1 (shifted by 1 to accommodate weave root)", app.tree.nodes[1].Depth)
	}
	// finn (originally depth 1) should now be depth 2.
	if app.tree.nodes[2].Name != "finn" {
		t.Errorf("tree.nodes[2].Name = %q, want %q", app.tree.nodes[2].Name, "finn")
	}
	if app.tree.nodes[2].Depth != 2 {
		t.Errorf("tree.nodes[2].Depth = %d, want 2 (shifted by 1 to accommodate weave root)", app.tree.nodes[2].Depth)
	}
}

// --- QUM-296: activity panel ---

func TestAppModel_ActivityTickMsg_UpdatesPanelForObservedAgent(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	app := resized.(AppModel)

	entries := []agentloop.ActivityEntry{
		{Kind: "assistant_text", Summary: "hello from weave"},
	}
	updated, _ := app.Update(ActivityTickMsg{Agent: "weave", Entries: entries})
	app = updated.(AppModel)

	view := stripANSI(app.activity.View())
	if !strings.Contains(view, "hello from weave") {
		t.Errorf("activity panel should show entry for observed agent; got:\n%s", view)
	}
}

func TestAppModel_ActivityTickMsg_IgnoredForOtherAgent(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	app := resized.(AppModel)

	// Observed agent is weave by default. A tick for "ghost" must not replace it.
	updated, _ := app.Update(ActivityTickMsg{
		Agent:   "ghost",
		Entries: []agentloop.ActivityEntry{{Kind: "assistant_text", Summary: "stale ghost entry"}},
	})
	app = updated.(AppModel)

	view := stripANSI(app.activity.View())
	if strings.Contains(view, "stale ghost entry") {
		t.Errorf("tick for non-observed agent must not update panel; got:\n%s", view)
	}
}

func TestAppModel_AgentSelectedMsg_SchedulesActivityRefresh(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	app := resized.(AppModel)

	nodes := []TreeNode{{Name: "tower", Type: "manager", Status: "active", Depth: 0}}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	_, cmd := app.Update(AgentSelectedMsg{Name: "tower"})
	if cmd == nil {
		t.Fatal("AgentSelectedMsg should return a non-nil cmd that fetches activity for the new agent")
	}
	msg := cmd()
	tick, ok := msg.(ActivityTickMsg)
	if !ok {
		if batch, isBatch := msg.(tea.BatchMsg); isBatch {
			for _, c := range batch {
				if am, amOK := c().(ActivityTickMsg); amOK {
					tick = am
					ok = true
					break
				}
			}
		}
	}
	if !ok {
		t.Fatalf("expected ActivityTickMsg from AgentSelectedMsg cmd, got %T", msg)
	}
	if tick.Agent != "tower" {
		t.Errorf("ActivityTickMsg.Agent = %q, want %q", tick.Agent, "tower")
	}
}

func TestAppModel_View_IncludesActivityPanelOnWideTerm(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	app := resized.(AppModel)

	// Seed the panel with an entry so rendering is non-placeholder.
	updated, _ := app.Update(ActivityTickMsg{
		Agent:   "weave",
		Entries: []agentloop.ActivityEntry{{Kind: "assistant_text", Summary: "sentinel-activity-line"}},
	})
	app = updated.(AppModel)

	view := stripANSI(app.View().Content)
	if !strings.Contains(view, "sentinel-activity-line") {
		t.Errorf("wide-terminal View() should include activity panel content; got:\n%s", view)
	}
}

func TestAppModel_View_HidesActivityPanelOnNarrowTerm(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(ActivityTickMsg{
		Agent:   "weave",
		Entries: []agentloop.ActivityEntry{{Kind: "assistant_text", Summary: "sentinel-activity-line"}},
	})
	app = updated.(AppModel)

	view := stripANSI(app.View().Content)
	if strings.Contains(view, "sentinel-activity-line") {
		t.Errorf("narrow-terminal View() should NOT render activity panel; got:\n%s", view)
	}
}

// --- QUM-259 Phase 4: auto-restart on EOF + quit-during-restart race ---

// collectBatchMsgs invokes a tea.Cmd and flattens any tea.BatchMsg into a
// slice of tea.Msg. Non-batch results are returned as a single-element slice.
// Nested batches are expanded recursively.
func collectBatchMsgs(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	raw := cmd()
	return expandBatch(t, raw)
}

func expandBatch(t *testing.T, msg tea.Msg) []tea.Msg {
	t.Helper()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			if c == nil {
				continue
			}
			out = append(out, expandBatch(t, c())...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func hasMsgOfType[T any](msgs []tea.Msg) bool {
	for _, m := range msgs {
		if _, ok := m.(T); ok {
			return true
		}
	}
	return false
}

// driveAsyncRestart runs the Cmd returned by a RestartSessionMsg update,
// extracts the RestartCompleteMsg it emits (ignoring the progress-tick
// branch), and feeds it back into the app to complete the restart cycle
// (QUM-260). Tests that previously relied on the synchronous restart
// behavior use this to observe the post-completion state.
func driveAsyncRestart(t *testing.T, app AppModel, cmd tea.Cmd) AppModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("driveAsyncRestart: RestartSessionMsg returned nil cmd")
	}
	raw := cmd()
	batch, ok := raw.(tea.BatchMsg)
	if !ok {
		t.Fatalf("driveAsyncRestart: expected tea.BatchMsg, got %T", raw)
	}
	var completion RestartCompleteMsg
	found := false
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		if msg, ok := sub().(RestartCompleteMsg); ok {
			completion = msg
			found = true
			break
		}
	}
	if !found {
		t.Fatal("driveAsyncRestart: no RestartCompleteMsg produced by restart cmd")
	}
	updated, _ := app.Update(completion)
	return updated.(AppModel)
}

func TestAppModel_SessionErrorMsg_EOF_AutoRestarts(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.turnState = TurnStreaming

	updated, cmd := app.Update(SessionErrorMsg{Err: io.EOF})
	app = updated.(AppModel)

	if app.showError {
		t.Error("showError should be false when EOF triggers auto-restart")
	}
	msgs := collectBatchMsgs(t, cmd)
	if !hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("expected SessionRestartingMsg in returned batch, got %v", msgs)
	}
	if !hasMsgOfType[RestartSessionMsg](msgs) {
		t.Errorf("expected RestartSessionMsg in returned batch, got %v", msgs)
	}
}

func TestAppModel_SessionErrorMsg_EOF_AutoRestartsEvenFromIdle(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// turnState is TurnIdle by default.
	updated, cmd := app.Update(SessionErrorMsg{Err: io.EOF})
	app = updated.(AppModel)

	if app.showError {
		t.Error("showError should be false for EOF even from idle")
	}
	msgs := collectBatchMsgs(t, cmd)
	if !hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("expected SessionRestartingMsg in batch when EOF fires from idle, got %v", msgs)
	}
}

func TestAppModel_SessionErrorMsg_WrappedEOF_AutoRestarts(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.turnState = TurnStreaming
	wrapped := fmt.Errorf("wrap: %w", io.EOF)

	updated, cmd := app.Update(SessionErrorMsg{Err: wrapped})
	app = updated.(AppModel)

	if app.showError {
		t.Error("showError should be false for wrapped EOF")
	}
	msgs := collectBatchMsgs(t, cmd)
	if !hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("wrapped EOF should still trigger auto-restart, got msgs=%v", msgs)
	}
}

func TestAppModel_SessionErrorMsg_NonEOFStreamingStillShowsDialog(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.turnState = TurnStreaming

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("non-eof failure")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("non-EOF streaming error must still show the error dialog (regression check)")
	}
}

func TestAppModel_SessionRestartingMsg_AppendsStatusAndDisablesInput(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionRestartingMsg{Reason: "session ended"})
	app = updated.(AppModel)

	// QUM-340: input is no longer disabled by turn state. Only assert the
	// status banner and idle reset.
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle", app.turnState)
	}
	found := false
	for _, entry := range app.viewportFor("weave").messages {
		if strings.Contains(entry.Content, "session ended") {
			found = true
			break
		}
	}
	if !found {
		t.Error("SessionRestartingMsg should append a status line to the viewport containing the reason")
	}
}

// --- QUM-260: async restart + ConsolidationProgressMsg ---

func TestAppModel_RestartSessionMsg_DoesNotBlockOnRestartFunc(t *testing.T) {
	// Regression test for QUM-260: RestartSessionMsg MUST return a cmd
	// without running restartFunc synchronously, so the Bubble Tea main
	// goroutine is not blocked for ~30s while FinalizeHandoff + Prepare
	// execute.
	release := make(chan struct{})
	restartStarted := make(chan struct{})
	restartCalls := 0

	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		restartCalls++
		close(restartStarted)
		<-release
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	done := make(chan struct{})
	var cmd tea.Cmd
	go func() {
		_, c := app.Update(RestartSessionMsg{})
		cmd = c
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Update(RestartSessionMsg) blocked longer than 1s — it should return immediately")
	}
	if restartCalls != 0 {
		t.Error("restartFunc must not be invoked synchronously by Update")
	}
	if cmd == nil {
		t.Fatal("RestartSessionMsg should return a non-nil cmd")
	}

	// Draining the cmd kicks off restartFunc in the background. The outer
	// cmd returns a tea.BatchMsg; the real runtime iterates it and runs
	// each sub-cmd concurrently, so we mimic that here.
	go func() {
		raw := cmd()
		if batch, ok := raw.(tea.BatchMsg); ok {
			for _, sub := range batch {
				if sub != nil {
					go func(c tea.Cmd) { _ = c() }(sub)
				}
			}
		}
	}()
	select {
	case <-restartStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("restartFunc never started after draining the cmd")
	}
	close(release)
}

func TestAppModel_RestartSessionMsg_SetsRestartingAndSchedulesTick(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		<-release
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	// Tiny interval so tea.Tick fires quickly in tests.
	app.restartTick = time.Millisecond

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)

	if !app.restarting {
		t.Fatal("restarting flag should be true after RestartSessionMsg")
	}
	if app.restartStartedAt.IsZero() {
		t.Error("restartStartedAt should be set when restart begins")
	}
	// QUM-340: input is no longer disabled by turn state — restart-in-flight
	// is signalled via the status bar elapsed counter, not the input bar.
	if cmd == nil {
		t.Fatal("expected a non-nil cmd batch from RestartSessionMsg")
	}

	// Run just the tick sub-cmd: it's the one that doesn't block on release.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected tea.BatchMsg, got %T", cmd())
	}
	var tickMsg tea.Msg
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		// The tea.Tick branch yields ConsolidationProgressMsg; the restart
		// branch blocks on <-release, so skip it.
		done := make(chan tea.Msg, 1)
		go func(c tea.Cmd) { done <- c() }(sub)
		select {
		case msg := <-done:
			if _, ok := msg.(ConsolidationProgressMsg); ok {
				tickMsg = msg
			}
		case <-time.After(200 * time.Millisecond):
			// blocked branch (restart func) — skip it.
		}
		if tickMsg != nil {
			break
		}
	}
	if tickMsg == nil {
		t.Fatal("expected a ConsolidationProgressMsg tick to be emitted")
	}
}

func TestAppModel_ConsolidationProgressMsg_UpdatesStatusBar(t *testing.T) {
	app := newTestAppModel(t)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)
	app.restarting = true
	app.restartStartedAt = time.Now()
	app.restartTick = time.Millisecond

	updated, cmd := app.Update(ConsolidationProgressMsg{Elapsed: 5 * time.Second})
	app = updated.(AppModel)

	if app.statusBar.restartElapsed != 5*time.Second {
		t.Errorf("statusBar.restartElapsed = %v, want 5s", app.statusBar.restartElapsed)
	}
	if cmd == nil {
		t.Error("should reschedule another tick while restart is in flight")
	}
}

func TestAppModel_ConsolidationProgressMsg_WhenNotRestarting_NoOp(t *testing.T) {
	app := newTestAppModel(t)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)

	updated, cmd := app.Update(ConsolidationProgressMsg{Elapsed: 3 * time.Second})
	app = updated.(AppModel)

	if cmd != nil {
		t.Error("should not reschedule tick when not restarting")
	}
	if app.statusBar.restartElapsed != 0 {
		t.Errorf("restartElapsed should stay 0 when not restarting, got %v", app.statusBar.restartElapsed)
	}
}

func TestAppModel_RestartCompleteMsg_Success_InstallsBridgeAndClearsRestarting(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)
	app.restarting = true
	app.statusBar.SetRestartElapsed(7 * time.Second)

	newBridge := NewBridge(context.Background(), newMockSession())
	newBridge.SetSessionID("abcdef12-3456-7890-abcd-ef1234567890")

	updated, cmd := app.Update(RestartCompleteMsg{Bridge: newBridge, Err: nil})
	app = updated.(AppModel)

	if app.restarting {
		t.Error("restarting flag should be cleared after RestartCompleteMsg")
	}
	if app.statusBar.restartElapsed != 0 {
		t.Error("restartElapsed indicator should be cleared on completion")
	}
	if app.bridge != newBridge {
		t.Error("new bridge should be installed")
	}
	if app.input.disabled {
		t.Error("input should be re-enabled on successful completion")
	}
	if cmd == nil {
		t.Error("expected bridge.Initialize cmd after successful restart")
	}
}

func TestAppModel_RestartCompleteMsg_Error_ShowsDialog(t *testing.T) {
	app := newTestAppModel(t)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)
	app.restarting = true

	updated, _ := app.Update(RestartCompleteMsg{Err: fmt.Errorf("boom")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("error dialog should be shown when restart fails")
	}
	if app.restarting {
		t.Error("restarting flag should be cleared even on error")
	}
}

func TestAppModel_RestartSessionMsg_CoalescesWhileRestarting(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	restartCalls := 0
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		restartCalls++
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.restarting = true // pretend a restart is already in flight

	_, cmd := app.Update(RestartSessionMsg{})
	if cmd != nil {
		t.Error("second RestartSessionMsg while restarting should be a no-op")
	}
	if restartCalls != 0 {
		t.Error("restartFunc should not be invoked while a restart is already in flight")
	}
}

func TestAppModel_RestartSessionMsg_AfterQuitConfirmed_ReturnsTeaQuit(t *testing.T) {
	restartCalled := false
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		restartCalled = true
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Simulate the user confirming Ctrl-C.
	updated, _ := app.Update(ConfirmResultMsg{Confirmed: true})
	app = updated.(AppModel)
	if !app.quitting {
		t.Fatal("ConfirmResultMsg{Confirmed:true} should set quitting=true")
	}

	// A pending RestartSessionMsg arriving after quit confirmation must
	// short-circuit to tea.Quit and MUST NOT invoke restartFunc (that would
	// leak a new bridge).
	updated, cmd := app.Update(RestartSessionMsg{})
	_ = updated
	if cmd == nil {
		t.Fatal("RestartSessionMsg while quitting should return a tea.Quit cmd")
	}
	if result, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd() = %T, want tea.QuitMsg", result)
	}
	if restartCalled {
		t.Error("restartFunc must NOT be called when quitting=true")
	}
}

func TestAppModel_TurnState_UpdatesWeaveStatus(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Trigger a turn state change that should propagate to the weave node status.
	updated, _ := app.Update(TurnStateMsg{State: TurnThinking})
	app = updated.(AppModel)

	// The weave node in the tree should reflect the new turn state.
	if len(app.tree.nodes) == 0 {
		t.Fatal("tree should not be empty after TurnStateMsg")
	}
	weaveNode := app.tree.nodes[0]
	if weaveNode.Name != "weave" {
		t.Fatalf("tree.nodes[0].Name = %q, want %q", weaveNode.Name, "weave")
	}
	// The status of the weave node should not be the zero-value empty string
	// — it should reflect the turn state.
	if weaveNode.Status == "" {
		t.Error("weave node Status should be non-empty after TurnStateMsg (should reflect turn state)")
	}
}

func TestAppModel_PreloadTranscript_SetsViewportMessages(t *testing.T) {
	m := newTestAppModel(t)
	entries := []MessageEntry{
		{Type: MessageUser, Content: "hello", Complete: true},
		{Type: MessageAssistant, Content: "hi", Complete: true},
		{Type: MessageStatus, Content: "Resumed from prior session", Complete: true},
	}
	m.PreloadTranscript(entries)

	got := m.viewportFor("weave").GetMessages()
	if len(got) != 3 {
		t.Fatalf("len(viewport messages) = %d, want 3", len(got))
	}
	if got[0].Type != MessageUser || got[0].Content != "hello" {
		t.Errorf("got[0] = %+v, want MessageUser 'hello'", got[0])
	}
	if got[1].Type != MessageAssistant || got[1].Content != "hi" {
		t.Errorf("got[1] = %+v, want MessageAssistant 'hi'", got[1])
	}
	if got[2].Type != MessageStatus || got[2].Content != "Resumed from prior session" {
		t.Errorf("got[2] = %+v, want trailing status", got[2])
	}
}

func TestAppModel_HandoffRequestedMsg_TriggersRestart(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(HandoffRequestedMsg{})
	if cmd == nil {
		t.Fatal("HandoffRequestedMsg should return a batch cmd")
	}
	msgs := collectBatchMsgs(t, cmd)
	if !hasMsgOfType[SessionRestartingMsg](msgs) {
		t.Errorf("expected SessionRestartingMsg in batch, got %v", msgs)
	}
	if !hasMsgOfType[RestartSessionMsg](msgs) {
		t.Errorf("expected RestartSessionMsg in batch, got %v", msgs)
	}
}

func TestAppModel_RestartSessionMsg_ClearsViewport(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		nb := NewBridge(context.Background(), newMockSession())
		nb.SetSessionID("newsession0000000000000000000000ffff")
		return nb, nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Seed the viewport with prior-session conversation.
	app.viewportFor("weave").AppendUserMessage("old user message")
	app.viewportFor("weave").AppendAssistantChunk("old assistant reply")
	app.viewportFor("weave").FinalizeAssistantMessage()

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	msgs := app.viewportFor("weave").GetMessages()
	for _, e := range msgs {
		if strings.Contains(e.Content, "old user message") || strings.Contains(e.Content, "old assistant reply") {
			t.Errorf("viewport should be cleared on restart; still contains prior message: %+v", e)
		}
	}
}

func TestAppModel_RestartSessionMsg_AppendsNewSessionBanner(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		nb := NewBridge(context.Background(), newMockSession())
		nb.SetSessionID("abcdef12-3456-7890-abcd-ef1234567890")
		return nb, nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	// After restart, the viewport content should contain the session banner
	// with the new session ID (QUM-390).
	vp := app.viewportFor("weave")
	view := vp.View()
	if !strings.Contains(view, "abcdef12") {
		t.Errorf("expected viewport to contain session ID 'abcdef12' in banner, got: %s", view)
	}
}

func TestAppModel_RestartSessionMsg_UpdatesStatusBarSessionID(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		nb := NewBridge(context.Background(), newMockSession())
		nb.SetSessionID("deadbeef-0000-0000-0000-000000000000")
		return nb, nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	if got := app.statusBar.sessionID; got != "deadbeef" {
		t.Errorf("statusBar.sessionID = %q, want %q (8-char truncation of new session id)", got, "deadbeef")
	}
}

func TestAppModel_SessionInitializedMsg_UpdatesStatusBarSessionID(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	bridge.SetSessionID("cafebabe-1111-2222-3333-444455556666")
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionInitializedMsg{})
	app = updated.(AppModel)

	if got := app.statusBar.sessionID; got != "cafebabe" {
		t.Errorf("statusBar.sessionID = %q, want %q after SessionInitializedMsg", got, "cafebabe")
	}
}

func TestAppModel_SessionInitializedMsg_DoesNotClearPreloadedTranscript(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	bridge.SetSessionID("aaaaaaaa-1111-2222-3333-444455556666")
	m := newTestAppModelWithBridge(t, bridge)

	entries := []MessageEntry{
		{Type: MessageUser, Content: "resumed hello", Complete: true},
		{Type: MessageAssistant, Content: "resumed reply", Complete: true},
	}
	m.PreloadTranscript(entries)

	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionInitializedMsg{})
	app = updated.(AppModel)

	msgs := app.viewportFor("weave").GetMessages()
	if len(msgs) < 2 {
		t.Fatalf("preloaded transcript was cleared by SessionInitializedMsg; got %d messages, want >=2", len(msgs))
	}
	if msgs[0].Content != "resumed hello" || msgs[1].Content != "resumed reply" {
		t.Errorf("preloaded transcript was corrupted; got %+v", msgs)
	}
}

func TestAppModel_PreloadTranscript_EmptyNoOp(t *testing.T) {
	m := newTestAppModel(t)
	m.PreloadTranscript(nil)
	if got := m.viewportFor("weave").GetMessages(); len(got) != 0 {
		t.Errorf("len(viewport messages) = %d, want 0", len(got))
	}
}

// seedScrollableViewport fills the app's viewport with enough assistant content
// that mouse wheel scrolling has observable effect (content taller than viewport
// height). Returns the app with viewport already populated.
func seedScrollableViewport(t *testing.T, app AppModel) AppModel {
	t.Helper()
	for i := 0; i < 200; i++ {
		app.viewportFor("weave").AppendAssistantChunk(fmt.Sprintf("scroll line %d\n", i))
	}
	app.viewportFor("weave").FinalizeAssistantMessage()
	return app
}

// QUM-280: mouse support for viewport scroll.
func TestAppModel_View_EnablesMouseCellMotion(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)
	v := app.View()
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode = %v, want tea.MouseModeCellMotion", v.MouseMode)
	}
}

func TestAppModel_View_MouseModeStillSetWhenTooSmall(t *testing.T) {
	// Even in the "too small" fallback view, mouse mode should be enabled —
	// a later resize should not require a full re-init to activate wheel.
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	app := updated.(AppModel)
	if !app.tooSmall {
		t.Fatal("precondition: expected tooSmall to be true for 10x5")
	}
	v := app.View()
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode (too-small) = %v, want tea.MouseModeCellMotion", v.MouseMode)
	}
}

func TestAppModel_MouseWheelUp_DisablesAutoScroll(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	if !app.viewportFor("weave").IsAutoScroll() {
		t.Fatal("precondition: autoScroll should be true after seeding")
	}

	out, _ := app.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	app = out.(AppModel)

	if app.viewportFor("weave").IsAutoScroll() {
		t.Error("expected autoScroll=false after MouseWheelUp; mouse wheel msg did not reach viewport")
	}
}

func TestAppModel_MouseWheel_SuppressedByModalHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	app.showHelp = true

	out, _ := app.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	app = out.(AppModel)

	if !app.viewportFor("weave").IsAutoScroll() {
		t.Error("expected autoScroll to remain true when help modal is open")
	}
}

func TestAppModel_MouseWheel_SuppressedByModalConfirm(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	app.showConfirm = true

	out, _ := app.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	app = out.(AppModel)

	if !app.viewportFor("weave").IsAutoScroll() {
		t.Error("expected autoScroll to remain true when confirm dialog is open")
	}
}

func TestAppModel_MouseClick_DoesNotCrash(t *testing.T) {
	// Non-wheel mouse events should be accepted without panic; we don't route
	// clicks anywhere today but they must be absorbed gracefully.
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	_, _ = app.Update(tea.MouseClickMsg{Button: tea.MouseLeft, X: 10, Y: 10})
	_, _ = app.Update(tea.MouseMotionMsg{X: 10, Y: 10})
	_, _ = app.Update(tea.MouseReleaseMsg{Button: tea.MouseLeft, X: 10, Y: 10})
}

// --- QUM-281: viewport selection & yank ---

// seedViewportApp returns an app with the viewport panel active and a few
// assistant messages present, ready for select-mode testing.
func seedViewportApp(t *testing.T) AppModel {
	t.Helper()
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app.viewportFor("weave").AppendAssistantChunk("first reply")
	app.viewportFor("weave").FinalizeAssistantMessage()
	app.viewportFor("weave").AppendAssistantChunk("second reply")
	app.viewportFor("weave").FinalizeAssistantMessage()
	app.viewportFor("weave").AppendAssistantChunk("third reply")
	app.viewportFor("weave").FinalizeAssistantMessage()
	app.activePanel = PanelViewport
	app.updateFocus()
	return app
}

func TestAppModel_VEntersSelectModeOnViewport(t *testing.T) {
	app := seedViewportApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	if !app.viewportFor("weave").IsSelecting() {
		t.Error("pressing 'v' on viewport panel should enter select mode")
	}
}

func TestAppModel_VOnInputPanelDoesNotSelect(t *testing.T) {
	app := seedViewportApp(t)
	app.activePanel = PanelInput
	app.updateFocus()
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	if app.viewportFor("weave").IsSelecting() {
		t.Error("pressing 'v' on input panel must NOT enter select mode")
	}
}

func TestAppModel_EscExitsSelectMode(t *testing.T) {
	app := seedViewportApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.viewportFor("weave").IsSelecting() {
		t.Error("Esc should exit select mode")
	}
}

func TestAppModel_YYanksRawMarkdownAndExits(t *testing.T) {
	app := seedViewportApp(t)
	// Enter select mode (cursor on last msg), extend up by 1, then yank.
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	updated, _ = app.Update(tea.KeyPressMsg{Code: 'k'})
	app = updated.(AppModel)
	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'y'})
	app = updated.(AppModel)

	if app.viewportFor("weave").IsSelecting() {
		t.Error("'y' should exit select mode after yank")
	}
	if cmd == nil {
		t.Fatal("'y' should return a non-nil Cmd (clipboard + status)")
	}
	// The Cmd should produce a BatchMsg or a setClipboard-like Msg. We verify
	// by executing it and collecting messages.
	msgs := collectCmdMsgs(cmd)
	found := false
	for _, m := range msgs {
		// bubbletea v2's setClipboardMsg is a private type, but its string form
		// equals the payload. Cast-check via fmt.Sprint.
		if s, ok := m.(fmt.Stringer); ok && strings.Contains(s.String(), "second reply") && strings.Contains(s.String(), "third reply") {
			found = true
			break
		}
		// Fallback: any msg whose stringified form contains the content.
		if strings.Contains(fmt.Sprintf("%v", m), "second reply") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("yank cmd did not emit clipboard msg with selected content; msgs=%v", msgs)
	}
}

// collectCmdMsgs executes a Cmd, unwrapping tea.BatchMsg into its constituent
// cmds and collecting all resulting Msgs.
func collectCmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	out := []tea.Msg{}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			out = append(out, collectCmdMsgs(c)...)
		}
		return out
	}
	return append(out, msg)
}

func TestAppModel_JKMoveSelectionCursor(t *testing.T) {
	app := seedViewportApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	// cursor starts at index 2 (last). 'k' moves up.
	updated, _ = app.Update(tea.KeyPressMsg{Code: 'k'})
	app = updated.(AppModel)
	updated, _ = app.Update(tea.KeyPressMsg{Code: 'k'})
	app = updated.(AppModel)
	raw := app.viewportFor("weave").SelectedRaw()
	for _, want := range []string{"first reply", "second reply", "third reply"} {
		if !strings.Contains(raw, want) {
			t.Errorf("SelectedRaw() after v+k+k should contain %q, got %q", want, raw)
		}
	}
}

func TestAppModel_StatusBarShowsSelectMode(t *testing.T) {
	app := seedViewportApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, "SELECT") {
		t.Errorf("status bar should show SELECT indicator when selecting, got: %s", view)
	}
}

// --- Tests for QUM-311 / QUM-205: TUI inbox notifier + weave root unread ---

func TestAppModel_InboxArrivalMsg_AppendsStatusBanner(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	updated, _ := app.Update(InboxArrivalMsg{From: "pretend-child", Subject: "hello"})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "inbox: new message from pretend-child") {
		t.Errorf("viewport should show inbox banner after InboxArrivalMsg, got:\n%s", view)
	}
}

func TestAppModel_InboxArrivalMsg_BumpsRootUnreadWithoutSupervisor(t *testing.T) {
	// Without a supervisor/sprawlRoot, the handler cannot poll disk, so it
	// must bump the local rootUnread counter and rebuild the tree so the
	// weave row reflects the arrival immediately.
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	if app.rootUnread != 0 {
		t.Fatalf("pre-condition: rootUnread = %d, want 0", app.rootUnread)
	}

	updated, _ := app.Update(InboxArrivalMsg{From: "pretend-child"})
	app = updated.(AppModel)

	if app.rootUnread != 1 {
		t.Errorf("rootUnread = %d after InboxArrivalMsg, want 1", app.rootUnread)
	}
	// The synthesized weave root node in the tree should reflect the bump.
	if len(app.tree.nodes) == 0 || app.tree.nodes[0].Name != "weave" {
		t.Fatalf("tree.nodes[0] should be weave, got %+v", app.tree.nodes)
	}
	if app.tree.nodes[0].Unread != 1 {
		t.Errorf("weave row Unread = %d, want 1", app.tree.nodes[0].Unread)
	}
}

func TestAppModel_InboxArrivalMsg_EmptyFromUsesFallback(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	updated, _ := app.Update(InboxArrivalMsg{})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "inbox: new message from unknown") {
		t.Errorf("viewport should show fallback banner when From empty, got:\n%s", view)
	}
}

func TestAppModel_AgentTreeMsg_ThreadsRootUnread(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(AgentTreeMsg{Nodes: nil, RootUnread: 7})
	app = updated.(AppModel)

	if app.rootUnread != 7 {
		t.Errorf("rootUnread = %d after AgentTreeMsg, want 7", app.rootUnread)
	}
	if len(app.tree.nodes) == 0 || app.tree.nodes[0].Name != "weave" {
		t.Fatalf("tree.nodes[0] should be weave, got %+v", app.tree.nodes)
	}
	if app.tree.nodes[0].Unread != 7 {
		t.Errorf("weave row Unread = %d, want 7", app.tree.nodes[0].Unread)
	}
}

func TestAppModel_AgentTreeMsg_RisingRootUnreadEmitsBanner(t *testing.T) {
	// QUM-311: out-of-process inbox arrivals (child `sprawl messages send`)
	// land on disk and are picked up on the next 2s tickAgentsCmd. The
	// AgentTreeMsg handler must notice the rise and surface a banner so the
	// user gets the same UX as in-process deliveries.
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	// Seed with 0 unread — no banner expected.
	updated, _ := app.Update(AgentTreeMsg{RootUnread: 0})
	app = updated.(AppModel)
	before := stripAnsi(app.viewportFor("weave").View())
	if strings.Contains(before, "inbox:") {
		t.Fatalf("pre-condition: no banner expected before rise, got:\n%s", before)
	}

	// Tick reveals a new message on disk.
	updated, _ = app.Update(AgentTreeMsg{RootUnread: 1})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "inbox: 1 new message(s) for weave") {
		t.Errorf("viewport should show rise banner after RootUnread 0→1, got:\n%s", view)
	}
}

func TestAppModel_AgentTreeMsg_NoBannerWhenUnreadUnchanged(t *testing.T) {
	// Subsequent ticks with an unchanged unread count must not re-fire the
	// banner — otherwise the viewport spams a banner every 2s until the user
	// reads the message.
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	// First tick sets unread to 2 (one banner).
	updated, _ := app.Update(AgentTreeMsg{RootUnread: 2})
	app = updated.(AppModel)
	firstView := stripAnsi(app.viewportFor("weave").View())
	firstBanners := strings.Count(firstView, "inbox:")
	if firstBanners != 1 {
		t.Fatalf("pre-condition: expected 1 banner after first rise, got %d in:\n%s", firstBanners, firstView)
	}

	// Second tick with the same count — no additional banner.
	updated, _ = app.Update(AgentTreeMsg{RootUnread: 2})
	app = updated.(AppModel)
	secondView := stripAnsi(app.viewportFor("weave").View())
	if got := strings.Count(secondView, "inbox:"); got != firstBanners {
		t.Errorf("banner count changed on unchanged-tick: got %d, want %d\n%s", got, firstBanners, secondView)
	}
}

// --- Tests for QUM-323: InboxDrainMsg wires the drained flush prompt into
//     Claude's next user turn, with pendingDrainIDs committed after the send
//     succeeds. ---

func TestAppModel_InboxDrainMsg_NoBridge_NoOp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	// bridge is nil in newTestAppModel; handler must short-circuit.
	updated, cmd := app.Update(InboxDrainMsg{
		Prompt: "[inbox] hi", EntryIDs: []string{"a1"},
	})
	app = updated.(AppModel)
	if cmd != nil {
		t.Errorf("expected nil cmd when bridge is nil, got %v", cmd)
	}
	if len(app.pendingDrainIDs) != 0 {
		t.Errorf("expected pendingDrainIDs empty, got %v", app.pendingDrainIDs)
	}
}

func TestAppModel_InboxDrainMsg_EmptyPrompt_NoOp(t *testing.T) {
	ms := newMockSession()
	bridge := NewBridge(context.Background(), ms)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)

	updated, cmd := app.Update(InboxDrainMsg{Prompt: "", EntryIDs: []string{"a1"}})
	app = updated.(AppModel)
	if cmd != nil {
		t.Errorf("expected nil cmd for empty prompt, got %v", cmd)
	}
	if len(app.pendingDrainIDs) != 0 {
		t.Errorf("expected pendingDrainIDs empty, got %v", app.pendingDrainIDs)
	}
}

func TestAppModel_InboxDrainMsg_DroppedWhenMidTurn(t *testing.T) {
	ms := newMockSession()
	bridge := NewBridge(context.Background(), ms)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)
	app.turnState = TurnStreaming // mid-turn

	updated, cmd := app.Update(InboxDrainMsg{
		Prompt: "[inbox] body", EntryIDs: []string{"a1"},
	})
	app = updated.(AppModel)
	if cmd != nil {
		t.Errorf("expected nil cmd when not idle, got non-nil")
	}
	if len(app.pendingDrainIDs) != 0 {
		t.Errorf("pending IDs should remain empty (entries stay in queue), got %v", app.pendingDrainIDs)
	}
}

func TestAppModel_InboxDrainMsg_IdleAppendsBannerAndStashesIDs(t *testing.T) {
	ms := newMockSession()
	bridge := NewBridge(context.Background(), ms)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)

	updated, cmd := app.Update(InboxDrainMsg{
		Prompt:   "[inbox] You received 1 message(s)...",
		EntryIDs: []string{"a1", "a2"},
		Class:    "async",
	})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("expected non-nil cmd (bridge.SendMessage)")
	}
	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "inbox: draining 2 async message(s) into next prompt") {
		t.Errorf("expected draining banner, got:\n%s", view)
	}
	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v, want TurnThinking", app.turnState)
	}
	if len(app.pendingDrainIDs) != 2 || app.pendingDrainIDs[0] != "a1" || app.pendingDrainIDs[1] != "a2" {
		t.Errorf("pendingDrainIDs = %v, want [a1 a2]", app.pendingDrainIDs)
	}

	// QUM-338: the drained prompt should be surfaced in the weave viewport as
	// a MessageSystem entry (not MessageUser) so the user sees it as a system
	// notification rather than something they typed.
	weaveVP := app.viewportFor("weave")
	entries := weaveVP.GetMessages()
	const wantPrompt = "[inbox] You received 1 message(s)..."
	var foundSystem bool
	for _, e := range entries {
		if e.Type == MessageUser && e.Content == wantPrompt {
			t.Errorf("drained prompt should not be a MessageUser entry, got: %+v", e)
		}
		if e.Type == MessageSystem && e.Content == wantPrompt {
			foundSystem = true
		}
	}
	if !foundSystem {
		t.Errorf("expected a MessageSystem entry with the drained prompt %q; got entries: %+v", wantPrompt, entries)
	}
	if !strings.Contains(stripAnsi(weaveVP.View()), "✉") {
		t.Errorf("expected mail glyph '✉' in rendered weave viewport for drained system message, got:\n%s", stripAnsi(weaveVP.View()))
	}
}

func TestAppModel_InboxDrainMsg_InterruptClassBanner(t *testing.T) {
	ms := newMockSession()
	bridge := NewBridge(context.Background(), ms)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)

	updated, _ := app.Update(InboxDrainMsg{
		Prompt: "[interrupt] x", EntryIDs: []string{"i1"}, Class: "interrupt",
	})
	app = updated.(AppModel)
	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "inbox: draining 1 interrupt message(s)") {
		t.Errorf("expected interrupt-class banner, got:\n%s", view)
	}
}

func TestAppModel_UserMessageSentMsg_ClearsPendingDrainIDs(t *testing.T) {
	// After a drained prompt is on the wire, UserMessageSentMsg must clear
	// pendingDrainIDs so subsequent turns don't re-commit the same entries.
	ms := newMockSession()
	bridge := NewBridge(context.Background(), ms)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)
	app.pendingDrainIDs = []string{"a1"}

	updated, _ := app.Update(UserMessageSentMsg{})
	app = updated.(AppModel)
	if len(app.pendingDrainIDs) != 0 {
		t.Errorf("pendingDrainIDs should be cleared after UserMessageSentMsg, got %v", app.pendingDrainIDs)
	}
}

func TestPeekAndDrainCmd_EmptyQueue_ReturnsNil(t *testing.T) {
	tmpDir := t.TempDir()
	msg := peekAndDrainCmd(tmpDir, "weave")()
	if msg != nil {
		t.Errorf("expected nil msg for empty queue, got %v", msg)
	}
}

func TestPeekAndDrainCmd_AsyncEntries_ReturnsDrainMsg(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := agentloop.Enqueue(tmpDir, "weave", agentloop.Entry{
		Class: agentloop.ClassAsync, From: "ghost",
		Subject: "s", Body: "RED-FLAG-BODY",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	msg := peekAndDrainCmd(tmpDir, "weave")()
	drain, ok := msg.(InboxDrainMsg)
	if !ok {
		t.Fatalf("expected InboxDrainMsg, got %T: %v", msg, msg)
	}
	if drain.Class != "async" {
		t.Errorf("expected async class, got %q", drain.Class)
	}
	if !strings.Contains(drain.Prompt, "RED-FLAG-BODY") {
		t.Errorf("expected body in prompt, got:\n%s", drain.Prompt)
	}
	if len(drain.EntryIDs) != 1 {
		t.Errorf("expected 1 entry ID, got %d", len(drain.EntryIDs))
	}
}

func TestPeekAndDrainCmd_InterruptPriority(t *testing.T) {
	tmpDir := t.TempDir()
	if _, err := agentloop.Enqueue(tmpDir, "weave", agentloop.Entry{
		Class: agentloop.ClassAsync, From: "a", Subject: "s", Body: "async-body",
	}); err != nil {
		t.Fatalf("enqueue async: %v", err)
	}
	if _, err := agentloop.Enqueue(tmpDir, "weave", agentloop.Entry{
		Class: agentloop.ClassInterrupt, From: "b", Subject: "s2", Body: "interrupt-body",
	}); err != nil {
		t.Fatalf("enqueue interrupt: %v", err)
	}
	msg := peekAndDrainCmd(tmpDir, "weave")()
	drain, ok := msg.(InboxDrainMsg)
	if !ok {
		t.Fatalf("expected InboxDrainMsg, got %T", msg)
	}
	if drain.Class != "interrupt" {
		t.Errorf("expected interrupt to take priority, got class=%q", drain.Class)
	}
	if !strings.Contains(drain.Prompt, "interrupt-body") {
		t.Errorf("expected interrupt body in prompt, got:\n%s", drain.Prompt)
	}
}

func TestCommitDrainCmd_MovesEntriesToDelivered(t *testing.T) {
	tmpDir := t.TempDir()
	e, err := agentloop.Enqueue(tmpDir, "weave", agentloop.Entry{
		Class: agentloop.ClassAsync, From: "x", Subject: "s", Body: "b",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	commitDrainCmd(tmpDir, "weave", []string{e.ID})()

	pending, _ := agentloop.ListPending(tmpDir, "weave")
	if len(pending) != 0 {
		t.Errorf("expected pending empty after commit, got %d", len(pending))
	}
	delivered, _ := agentloop.ListDelivered(tmpDir, "weave")
	if len(delivered) != 1 {
		t.Errorf("expected 1 delivered entry, got %d", len(delivered))
	}
}

func TestCommitDrainCmd_MissingIDsNotFatal(t *testing.T) {
	tmpDir := t.TempDir()
	// Should not panic / return non-nil msg for nonexistent IDs.
	msg := commitDrainCmd(tmpDir, "weave", []string{"does-not-exist"})()
	if msg != nil {
		t.Errorf("expected nil msg, got %v", msg)
	}
}

// QUM-335: Ctrl+O flips the global toolInputsExpanded flag and propagates
// the new state to every per-agent viewport so already-rendered tool calls
// flip immediately. (Rebound from Ctrl+E to match Claude Code's expand
// convention.)
func TestAppModel_CtrlOToggleToolInputsExpanded(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app := resized.(AppModel)

	if app.toolInputsExpanded {
		t.Fatal("toolInputsExpanded should default to false")
	}
	// Seed a tool call so the viewport has something to flip.
	app.rootVP().AppendToolCall("Bash", "", true, "ls", "ls -la /tmp")

	pressed, _ := app.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	app = pressed.(AppModel)
	if !app.toolInputsExpanded {
		t.Errorf("Ctrl+O should set toolInputsExpanded to true")
	}
	if !app.rootVP().ToolInputsExpanded() {
		t.Errorf("root viewport should mirror the global expanded flag after Ctrl+O")
	}

	pressed, _ = app.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	app = pressed.(AppModel)
	if app.toolInputsExpanded {
		t.Errorf("second Ctrl+O should toggle toolInputsExpanded back to false")
	}
	if app.rootVP().ToolInputsExpanded() {
		t.Errorf("root viewport flag should follow the global state on toggle-off")
	}
}

// QUM-335: when the global expand flag is on, a viewport lazy-created for a
// freshly-observed agent must inherit that flag so cycling agents preserves
// the user's chosen mode.
func TestAppModel_NewAgentBufferInheritsExpandedFlag(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app := resized.(AppModel)

	pressed, _ := app.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	app = pressed.(AppModel)

	vp := app.viewportFor("finn")
	if !vp.ToolInputsExpanded() {
		t.Errorf("lazily-created agent viewport should inherit toolInputsExpanded=true, got false")
	}
}

// QUM-335: ToolCallMsg with FullInput populated reaches the rootVP's
// MessageEntry as ToolInputFull so a later toggle can render it.
func TestAppModel_ToolCallMsg_PreservesFullInput(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	app := resized.(AppModel)

	updated, _ := app.Update(ToolCallMsg{
		ToolName:  "Bash",
		ToolID:    "t-1",
		Approved:  true,
		Input:     "ls",
		FullInput: "ls -la /tmp",
	})
	app = updated.(AppModel)

	msgs := app.rootVP().GetMessages()
	if len(msgs) == 0 {
		t.Fatal("expected at least one message after ToolCallMsg")
	}
	last := msgs[len(msgs)-1]
	if last.Type != MessageToolCall {
		t.Fatalf("last message type = %v, want MessageToolCall", last.Type)
	}
	if last.ToolInput != "ls" {
		t.Errorf("ToolInput = %q, want %q", last.ToolInput, "ls")
	}
	if last.ToolInputFull != "ls -la /tmp" {
		t.Errorf("ToolInputFull = %q, want %q", last.ToolInputFull, "ls -la /tmp")
	}
}

// --- QUM-340: type-while-busy queue ---

// busyAppWithBridge returns a ready, sized AppModel mid-turn (TurnStreaming)
// suitable for exercising the pendingSubmit state machine.
func busyAppWithBridge(t *testing.T) AppModel {
	t.Helper()
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)
	app.turnState = TurnStreaming
	return app
}

func TestAppModel_SubmitMsg_WhileBusy_QueuesPending(t *testing.T) {
	app := busyAppWithBridge(t)

	updated, cmd := app.Update(SubmitMsg{Text: "next prompt"})
	app = updated.(AppModel)

	if app.pendingSubmit != "next prompt" {
		t.Errorf("pendingSubmit = %q, want %q", app.pendingSubmit, "next prompt")
	}
	if app.input.PendingPreview() != "next prompt" {
		t.Errorf("input pending preview = %q, want %q", app.input.PendingPreview(), "next prompt")
	}
	if cmd != nil {
		t.Errorf("queued SubmitMsg should not return a cmd, got %T", cmd())
	}
	for _, m := range app.viewportFor("weave").GetMessages() {
		if m.Type == MessageUser && m.Content == "next prompt" {
			t.Error("queued submit must not be appended as a user message until it dispatches")
		}
	}
}

func TestAppModel_SubmitMsg_SecondWhileBusy_ReplacesQueued(t *testing.T) {
	app := busyAppWithBridge(t)
	updated, _ := app.Update(SubmitMsg{Text: "first"})
	app = updated.(AppModel)
	updated, _ = app.Update(SubmitMsg{Text: "second"})
	app = updated.(AppModel)

	if app.pendingSubmit != "second" {
		t.Errorf("pendingSubmit = %q, want %q (single-slot semantics)", app.pendingSubmit, "second")
	}
	if app.input.PendingPreview() != "second" {
		t.Errorf("indicator preview = %q, want %q", app.input.PendingPreview(), "second")
	}
}

func TestAppModel_SessionResultMsg_DispatchesPendingSubmit(t *testing.T) {
	app := busyAppWithBridge(t)
	app.pendingSubmit = "auto-fire me"
	app.input.SetPendingPreview("auto-fire me")

	updated, cmd := app.Update(SessionResultMsg{Result: "done", DurationMs: 10})
	app = updated.(AppModel)

	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared after auto-fire, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("indicator preview should be cleared after auto-fire, got %q", app.input.PendingPreview())
	}
	if cmd == nil {
		t.Fatal("SessionResultMsg with queued submit should return a cmd dispatching the SubmitMsg")
	}
	resolved := cmd()
	subMsg, ok := resolved.(SubmitMsg)
	if !ok {
		t.Fatalf("auto-fire cmd resolved to %T, want SubmitMsg", resolved)
	}
	if subMsg.Text != "auto-fire me" {
		t.Errorf("dispatched SubmitMsg.Text = %q, want %q", subMsg.Text, "auto-fire me")
	}
}

func TestAppModel_SessionResultMsg_NoQueuedSubmit_NoCmd(t *testing.T) {
	app := busyAppWithBridge(t)
	updated, cmd := app.Update(SessionResultMsg{Result: "done", DurationMs: 5})
	app = updated.(AppModel)
	if cmd != nil {
		t.Errorf("SessionResultMsg with empty queue should not return a cmd, got %T", cmd())
	}
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle", app.turnState)
	}
}

func TestAppModel_Esc_ClearsPendingSubmit(t *testing.T) {
	app := busyAppWithBridge(t)
	app.pendingSubmit = "draft"
	app.input.SetPendingPreview("draft")
	// Make sure a partial composition in the textarea buffer survives.
	app.input.ta.SetValue("composing more")

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if app.pendingSubmit != "" {
		t.Errorf("Esc should clear pendingSubmit, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("Esc should clear indicator preview, got %q", app.input.PendingPreview())
	}
	if app.input.ta.Value() != "composing more" {
		t.Errorf("Esc must not clear the textarea buffer, got %q", app.input.ta.Value())
	}
}

func TestAppModel_PendingSubmit_PersistsAcrossAgentCycle(t *testing.T) {
	sup := &mockSupervisor{}
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, sup, "", nil)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{{Name: "tower", Type: "manager"}}})
	app = updated.(AppModel)

	app.turnState = TurnStreaming
	updated, _ = app.Update(SubmitMsg{Text: "stash this"})
	app = updated.(AppModel)
	if app.pendingSubmit != "stash this" {
		t.Fatalf("setup: pendingSubmit = %q, want %q", app.pendingSubmit, "stash this")
	}

	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)
	if app.pendingSubmit != "stash this" {
		t.Errorf("pendingSubmit must survive cycle to child, got %q", app.pendingSubmit)
	}
	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)
	if app.pendingSubmit != "stash this" {
		t.Errorf("pendingSubmit must survive cycle back to root, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "stash this" {
		t.Errorf("indicator preview should be restored after cycle-back, got %q", app.input.PendingPreview())
	}
}

func TestAppModel_SessionRestartingMsg_DropsPendingSubmit(t *testing.T) {
	app := busyAppWithBridge(t)
	app.pendingSubmit = "won't survive restart"
	app.input.SetPendingPreview("won't survive restart")

	updated, _ := app.Update(SessionRestartingMsg{Reason: "handoff"})
	app = updated.(AppModel)

	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared on session restart, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("indicator preview should be cleared on session restart, got %q", app.input.PendingPreview())
	}
	found := false
	for _, m := range app.viewportFor("weave").GetMessages() {
		if m.Type == MessageStatus && strings.Contains(m.Content, "queued message dropped") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a 'queued message dropped' status banner after session restart")
	}
}

func TestAppModel_InputAlwaysEditable_MidTurn(t *testing.T) {
	// Regression for issue B: cycling root → child → root mid-turn must not
	// leave the input bar in a state where SubmitMsg silently drops the input.
	sup := &mockSupervisor{}
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, sup, "", nil)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)
	updated, _ := app.Update(AgentTreeMsg{Nodes: []TreeNode{{Name: "tower", Type: "manager"}}})
	app = updated.(AppModel)
	app.turnState = TurnStreaming

	// Cycle away then back.
	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)
	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	// SubmitMsg while still streaming must queue, not be silently dropped.
	updated, _ = app.Update(SubmitMsg{Text: "do not drop me"})
	app = updated.(AppModel)
	if app.pendingSubmit != "do not drop me" {
		t.Errorf("post-cycle SubmitMsg must queue (regression QUM-340 issue B); got pendingSubmit=%q", app.pendingSubmit)
	}
}

// --- QUM-380: ESC interrupt during streaming/thinking ---

func TestAppModel_Esc_InterruptsDuringStreaming(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("ESC during streaming should return a cmd (interrupt)")
	}
	// The cmd should call bridge.Interrupt which calls mock.Interrupt.
	result := cmd()
	if !mock.interruptCalled {
		t.Error("ESC during streaming should call Interrupt on the session")
	}
	// Should return an InterruptResultMsg.
	if _, ok := result.(InterruptResultMsg); !ok {
		t.Errorf("interrupt cmd should return InterruptResultMsg, got %T", result)
	}
}

func TestAppModel_Esc_InterruptsDuringThinking(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnThinking
	app.statusBar.SetTurnState(TurnThinking)

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("ESC during thinking should return a cmd (interrupt)")
	}
	result := cmd()
	if !mock.interruptCalled {
		t.Error("ESC during thinking should call Interrupt on the session")
	}
	if _, ok := result.(InterruptResultMsg); !ok {
		t.Errorf("interrupt cmd should return InterruptResultMsg, got %T", result)
	}
}

func TestAppModel_Esc_NoInterruptDuringIdle(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnIdle

	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	// When idle with no pendingSubmit, ESC should delegate to panel (no interrupt).
	if mock.interruptCalled {
		t.Error("ESC during idle should NOT call Interrupt")
	}
	_ = cmd
}

func TestAppModel_Esc_PendingSubmitTakesPriority(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming
	app.pendingSubmit = "queued"
	app.input.SetPendingPreview("queued")

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	// Pending submit clear takes priority over interrupt.
	if app.pendingSubmit != "" {
		t.Errorf("ESC with pendingSubmit should clear it first, got %q", app.pendingSubmit)
	}
	if mock.interruptCalled {
		t.Error("ESC with pendingSubmit should NOT call Interrupt (clear queue takes priority)")
	}
}

func TestAppModel_Esc_HelpDismissTakesPriority(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming
	app.showHelp = true

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if app.showHelp {
		t.Error("ESC with help visible should dismiss help")
	}
	if mock.interruptCalled {
		t.Error("ESC with help visible should NOT call Interrupt")
	}
}

func TestAppModel_Esc_SelectModeTakesPriority(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	// Seed viewport with messages so select mode can activate.
	app.viewportFor("weave").AppendAssistantChunk("some text")
	app.viewportFor("weave").FinalizeAssistantMessage()
	app.turnState = TurnStreaming
	app.activePanel = PanelViewport
	app.updateFocus()
	// Enter select mode.
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	if !app.observedVP().IsSelecting() {
		t.Fatal("precondition: should be in select mode")
	}

	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if app.observedVP().IsSelecting() {
		t.Error("ESC in select mode should exit select mode")
	}
	if mock.interruptCalled {
		t.Error("ESC in select mode should NOT call Interrupt")
	}
}

func TestAppModel_InterruptResultMsg_ShowsStatus(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming

	updated, _ := app.Update(InterruptResultMsg{})
	app = updated.(AppModel)

	found := false
	for _, m := range app.viewportFor("weave").GetMessages() {
		if m.Type == MessageStatus && strings.Contains(m.Content, "Interrupt") {
			found = true
			break
		}
	}
	if !found {
		t.Error("InterruptResultMsg should append a status message about the interrupt")
	}
}

func TestAppModel_InterruptResultMsg_Error(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming

	updated, _ := app.Update(InterruptResultMsg{Err: fmt.Errorf("interrupt failed")})
	app = updated.(AppModel)

	found := false
	for _, m := range app.viewportFor("weave").GetMessages() {
		if m.Type == MessageStatus && strings.Contains(m.Content, "interrupt failed") {
			found = true
			break
		}
	}
	if !found {
		t.Error("InterruptResultMsg with error should show the error in status")
	}
}

// QUM-386: AssistantContentMsg dispatches each inner msg to the viewport.
func TestAppModel_AssistantContentMsg_DispatchesAll(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = resized.(AppModel)

	// Simulate receiving a batch of two parallel Agent tool calls.
	contentMsg := AssistantContentMsg{
		Msgs: []tea.Msg{
			ToolCallMsg{ToolName: "Agent", ToolID: "a1", Approved: true, Input: "task A"},
			ToolCallMsg{ToolName: "Agent", ToolID: "a2", Approved: true, Input: "task B"},
		},
	}
	updated, _ := m.Update(contentMsg)
	app := updated.(AppModel)

	// Both tool calls should be in the root viewport.
	msgs := app.rootVP().GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages in viewport, want 2", len(msgs))
	}
	if msgs[0].Content != "Agent" || msgs[0].ToolID != "a1" {
		t.Errorf("msgs[0] = {Content:%q, ToolID:%q}, want {Agent, a1}", msgs[0].Content, msgs[0].ToolID)
	}
	if msgs[1].Content != "Agent" || msgs[1].ToolID != "a2" {
		t.Errorf("msgs[1] = {Content:%q, ToolID:%q}, want {Agent, a2}", msgs[1].Content, msgs[1].ToolID)
	}
	// Both should be depth 0 (parallel siblings).
	if msgs[0].Depth != 0 || msgs[1].Depth != 0 {
		t.Errorf("parallel Agent depths = {%d, %d}, want {0, 0}", msgs[0].Depth, msgs[1].Depth)
	}
}

func TestAppModel_Esc_NoBridgeNoInterrupt(t *testing.T) {
	app := newTestAppModel(t)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)
	app.turnState = TurnStreaming

	_, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	// No bridge → no interrupt cmd, should not panic.
	if cmd != nil {
		t.Error("ESC during streaming with no bridge should not return a cmd")
	}
}

// --- QUM-385: Token counter wiring tests ---

func TestAppModel_SessionUsageMsg_UpdatesStatusBar(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	// Deliver usage via AssistantContentMsg (as bridge does).
	updated, _ := app.Update(AssistantContentMsg{
		Msgs: []tea.Msg{SessionUsageMsg{InputTokens: 15000, OutputTokens: 300}},
	})
	app = updated.(AppModel)

	if app.statusBar.contextTokens != 15000 {
		t.Errorf("contextTokens = %d, want 15000", app.statusBar.contextTokens)
	}
}

func TestAppModel_SessionModelMsg_SetsContextLimit(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	updated, cmd := app.Update(SessionModelMsg{Model: "claude-opus-4-7-20260301"})
	app = updated.(AppModel)

	if app.statusBar.contextLimit != 1_000_000 {
		t.Errorf("contextLimit = %d, want 1000000", app.statusBar.contextLimit)
	}
	// Should return a WaitForEvent cmd since bridge is present.
	if cmd == nil {
		t.Error("expected non-nil cmd (WaitForEvent) after SessionModelMsg with bridge")
	}
}

func TestAppModel_SessionModelMsg_NoBridge(t *testing.T) {
	app := newTestAppModel(t)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	_, cmd := app.Update(SessionModelMsg{Model: "claude-opus-4-7-20260301"})

	if cmd != nil {
		t.Error("expected nil cmd when no bridge is present")
	}
}

func TestAppModel_RestartComplete_ResetsTokenUsage(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	// Set some token usage.
	app.statusBar.SetContextLimit(1_000_000)
	app.statusBar.SetTokenUsage(50000)
	app.restarting = true

	// Deliver restart complete.
	newBridge := NewBridge(context.Background(), newMockSession())
	newBridge.SetSessionID("abcdef12-new")
	updated, _ := app.Update(RestartCompleteMsg{Bridge: newBridge, Err: nil})
	app = updated.(AppModel)

	if app.statusBar.contextTokens != 0 {
		t.Errorf("contextTokens should be 0 after restart, got %d", app.statusBar.contextTokens)
	}
	// contextLimit should be preserved (model usually doesn't change).
	if app.statusBar.contextLimit != 1_000_000 {
		t.Errorf("contextLimit should be preserved across restart, got %d", app.statusBar.contextLimit)
	}
}

func TestAppModel_UsageAlongsideText_BothProcessed(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	updated, _ := app.Update(AssistantContentMsg{
		Msgs: []tea.Msg{
			AssistantTextMsg{Text: "hello"},
			SessionUsageMsg{InputTokens: 5000, OutputTokens: 200},
		},
	})
	app = updated.(AppModel)

	// Verify text was processed (turn state should be streaming).
	if app.turnState != TurnStreaming {
		t.Errorf("turnState = %v, want TurnStreaming after receiving text", app.turnState)
	}
	// Verify usage was processed.
	if app.statusBar.contextTokens != 5000 {
		t.Errorf("contextTokens = %d, want 5000", app.statusBar.contextTokens)
	}
}

// --- QUM-391: Consolidation visibility tests ---

// TestAppModel_ConsolidationPhaseMsg_AppendsStatusAndUpdatesLabel verifies
// that a ConsolidationPhaseMsg appends a status entry in the root viewport
// containing the phase text and updates the status bar's restart label.
func TestAppModel_ConsolidationPhaseMsg_AppendsStatusAndUpdatesLabel(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.restarting = true

	updated, _ := app.Update(ConsolidationPhaseMsg{Phase: "Consolidating timeline..."})
	app = updated.(AppModel)

	// Verify the root viewport has a status entry containing the phase text.
	msgs := app.rootVP().GetMessages()
	found := false
	for _, e := range msgs {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Consolidating timeline...") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("rootVP should contain a status message with 'Consolidating timeline...', got %d messages: %+v", len(msgs), msgs)
	}

	// Verify status bar label is set.
	if app.statusBar.restartLabel == "" {
		t.Error("statusBar.restartLabel should be set after ConsolidationPhaseMsg")
	}
}

// TestAppModel_ConsolidationCompleteMsg_Success_AppendsCompleteBanner verifies
// that a successful ConsolidationCompleteMsg appends a completion banner with
// the duration in the root viewport.
func TestAppModel_ConsolidationCompleteMsg_Success_AppendsCompleteBanner(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.restarting = true

	updated, _ := app.Update(ConsolidationCompleteMsg{Duration: 15 * time.Second})
	app = updated.(AppModel)

	msgs := app.rootVP().GetMessages()
	found := false
	for _, e := range msgs {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Consolidation complete") && strings.Contains(e.Content, "15s") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("rootVP should contain 'Consolidation complete (15s)' status, got messages: %+v", msgs)
	}
}

// TestAppModel_ConsolidationCompleteMsg_Error_AppendsFailureBanner verifies
// that a ConsolidationCompleteMsg with an error appends a failure banner
// containing the error message.
func TestAppModel_ConsolidationCompleteMsg_Error_AppendsFailureBanner(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.restarting = true

	updated, _ := app.Update(ConsolidationCompleteMsg{Err: fmt.Errorf("timeout")})
	app = updated.(AppModel)

	msgs := app.rootVP().GetMessages()
	found := false
	for _, e := range msgs {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Consolidation failed") && strings.Contains(e.Content, "timeout") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("rootVP should contain 'Consolidation failed: timeout' status, got messages: %+v", msgs)
	}
}

// TestAppModel_RestartCompleteMsg_PreservesStatusMessages verifies that
// status messages (e.g. consolidation phase banners) survive through
// a RestartCompleteMsg and are not lost when the restart completes.
func TestAppModel_RestartCompleteMsg_PreservesStatusMessages(t *testing.T) {
	mock := newMockSession()
	bridge := NewBridge(context.Background(), mock)
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)

	// Append a status message before restart.
	app.rootVP().AppendStatus("Consolidating timeline...")

	// Set up restart with a new bridge.
	newBridge := NewBridge(context.Background(), newMockSession())
	app.restartFunc = func() (*Bridge, error) { return newBridge, nil }

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	// The status message should still be present after restart.
	msgs := app.rootVP().GetMessages()
	found := false
	for _, e := range msgs {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Consolidating timeline...") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("status message 'Consolidating timeline...' should survive RestartCompleteMsg, got messages: %+v", msgs)
	}
}
