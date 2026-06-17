package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func ctrlKey(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c, Mod: tea.ModCtrl} }

// TestCtrlU_Recall_WeaveOnly: Ctrl+U fires the bridge Recall when the observed
// agent is the root (weave), and is a no-op when a child is observed.
func TestCtrlU_Recall_WeaveOnly(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Observing root → recall fires.
	app.observedAgent = app.rootAgent
	updated, _ := app.Update(ctrlKey('u'))
	app = updated.(AppModel)
	if fake.recallCalls != 1 {
		t.Fatalf("recallCalls = %d, want 1 when observing root", fake.recallCalls)
	}

	// Observing a child → no-op.
	app.observedAgent = "some-child"
	_, _ = app.Update(ctrlKey('u'))
	if got := fake.recallCalls; got != 1 {
		t.Errorf("recallCalls = %d after child Ctrl+U, want still 1 (weave-only)", got)
	}
}

func TestCtrlU_Recall_NoopWhenModalUp(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.observedAgent = app.rootAgent
	app.showHelp = true

	updated, _ := app.Update(ctrlKey('u'))
	_ = updated
	if fake.recallCalls != 0 {
		t.Errorf("recallCalls = %d with a modal up, want 0", fake.recallCalls)
	}
}

func TestCtrlG_SendAllNow_WeaveOnly(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	app.observedAgent = app.rootAgent
	updated, _ := app.Update(ctrlKey('g'))
	app = updated.(AppModel)
	if fake.sendAllNowCalls != 1 {
		t.Fatalf("sendAllNowCalls = %d, want 1 when observing root", fake.sendAllNowCalls)
	}

	app.observedAgent = "some-child"
	_, _ = app.Update(ctrlKey('g'))
	if fake.sendAllNowCalls != 1 {
		t.Errorf("sendAllNowCalls = %d after child Ctrl+G, want still 1 (weave-only)", fake.sendAllNowCalls)
	}
}

func TestPromptsRecalledMsg_RehydratesInput(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	// Pre-existing single-slot pending submit must be cleared by a recall.
	app.pendingSubmit = "stale"
	app.input.SetPendingPreview("stale")

	updated, _ := app.Update(PromptsRecalledMsg{Text: "line one\nline two"})
	app = updated.(AppModel)

	if app.input.Value() != "line one\nline two" {
		t.Errorf("input value = %q, want rehydrated text", app.input.Value())
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit = %q, want cleared after recall", app.pendingSubmit)
	}
}

// TestPromptsRecalledMsg_EmptyPreservesPendingSubmit: an empty recall (nothing
// was pending) must NOT clobber a stashed pendingSubmit — that would silently
// drop the user's queued draft.
func TestPromptsRecalledMsg_EmptyPreservesPendingSubmit(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.pendingSubmit = "stashed draft"
	app.input.SetPendingPreview("stashed draft")

	updated, _ := app.Update(PromptsRecalledMsg{Text: ""})
	app = updated.(AppModel)

	if app.pendingSubmit != "stashed draft" {
		t.Errorf("pendingSubmit = %q, want preserved %q on empty recall", app.pendingSubmit, "stashed draft")
	}
}

func TestPromptsRecalledMsg_Error_Toasts(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(PromptsRecalledMsg{Text: "kept", Err: errors.New("cancel boom")})
	app = updated.(AppModel)

	// Text still rehydrated despite the error.
	if app.input.Value() != "kept" {
		t.Errorf("input value = %q, want %q even on error", app.input.Value(), "kept")
	}
	if app.toasts.Empty() {
		t.Error("expected an error toast on a partial recall failure")
	}
}

func TestSendAllNowResultMsg_Error_Toasts(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SendAllNowResultMsg{Err: errors.New("now boom")})
	app = updated.(AppModel)
	if app.toasts.Empty() {
		t.Error("expected an error toast on send-all-now failure")
	}
}

// TestQueuedIndicator_Lifecycle: a sent user prompt registers as queued; its
// consumption (isReplay) clears it; a cancellation clears it too.
func TestQueuedIndicator_Lifecycle(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1"})
	app = updated.(AppModel)
	updated, _ = app.Update(UserMessageSentMsg{UUID: "u2"})
	app = updated.(AppModel)
	if got := app.queuedUserCount(); got != 2 {
		t.Fatalf("queued count = %d, want 2", got)
	}

	// Consume u1 (sent).
	updated, _ = app.Update(UserMessageConsumedMsg{UUID: "u1"})
	app = updated.(AppModel)
	if got := app.queuedUserCount(); got != 1 {
		t.Errorf("queued count = %d after consume, want 1", got)
	}

	// Cancel u2.
	updated, _ = app.Update(UserMessageCancelledMsg{UUID: "u2"})
	app = updated.(AppModel)
	if got := app.queuedUserCount(); got != 0 {
		t.Errorf("queued count = %d after cancel, want 0", got)
	}
}

// TestQueuedIndicator_IgnoresSystemConsumed: a consumed event for a uuid the TUI
// never tracked (a system message) must not underflow / affect the count.
func TestQueuedIndicator_IgnoresSystemConsumed(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(UserMessageConsumedMsg{UUID: "system-uuid"})
	app = updated.(AppModel)
	if got := app.queuedUserCount(); got != 0 {
		t.Errorf("queued count = %d, want 0 (untracked uuid ignored)", got)
	}
}

func TestQueuedIndicator_RenderedInInput(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1"})
	app = updated.(AppModel)

	if !strings.Contains(app.input.View(), "queued") {
		t.Errorf("input view should surface a queued indicator, got:\n%s", app.input.View())
	}
}
