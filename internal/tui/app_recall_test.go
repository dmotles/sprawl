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

// QUM-830: a rapid double-tap of Ctrl+G must NOT launch a second concurrent
// SendAllNow. The first press marks send-all-now in-flight; a second press
// before the SendAllNowResultMsg lands is a no-op (no second goroutine that
// could race the runtime's cancel-and-replace cycle). The result message clears
// the in-flight latch so a later, deliberate Ctrl+G fires again.
func TestCtrlG_DoubleTap_Debounced(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.observedAgent = app.rootAgent

	// First Ctrl+G fires.
	updated, _ := app.Update(ctrlKey('g'))
	app = updated.(AppModel)
	if fake.sendAllNowCalls != 1 {
		t.Fatalf("sendAllNowCalls = %d after first Ctrl+G, want 1", fake.sendAllNowCalls)
	}

	// Second Ctrl+G before the result lands is debounced.
	updated, _ = app.Update(ctrlKey('g'))
	app = updated.(AppModel)
	if fake.sendAllNowCalls != 1 {
		t.Fatalf("sendAllNowCalls = %d after rapid second Ctrl+G, want 1 (debounced)", fake.sendAllNowCalls)
	}

	// The flush completes; the latch clears. (fakeSessionBackend.SendAllNow
	// increments sendAllNowCalls synchronously and the result msg is fed
	// directly here, so the count is an exact proxy for "a goroutine launched".)
	updated, _ = app.Update(SendAllNowResultMsg{})
	app = updated.(AppModel)

	// A subsequent deliberate Ctrl+G fires again.
	updated, _ = app.Update(ctrlKey('g'))
	app = updated.(AppModel)
	if fake.sendAllNowCalls != 2 {
		t.Errorf("sendAllNowCalls = %d after post-result Ctrl+G, want 2 (latch cleared)", fake.sendAllNowCalls)
	}

	// An ERRORED flush must also clear the latch — otherwise a single failed
	// send-all-now wedges Ctrl+G dead until session restart. (The reducer
	// early-returns on msg.Err, so the latch-clear must run on both legs.)
	updated, _ = app.Update(SendAllNowResultMsg{Err: errors.New("now boom")})
	app = updated.(AppModel)
	updated, _ = app.Update(ctrlKey('g'))
	app = updated.(AppModel)
	if fake.sendAllNowCalls != 3 {
		t.Errorf("sendAllNowCalls = %d after errored-result Ctrl+G, want 3 (latch must clear on error too)", fake.sendAllNowCalls)
	}
}

func TestPromptsRecalledMsg_RehydratesInput(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(PromptsRecalledMsg{Text: "line one\nline two"})
	app = updated.(AppModel)

	if app.input.Value() != "line one\nline two" {
		t.Errorf("input value = %q, want rehydrated text", app.input.Value())
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

// QUM-826: UserMessageConsumedMsg is pump-delivered (translated from
// EventUserMessageConsumed) and is the FIRST non-nil event of every typed
// turn. Its reducer MUST re-issue WaitForEvent or the bubbletea event pump
// parks before any assistant content is read, freezing live render while the
// session still persists. This pins the re-arm.
func TestAppModel_UserMessageConsumedMsg_RearmsPump(t *testing.T) {
	delegate := &continuousFakeDelegate{}
	app := readyAppWithBridge(t, delegate)

	// Register a tracked prompt, then reset the wait counter so the assertion
	// below isolates the consumed reducer's re-arm.
	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1"})
	app = updated.(AppModel)
	delegate.waitCalls = 0

	updated, cmd := app.Update(UserMessageConsumedMsg{UUID: "u1"})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("UserMessageConsumedMsg returned nil cmd; expected a WaitForEvent re-arm")
	}
	if !runCmdsForSentinel(t, cmd) {
		t.Error("UserMessageConsumedMsg cmd did not re-arm WaitForEvent (no sentinel produced)")
	}
	if delegate.waitCalls == 0 {
		t.Error("UserMessageConsumedMsg should call WaitForEvent on a continuous bridge; waitCalls = 0")
	}
	// Existing mutation must survive: the tracked prompt is cleared.
	if got := app.queuedUserCount(); got != 0 {
		t.Errorf("queued count = %d after consume, want 0", got)
	}
}

// QUM-826: UserMessageCancelledMsg is pump-delivered too (translated from
// EventUserMessageCancelled) and must re-arm the pump for the same reason.
func TestAppModel_UserMessageCancelledMsg_RearmsPump(t *testing.T) {
	delegate := &continuousFakeDelegate{}
	app := readyAppWithBridge(t, delegate)

	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1"})
	app = updated.(AppModel)
	delegate.waitCalls = 0

	updated, cmd := app.Update(UserMessageCancelledMsg{UUID: "u1"})
	app = updated.(AppModel)

	if cmd == nil {
		t.Fatal("UserMessageCancelledMsg returned nil cmd; expected a WaitForEvent re-arm")
	}
	if !runCmdsForSentinel(t, cmd) {
		t.Error("UserMessageCancelledMsg cmd did not re-arm WaitForEvent (no sentinel produced)")
	}
	if delegate.waitCalls == 0 {
		t.Error("UserMessageCancelledMsg should call WaitForEvent on a continuous bridge; waitCalls = 0")
	}
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

// QUM-833: the "⏳ N queued" indicator is retired; a queued user prompt now
// renders instantly as an inline bubble in the pending zone and is tracked by
// ZoneUserCount (which still drives the HasQueued recall/send-all-now bindings).
func TestQueuedPrompt_TrackedInZoneAndRendered(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1", Text: "queued prompt"})
	app = updated.(AppModel)

	if app.queuedUserCount() != 1 {
		t.Errorf("queuedUserCount = %d, want 1 (zone tracks the un-consumed prompt)", app.queuedUserCount())
	}
	out := stripAnsi(app.viewportFor(app.rootAgent).ChatList().Render(80))
	if !strings.Contains(out, "queued prompt") {
		t.Errorf("queued prompt should render instantly as an inline bubble; got:\n%s", out)
	}
}

// --- QUM-1112: preserved text from a failed send-all-now ---

// TestSendAllNowResultMsg_PreservedText_RestoredToInput answers AC 2's "state
// which" at the TUI layer: text the runtime cancelled but could not send is
// restored to the INPUT, which is the only surface left after the pending-zone
// bubble was dropped.
func TestSendAllNowResultMsg_PreservedText_RestoredToInput(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	updated, _ := app.Update(SendAllNowResultMsg{Err: errors.New("now boom"), PreservedText: "A\nB"})
	app = updated.(AppModel)

	if app.input.Value() != "A\nB" {
		t.Errorf("input value = %q, want the preserved text %q restored — otherwise the typed prompt is unreachable (QUM-1112)", app.input.Value(), "A\nB")
	}
	if app.sendAllNowInFlight {
		t.Error("sendAllNowInFlight still latched after an errored result")
	}
}

// TestSendAllNowResultMsg_PreservedText_DoesNotClobberTypedInput: restoring must
// not itself destroy text — anything typed during the in-flight flush is kept,
// with the preserved text prepended (it was submitted first).
func TestSendAllNowResultMsg_PreservedText_DoesNotClobberTypedInput(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.input.SetValue("typed")

	updated, _ := app.Update(SendAllNowResultMsg{Err: errors.New("now boom"), PreservedText: "A"})
	app = updated.(AppModel)

	if got := app.input.Value(); got != "A\ntyped" {
		t.Errorf("input value = %q, want %q — neither the preserved nor the concurrently-typed text may be lost", got, "A\ntyped")
	}
}

// TestSendAllNowResultMsg_PreservedText_Empty_LeavesInputUntouched is the
// over-fix negative: a naive `SetValue(preserved + "\n" + old)` passes the two
// tests above while injecting a stray leading newline into the user's input on
// every success and every bare failure.
func TestSendAllNowResultMsg_PreservedText_Empty_LeavesInputUntouched(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  SendAllNowResultMsg
	}{
		{name: "success", msg: SendAllNowResultMsg{}},
		{name: "bare failure", msg: SendAllNowResultMsg{Err: errors.New("now boom")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeSessionBackend()
			m := newTestAppModelWithBridge(t, fake)
			resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			app := resized.(AppModel)
			app.input.SetValue("half-typed thought")

			updated, _ := app.Update(tc.msg)
			app = updated.(AppModel)

			if got := app.input.Value(); got != "half-typed thought" {
				t.Errorf("input value = %q, want it byte-identical — nothing was preserved, so nothing may be injected", got)
			}
		})
	}
}

// TestSendAllNowResultMsg_ToastDistinguishesPreserved is AC 3: the error surface
// must distinguish "failed, your text is preserved in the input" from a bare
// failure. Asserted on the specific claim the preserved branch commits to — a
// bare `bare != preserved` delta is satisfied by appending a space — plus the
// complement, that the bare branch does NOT make that claim.
func TestSendAllNowResultMsg_ToastDistinguishesPreserved(t *testing.T) {
	boom := errors.New("now boom")

	toastText := func(t *testing.T, msg SendAllNowResultMsg) string {
		t.Helper()
		fake := newFakeSessionBackend()
		m := newTestAppModelWithBridge(t, fake)
		resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		app := resized.(AppModel)
		updated, _ := app.Update(msg)
		app = updated.(AppModel)
		got := app.toasts.Toasts()
		if len(got) != 1 {
			t.Fatalf("toasts = %d for %+v, want exactly 1", len(got), msg)
		}
		return got[0].Text
	}

	bare := toastText(t, SendAllNowResultMsg{Err: boom})
	preserved := toastText(t, SendAllNowResultMsg{Err: boom, PreservedText: "A"})

	// The claim the user must be able to act on: their text is back in the input.
	if !strings.Contains(preserved, "restored to the input") {
		t.Errorf("preserved-branch toast = %q, want it to tell the user their text was restored to the input (QUM-1112 AC 3)", preserved)
	}
	if strings.Contains(bare, "restored to the input") {
		t.Errorf("bare-failure toast = %q, must NOT claim text was restored — nothing was preserved", bare)
	}
	if bare == preserved {
		t.Errorf("toast text is identical with and without preserved text (%q)", bare)
	}
}
