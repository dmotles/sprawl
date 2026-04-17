package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
	if !restartCalled {
		t.Error("restartFunc should have been called")
	}
	if cmd == nil {
		t.Error("RestartSessionMsg should return a cmd to initialize the new bridge")
	}
}

func TestAppModel_RestartSessionMsg_RestartFails(t *testing.T) {
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", func() (*Bridge, error) {
		return nil, fmt.Errorf("failed to restart")
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("showError should be true when restart fails")
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

	updated, _ := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)

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
