package tui

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-728: Ctrl+\ triggers the incident snapshot request. The key handler
// emits IncidentSnapshotRequestedMsg; the AppModel reducer then dispatches
// the configured snapshotCmd.
func TestAppModel_CtrlBackslashTriggersSnapshotCmd(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.SetSnapshotCmd(func() tea.Msg {
		return IncidentSnapshotCompleteMsg{Path: "/tmp/snap"}
	})

	updated, cmd := app.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	_ = updated
	if cmd == nil {
		t.Fatal("Ctrl+\\ should return a non-nil cmd emitting IncidentSnapshotRequestedMsg")
	}
	msg := cmd()
	if _, ok := msg.(IncidentSnapshotRequestedMsg); !ok {
		t.Errorf("cmd() = %T, want IncidentSnapshotRequestedMsg", msg)
	}
}

// QUM-728: IncidentSnapshotRequestedMsg invokes the configured snapshotCmd
// and surfaces a "capturing" transient label.
func TestAppModel_IncidentSnapshotRequestedRunsConfiguredCmd(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	var invoked atomic.Int32
	app.SetSnapshotCmd(func() tea.Msg {
		invoked.Add(1)
		return IncidentSnapshotCompleteMsg{Path: "/tmp/x"}
	})

	updated, cmd := app.Update(IncidentSnapshotRequestedMsg{})
	app = updated.(AppModel)
	if cmd == nil {
		t.Fatal("IncidentSnapshotRequestedMsg should return a cmd")
	}
	if got := cmd(); got == nil {
		t.Fatal("cmd() returned nil msg")
	} else if c, ok := got.(IncidentSnapshotCompleteMsg); !ok {
		t.Errorf("cmd() = %T, want IncidentSnapshotCompleteMsg", got)
	} else if c.Path != "/tmp/x" {
		t.Errorf("complete.Path=%q, want /tmp/x", c.Path)
	}
	if invoked.Load() != 1 {
		t.Errorf("snapshotCmd invocations=%d, want 1", invoked.Load())
	}
	if !strings.Contains(strings.ToLower(app.statusBar.TransientLabel()), "capturing") {
		t.Errorf("transient label should mention 'capturing'; got %q", app.statusBar.TransientLabel())
	}
}

// QUM-728: When no snapshotCmd is wired, IncidentSnapshotRequestedMsg yields
// a synthetic IncidentSnapshotCompleteMsg carrying an error.
func TestAppModel_IncidentSnapshotRequestedWithoutCmdReturnsErrorComplete(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	_, cmd := app.Update(IncidentSnapshotRequestedMsg{})
	if cmd == nil {
		t.Fatal("expected non-nil cmd carrying error-complete fallback")
	}
	got := cmd()
	complete, ok := got.(IncidentSnapshotCompleteMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want IncidentSnapshotCompleteMsg", got)
	}
	if complete.Err == nil {
		t.Error("expected non-nil Err when snapshotCmd is not configured")
	}
}

// QUM-728: success path puts the path into the transient label.
func TestAppModel_IncidentSnapshotCompleteSuccessUpdatesStatusBar(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(IncidentSnapshotCompleteMsg{Path: "/foo/bar"})
	app = updated.(AppModel)
	label := app.statusBar.TransientLabel()
	if !strings.Contains(label, "/foo/bar") {
		t.Errorf("transient label %q should contain '/foo/bar'", label)
	}
}

// QUM-728: error path spawns a toast and surfaces a "snapshot failed"
// transient label.
func TestAppModel_IncidentSnapshotCompleteErrorSpawnsToast(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(IncidentSnapshotCompleteMsg{Err: errors.New("boom")})
	app = updated.(AppModel)

	if app.toasts.Empty() {
		t.Error("expected a toast to be spawned on snapshot failure")
	}
	label := app.statusBar.TransientLabel()
	if !strings.Contains(strings.ToLower(label), "snapshot failed") {
		t.Errorf("transient label %q should mention 'snapshot failed'", label)
	}
	// Toast text should mention the underlying error.
	var found bool
	for _, ts := range app.toasts.Toasts() {
		if strings.Contains(ts.Text, "boom") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a toast carrying 'boom'; got toasts: %+v", app.toasts.Toasts())
	}
}

// QUM-728: Ctrl+\ is suppressed while a modal is up — mirrors the modal
// precedence applied to other Ctrl-hotkey handlers (Ctrl+L etc.).
func TestAppModel_CtrlBackslashSuppressedByModal(t *testing.T) {
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	var invoked atomic.Int32
	app.SetSnapshotCmd(func() tea.Msg {
		invoked.Add(1)
		return IncidentSnapshotCompleteMsg{Path: "/x"}
	})
	app.showHelp = true

	_, cmd := app.Update(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	// Drain cmd (if any) — but it must NOT be the snapshot request that runs
	// the configured cmd downstream. Easiest assertion: keep invoked at 0.
	if cmd != nil {
		// Invoke once — if it returns IncidentSnapshotRequestedMsg, the modal
		// precedence is broken.
		if got := cmd(); got != nil {
			if _, isReq := got.(IncidentSnapshotRequestedMsg); isReq {
				t.Error("Ctrl+\\ leaked past help modal — emitted IncidentSnapshotRequestedMsg")
			}
		}
	}
	if invoked.Load() != 0 {
		t.Errorf("snapshotCmd was invoked %d times despite open help modal", invoked.Load())
	}
}
