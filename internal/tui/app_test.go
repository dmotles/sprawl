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
	for _, entry := range app.viewport.messages {
		if entry.Type == MessageError && strings.Contains(entry.Content, "some transient error") {
			found = true
			break
		}
	}
	if !found {
		t.Error("error text should be appended to viewport when error arrives during idle")
	}
}

func TestAppModel_SessionErrorMsg_WhenStreaming_DisablesInput(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.turnState = TurnStreaming

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("process died")})
	app = updated.(AppModel)

	if !app.input.disabled {
		t.Error("input should be disabled when error dialog is shown")
	}
}

func TestAppModel_RestartSessionMsg_ReenablesInput(t *testing.T) {
	mock := newMockSession()
	ctx := context.Background()
	bridge := NewBridge(ctx, mock)

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (*Bridge, error) {
		return NewBridge(context.Background(), newMockSession()), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.showError = true
	app.input.SetDisabled(true)

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	if app.input.disabled {
		t.Error("input should be re-enabled after successful restart")
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
	for _, entry := range app.viewport.messages {
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
	for _, entry := range app.viewport.messages {
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
func (m *mockSupervisor) Delegate(_ context.Context, _, _ string) error       { return nil }
func (m *mockSupervisor) Message(_ context.Context, _, _, _ string) error     { return nil }
func (m *mockSupervisor) Merge(_ context.Context, _, _ string, _ bool) error  { return nil }
func (m *mockSupervisor) Retire(_ context.Context, _ string, _, _ bool) error { return nil }
func (m *mockSupervisor) Kill(_ context.Context, _ string) error              { return nil }
func (m *mockSupervisor) Shutdown(_ context.Context) error                    { return nil }
func (m *mockSupervisor) Handoff(_ context.Context, _ string) error           { return nil }
func (m *mockSupervisor) HandoffRequested() <-chan struct{}                   { return nil }
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
	app.viewport.AppendUserMessage("root message")

	// Switch to observing tower.
	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)

	// The observed agent should now be "tower".
	if app.observedAgent != "tower" {
		t.Errorf("observedAgent = %q after selecting tower, want %q", app.observedAgent, "tower")
	}

	// The viewport should NOT contain the root message (it should be buffered).
	view := app.viewport.View()
	if strings.Contains(view, "root message") {
		t.Error("viewport should not show root agent's messages when observing tower")
	}

	// Switch back to weave — root message should be restored.
	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	view = app.viewport.View()
	if !strings.Contains(view, "root message") {
		t.Errorf("viewport should show root agent's messages after switching back, got:\n%s", view)
	}
}

func TestAppModel_AgentSelectedMsg_DisablesInputForNonRoot(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	// Select a non-root agent.
	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)

	// Input should be disabled when observing a non-root agent.
	if !app.input.disabled {
		t.Error("input should be disabled when observing non-root agent 'tower'")
	}
}

func TestAppModel_AgentSelectedMsg_EnablesInputForRoot(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	nodes := []TreeNode{
		{Name: "tower", Type: "manager", Status: "active", Depth: 0},
	}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	// Select non-root first.
	updated, _ = app.Update(AgentSelectedMsg{Name: "tower"})
	app = updated.(AppModel)

	// Select root agent — input should be enabled.
	updated, _ = app.Update(AgentSelectedMsg{Name: "weave"})
	app = updated.(AppModel)

	if app.input.disabled {
		t.Error("input should be enabled when observing root agent 'weave'")
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

	if !app.input.disabled {
		t.Error("SessionRestartingMsg should disable input")
	}
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle", app.turnState)
	}
	found := false
	for _, entry := range app.viewport.messages {
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
	if !app.input.disabled {
		t.Error("input should be disabled while restart is in flight")
	}
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

	got := m.viewport.GetMessages()
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
	app.viewport.AppendUserMessage("old user message")
	app.viewport.AppendAssistantChunk("old assistant reply")
	app.viewport.FinalizeAssistantMessage()

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	msgs := app.viewport.GetMessages()
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

	msgs := app.viewport.GetMessages()
	found := false
	for _, e := range msgs {
		if e.Type == MessageStatus && strings.Contains(e.Content, "New session started") && strings.Contains(e.Content, "abcdef12") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected viewport to contain status banner '— New session started (abcdef12) —', got messages: %+v", msgs)
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

	msgs := app.viewport.GetMessages()
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
	if got := m.viewport.GetMessages(); len(got) != 0 {
		t.Errorf("len(viewport messages) = %d, want 0", len(got))
	}
}

// seedScrollableViewport fills the app's viewport with enough assistant content
// that mouse wheel scrolling has observable effect (content taller than viewport
// height). Returns the app with viewport already populated.
func seedScrollableViewport(t *testing.T, app AppModel) AppModel {
	t.Helper()
	for i := 0; i < 200; i++ {
		app.viewport.AppendAssistantChunk(fmt.Sprintf("scroll line %d\n", i))
	}
	app.viewport.FinalizeAssistantMessage()
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
	if !app.viewport.IsAutoScroll() {
		t.Fatal("precondition: autoScroll should be true after seeding")
	}

	out, _ := app.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	app = out.(AppModel)

	if app.viewport.IsAutoScroll() {
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

	if !app.viewport.IsAutoScroll() {
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

	if !app.viewport.IsAutoScroll() {
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
	app.viewport.AppendAssistantChunk("first reply")
	app.viewport.FinalizeAssistantMessage()
	app.viewport.AppendAssistantChunk("second reply")
	app.viewport.FinalizeAssistantMessage()
	app.viewport.AppendAssistantChunk("third reply")
	app.viewport.FinalizeAssistantMessage()
	app.activePanel = PanelViewport
	app.updateFocus()
	return app
}

func TestAppModel_VEntersSelectModeOnViewport(t *testing.T) {
	app := seedViewportApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	if !app.viewport.IsSelecting() {
		t.Error("pressing 'v' on viewport panel should enter select mode")
	}
}

func TestAppModel_VOnInputPanelDoesNotSelect(t *testing.T) {
	app := seedViewportApp(t)
	app.activePanel = PanelInput
	app.updateFocus()
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	if app.viewport.IsSelecting() {
		t.Error("pressing 'v' on input panel must NOT enter select mode")
	}
}

func TestAppModel_EscExitsSelectMode(t *testing.T) {
	app := seedViewportApp(t)
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'v'})
	app = updated.(AppModel)
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.viewport.IsSelecting() {
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

	if app.viewport.IsSelecting() {
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
	raw := app.viewport.SelectedRaw()
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

	view := stripAnsi(app.viewport.View())
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

	view := stripAnsi(app.viewport.View())
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
	before := stripAnsi(app.viewport.View())
	if strings.Contains(before, "inbox:") {
		t.Fatalf("pre-condition: no banner expected before rise, got:\n%s", before)
	}

	// Tick reveals a new message on disk.
	updated, _ = app.Update(AgentTreeMsg{RootUnread: 1})
	app = updated.(AppModel)

	view := stripAnsi(app.viewport.View())
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
	firstView := stripAnsi(app.viewport.View())
	firstBanners := strings.Count(firstView, "inbox:")
	if firstBanners != 1 {
		t.Fatalf("pre-condition: expected 1 banner after first rise, got %d in:\n%s", firstBanners, firstView)
	}

	// Second tick with the same count — no additional banner.
	updated, _ = app.Update(AgentTreeMsg{RootUnread: 2})
	app = updated.(AppModel)
	secondView := stripAnsi(app.viewport.View())
	if got := strings.Count(secondView, "inbox:"); got != firstBanners {
		t.Errorf("banner count changed on unchanged-tick: got %d, want %d\n%s", got, firstBanners, secondView)
	}
}
