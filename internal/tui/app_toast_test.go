package tui

import (
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
	idx := strings.Index(stripped, "anchored")
	// On a 160-col terminal, right-anchored content should sit past the
	// midpoint of the line.
	if idx < 80 {
		t.Errorf("after resize to width=160, 'anchored' at col %d, want right-side (>= 80) in row: %q", idx, stripped)
	}
}
