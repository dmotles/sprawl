package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/supervisor/supervisortest"
)

func newTestAppModel(t *testing.T) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", nil)
}

func newTestAppModelWithBridge(t *testing.T, bridge SessionBackend) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", nil)
}

// TestAppModel_StatusBarAndBannerShowVersion verifies that the version arg
// passed to NewAppModel is reflected in both the status bar (live binary
// version indicator) and the session banner. Originally a QUM-464 guard test
// asserting a split between persisted and live versions; collapsed in QUM-486
// after the persisted-version split was retired in QUM-466.
func TestAppModel_StatusBarAndBannerShowVersion(t *testing.T) {
	const version = "v0.1.10-165-gABCDEF"

	m := NewAppModel("colour212", "testrepo", version, nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	app := updated.(AppModel)

	statusView := app.statusBar.View()
	if !strings.Contains(statusView, "ABCDEF") {
		t.Errorf("status bar should contain version %q, got:\n%s", version, statusView)
	}

	// QUM-675 S5: the SessionBanner viewport entry was dropped. The version
	// is only surfaced via the status bar segment (asserted above).
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
	// QUM-661: the chassis port stripped panel borders, so the legacy
	// bottom-border-glyph (╰) assertion is no longer applicable. The
	// height-clamp invariant above is now the load-bearing check.
}

// QUM-695: Tab/Shift+Tab no longer cycle panels — `activePanel` was deleted.
// Tab is now delivered to the input panel as a literal keystroke.

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

// QUM-409: Ctrl+C with non-empty input clears the input rather than triggering
// the quit-confirm dialog (REPL convention).
func TestAppModel_CtrlCWithNonEmptyInputClears(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.input.SetValue("hello world")

	updated, cmd := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if app.showConfirm {
		t.Error("Ctrl+C with non-empty input must NOT show quit confirm")
	}
	if app.input.Value() != "" {
		t.Errorf("Ctrl+C with non-empty input must clear input, got %q", app.input.Value())
	}
	if cmd != nil {
		t.Error("Ctrl+C clearing input should not return a cmd")
	}
}

// QUM-409: Ctrl+C when input is whitespace-only also clears (TrimSpace check)
// rather than treating whitespace as content worth preserving.
func TestAppModel_CtrlCWithWhitespaceOnlyInputQuits(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.input.SetValue("   \n\t ")

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if !app.showConfirm {
		t.Error("Ctrl+C with whitespace-only input should fall through to quit confirm")
	}
}

// QUM-409: explicit empty-input branch — preserves prior quit-confirm behavior.
func TestAppModel_CtrlCWithEmptyInputShowsConfirm(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	if app.input.Value() != "" {
		t.Fatalf("precondition: input should be empty, got %q", app.input.Value())
	}

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if !app.showConfirm {
		t.Error("Ctrl+C with empty input must show quit confirm (existing behavior)")
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

// QUM-695: `?` is no longer wired as a help key — F1 is canonical. The
// removal of activePanel means `?` is always typeable in the input.
func TestAppModel_QuestionMarkDoesNotOpenHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.updateFocus()

	updated, _ := app.Update(tea.KeyPressMsg{Code: '?'})
	app = updated.(AppModel)
	if app.showHelp {
		t.Error("? should NOT open help post-QUM-695; F1 is the help key")
	}
}

func TestAppModel_F1OpensHelp(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.updateFocus()

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyF1})
	app = updated.(AppModel)
	if !app.showHelp {
		t.Error("F1 should toggle help")
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
	if !app.showConfirm {
		t.Fatal("setup: confirm should be visible")
	}
	priorInput := app.input.Value()

	// QUM-695: with panel cycling removed, Tab must not reach the input
	// textarea while the confirm dialog owns keystrokes.
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = updated.(AppModel)
	if app.input.Value() != priorInput {
		t.Errorf("Tab should be swallowed when confirm is showing, input changed from %q to %q", priorInput, app.input.Value())
	}
	if !app.showConfirm {
		t.Error("confirm dialog should remain open after Tab")
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
	mock := newFakeSessionBackend()
	bridge := mock
	m := newTestAppModelWithBridge(t, bridge)

	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init() with bridge should return a cmd, got nil")
	}
}

func TestAppModel_SubmitMsg_SendsViabridge(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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

// QUM-677 S7 pivot: a ThinkingMsg arriving through Update must materialize
// a count-marker ThinkingItem in the root chat list, preserve TurnThinking
// state (no premature transition to Streaming), and coalesce subsequent
// ThinkingMsgs into the trailing marker rather than spawning new rows.
func TestAppModel_ThinkingMsg_AppendsCountMarker(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.setTurnState(TurnThinking)

	updated, _ := app.Update(ThinkingMsg{Text: ""})
	app = updated.(AppModel)
	updated, _ = app.Update(ThinkingMsg{Text: ""})
	app = updated.(AppModel)

	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v after ThinkingMsg, want TurnThinking (unchanged)", app.turnState)
	}
	cl := app.rootBuf().cl
	if cl == nil {
		t.Fatal("rootBuf().cl is nil")
	}
	thinkingCount := 0
	var ti *ThinkingItem
	for _, env := range cl.items {
		if t2, ok := env.item.(*ThinkingItem); ok {
			ti = t2
			thinkingCount++
		}
	}
	if thinkingCount != 1 {
		t.Fatalf("ThinkingMsg routing produced %d ThinkingItems, want 1 (coalesced)", thinkingCount)
	}
	if ti.Count() != 2 {
		t.Errorf("ThinkingItem.Count() = %d, want 2", ti.Count())
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	// QUM-675 S5: SessionErrorMsg (non-EOF) now always escalates to the γ
	// overlay, even from Idle. The previous "no dialog when idle" behavior
	// (AppendError into the viewport) was retired with the structural rewrite.
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("some error")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("showError should be true when SessionErrorMsg received during idle (QUM-675 S5)")
	}
}

func TestAppModel_SessionErrorMsg_ShowsDialog_WhenThinking(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
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

	priorInput := app.input.Value()
	tabMsg := tea.KeyPressMsg{Code: tea.KeyTab}
	updated, _ := app.Update(tabMsg)
	app = updated.(AppModel)

	// QUM-695: with panel cycling removed, Tab must not reach the input
	// textarea while the error dialog owns keystrokes.
	if app.input.Value() != priorInput {
		t.Errorf("Tab should be swallowed by error dialog, input changed from %q to %q", priorInput, app.input.Value())
	}
	if !app.showError {
		t.Error("error dialog should remain open after Tab")
	}
}

func TestAppModel_RestartSessionMsg_ClearsError(t *testing.T) {
	restartCalled := false
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		restartCalled = true
		newMock := newFakeSessionBackend()
		return newMock, nil
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
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, "", func() (SessionBackend, error) {
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
	// QUM-675 S5: rerouted — non-EOF idle errors now show the γ overlay
	// instead of appending a MessageError to the viewport.
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SessionErrorMsg{Err: fmt.Errorf("some transient error")})
	app = updated.(AppModel)

	if !app.showError {
		t.Error("non-EOF SessionErrorMsg should escalate to the γ overlay (QUM-675 S5)")
	}
	// QUM-693: MessageError never enters ChatList — vacuous assertion deleted.
}

func TestAppModel_SessionErrorMsg_WhenStreaming_ShowsErrorDialog(t *testing.T) {
	// QUM-340: turn-state no longer drives input.disabled. The error dialog
	// is its own modal — when it's up, the App routes all keys to it, so the
	// "input is unreachable" guarantee is provided by showError, not the
	// disabled flag.
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		return newFakeSessionBackend(), nil
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
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		return newFakeSessionBackend(), nil
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	for _, it := range app.viewportFor("weave").ChatList().Items() {
		if a, ok := it.(*AssistantTextItem); ok && strings.Contains(a.Text(), "pong") {
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
	for _, it := range app.viewportFor("weave").ChatList().Items() {
		if _, ok := it.(*AssistantTextItem); ok {
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
//
// The question-queue fields (QUM-527) are zero-valued by default so existing
// callers keep compiling. Tests that exercise the question path set
// peekDepth/peekHead to drive PeekQuestions return values, and read
// resolveCalls/cancelCalls to assert the AppModel forwarded msgs correctly.
type mockSupervisor struct {
	supervisortest.NoopSupervisor

	agents    []supervisor.AgentInfo
	statusErr error

	// Question-queue recording / programmable returns. Guarded by qmu so
	// concurrent accesses from a goroutine-fired forwarder don't race.
	qmu          sync.Mutex
	peekDepth    int
	peekHead     *supervisor.PendingQuestion
	resolveCalls []resolveCall
	cancelCalls  []cancelCall
	registered   []supervisor.QuestionConsumer
	unregistered []string
}

type resolveCall struct {
	ID   string
	Resp supervisor.QuestionResponse
}

type cancelCall struct {
	ID     string
	Reason string
}

func (m *mockSupervisor) Status(_ context.Context) ([]supervisor.AgentInfo, error) {
	return m.agents, m.statusErr
}

func (m *mockSupervisor) RegisterQuestionConsumer(c supervisor.QuestionConsumer) error {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	m.registered = append(m.registered, c)
	return nil
}

func (m *mockSupervisor) UnregisterQuestionConsumer(name string) {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	m.unregistered = append(m.unregistered, name)
}

func (m *mockSupervisor) ResolveQuestion(id string, resp supervisor.QuestionResponse) bool {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	m.resolveCalls = append(m.resolveCalls, resolveCall{ID: id, Resp: resp})
	return true
}

func (m *mockSupervisor) CancelQuestion(id, reason string) bool {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	m.cancelCalls = append(m.cancelCalls, cancelCall{ID: id, Reason: reason})
	return true
}

func (m *mockSupervisor) PeekQuestions() (int, *supervisor.PendingQuestion) {
	m.qmu.Lock()
	defer m.qmu.Unlock()
	return m.peekDepth, m.peekHead
}

func newTestAppModelWithSupervisor(t *testing.T, sup supervisor.Supervisor) AppModel {
	t.Helper()
	return NewAppModel("colour212", "testrepo", "v0.1.0", nil, sup, "/tmp/test-sprawl", nil)
}

// QUM-609: Esc dismisses the PopupFailed validate-failure modal so the user
// doesn't have to restart sprawl after a failed post-merge validate.
func TestAppModel_EscDismissesValidateFailedPopup(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)

	// Drive validate popup into PopupFailed state.
	app.validatePopup.Handle(ValidateEventMsg{Step: "merge.validate-started", KV: map[string]string{"cmd": "make validate"}})
	app.validatePopup.HandleTimer(validatePopupTimerMsg{})
	app.validatePopup.Handle(ValidateEventMsg{Step: "merge.validate-ended", KV: map[string]string{"exit": "1", "error": "boom"}})
	if app.validatePopup.State() != PopupFailed {
		t.Fatalf("setup: state=%d, want PopupFailed", app.validatePopup.State())
	}

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.validatePopup.State() != PopupHidden {
		t.Errorf("Esc must dismiss PopupFailed; state=%d, want PopupHidden", app.validatePopup.State())
	}
	if app.validatePopup.Visible() {
		t.Error("popup must be hidden after Esc dismiss")
	}
}

// QUM-609: Esc dismiss of the failure modal must run before the queued-submit
// preempt path, so dismissing the modal does not also fire off a queued prompt.
func TestAppModel_EscDismissPopupTakesPrecedenceOverPendingSubmit(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)
	app.validatePopup.Handle(ValidateEventMsg{Step: "merge.validate-started", KV: map[string]string{"cmd": "x"}})
	app.validatePopup.HandleTimer(validatePopupTimerMsg{})
	app.validatePopup.Handle(ValidateEventMsg{Step: "merge.validate-ended", KV: map[string]string{"exit": "1"}})
	app.pendingSubmit = "queued text"

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.validatePopup.State() != PopupHidden {
		t.Errorf("popup not dismissed; state=%d", app.validatePopup.State())
	}
	if app.pendingSubmit != "queued text" {
		t.Errorf("pendingSubmit should be preserved when Esc dismisses popup; got %q", app.pendingSubmit)
	}
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
	app.viewportFor("weave").ChatList().AppendUser("root message")

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
	// QUM-661: panel borders were stripped, so we can't count `╭` glyphs.
	// The hide-input invariant is enforced by the viewport-height-grew check
	// below and by the placeholder-absent check above.
	_ = rootView
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
	// QUM-664: placeholder text changed from "Type a message..." to the
	// inline key-binding hint. Substring "commands" appears in the new
	// placeholder and not elsewhere in the chassis.
	if !strings.Contains(view, "commands") {
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

// --- QUM-648: activity panel removed ---

// TestAppModel_View_NoActivityColumn_OnWideTerm asserts that on a wide
// terminal the viewport now reclaims all width previously reserved for the
// activity panel — tree + viewport must equal term width.
func TestAppModel_View_NoActivityColumn_OnWideTerm(t *testing.T) {
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	app := resized.(AppModel)

	layout := ComputeLayout(app.width, app.height, app.inputBoxHeight())
	// QUM-656: tree moved into the header, viewport claims the full width.
	if layout.ViewportWidth != layout.TermWidth {
		t.Errorf("viewport(%d) must equal term=%d (no left tree, no activity column)",
			layout.ViewportWidth, layout.TermWidth)
	}

	// View() must still render without referencing any activity-panel state.
	_ = app.View()
}

// peekActivityRecordingSupervisor counts PeekActivity calls so we can assert
// the TUI no longer fetches activity entries on agent selection.
type peekActivityRecordingSupervisor struct {
	mockSupervisor
	peekCalls int
}

func (s *peekActivityRecordingSupervisor) PeekActivity(_ context.Context, _ string, _ int) ([]agentloop.ActivityEntry, error) {
	s.peekCalls++
	return nil, nil
}

// TestAppModel_AgentSelectedMsg_DoesNotCallPeekActivity asserts that selecting
// an agent no longer triggers PeekActivity — the activity panel is gone, so
// there is no seed to fetch.
func TestAppModel_AgentSelectedMsg_DoesNotCallPeekActivity(t *testing.T) {
	sup := &peekActivityRecordingSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	app := resized.(AppModel)

	nodes := []TreeNode{{Name: "tower", Type: "manager", Status: "active", Depth: 0}}
	updated, _ := app.Update(AgentTreeMsg{Nodes: nodes})
	app = updated.(AppModel)

	_, _ = app.Update(AgentSelectedMsg{Name: "tower"})

	if sup.peekCalls != 0 {
		t.Errorf("AgentSelectedMsg must NOT call PeekActivity after QUM-648; got %d calls", sup.peekCalls)
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
			// QUM-479: bubbletea executes batch cmds concurrently in
			// goroutines; some cmds (notably activityStreamWaitCmd) may block
			// indefinitely waiting on EventBus events. Mirror the concurrent
			// semantics with a small timeout so tests don't deadlock.
			done := make(chan tea.Msg, 1)
			go func(cmd tea.Cmd) { done <- cmd() }(c)
			select {
			case sub := <-done:
				out = append(out, expandBatch(t, sub)...)
			case <-time.After(50 * time.Millisecond):
				// Treat as a blocking wait cmd — no msg yet.
			}
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
// extracts the RestartCompleteMsg it emits, and feeds it back into the app
// to complete the restart cycle (QUM-260). Tests that previously relied on
// the synchronous restart behavior use this to observe the post-completion
// state.
func driveAsyncRestart(t *testing.T, app AppModel, cmd tea.Cmd) AppModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("driveAsyncRestart: RestartSessionMsg returned nil cmd")
	}
	completion, ok := cmd().(RestartCompleteMsg)
	if !ok {
		t.Fatalf("driveAsyncRestart: expected RestartCompleteMsg, got %T", cmd())
	}
	updated, _ := app.Update(completion)
	return updated.(AppModel)
}

func TestAppModel_SessionErrorMsg_EOF_AutoRestarts(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	// QUM-675 S5: restart banner now lives on the statusbar transient label.
	if !strings.Contains(stripAnsi(app.statusBar.View()), "session ended") {
		t.Error("SessionRestartingMsg should set a transient label containing the reason")
	}
}

// --- QUM-260 / QUM-321: async restart dispatch ---

func TestAppModel_RestartSessionMsg_DoesNotBlockOnRestartFunc(t *testing.T) {
	// Regression test for QUM-260: RestartSessionMsg MUST return a cmd
	// without running restartFunc synchronously, so the Bubble Tea main
	// goroutine is not blocked for ~30s while FinalizeHandoff + Prepare
	// execute.
	release := make(chan struct{})
	restartStarted := make(chan struct{})
	restartCalls := 0

	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		restartCalls++
		close(restartStarted)
		<-release
		return newFakeSessionBackend(), nil
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

	// Draining the cmd kicks off restartFunc in the background.
	go func() { _ = cmd() }()
	select {
	case <-restartStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("restartFunc never started after draining the cmd")
	}
	close(release)
}

func TestAppModel_RestartSessionMsg_SetsRestartingFlag(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	mock := newFakeSessionBackend()
	bridge := mock
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		<-release
		return newFakeSessionBackend(), nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)

	if !app.restarting {
		t.Fatal("restarting flag should be true after RestartSessionMsg")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil cmd from RestartSessionMsg")
	}
}

func TestAppModel_RestartCompleteMsg_Success_InstallsBridgeAndClearsRestarting(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app = resized.(AppModel)
	app.restarting = true
	app.statusBar.SetRestartLabel("Consolidating timeline...")

	newBridge := newFakeSessionBackend()
	newBridge.SetSessionID("abcdef12-3456-7890-abcd-ef1234567890")

	updated, cmd := app.Update(RestartCompleteMsg{Bridge: newBridge, Err: nil})
	app = updated.(AppModel)

	if app.restarting {
		t.Error("restarting flag should be cleared after RestartCompleteMsg")
	}
	if app.statusBar.restartLabel != "" {
		t.Errorf("restartLabel should be cleared on completion (consolidation not running), got %q", app.statusBar.restartLabel)
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
	mock := newFakeSessionBackend()
	bridge := mock
	restartCalls := 0
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		restartCalls++
		return newFakeSessionBackend(), nil
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
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		restartCalled = true
		return newFakeSessionBackend(), nil
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

	// QUM-693: MessageStatus entries are silently skipped by ChatList.Reset
	// (status text routes to the statusbar transient label instead). The
	// viewport should contain only the user + assistant items.
	got := m.viewportFor("weave").ChatList().Items()
	if len(got) != 2 {
		t.Fatalf("len(viewport items) = %d, want 2 (status entry routes to statusbar)", len(got))
	}
	if u, ok := got[0].(*UserItem); !ok || u.Text() != "hello" {
		t.Errorf("got[0] = %+v, want UserItem 'hello'", got[0])
	}
	if a, ok := got[1].(*AssistantTextItem); !ok || a.Text() != "hi" {
		t.Errorf("got[1] = %+v, want AssistantTextItem 'hi'", got[1])
	}
}

func TestAppModel_HandoffRequestedMsg_TriggersRestart(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		nb := newFakeSessionBackend()
		nb.SetSessionID("newsession0000000000000000000000ffff")
		return nb, nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Seed the viewport with prior-session conversation.
	app.viewportFor("weave").ChatList().AppendUser("old user message")
	app.viewportFor("weave").ChatList().AppendAssistantChunk("old assistant reply")
	app.viewportFor("weave").ChatList().FinalizeAssistantMessage()

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	msgs := app.viewportFor("weave").ChatList().Items()
	for _, it := range msgs {
		if strings.Contains(itemContent(it), "old user message") || strings.Contains(itemContent(it), "old assistant reply") {
			t.Errorf("viewport should be cleared on restart; still contains prior message: %+v", it)
		}
	}
}

func TestAppModel_RestartSessionMsg_AppendsNewSessionBanner(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		nb := newFakeSessionBackend()
		nb.SetSessionID("abcdef12-3456-7890-abcd-ef1234567890")
		return nb, nil
	})
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, cmd := app.Update(RestartSessionMsg{})
	app = updated.(AppModel)
	app = driveAsyncRestart(t, app, cmd)

	// QUM-675 S5: SessionBanner viewport entry was dropped — the new
	// session ID is now surfaced via the status bar segment instead.
	if got := app.statusBar.sessionID; !strings.Contains(got, "abcdef12") {
		t.Errorf("statusBar.sessionID = %q, expected to contain 'abcdef12'", got)
	}
}

func TestAppModel_RestartSessionMsg_UpdatesStatusBarSessionID(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock

	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, nil, "", func() (SessionBackend, error) {
		nb := newFakeSessionBackend()
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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

	msgs := app.viewportFor("weave").ChatList().Items()
	if len(msgs) < 2 {
		t.Fatalf("preloaded transcript was cleared by SessionInitializedMsg; got %d items, want >=2", len(msgs))
	}
	if u, ok := msgs[0].(*UserItem); !ok || u.Text() != "resumed hello" {
		t.Errorf("msgs[0] = %+v, want UserItem 'resumed hello'", msgs[0])
	}
	if a, ok := msgs[1].(*AssistantTextItem); !ok || a.Text() != "resumed reply" {
		t.Errorf("msgs[1] = %+v, want AssistantTextItem 'resumed reply'", msgs[1])
	}
}

func TestAppModel_PreloadTranscript_EmptyNoOp(t *testing.T) {
	m := newTestAppModel(t)
	m.PreloadTranscript(nil)
	// QUM-675 S5: viewport starts empty (the initial SessionBanner was
	// dropped). Preloading nil must not add any messages.
	got := m.viewportFor("weave").ChatList().Items()
	if len(got) != 0 {
		t.Errorf("preload(nil) should leave viewport empty; got %d items: %+v", len(got), got)
	}
}

// seedScrollableViewport fills the app's viewport with enough assistant content
// that mouse wheel scrolling has observable effect (content taller than viewport
// height). Returns the app with viewport already populated.
func seedScrollableViewport(t *testing.T, app AppModel) AppModel {
	t.Helper()
	// QUM-675 S5: seed multiple finalized assistant messages so the
	// rendered content height clears the inner vp height threshold for
	// AtBottom() transitions on wheel events (previously the initial
	// SessionBanner padded the buffer; that banner was removed in S5).
	for chunk := 0; chunk < 4; chunk++ {
		for i := 0; i < 60; i++ {
			app.viewportFor("weave").ChatList().AppendAssistantChunk(fmt.Sprintf("scroll line %d-%d\n", chunk, i))
		}
		app.viewportFor("weave").ChatList().FinalizeAssistantMessage()
	}
	return app
}

// QUM-731: Mouse capture is on so the scroll wheel reaches the TUI. View()
// must emit MouseModeCellMotion in both the normal and the too-small fallback
// paths. The QUM-617 Ctrl+_/Ctrl+/ selection-mode toggle stays retired:
// pressing Ctrl+_ must be a no-op (no panic, no MouseMode change, no
// selectionMode field).
func TestAppModel_View_EnablesMouseCellMotion(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)
	if v := app.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("normal View().MouseMode = %v, want tea.MouseModeCellMotion", v.MouseMode)
	}

	// Same invariant in the too-small fallback path.
	m2 := newTestAppModel(t)
	updated2, _ := m2.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
	app2 := updated2.(AppModel)
	if !app2.tooSmall {
		t.Fatal("precondition: expected tooSmall to be true for 10x5")
	}
	if v := app2.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("too-small View().MouseMode = %v, want tea.MouseModeCellMotion", v.MouseMode)
	}
}

// QUM-653: Ctrl+_ (formerly Ctrl-/ selection-mode toggle) is no longer wired.
// QUM-731: MouseMode is now CellMotion at all times — Ctrl+_ must not toggle
// it off (the QUM-617 selection toggle stays retired).
func TestAppModel_NoSelectionModeToggle(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	if v := app.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("precondition: View().MouseMode = %v, want MouseModeCellMotion", v.MouseMode)
	}
	out, _ := app.Update(tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl})
	app = out.(AppModel)
	if v := app.View(); v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("after Ctrl+_: View().MouseMode = %v, want MouseModeCellMotion (no toggle)", v.MouseMode)
	}
}

// QUM-653: Home key scrolls the observed viewport to the top.
func TestAppModel_HomeKey_ScrollsViewportToTop(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	// Force a paint so the inner viewport reflects the seeded content height.
	_ = app.observedVP().region.View()
	if !app.observedVP().region.vp.AtBottom() {
		t.Fatalf("precondition: expected AtBottom after seeding+paint")
	}

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	app = out.(AppModel)
	if !app.observedVP().region.vp.AtTop() {
		t.Errorf("expected AtTop=true after KeyHome; YOffset=%d", app.observedVP().region.vp.YOffset())
	}
}

// QUM-653: End key scrolls the observed viewport to the bottom (after Home).
func TestAppModel_EndKey_ScrollsViewportToBottom(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()

	// Scroll to top first via KeyHome, then End must bring us back to bottom.
	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	app = out.(AppModel)
	if !app.observedVP().region.vp.AtTop() {
		t.Fatalf("precondition: KeyHome should have scrolled to top")
	}

	out, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	app = out.(AppModel)
	if !app.observedVP().region.vp.AtBottom() {
		t.Errorf("expected AtBottom=true after KeyEnd; YOffset=%d", app.observedVP().region.vp.YOffset())
	}
}

// QUM-774: when the input panel is empty, KeyUp recalls the newest history
// entry (shell-style muscle memory). The viewport must NOT scroll. Replaces
// the QUM-653 viewport-scroll path for empty input.
func TestAppModel_UpArrow_RecallsHistory_WhenInputEmpty(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	seedAppHistory(t, &app, []string{"first", "second"})
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	if !app.observedVP().region.vp.AtBottom() {
		t.Fatalf("precondition: expected AtBottom after seeding+paint")
	}
	beforeOffset := app.observedVP().region.vp.YOffset()
	if app.input.Value() != "" {
		t.Fatalf("precondition: input should be empty, got %q", app.input.Value())
	}

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = out.(AppModel)

	// Input must now hold the most recent history entry.
	if got := app.input.Value(); got != "second" {
		t.Errorf("KeyUp on empty input: input = %q, want %q (newest history entry)", got, "second")
	}
	// Viewport must NOT have moved.
	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset != beforeOffset {
		t.Errorf("KeyUp on empty input must not scroll viewport; before=%d after=%d", beforeOffset, afterOffset)
	}
}

// QUM-774: when the input panel has text, KeyUp is a no-op. No history
// navigation, no viewport scroll. Variant (b) — drop the QUM-410
// cursor-position-aware history path.
func TestAppModel_UpArrow_NoOp_WhenInputNonEmpty(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	seedAppHistory(t, &app, []string{"first", "second"})
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	beforeOffset := app.observedVP().region.vp.YOffset()
	app.input.SetValue("draft")

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = out.(AppModel)

	if got := app.input.Value(); got != "draft" {
		t.Errorf("KeyUp on non-empty input must be a no-op: input = %q, want %q", got, "draft")
	}
	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset != beforeOffset {
		t.Errorf("KeyUp on non-empty input must not scroll viewport; before=%d after=%d", beforeOffset, afterOffset)
	}
}

// QUM-731: a wheel-up MouseMsg with no modal up scrolls the observed viewport
// (YOffset decreases). Mirrors TestAppModel_PgUp_ScrollsViewport for the
// mouse-wheel input path.
func TestAppModel_MouseWheelUp_ScrollsViewport_WhenNoModal(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	if !app.observedVP().region.vp.AtBottom() {
		t.Fatalf("precondition: expected AtBottom after seeding+paint")
	}
	beforeOffset := app.observedVP().region.vp.YOffset()

	out, _ := app.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	app = out.(AppModel)

	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset >= beforeOffset {
		t.Errorf("expected YOffset to decrease after MouseWheelUp; before=%d after=%d", beforeOffset, afterOffset)
	}
	if app.observedVP().region.vp.AtBottom() {
		t.Errorf("expected AtBottom=false after MouseWheelUp")
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

// QUM-695: viewport selection / yank-mode (`v` / `j` / `k` / `y` / Esc) was
// removed wholesale. The tests that asserted the workflow are gone with it.

// --- Tests for QUM-311 / QUM-205: TUI inbox notifier + weave root unread ---

func TestAppModel_InboxArrivalMsg_AppendsStatusBanner(t *testing.T) {
	// QUM-465: handler now reconciles against disk-truth — seed an unread
	// maildir entry so the rise check fires.
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)
	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

	updated, _ := app.Update(InboxArrivalMsg{From: "pretend-child", Subject: "hello"})
	app = updated.(AppModel)

	// QUM-675 S5: inbox banner now on the statusbar transient label.
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, "inbox: 1 new message from pretend-child") {
		t.Errorf("statusbar should show inbox banner after InboxArrivalMsg, got:\n%s", view)
	}
}

func TestAppModel_InboxArrivalMsg_BumpsRootUnreadWithoutSupervisor(t *testing.T) {
	// QUM-465: post-fix, the handler reconciles against disk-truth. With no
	// sprawlRoot the disk poll is skipped and the handler is a no-op (this is
	// safe — without sprawlRoot the 2s tick can't run either). With a
	// sprawlRoot + a seeded unread, the handler bumps rootUnread to 1 and
	// rebuilds the tree.
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)
	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

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
	// QUM-465: seed disk so the post-fix disk-truth gate lets the banner fire.
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)
	app := newTestAppModelWithSprawlRoot(t, sprawlRoot)

	updated, _ := app.Update(InboxArrivalMsg{})
	app = updated.(AppModel)

	// QUM-675 S5: fallback banner now on the statusbar transient label.
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, "inbox: 1 new message from unknown") {
		t.Errorf("statusbar should show fallback banner when From empty, got:\n%s", view)
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
	// QUM-311: out-of-process inbox arrivals (external maildir writes) land
	// on disk and are picked up on the next 2s tickAgentsCmd. The
	// AgentTreeMsg handler must notice the rise and surface a banner so the
	// user gets the same UX as in-process deliveries.
	sup := &mockSupervisor{}
	m := newTestAppModelWithSupervisor(t, sup)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	// Seed with 0 unread — no banner expected.
	updated, _ := app.Update(AgentTreeMsg{RootUnread: 0})
	app = updated.(AppModel)
	before := stripAnsi(app.statusBar.View())
	if strings.Contains(before, "inbox:") {
		t.Fatalf("pre-condition: no banner expected before rise, got:\n%s", before)
	}

	// Tick reveals a new message on disk.
	updated, _ = app.Update(AgentTreeMsg{RootUnread: 1})
	app = updated.(AppModel)

	// QUM-675 S5: rise banner now lives on the statusbar transient label.
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, "inbox: 1 new message") {
		t.Errorf("statusbar should show rise banner after RootUnread 0→1, got:\n%s", view)
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

	// First tick sets unread to 2 — banner fires on the statusbar.
	updated, _ := app.Update(AgentTreeMsg{RootUnread: 2})
	app = updated.(AppModel)
	if !strings.Contains(stripAnsi(app.statusBar.View()), "inbox:") {
		t.Fatalf("pre-condition: expected inbox banner on statusbar after first rise, got:\n%s", stripAnsi(app.statusBar.View()))
	}
	// QUM-675 S5: the statusbar is last-write-wins; we can't detect a
	// repeat-fire by counting banners on the bar. Instead, clear the label
	// and assert the unchanged-count tick does NOT re-set it.
	app.statusBar.SetTransientLabel("")

	updated, _ = app.Update(AgentTreeMsg{RootUnread: 2})
	app = updated.(AppModel)
	if strings.Contains(stripAnsi(app.statusBar.View()), "inbox:") {
		t.Errorf("unchanged-tick re-fired inbox banner on statusbar; got:\n%s", stripAnsi(app.statusBar.View()))
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
	ms := newFakeSessionBackend()
	bridge := ms
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
	ms := newFakeSessionBackend()
	bridge := ms
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
	ms := newFakeSessionBackend()
	bridge := ms
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
	// QUM-675 S5: the draining banner is on the statusbar transient label,
	// not the viewport.
	if sb := stripAnsi(app.statusBar.View()); !strings.Contains(sb, "inbox: draining 2 async message(s) into next prompt") {
		t.Errorf("expected draining banner on statusbar, got:\n%s", sb)
	}
	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v, want TurnThinking", app.turnState)
	}
	if len(app.pendingDrainIDs) != 2 || app.pendingDrainIDs[0] != "a1" || app.pendingDrainIDs[1] != "a2" {
		t.Errorf("pendingDrainIDs = %v, want [a1 a2]", app.pendingDrainIDs)
	}

	// QUM-693: post-deletion the ChatList drops raw MessageSystem entries
	// (contract violators routed to the statusbar transient label). The
	// remaining structural guarantee is that the drain prompt does NOT
	// surface as a UserItem.
	weaveVP := app.viewportFor("weave")
	const wantPrompt = "[inbox] You received 1 message(s)..."
	for _, it := range weaveVP.ChatList().Items() {
		if u, ok := it.(*UserItem); ok && u.Text() == wantPrompt {
			t.Errorf("drained prompt should not be a UserItem, got: %+v", u)
		}
	}
}

// QUM-557: when the drained prompt is wrapped in <system-notification> tags,
// the AppModel must emit a MessageSystemNotification entry (not MessageSystem),
// so the live path matches the replay path on restart.
func TestAppModel_InboxDrainMsg_SystemNotificationEmitsNotificationEntry(t *testing.T) {
	ms := newFakeSessionBackend()
	bridge := ms
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)

	const wrapped = "<system-notification>From finn — msg id=9v6</system-notification>"
	updated, _ := app.Update(InboxDrainMsg{
		Prompt:   wrapped,
		EntryIDs: []string{"a1"},
		Class:    "async",
	})
	app = updated.(AppModel)

	weaveVP := app.viewportFor("weave")
	entries := weaveVP.ChatList().Items()
	var found *SystemNotificationItem
	for _, it := range entries {
		if n, ok := it.(*SystemNotificationItem); ok {
			found = n
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a SystemNotificationItem, got entries: %+v", entries)
	}
	if found.Interrupt() {
		t.Errorf("entry.Interrupt = true, want false (async)")
	}
	if found.Content() != "From finn — msg id=9v6" {
		t.Errorf("entry.Content = %q, want stripped body %q", found.Content(), "From finn — msg id=9v6")
	}
	// QUM-693: MessageSystem can never enter ChatList — negative deleted.
}

func TestAppModel_InboxDrainMsg_SystemNotificationInterruptFlag(t *testing.T) {
	ms := newFakeSessionBackend()
	bridge := ms
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)

	const wrapped = "<system-notification>[interrupt] From finn — msg id=9v6</system-notification>"
	updated, _ := app.Update(InboxDrainMsg{
		Prompt:   wrapped,
		EntryIDs: []string{"i1"},
		Class:    "interrupt",
	})
	app = updated.(AppModel)

	entries := app.viewportFor("weave").ChatList().Items()
	var found *SystemNotificationItem
	for _, it := range entries {
		if n, ok := it.(*SystemNotificationItem); ok {
			found = n
			break
		}
	}
	if found == nil {
		t.Fatalf("expected a SystemNotificationItem, got entries: %+v", entries)
	}
	if !found.Interrupt() {
		t.Errorf("entry.Interrupt = false, want true ([interrupt] body)")
	}
}

func TestAppModel_InboxDrainMsg_InterruptClassBanner(t *testing.T) {
	ms := newFakeSessionBackend()
	bridge := ms
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = resized.(AppModel)

	updated, _ := app.Update(InboxDrainMsg{
		Prompt: "[interrupt] x", EntryIDs: []string{"i1"}, Class: "interrupt",
	})
	app = updated.(AppModel)
	// QUM-675 S5: routed to statusbar transient label.
	if sb := stripAnsi(app.statusBar.View()); !strings.Contains(sb, "inbox: draining 1 interrupt message(s)") {
		t.Errorf("expected interrupt-class banner on statusbar, got:\n%s", sb)
	}
}

func TestAppModel_UserMessageSentMsg_ClearsPendingDrainIDs(t *testing.T) {
	// After a drained prompt is on the wire, UserMessageSentMsg must clear
	// pendingDrainIDs so subsequent turns don't re-commit the same entries.
	ms := newFakeSessionBackend()
	bridge := ms
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
	msg := peekAndDrainCmd(tmpDir, "weave", nil)()
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
	msg := peekAndDrainCmd(tmpDir, "weave", nil)()
	drain, ok := msg.(InboxDrainMsg)
	if !ok {
		t.Fatalf("expected InboxDrainMsg, got %T: %v", msg, msg)
	}
	if drain.Class != "async" {
		t.Errorf("expected async class, got %q", drain.Class)
	}
	// Post-QUM-555/QUM-556: the flush prompt is a single
	// `<system-notification>` line per entry citing the MCP tool name.
	// Assert on the sender citation in the new function-call shape.
	if !strings.Contains(drain.Prompt, "From ghost — mcp__sprawl__messages_read(id=") {
		t.Errorf("expected sender citation in prompt, got:\n%s", drain.Prompt)
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
	msg := peekAndDrainCmd(tmpDir, "weave", nil)()
	drain, ok := msg.(InboxDrainMsg)
	if !ok {
		t.Fatalf("expected InboxDrainMsg, got %T", msg)
	}
	if drain.Class != "interrupt" {
		t.Errorf("expected interrupt to take priority, got class=%q", drain.Class)
	}
	// Post-QUM-555/QUM-556: assert on the interrupt-tagged notification
	// shape (with MCP tool citation) rather than the inlined body (which is
	// no longer emitted).
	if !strings.Contains(drain.Prompt, "[interrupt] From b — mcp__sprawl__messages_read(id=") {
		t.Errorf("expected interrupt notification in prompt, got:\n%s", drain.Prompt)
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
	app.rootVP().ChatList().AppendToolCallWithHeader("Bash", "", true, "ls", "ls -la /tmp", "", nil, "")

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

	msgs := app.rootVP().ChatList().Items()
	if len(msgs) == 0 {
		t.Fatal("expected at least one item after ToolCallMsg")
	}
	last, ok := msgs[len(msgs)-1].(*ToolCallItem)
	if !ok {
		t.Fatalf("last item type = %T, want *ToolCallItem", msgs[len(msgs)-1])
	}
	if last.Input() != "ls" {
		t.Errorf("Input = %q, want %q", last.Input(), "ls")
	}
	if last.InputFull() != "ls -la /tmp" {
		t.Errorf("InputFull = %q, want %q", last.InputFull(), "ls -la /tmp")
	}
}

// --- QUM-340: type-while-busy queue ---

// busyAppWithBridge returns a ready, sized AppModel mid-turn (TurnStreaming)
// suitable for exercising the pendingSubmit state machine.
func busyAppWithBridge(t *testing.T) AppModel {
	t.Helper()
	mock := newFakeSessionBackend()
	bridge := mock
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
	for _, it := range app.viewportFor("weave").ChatList().Items() {
		if u, ok := it.(*UserItem); ok && u.Text() == "next prompt" {
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

// TestAppModel_SessionResultMsg_FaultPath_FinalizesOnceAndUngates is the
// QUM-635 TUI guard for defects 2/3. A mid-turn backend fault surfaces as
// EventTurnFailed -> SessionResultMsg{IsError:true} (see
// event_translate.go). The TUI must finalize the turn (TurnStreaming ->
// TurnIdle), re-fire any queued submit (ungate input), and be idempotent if a
// second terminal message somehow arrives — so a fault can never strand the
// TUI "streaming" with input gated.
func TestAppModel_SessionResultMsg_FaultPath_FinalizesOnceAndUngates(t *testing.T) {
	app := busyAppWithBridge(t)
	app.pendingSubmit = "queued after fault"
	app.input.SetPendingPreview("queued after fault")

	// The IsError SessionResultMsg is exactly what EventTurnFailed translates
	// to when the D1 watchdog cancels a turn blocked on ask_user_question.
	updated, cmd := app.Update(SessionResultMsg{
		IsError: true,
		Result:  "backend: reader hang timeout (no frames within HangTimeout)",
	})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle after fault-path SessionResultMsg", app.turnState)
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared (ungated) after fault, got %q", app.pendingSubmit)
	}
	if cmd == nil {
		t.Fatal("fault with queued submit should return a cmd re-firing the SubmitMsg (ungate)")
	}
	subMsg, ok := cmd().(SubmitMsg)
	if !ok {
		t.Fatalf("auto-fire cmd resolved to %T, want SubmitMsg", cmd())
	}
	if subMsg.Text != "queued after fault" {
		t.Errorf("dispatched SubmitMsg.Text = %q, want %q", subMsg.Text, "queued after fault")
	}

	// Idempotent: a second terminal message (e.g. a racing normal result) must
	// not panic, must leave turnState Idle, and has nothing left to re-fire.
	updated2, cmd2 := app.Update(SessionResultMsg{IsError: true, Result: "again"})
	app = updated2.(AppModel)
	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after second terminal msg, want TurnIdle (idempotent)", app.turnState)
	}
	if cmd2 != nil {
		t.Errorf("second terminal msg should have no queued submit to re-fire, got cmd %T", cmd2())
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	// QUM-675 S5: the "queued message dropped" banner now lives on the
	// statusbar transient label.
	if !strings.Contains(stripAnsi(app.statusBar.View()), "queued message dropped") {
		t.Error("expected a 'queued message dropped' status banner on the statusbar after session restart")
	}
}

func TestAppModel_InputAlwaysEditable_MidTurn(t *testing.T) {
	// Regression for issue B: cycling root → child → root mid-turn must not
	// leave the input bar in a state where SubmitMsg silently drops the input.
	sup := &mockSupervisor{}
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("ESC during streaming should return a cmd (interrupt)")
	}
	// The cmd is a Batch(interrupt, toast-timer-tick) — find the
	// InterruptResultMsg among the batched commands.
	if !batchContainsInterruptResult(cmd) {
		t.Errorf("interrupt cmd should produce InterruptResultMsg, got %T", cmd())
	}
	if !mock.interruptCalled {
		t.Error("ESC during streaming should call Interrupt on the session")
	}
}

func TestAppModel_Esc_InterruptsDuringThinking(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnThinking
	app.statusBar.SetTurnState(TurnThinking)

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("ESC during thinking should return a cmd (interrupt)")
	}
	if !batchContainsInterruptResult(cmd) {
		t.Errorf("interrupt cmd should produce InterruptResultMsg, got %T", cmd())
	}
	if !mock.interruptCalled {
		t.Error("ESC during thinking should call Interrupt on the session")
	}
}

// batchContainsInterruptResult invokes cmd (expected to be tea.Batch of the
// real Interrupt cmd + the toast timer tick — QUM-697) and reports whether
// any branch produces an InterruptResultMsg. Tolerates a bare cmd too.
func batchContainsInterruptResult(cmd tea.Cmd) bool {
	msg := cmd()
	if _, ok := msg.(InterruptResultMsg); ok {
		return true
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return false
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		if _, ok := c().(InterruptResultMsg); ok {
			return true
		}
	}
	return false
}

func TestAppModel_Esc_NoInterruptDuringIdle(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
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

// QUM-695: TestAppModel_Esc_SelectModeTakesPriority deleted along with the
// rest of viewport yank-mode.

func TestAppModel_InterruptResultMsg_ShowsStatus(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming

	updated, _ := app.Update(InterruptResultMsg{})
	app = updated.(AppModel)

	// QUM-675 S5: interrupt status now lands on the statusbar transient label.
	if !strings.Contains(stripAnsi(app.statusBar.View()), "Interrupt") {
		t.Error("InterruptResultMsg should set a transient status label about the interrupt")
	}
	// QUM-475: the request-ack path must NOT transition turnState. Only the
	// terminal InterruptCompletedMsg / SessionResultMsg events finalize a turn.
	if app.turnState != TurnStreaming {
		t.Errorf("InterruptResultMsg (request-ack) must not change turnState; got %v, want TurnStreaming", app.turnState)
	}
}

func TestAppModel_InterruptResultMsg_Error(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming

	updated, _ := app.Update(InterruptResultMsg{Err: fmt.Errorf("interrupt failed")})
	app = updated.(AppModel)

	// QUM-675 S5: interrupt error status now lands on the statusbar transient label.
	if !strings.Contains(stripAnsi(app.statusBar.View()), "interrupt failed") {
		t.Error("InterruptResultMsg with error should show the error in the transient status label")
	}
	// QUM-475: even on the error branch, the request-ack must not finalize.
	if app.turnState != TurnStreaming {
		t.Errorf("InterruptResultMsg with error must not change turnState; got %v, want TurnStreaming", app.turnState)
	}
}

// QUM-475: InterruptCompletedMsg is the new TERMINAL message dispatched by
// the TUIAdapter when EventInterrupted fires (i.e. the interrupted turn has
// drained). It must drive the same finalize behavior as SessionResultMsg:
// move turnState back to TurnIdle, finalize any pending assistant chunk, and
// fire pendingSubmit / drain side effects.
func TestAppModel_InterruptCompletedMsg_ReturnsToIdle(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming
	// Plant a pending assistant chunk so we can assert it's finalized.
	app.rootVP().ChatList().AppendAssistantChunk("partial response ")

	updated, _ := app.Update(InterruptCompletedMsg{Result: "stopped", DurationMs: 5})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v, want TurnIdle (InterruptCompletedMsg must finalize)", app.turnState)
	}
	if app.rootVP().ChatList().HasPendingAssistant() {
		t.Errorf("InterruptCompletedMsg must finalize pending assistant message, but HasPendingAssistant() is still true")
	}
}

func TestAppModel_InterruptCompletedMsg_DispatchesQueuedSubmit(t *testing.T) {
	app := busyAppWithBridge(t)
	app.pendingSubmit = "hello"
	app.input.SetPendingPreview("hello")

	updated, cmd := app.Update(InterruptCompletedMsg{})
	app = updated.(AppModel)

	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared after auto-fire, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("input pending preview should be cleared, got %q", app.input.PendingPreview())
	}
	if cmd == nil {
		t.Fatal("InterruptCompletedMsg with queued submit should return a cmd dispatching SubmitMsg")
	}
	resolved := cmd()
	// The cmd may be a tea.Batch; if so we walk it. Since SessionResultMsg's
	// equivalent test (TestAppModel_SessionResultMsg_DispatchesPendingSubmit)
	// asserts directly on the resolved msg, we mirror that expectation.
	if subMsg, ok := resolved.(SubmitMsg); ok {
		if subMsg.Text != "hello" {
			t.Errorf("dispatched SubmitMsg.Text = %q, want %q", subMsg.Text, "hello")
		}
		return
	}
	// Fallback: a tea.BatchMsg-like aggregate. Walk it.
	if batch, ok := resolved.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if subMsg, ok := c().(SubmitMsg); ok {
				if subMsg.Text == "hello" {
					return
				}
			}
		}
	}
	t.Fatalf("InterruptCompletedMsg cmd did not produce SubmitMsg{Text:hello}; got %T", resolved)
}

// QUM-475: when the interrupt completes the AppModel is back to idle, so the
// next 2s tick (or any peekAndDrain) should see TurnIdle and be free to drain
// queued inbox entries. Here we assert directly on the post-Update state: no
// pendingSubmit, turnState=TurnIdle. The "drain fires next tick" coupling is
// exercised by TestAppModel_AgentTreeMsg_*, which gates drainCmd on
// `m.turnState == TurnIdle`.
func TestAppModel_InterruptCompletedMsg_LeavesAppDrainable(t *testing.T) {
	app := busyAppWithBridge(t)

	updated, _ := app.Update(InterruptCompletedMsg{})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Fatalf("turnState = %v, want TurnIdle so the next tick can drain", app.turnState)
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit = %q, want empty", app.pendingSubmit)
	}
}

// QUM-475: an InboxArrivalMsg arriving DURING an in-flight interrupt (turnState
// still TurnStreaming) must append a banner but NOT drain (drains require
// idle). When the InterruptCompletedMsg subsequently arrives, the AppModel
// must transition to TurnIdle so the very next tickAgentsCmd can drain. This
// is the wedge scenario from docs/forensics/tui-weave-wedge-2026-05-05.md.
func TestAppModel_InterruptCompletedMsg_NotificationDuringInterruptPending(t *testing.T) {
	sprawlRoot := t.TempDir()
	seedUnreadForWeave(t, sprawlRoot, 1)

	mock := newFakeSessionBackend()
	bridge := mock
	sup := &mockSupervisor{}
	m := NewAppModel("colour212", "testrepo", "v0.1.0", bridge, sup, sprawlRoot, nil)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app.turnState = TurnStreaming

	// Notification first — banner appends, but no drain because non-idle.
	updated, _ := app.Update(InboxArrivalMsg{From: "child", Subject: "ping"})
	app = updated.(AppModel)
	if app.turnState != TurnStreaming {
		t.Fatalf("InboxArrivalMsg must not change turnState mid-interrupt; got %v", app.turnState)
	}

	// Now the interrupt drains.
	updated, _ = app.Update(InterruptCompletedMsg{})
	app = updated.(AppModel)
	if app.turnState != TurnIdle {
		t.Fatalf("after InterruptCompletedMsg turnState = %v, want TurnIdle", app.turnState)
	}
}

// QUM-475: the SessionResultMsg path re-arms the continuous-bridge
// WaitForEvent (see app.go:762). The InterruptCompletedMsg path must do the
// same so we don't park the event pump after a user-initiated interrupt.
// This test guards the existence of a finalizeTurn helper that both code
// paths share. If the helper is missing, the test fails to compile.
func TestAppModel_finalizeTurn_RearmsContinuousBridge(t *testing.T) {
	delegate := &continuousFakeDelegate{}
	bridge := delegate
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming

	cmd := app.finalizeTurn()
	if cmd == nil {
		t.Fatal("finalizeTurn() returned nil cmd; expected a re-arm cmd when bridge is continuous")
	}
	if delegate.waitCalls == 0 {
		t.Errorf("finalizeTurn() should re-arm WaitForEvent on a continuous bridge; waitCalls = 0")
	}
}

// QUM-475: SessionErrorMsg (non-EOF) is a terminal handler too. It must route
// through finalizeTurn so a queued pendingSubmit auto-fires and the
// continuous-bridge event pump stays armed — mirroring SessionResultMsg /
// InterruptCompletedMsg behavior. The EOF branch is exempt: it triggers a
// session restart, not idle, and is covered by the EOF_AutoRestarts tests.
func TestAppModel_SessionErrorMsg_FinalizesTurn(t *testing.T) {
	delegate := &continuousFakeDelegate{}
	bridge := delegate
	app := readyAppWithBridge(t, bridge)
	app.turnState = TurnStreaming
	app.pendingSubmit = "queued"
	app.input.SetPendingPreview("queued")

	updated, cmd := app.Update(SessionErrorMsg{Err: fmt.Errorf("non-eof failure")})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Fatalf("turnState = %v, want TurnIdle after finalizeTurn", app.turnState)
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit = %q, want empty (finalizeTurn should clear it)", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("input pending preview = %q, want empty", app.input.PendingPreview())
	}
	if !app.showError {
		t.Error("streaming non-EOF error must still show the error dialog")
	}
	if cmd == nil {
		t.Fatal("SessionErrorMsg with queued submit + continuous bridge should return a cmd")
	}

	// Walk the resolved cmd looking for the dispatched SubmitMsg.
	resolved := cmd()
	foundSubmit := false
	if subMsg, ok := resolved.(SubmitMsg); ok && subMsg.Text == "queued" {
		foundSubmit = true
	} else if batch, ok := resolved.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if subMsg, ok := c().(SubmitMsg); ok && subMsg.Text == "queued" {
				foundSubmit = true
				break
			}
		}
	}
	if !foundSubmit {
		t.Errorf("SessionErrorMsg cmd did not dispatch SubmitMsg{Text:queued}; got %T", resolved)
	}
	if delegate.waitCalls == 0 {
		t.Error("SessionErrorMsg should re-arm WaitForEvent on a continuous bridge")
	}
}

// QUM-475: SessionErrorMsg (non-EOF) from idle state — no dialog, viewport
// gets the error, finalizeTurn still re-arms the continuous bridge.
func TestAppModel_SessionErrorMsg_FromIdle_FinalizesTurn(t *testing.T) {
	delegate := &continuousFakeDelegate{}
	bridge := delegate
	app := readyAppWithBridge(t, bridge)
	// turnState is TurnIdle by default.

	updated, cmd := app.Update(SessionErrorMsg{Err: fmt.Errorf("non-eof failure")})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Fatalf("turnState = %v, want TurnIdle", app.turnState)
	}
	// QUM-675 S5: non-EOF SessionErrorMsg always escalates to the γ overlay,
	// even from Idle (was previously a viewport AppendError on the Idle path).
	if !app.showError {
		t.Error("non-EOF SessionErrorMsg from Idle should show the γ error dialog (QUM-675 S5)")
	}
	if cmd == nil {
		t.Fatal("SessionErrorMsg should re-arm WaitForEvent on a continuous bridge")
	}
	if delegate.waitCalls == 0 {
		t.Error("SessionErrorMsg from idle should re-arm WaitForEvent on a continuous bridge")
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

	// Both tool calls should be in the root viewport. QUM-693: banners never
	// enter ChatList so no filtering needed.
	items := app.rootVP().ChatList().Items()
	tools := make([]*ToolCallItem, 0, 2)
	for _, it := range items {
		if t, ok := it.(*ToolCallItem); ok {
			tools = append(tools, t)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("got %d ToolCallItems in viewport, want 2", len(tools))
	}
	if tools[0].Name() != "Agent" || tools[0].ToolID() != "a1" {
		t.Errorf("tools[0] = {Name:%q, ToolID:%q}, want {Agent, a1}", tools[0].Name(), tools[0].ToolID())
	}
	if tools[1].Name() != "Agent" || tools[1].ToolID() != "a2" {
		t.Errorf("tools[1] = {Name:%q, ToolID:%q}, want {Agent, a2}", tools[1].Name(), tools[1].ToolID())
	}
	// Both should be depth 0 (parallel siblings).
	if tools[0].Depth() != 0 || tools[1].Depth() != 0 {
		t.Errorf("parallel Agent depths = {%d, %d}, want {0, 0}", tools[0].Depth(), tools[1].Depth())
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
	mock := newFakeSessionBackend()
	bridge := mock
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	// Deliver usage via AssistantContentMsg (as bridge does). The displayed
	// counter must reflect TRUE context window usage, which is the sum of
	// input_tokens + cache_read + cache_creation (QUM-385).
	updated, _ := app.Update(AssistantContentMsg{
		Msgs: []tea.Msg{SessionUsageMsg{
			InputTokens:              15000,
			OutputTokens:             300,
			CacheReadInputTokens:     50,
			CacheCreationInputTokens: 100,
		}},
	})
	app = updated.(AppModel)

	if app.statusBar.contextTokens != 15150 {
		t.Errorf("contextTokens = %d, want 15150 (input+cache_read+cache_creation)", app.statusBar.contextTokens)
	}
}

func TestAppModel_SessionModelMsg_SetsContextLimit(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock
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
	mock := newFakeSessionBackend()
	bridge := mock
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	// Set some token usage.
	app.statusBar.SetContextLimit(1_000_000)
	app.statusBar.SetTokenUsage(50000)
	app.restarting = true

	// Deliver restart complete.
	newBridge := newFakeSessionBackend()
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
	mock := newFakeSessionBackend()
	bridge := mock
	app := newTestAppModelWithBridge(t, bridge)
	resized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = resized.(AppModel)

	updated, _ := app.Update(AssistantContentMsg{
		Msgs: []tea.Msg{
			AssistantTextMsg{Text: "hello"},
			SessionUsageMsg{
				InputTokens:              5000,
				OutputTokens:             200,
				CacheReadInputTokens:     2000,
				CacheCreationInputTokens: 500,
			},
		},
	})
	app = updated.(AppModel)

	// Verify text was processed (turn state should be streaming).
	if app.turnState != TurnStreaming {
		t.Errorf("turnState = %v, want TurnStreaming after receiving text", app.turnState)
	}
	// Verify usage was processed: warm-cache turn must sum all three
	// input-side fields (QUM-385).
	if app.statusBar.contextTokens != 7500 {
		t.Errorf("contextTokens = %d, want 7500 (5000+2000+500)", app.statusBar.contextTokens)
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

	// QUM-675 S5: the duplicate viewport status banner was dropped — the
	// restartLabel statusbar segment is now the single surface for the
	// consolidation phase text.
	if app.statusBar.restartLabel == "" {
		t.Error("statusBar.restartLabel should be set after ConsolidationPhaseMsg")
	}
	if !strings.Contains(stripAnsi(app.statusBar.View()), "Consolidating timeline...") {
		t.Errorf("statusbar should render the consolidation phase; got: %s", stripAnsi(app.statusBar.View()))
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

	// QUM-675 S5: success banner now lives on the statusbar transient label.
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, "Consolidation complete") || !strings.Contains(view, "15s") {
		t.Errorf("statusbar should contain 'Consolidation complete (15s)'; got: %s", view)
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

	// QUM-675 S5: failure banner now lives on the statusbar transient label.
	view := stripAnsi(app.statusBar.View())
	if !strings.Contains(view, "Consolidation failed") || !strings.Contains(view, "timeout") {
		t.Errorf("statusbar should contain 'Consolidation failed: timeout'; got: %s", view)
	}
}

// TestAppModel_RestartCompleteMsg_PreservesStatusMessages was removed in
// QUM-675 S5: with status/banner text rerouted out of the viewport, there
// is nothing to preserve across restart. The dedicated statusbar surfaces
// (restartLabel for consolidation phases; transientLabel for one-shot
// banners) survive the restart-clear by virtue of not being in the
// viewport in the first place.

// --- QUM-399 Phase 3 continuous-bridge AppModel routing tests ---

// continuousFakeDelegate is a BridgeDelegate that always reports
// IsContinuous()==true and counts WaitForEvent invocations so tests can
// detect whether the AppModel kicked off the event pump on the
// continuous-bridge code paths.
type continuousFakeDelegate struct {
	waitCalls int
	initCalls int
	sendCalls int
	intCalls  int
	closeCnt  int
	sessID    string
}

func (c *continuousFakeDelegate) Initialize() tea.Cmd {
	c.initCalls++
	return func() tea.Msg { return nil }
}

func (c *continuousFakeDelegate) SendMessage(_ string) tea.Cmd {
	c.sendCalls++
	return func() tea.Msg { return nil }
}

func (c *continuousFakeDelegate) WaitForEvent() tea.Cmd {
	c.waitCalls++
	// Return a sentinel that tests can inspect.
	return func() tea.Msg { return continuousWaitSentinel{} }
}

func (c *continuousFakeDelegate) Interrupt() tea.Cmd {
	c.intCalls++
	return func() tea.Msg { return nil }
}

// InterruptAndSend stub so this fake satisfies SessionBackend post-QUM-630.
// Counter intentionally piggybacks on intCalls for now since no current
// test inspects it via this delegate.
func (c *continuousFakeDelegate) InterruptAndSend(_ string) tea.Cmd {
	c.intCalls++
	return func() tea.Msg { return InterruptResultMsg{} }
}

// Recall / SendAllNow stubs so this fake satisfies SessionBackend post-QUM-824.
func (c *continuousFakeDelegate) Recall() tea.Cmd {
	return func() tea.Msg { return PromptsRecalledMsg{} }
}

func (c *continuousFakeDelegate) SendAllNow() tea.Cmd {
	return func() tea.Msg { return SendAllNowResultMsg{} }
}

func (c *continuousFakeDelegate) Close() error       { c.closeCnt++; return nil }
func (c *continuousFakeDelegate) SessionID() string  { return c.sessID }
func (c *continuousFakeDelegate) IsContinuous() bool { return true }

type continuousWaitSentinel struct{}

// runCmdsForSentinel runs the returned tea.Cmd (possibly a tea.Batch) and
// returns true if a continuousWaitSentinel was produced anywhere in the
// resulting message tree. Used to detect that WaitForEvent was scheduled.
func runCmdsForSentinel(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msg := cmd()
	return scanMsgForSentinel(msg)
}

func scanMsgForSentinel(msg tea.Msg) bool {
	if _, ok := msg.(continuousWaitSentinel); ok {
		return true
	}
	// tea.BatchMsg is a slice of tea.Cmd.
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if scanMsgForSentinel(c()) {
				return true
			}
		}
	}
	return false
}

// QUM-399: SessionInitializedMsg with a continuous bridge must kick off
// WaitForEvent so the autonomous event stream begins draining immediately.
func TestAppModel_SessionInitializedMsg_KicksOffWaitForEvent_WhenContinuous(t *testing.T) {
	d := &continuousFakeDelegate{sessID: "sess-continuous"}
	bridge := d
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(SessionInitializedMsg{})

	// The cmd should ultimately invoke our delegate's WaitForEvent.
	_ = runCmdsForSentinel(t, cmd)
	if d.waitCalls == 0 {
		t.Errorf("WaitForEvent not invoked on SessionInitializedMsg with continuous bridge; want >=1")
	}
}

// QUM-399: SessionResultMsg with a continuous bridge must keep the event
// pump running across turn boundaries.
func TestAppModel_SessionResultMsg_KicksOffWaitForEvent_WhenContinuous(t *testing.T) {
	d := &continuousFakeDelegate{sessID: "sess-continuous"}
	bridge := d
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(SessionResultMsg{IsError: false, DurationMs: 10})
	_ = runCmdsForSentinel(t, cmd)
	if d.waitCalls == 0 {
		t.Errorf("WaitForEvent not invoked on SessionResultMsg with continuous bridge; want >=1")
	}
}

// QUM-399: legacy bridges must NOT kick off WaitForEvent on
// SessionResultMsg — that path only runs the cost computation cmd. This
// guards the IsContinuous() gate on the SessionResultMsg handler.
func TestAppModel_SessionResultMsg_DoesNotKickWaitForEvent_OnLegacyBridge(t *testing.T) {
	mock := newFakeSessionBackend()
	bridge := mock // legacy: IsContinuous() == false
	// We can't directly count WaitForEvent on a legacy bridge, so assert via
	// IsContinuous being false (the precondition for the gate).
	if bridge.IsContinuous() {
		t.Fatalf("legacy bridge should not be continuous")
	}
	m := newTestAppModelWithBridge(t, bridge)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	// Just ensure it does not panic and returns a non-nil cmd (cost cmd).
	_, _ = app.Update(SessionResultMsg{IsError: false, DurationMs: 5})
}

// TestUpdate_AutoContinueMsg (QUM-634): dispatching an AutoContinueMsg through
// the app Update must append a MessageAutoTrigger entry (carrying the summary)
// to the root viewport, so the autonomous turn renders a visible trigger
// marker before the assistant response.
func TestUpdate_AutoContinueMsg(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)

	const summary = "X"
	updated, _ := app.Update(AutoContinueMsg{Summary: summary})
	app = updated.(AppModel)

	entries := app.rootVP().ChatList().Items()
	var found bool
	for _, it := range entries {
		if a, ok := it.(*AutoTriggerItem); ok {
			found = true
			if a.Summary() != summary {
				t.Errorf("auto-trigger summary = %q, want %q", a.Summary(), summary)
			}
		}
	}
	if !found {
		t.Errorf("expected an AutoTriggerItem in the root viewport after AutoContinueMsg; got entries: %+v", entries)
	}
}

// QUM-774: when input is empty, KeyDown walks forward through history that
// was previously recalled by KeyUp. After KeyUp loaded the newest entry,
// KeyDown past the newest restores the (empty) live buffer. Viewport must
// NOT scroll.
func TestAppModel_DownArrow_RecallsHistory_WhenInputEmpty(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	seedAppHistory(t, &app, []string{"first", "second"})
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	if !app.observedVP().region.vp.AtBottom() {
		t.Fatalf("precondition: expected AtBottom after seeding+paint")
	}
	if app.input.Value() != "" {
		t.Fatalf("precondition: input should be empty, got %q", app.input.Value())
	}

	// Walk Up to "first" (oldest).
	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = out.(AppModel)
	out, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = out.(AppModel)
	if got := app.input.Value(); got != "first" {
		t.Fatalf("precondition: expected %q after two KeyUps, got %q", "first", got)
	}
	beforeOffset := app.observedVP().region.vp.YOffset()

	// Down → "second".
	out, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = out.(AppModel)
	if got := app.input.Value(); got != "second" {
		t.Errorf("after KeyDown #1: input = %q, want %q", got, "second")
	}
	// Down past newest → live buffer (empty) restored.
	out, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = out.(AppModel)
	if got := app.input.Value(); got != "" {
		t.Errorf("after KeyDown past newest: input = %q, want %q (live buffer restored)", got, "")
	}
	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset != beforeOffset {
		t.Errorf("KeyDown on empty input must not scroll viewport; before=%d after=%d", beforeOffset, afterOffset)
	}
}

// QUM-774: when input has text, KeyDown is a no-op (variant b). No history
// navigation, no viewport scroll.
func TestAppModel_DownArrow_NoOp_WhenInputNonEmpty(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	seedAppHistory(t, &app, []string{"first", "second"})
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	beforeOffset := app.observedVP().region.vp.YOffset()
	app.input.SetValue("draft")

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = out.(AppModel)

	if got := app.input.Value(); got != "draft" {
		t.Errorf("KeyDown on non-empty input must be a no-op: input = %q, want %q", got, "draft")
	}
	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset != beforeOffset {
		t.Errorf("KeyDown on non-empty input must not scroll viewport; before=%d after=%d", beforeOffset, afterOffset)
	}
}

// QUM-653: PgUp scrolls the viewport up (regression guard — must keep working
// after Up/Down rewire).
func TestAppModel_PgUp_ScrollsViewport(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	if !app.observedVP().region.vp.AtBottom() {
		t.Fatalf("precondition: expected AtBottom after seeding+paint")
	}
	beforeOffset := app.observedVP().region.vp.YOffset()

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	app = out.(AppModel)

	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset >= beforeOffset {
		t.Errorf("expected YOffset to decrease after KeyPgUp; before=%d after=%d", beforeOffset, afterOffset)
	}
	if app.observedVP().region.vp.AtBottom() {
		t.Errorf("expected AtBottom=false after KeyPgUp")
	}
}

// QUM-653: PgDn scrolls the viewport down (regression guard).
func TestAppModel_PgDn_ScrollsViewport(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()

	// Scroll to top first via repeated PgUp so PgDn has room to scroll down.
	for i := 0; i < 20; i++ {
		out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
		app = out.(AppModel)
		if app.observedVP().region.vp.AtTop() {
			break
		}
	}
	if !app.observedVP().region.vp.AtTop() {
		t.Fatalf("precondition: KeyPgUp loop should have reached AtTop")
	}
	beforeOffset := app.observedVP().region.vp.YOffset()

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	app = out.(AppModel)

	afterOffset := app.observedVP().region.vp.YOffset()
	if afterOffset <= beforeOffset {
		t.Errorf("expected YOffset to increase after KeyPgDown; before=%d after=%d", beforeOffset, afterOffset)
	}
}

// QUM-774: PgUp scrolls the viewport even when the input has content
// (variant b: PgUp/PgDn ignore the input state).
func TestAppModel_PgUp_ScrollsViewport_WhenInputNonEmpty(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	app.input.SetValue("draft")
	beforeOffset := app.observedVP().region.vp.YOffset()

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	app = out.(AppModel)

	if got := app.input.Value(); got != "draft" {
		t.Errorf("PgUp must not mutate input; got %q, want %q", got, "draft")
	}
	if app.observedVP().region.vp.YOffset() >= beforeOffset {
		t.Errorf("PgUp on non-empty input must still scroll viewport; before=%d after=%d", beforeOffset, app.observedVP().region.vp.YOffset())
	}
}

// QUM-774: mouse wheel still scrolls the viewport even when the input has
// content.
func TestAppModel_MouseWheelUp_ScrollsViewport_WhenInputNonEmpty(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	app.input.SetValue("draft")
	beforeOffset := app.observedVP().region.vp.YOffset()

	out, _ := app.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	app = out.(AppModel)

	if got := app.input.Value(); got != "draft" {
		t.Errorf("MouseWheelUp must not mutate input; got %q, want %q", got, "draft")
	}
	if app.observedVP().region.vp.YOffset() >= beforeOffset {
		t.Errorf("MouseWheelUp on non-empty input must still scroll viewport; before=%d after=%d", beforeOffset, app.observedVP().region.vp.YOffset())
	}
}

// QUM-774: empty input + KeyDown with no prior KeyUp is a no-op (history
// cursor is at the live buffer position; nothing to walk forward to).
func TestAppModel_DownArrow_NoOp_WhenInputEmpty_NoHistoryStash(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	seedAppHistory(t, &app, []string{"first", "second"})
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	beforeOffset := app.observedVP().region.vp.YOffset()
	if app.input.Value() != "" {
		t.Fatalf("precondition: input should be empty")
	}

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = out.(AppModel)

	if got := app.input.Value(); got != "" {
		t.Errorf("KeyDown with no prior Up: input = %q, want %q", got, "")
	}
	if app.observedVP().region.vp.YOffset() != beforeOffset {
		t.Errorf("KeyDown with no history stash must not scroll viewport; before=%d after=%d", beforeOffset, app.observedVP().region.vp.YOffset())
	}
}

// QUM-653: when a modal (help) is open, KeyHome must NOT scroll the viewport.
// Mirrors the gating pattern in TestAppModel_MouseWheel_SuppressedByModalHelp.
func TestAppModel_HomeKey_NoScroll_WhenModalOpen(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)
	app = seedScrollableViewport(t, app)
	_ = app.observedVP().region.View()
	if !app.observedVP().region.vp.AtBottom() {
		t.Fatalf("precondition: expected AtBottom after seeding+paint")
	}

	app.showHelp = true

	out, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	app = out.(AppModel)

	if !app.observedVP().region.vp.AtBottom() {
		t.Errorf("expected viewport to remain AtBottom when help modal is open; YOffset=%d", app.observedVP().region.vp.YOffset())
	}
}

// QUM-653: the status bar must NOT render the legacy "SELECT (mouse capture
// off)" banner after Ctrl+_ — the selection-mode toggle was removed.
func TestAppModel_StatusBar_NoSelectBanner_AfterCtrlUnderscore(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := resized.(AppModel)

	out, _ := app.Update(tea.KeyPressMsg{Code: '_', Mod: tea.ModCtrl})
	app = out.(AppModel)

	view := stripANSI(app.statusBar.View())
	if strings.Contains(view, "SELECT") {
		t.Errorf("status bar must not contain SELECT banner after Ctrl+_; got:\n%s", view)
	}
}
