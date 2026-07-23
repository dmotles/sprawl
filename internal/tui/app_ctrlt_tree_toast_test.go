package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-895: Ctrl+T opens/closes the agent-tree modal (was toast-dismiss);
// toast-dismiss moves to Esc, ordered below modal-dismiss and above
// queue-reload/interrupt.

func ctrlT() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl} }

func esc() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEscape} }

// applyCmd executes a returned tea.Cmd (if any) and feeds its message back into
// the model, so a key handler that emits ToggleTreeMsg drives the reducer.
func applyCmd(t *testing.T, app AppModel, cmd tea.Cmd) AppModel {
	t.Helper()
	if cmd == nil {
		return app
	}
	msg := cmd()
	if msg == nil {
		return app
	}
	updated, _ := app.Update(msg)
	return updated.(AppModel)
}

func TestAppModel_CtrlT_EmitsToggleTreeMsg(t *testing.T) {
	app := readyApp(t)
	_, cmd := app.Update(ctrlT())
	if cmd == nil {
		t.Fatal("Ctrl+T should return a cmd emitting ToggleTreeMsg")
	}
	if _, ok := cmd().(ToggleTreeMsg); !ok {
		t.Errorf("Ctrl+T cmd = %T, want ToggleTreeMsg", cmd())
	}
}

func TestAppModel_CtrlT_OpensAndClosesTree(t *testing.T) {
	app := readyApp(t)
	if app.showTree {
		t.Fatal("setup: tree should start closed")
	}

	// First Ctrl+T opens the tree.
	updated, cmd := app.Update(ctrlT())
	app = applyCmd(t, updated.(AppModel), cmd)
	if !app.showTree {
		t.Error("first Ctrl+T should open the tree modal")
	}

	// Second Ctrl+T (tree open) must reach the handler and toggle it closed,
	// not be swallowed by the tree modal's own key routing.
	updated, cmd = app.Update(ctrlT())
	app = applyCmd(t, updated.(AppModel), cmd)
	if app.showTree {
		t.Error("second Ctrl+T should close the open tree modal")
	}
}

func TestAppModel_CtrlT_DoesNotOpenOverHigherModal(t *testing.T) {
	app := readyApp(t)
	app.showHelp = true

	updated, cmd := app.Update(ctrlT())
	app = applyCmd(t, updated.(AppModel), cmd)

	if app.showTree {
		t.Error("Ctrl+T must NOT open the tree over a higher-priority modal (help)")
	}
	if !app.showHelp {
		t.Error("help modal should be unaffected by Ctrl+T")
	}
}

func TestAppModel_CtrlT_TogglesRegardlessOfToast(t *testing.T) {
	app := readyApp(t)
	app.toasts.Spawn(Toast{Text: "hi", Style: ToastInfo, DismissOn: UserOnlyDismiss()})
	if app.toasts.Empty() {
		t.Fatal("setup: toast should be present")
	}

	_, cmd := app.Update(ctrlT())
	if cmd == nil {
		t.Fatal("Ctrl+T should emit ToggleTreeMsg even with a toast up")
	}
	if _, ok := cmd().(ToggleTreeMsg); !ok {
		t.Errorf("Ctrl+T cmd = %T, want ToggleTreeMsg (must NOT dismiss toasts)", cmd())
	}
	if app.toasts.Empty() {
		t.Error("Ctrl+T must NOT dismiss toasts anymore (that moved to Esc)")
	}
}

func TestAppModel_Esc_ClearsToastsWhenNoModal(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnIdle
	app.toasts.Spawn(Toast{Text: "hi", Style: ToastInfo, DismissOn: TimerDismiss(0)})
	if app.toasts.Empty() {
		t.Fatal("setup: toast should be present")
	}

	updated, cmd := app.Update(esc())
	app = updated.(AppModel)

	if !app.toasts.Empty() {
		t.Error("Esc should clear all toasts when no modal is up")
	}
	if mock.interruptCalled {
		t.Error("Esc that cleared a toast must NOT interrupt the turn")
	}
	if cmd != nil {
		t.Errorf("Esc that cleared a toast should consume the key (nil cmd), got %T", cmd())
	}
}

func TestAppModel_Esc_ClearsPersistentToast(t *testing.T) {
	app := readyApp(t)
	app.toasts.Spawn(Toast{Text: "agent died", Style: ToastError, DismissOn: UserOnlyDismiss()})
	if app.toasts.Empty() {
		t.Fatal("setup: persistent toast should be present")
	}

	updated, _ := app.Update(esc())
	app = updated.(AppModel)

	if !app.toasts.Empty() {
		t.Error("Esc should clear persistent (user-only) toasts too")
	}
}

func TestAppModel_Esc_ToastClearThenInterrupt(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)
	app.toasts.Spawn(Toast{Text: "hi", Style: ToastInfo, DismissOn: UserOnlyDismiss()})

	// First Esc clears the toast; must NOT interrupt.
	updated, cmd := app.Update(esc())
	app = updated.(AppModel)
	if !app.toasts.Empty() {
		t.Error("first Esc should clear the toast")
	}
	if mock.interruptCalled {
		t.Error("first Esc (toast up) must NOT interrupt the turn")
	}
	if cmd != nil {
		t.Errorf("first Esc should consume the key (nil cmd), got %T", cmd())
	}

	// Second Esc (no toast) interrupts the running turn.
	updated, cmd = app.Update(esc())
	app = updated.(AppModel)
	if !mock.interruptCalled {
		t.Error("second Esc should interrupt the running turn")
	}
	if cmd == nil {
		t.Fatal("second Esc should return an interrupt cmd")
	}
}

func TestAppModel_Esc_NoToast_StreamingStillInterrupts(t *testing.T) {
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)
	if !app.toasts.Empty() {
		t.Fatal("setup: no toast should be present")
	}

	updated, cmd := app.Update(esc())
	_ = updated
	if !mock.interruptCalled {
		t.Error("with no toast up, Esc during streaming must interrupt (unchanged)")
	}
	if cmd == nil {
		t.Fatal("Esc during streaming should return an interrupt cmd")
	}
}

func TestAppModel_Esc_ModalDismissBeforeToast(t *testing.T) {
	app := readyApp(t)
	app.showHelp = true
	app.toasts.Spawn(Toast{Text: "hi", Style: ToastInfo, DismissOn: UserOnlyDismiss()})

	updated, _ := app.Update(esc())
	app = updated.(AppModel)

	if app.showHelp {
		t.Error("Esc should dismiss the modal first")
	}
	if app.toasts.Empty() {
		t.Error("toast must survive the modal-dismiss Esc (ordering ii)")
	}
}
