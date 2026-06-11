package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
)

// applyResize sends a 120x40 WindowSizeMsg and returns the resulting AppModel.
func applyResize(t *testing.T, m AppModel) AppModel {
	t.Helper()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return updated.(AppModel)
}

func sendMsg(t *testing.T, app AppModel, msg tea.Msg) AppModel {
	t.Helper()
	updated, _ := app.Update(msg)
	return updated.(AppModel)
}

func TestApp_ToastSpawnMsg_AppearsInView(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "recovery complete", Style: ToastInfo, DismissOn: UserOnlyDismiss()},
	})
	v := app.View()
	if !strings.Contains(ansi.Strip(v.Content), "recovery complete") {
		t.Errorf("View().Content should contain 'recovery complete'; got:\n%s", v.Content)
	}
}

func TestApp_CtrlT_DismissesAllToasts(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "toast-AAA", DismissOn: UserOnlyDismiss()},
	})
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "toast-BBB", DismissOn: UserOnlyDismiss()},
	})

	app = sendMsg(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})

	if !app.toasts.Empty() {
		t.Errorf("Ctrl+T should clear all toasts; Toasts()=%d", len(app.toasts.Toasts()))
	}
	v := app.View()
	stripped := ansi.Strip(v.Content)
	if strings.Contains(stripped, "toast-AAA") || strings.Contains(stripped, "toast-BBB") {
		t.Errorf("toasts remain in view after Ctrl+T; content:\n%s", v.Content)
	}
}

func TestApp_ToastDismissMsg_RemovesOne(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{ID: "a", Text: "toast-AAA", DismissOn: UserOnlyDismiss()},
	})
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{ID: "b", Text: "toast-BBB", DismissOn: UserOnlyDismiss()},
	})

	app = sendMsg(t, app, ToastDismissMsg{ID: "a"})
	stripped := ansi.Strip(app.View().Content)
	if strings.Contains(stripped, "toast-AAA") {
		t.Error("toast 'a' should have been removed by ToastDismissMsg")
	}
	if !strings.Contains(stripped, "toast-BBB") {
		t.Errorf("toast 'b' should still be present; content:\n%s", stripped)
	}
}

func TestApp_ToastConditionClearedMsg_Removes(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "event-toast", DismissOn: ConditionDismiss("event1")},
	})
	app = sendMsg(t, app, ToastConditionClearedMsg{ID: "event1"})
	if !app.toasts.Empty() {
		t.Errorf("ToastConditionClearedMsg('event1') should remove matching toast; left %d", len(app.toasts.Toasts()))
	}
	if strings.Contains(ansi.Strip(app.View().Content), "event-toast") {
		t.Error("event-toast should not appear in view after condition clear")
	}
}

func TestApp_ToastTimerMsg_AutoRemoves(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "timed-toast", DismissOn: TimerDismiss(50 * time.Millisecond)},
	})
	toasts := app.toasts.Toasts()
	if len(toasts) != 1 {
		t.Fatalf("setup: expected 1 toast, got %d", len(toasts))
	}
	id := toasts[0].ID
	if id == "" {
		t.Fatal("setup: spawned toast should have an auto-assigned ID")
	}

	app = sendMsg(t, app, toastTimerMsg{ID: id})
	if !app.toasts.Empty() {
		t.Errorf("toastTimerMsg should remove the matching toast; left %d", len(app.toasts.Toasts()))
	}
}

func TestApp_TwoToastsCoexistAndDismissIndependently(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "survivor-XYZ", DismissOn: UserOnlyDismiss()},
	})
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "doomed-PQR", DismissOn: ConditionDismiss("ev")},
	})

	stripped := ansi.Strip(app.View().Content)
	if !strings.Contains(stripped, "survivor-XYZ") || !strings.Contains(stripped, "doomed-PQR") {
		t.Fatalf("both toasts should be visible; got:\n%s", stripped)
	}

	app = sendMsg(t, app, ToastConditionClearedMsg{ID: "ev"})
	stripped = ansi.Strip(app.View().Content)
	if strings.Contains(stripped, "doomed-PQR") {
		t.Errorf("doomed-PQR should have been cleared; got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "survivor-XYZ") {
		t.Errorf("survivor-XYZ should still be present; got:\n%s", stripped)
	}

	app = sendMsg(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	stripped = ansi.Strip(app.View().Content)
	if strings.Contains(stripped, "survivor-XYZ") {
		t.Errorf("Ctrl+T should have removed survivor-XYZ; got:\n%s", stripped)
	}
	if !app.toasts.Empty() {
		t.Errorf("toasts list should be empty after Ctrl+T; got %d", len(app.toasts.Toasts()))
	}
}

func TestApp_ModalOverlaysToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "hidden-by-modal", DismissOn: UserOnlyDismiss()},
	})
	app = sendMsg(t, app, ToggleHelpMsg{})
	if !app.showHelp {
		t.Fatal("setup: showHelp should be true after ToggleHelpMsg")
	}

	stripped := ansi.Strip(app.View().Content)
	if strings.Contains(stripped, "hidden-by-modal") {
		t.Errorf("modal must fully replace content — toast should not bleed through; got:\n%s", stripped)
	}
}

// --- QUM-651: lifecycle toast consumers (recovery, interrupt, fault) ---

// TestApp_AgentsResumedMsg_SpawnsRecoveryToast verifies that when the
// runEnter startup scan resumes N>0 child agents, the TUI surfaces an Info
// toast "recovered N agents" with a 5s timer-dismiss (QUM-651 consumer #1).
func TestApp_AgentsResumedMsg_SpawnsRecoveryToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	updated, cmd := app.Update(AgentsResumedMsg{Resumed: 3, Failed: 0})
	app = updated.(AppModel)

	toasts := app.toasts.Toasts()
	if len(toasts) != 1 {
		t.Fatalf("expected 1 toast after AgentsResumedMsg{Resumed:3}, got %d", len(toasts))
	}
	got := toasts[0]
	if got.Text != "recovered 3 agents" {
		t.Errorf("toast.Text = %q, want %q", got.Text, "recovered 3 agents")
	}
	if got.Style != ToastInfo {
		t.Errorf("toast.Style = %v, want ToastInfo", got.Style)
	}
	if got.DismissOn.Kind != DismissTimer || got.DismissOn.Timer != 5*time.Second {
		t.Errorf("toast.DismissOn = %+v, want TimerDismiss(5s)", got.DismissOn)
	}
	if cmd == nil {
		t.Error("AgentsResumedMsg with Resumed>0 should return a non-nil cmd (timer tick)")
	}
}

// TestApp_AgentsResumedMsg_ZeroResumed_NoToast verifies the early-return
// path: a 0/0 msg is silent (no toast, existing transient-label behavior
// unchanged).
func TestApp_AgentsResumedMsg_ZeroResumed_NoToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, AgentsResumedMsg{Resumed: 0, Failed: 0})
	if !app.toasts.Empty() {
		t.Errorf("AgentsResumedMsg{0,0} should not spawn a toast; got %d", len(app.toasts.Toasts()))
	}
}

// TestApp_AgentsResumedMsg_FailedOnly_NoToast verifies that a startup with
// only failures (no successful resumes) does NOT spawn a recovery toast —
// the spec only spawns on Resumed>0.
func TestApp_AgentsResumedMsg_FailedOnly_NoToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, AgentsResumedMsg{Resumed: 0, Failed: 2})
	if !app.toasts.Empty() {
		t.Errorf("AgentsResumedMsg{0,2} should not spawn a recovery toast; got %d", len(app.toasts.Toasts()))
	}
}

// TestApp_BackendFaultMsg_SpawnsErrorToast verifies that a backend fault
// produces an Error toast "<agent> faulted: <reason>" with a
// Condition("fault-<agent>") dismissal (QUM-651 consumer #3).
func TestApp_BackendFaultMsg_SpawnsErrorToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, BackendFaultMsg{
		Agent:  "alpha",
		Class:  "HangTimeout",
		Reason: "stalled",
	})
	toasts := app.toasts.Toasts()
	if len(toasts) != 1 {
		t.Fatalf("expected 1 toast after BackendFaultMsg, got %d", len(toasts))
	}
	got := toasts[0]
	if got.Text != "alpha faulted: stalled" {
		t.Errorf("toast.Text = %q, want %q", got.Text, "alpha faulted: stalled")
	}
	if got.Style != ToastError {
		t.Errorf("toast.Style = %v, want ToastError", got.Style)
	}
	if got.DismissOn.Kind != DismissCondition || got.DismissOn.Condition != "fault-alpha" {
		t.Errorf("toast.DismissOn = %+v, want ConditionDismiss(\"fault-alpha\")", got.DismissOn)
	}
	// Regression guard: the per-agent fault sticker must still be set
	// (the tree-row FAULT badge is the persistent surface; the toast is
	// the transient surface).
	if _, ok := app.faults["alpha"]; !ok {
		t.Errorf("BackendFaultMsg should still populate m.faults[\"alpha\"]; got faults=%+v", app.faults)
	}
}

// TestApp_BackendFaultClearedMsg_ClearsFaultToast verifies that on
// in-place recovery the fault toast is removed (condition-dismissed).
func TestApp_BackendFaultClearedMsg_ClearsFaultToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, BackendFaultMsg{Agent: "alpha", Class: "X", Reason: "boom"})
	if app.toasts.Empty() {
		t.Fatalf("setup: fault toast should be present")
	}
	app = sendMsg(t, app, BackendFaultClearedMsg{Agent: "alpha"})
	if !app.toasts.Empty() {
		t.Errorf("BackendFaultClearedMsg should clear matching fault toast; got %d", len(app.toasts.Toasts()))
	}
	// Existing behavior preserved: the sticker is dropped.
	if _, ok := app.faults["alpha"]; ok {
		t.Errorf("BackendFaultClearedMsg should drop m.faults[\"alpha\"]; got %+v", app.faults)
	}
}

// TestApp_BackendFaultClearedMsg_NoPriorFault_SuppressesRecoveryLabel verifies
// that the retire-driven fault-clear path (QUM-776) does NOT set the
// "backend recovered on X" transient label — that label is recovery-specific
// and is misleading when the agent is gone. The reducer must gate that label
// (and the child-adapter rebuild) on whether m.faults[agent] was actually
// populated before the clear.
func TestApp_BackendFaultClearedMsg_NoPriorFault_SuppressesRecoveryLabel(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	// No prior BackendFaultMsg — m.faults is empty for "alpha".
	app = sendMsg(t, app, BackendFaultClearedMsg{Agent: "alpha"})
	if got := app.statusBar.TransientLabel(); strings.Contains(got, "backend recovered on alpha") {
		t.Errorf("fault-clear without prior fault should NOT set recovery transient label; got %q", got)
	}
}

// TestApp_BackendFaultClearedMsg_PerAgent verifies that clearing one
// agent's fault does NOT remove another agent's fault toast.
func TestApp_BackendFaultClearedMsg_PerAgent(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, BackendFaultMsg{Agent: "alpha", Reason: "a"})
	app = sendMsg(t, app, BackendFaultMsg{Agent: "beta", Reason: "b"})
	if len(app.toasts.Toasts()) != 2 {
		t.Fatalf("setup: expected 2 fault toasts, got %d", len(app.toasts.Toasts()))
	}
	app = sendMsg(t, app, BackendFaultClearedMsg{Agent: "alpha"})
	toasts := app.toasts.Toasts()
	if len(toasts) != 1 {
		t.Fatalf("expected 1 surviving toast, got %d", len(toasts))
	}
	if toasts[0].DismissOn.Condition != "fault-beta" {
		t.Errorf("survivor toast condition = %q, want %q", toasts[0].DismissOn.Condition, "fault-beta")
	}
}

// TestApp_Esc_SpawnsInterruptToast verifies the Esc-during-turn key path
// also spawns an Info toast "interrupt sent to <agent>" with
// Condition("interrupt-<agent>") dismissal (QUM-651 consumer #2 spawn).
func TestApp_Esc_SpawnsInterruptToast(t *testing.T) {
	mock := newFakeSessionBackend()
	app := newTestAppModelWithBridge(t, mock)
	app = applyResize(t, app)
	app.turnState = TurnStreaming

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	toasts := app.toasts.Toasts()
	if len(toasts) != 1 {
		t.Fatalf("expected 1 toast after Esc during streaming, got %d", len(toasts))
	}
	got := toasts[0]
	wantText := "interrupt sent to " + app.rootAgent
	if got.Text != wantText {
		t.Errorf("toast.Text = %q, want %q", got.Text, wantText)
	}
	if got.Style != ToastInfo {
		t.Errorf("toast.Style = %v, want ToastInfo", got.Style)
	}
	// QUM-697: interrupt toast now auto-dismisses on a 2s timer so it remains
	// visible regardless of how fast the InterruptResultMsg ack lands.
	if got.DismissOn.Kind != DismissTimer || got.DismissOn.Timer != 2*time.Second {
		t.Errorf("toast.DismissOn = %+v, want TimerDismiss(2s)", got.DismissOn)
	}
}

// TestApp_Esc_InterruptToast_OnlyDuringActiveTurn verifies that Esc when
// idle does NOT spawn the interrupt toast.
func TestApp_Esc_InterruptToast_OnlyDuringActiveTurn(t *testing.T) {
	mock := newFakeSessionBackend()
	app := newTestAppModelWithBridge(t, mock)
	app = applyResize(t, app)
	// turnState defaults to TurnIdle.

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if !app.toasts.Empty() {
		t.Errorf("Esc during idle should NOT spawn an interrupt toast; got %d", len(app.toasts.Toasts()))
	}
}

// TestApp_InterruptResultMsg_NoErr_KeepsInterruptToast verifies that the
// supervisor-side ack (Err==nil) does NOT clear the interrupt toast. Under
// QUM-697 the toast auto-dismisses on a 2s timer; clearing it on the ack
// would race the first paint and the user would never see it.
func TestApp_InterruptResultMsg_NoErr_KeepsInterruptToast(t *testing.T) {
	mock := newFakeSessionBackend()
	app := newTestAppModelWithBridge(t, mock)
	app = applyResize(t, app)
	app.turnState = TurnStreaming

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.toasts.Empty() {
		t.Fatalf("setup: interrupt toast should be present")
	}
	id := app.toasts.Toasts()[0].ID

	app = sendMsg(t, app, InterruptResultMsg{Err: nil})
	if app.toasts.Empty() {
		t.Errorf("InterruptResultMsg{Err:nil} must NOT clear interrupt toast (QUM-697); timer-dismiss only")
	}

	// And the timer-dismiss path still removes it.
	app = sendMsg(t, app, toastTimerMsg{ID: id})
	if !app.toasts.Empty() {
		t.Errorf("toastTimerMsg should remove the interrupt toast; got %d", len(app.toasts.Toasts()))
	}
}

// TestApp_InterruptResultMsg_WithErr_KeepsInterruptToast verifies that a
// failed interrupt-write leaves the toast in place so the user knows the
// interrupt did NOT land.
func TestApp_InterruptResultMsg_WithErr_KeepsInterruptToast(t *testing.T) {
	mock := newFakeSessionBackend()
	app := newTestAppModelWithBridge(t, mock)
	app = applyResize(t, app)
	app.turnState = TurnStreaming

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)
	if app.toasts.Empty() {
		t.Fatalf("setup: interrupt toast should be present")
	}

	app = sendMsg(t, app, InterruptResultMsg{Err: fmt.Errorf("write failed")})
	if app.toasts.Empty() {
		t.Errorf("InterruptResultMsg{Err:!=nil} should NOT clear interrupt toast")
	}
}

func TestApp_ResizeReanchorsToast(t *testing.T) {
	m := newTestAppModel(t)
	app := applyResize(t, m)
	app = sendMsg(t, app, ToastSpawnMsg{
		Toast: Toast{Text: "anchored", DismissOn: UserOnlyDismiss()},
	})
	// Send a different size to force re-anchoring.
	updated, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	app = updated.(AppModel)

	lines := strings.Split(app.View().Content, "\n")
	foundRow := -1
	for i, ln := range lines {
		if strings.Contains(ansi.Strip(ln), "anchored") {
			foundRow = i
			break
		}
	}
	if foundRow < 0 {
		t.Fatalf("'anchored' not present after resize; content:\n%s", app.View().Content)
	}
	stripped := ansi.Strip(lines[foundRow])
	// QUM-804: toasts are left-aligned with a 2-col left margin. On any width
	// the box hugs the left edge: 2 base cols, then the box border + pad.
	if !strings.HasPrefix(stripped, "  │ anchored") {
		t.Errorf("after resize to width=160, 'anchored' not left-aligned at margin 2 in row: %q", stripped)
	}
}
