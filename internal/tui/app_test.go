package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newTestAppModel(t *testing.T) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", nil)
}

func newTestAppModelWithBridge(t *testing.T, bridge *Bridge) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", bridge)
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

func TestAppModel_CtrlCQuits(t *testing.T) {
	m := newTestAppModel(t)
	msg := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	_, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("Ctrl+C should return a command")
	}
	// Execute the command and check it produces QuitMsg.
	result := cmd()
	if _, ok := result.(tea.QuitMsg); !ok {
		t.Errorf("Ctrl+C cmd() = %T, want tea.QuitMsg", result)
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
