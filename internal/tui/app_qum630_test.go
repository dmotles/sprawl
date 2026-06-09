// QUM-630 TDD tests: Esc on a queued message preempts (interrupt + send),
// Ctrl+C on a queued message recalls/clears. These tests are written before
// implementation lands and are EXPECTED TO FAIL in the red phase.
//
// Behaviour matrix:
//
//   Msg queued + turn active : Esc -> InterruptAndSend(queued)
//                              Ctrl+C -> recall queued to prompt (or clear pending)
//   Msg queued + idle        : Esc -> SubmitMsg{queued}
//                              Ctrl+C -> recall queued to prompt
//   No queue + turn active   : Esc -> Interrupt (unchanged, QUM-380)
//                              Ctrl+C -> clear prompt if text, else quit-confirm
//
// Invariants encoded by these tests:
//   - When the interrupt path is rejected (interruptAndSendErr non-nil), the
//     TUI MUST still call InterruptAndSend exactly once with the queued text
//     — the adapter owns the enqueue-on-failure fallback (QUM-619 contract).
//   - History gains the queued text on Esc-preempt via m.history.Append.
//   - Ctrl+C with queued + non-empty input never clobbers the in-progress
//     input; it only drops the queue.

package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// queuedStreamingApp returns a ready, sized AppModel mid-turn (TurnStreaming)
// with a pre-staged pendingSubmit so each test starts from the same point.
func queuedStreamingApp(t *testing.T, mock *fakeSessionBackend, queued string) AppModel {
	t.Helper()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)
	app.pendingSubmit = queued
	app.input.SetPendingPreview(queued)
	return app
}

func queuedThinkingApp(t *testing.T, mock *fakeSessionBackend, queued string) AppModel {
	t.Helper()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnThinking
	app.statusBar.SetTurnState(TurnThinking)
	app.pendingSubmit = queued
	app.input.SetPendingPreview(queued)
	return app
}

func queuedIdleApp(t *testing.T, mock *fakeSessionBackend, queued string) AppModel {
	t.Helper()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnIdle
	app.pendingSubmit = queued
	app.input.SetPendingPreview(queued)
	return app
}

// findSubmitMsg invokes a cmd (possibly a tea.Batch) and reports the first
// SubmitMsg encountered. Mirrors batchContainsInterruptResult but for the
// idle-Esc-preempt path.
func findSubmitMsg(cmd tea.Cmd) (SubmitMsg, bool) {
	if cmd == nil {
		return SubmitMsg{}, false
	}
	msg := cmd()
	if sm, ok := msg.(SubmitMsg); ok {
		return sm, true
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return SubmitMsg{}, false
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		inner := c()
		if sm, ok := inner.(SubmitMsg); ok {
			return sm, true
		}
	}
	return SubmitMsg{}, false
}

// --- Esc behaviour ----------------------------------------------------------

func TestEsc_QueuedAndStreaming_CallsInterruptAndSend(t *testing.T) {
	mock := newFakeSessionBackend()
	app := queuedStreamingApp(t, mock, "preempt me")

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if mock.interruptAndSendCalls != 1 {
		t.Errorf("InterruptAndSend should be called exactly once, got %d", mock.interruptAndSendCalls)
	}
	if mock.lastInterruptAndSendText != "preempt me" {
		t.Errorf("InterruptAndSend text = %q, want %q", mock.lastInterruptAndSendText, "preempt me")
	}
	if mock.interruptCalled {
		t.Error("plain Interrupt must NOT be called on Esc-preempt; InterruptAndSend supplants it")
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared after Esc-preempt, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("preview should be cleared after Esc-preempt, got %q", app.input.PendingPreview())
	}
	if cmd == nil {
		t.Fatal("Esc-preempt must return a cmd (InterruptAndSend + transient label)")
	}
	// Loose assertion: a transient "Interrupting" label spawns on the statusbar.
	if !strings.Contains(stripAnsi(app.statusBar.View()), "Interrupt") {
		t.Errorf("statusbar should carry a transient interrupt label after Esc-preempt, got %q", stripAnsi(app.statusBar.View()))
	}
	// History MUST gain the queued text so the user can recall it via Up.
	if app.history == nil {
		t.Fatal("history must be initialized")
	}
	found := false
	for i := 0; i < app.history.Len(); i++ {
		if app.history.At(i) == "preempt me" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("history must contain the preempted text %q after Esc-preempt", "preempt me")
	}
}

func TestEsc_QueuedAndThinking_CallsInterruptAndSend(t *testing.T) {
	mock := newFakeSessionBackend()
	app := queuedThinkingApp(t, mock, "go now")

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if mock.interruptAndSendCalls != 1 {
		t.Errorf("InterruptAndSend should be called exactly once during thinking, got %d", mock.interruptAndSendCalls)
	}
	if mock.lastInterruptAndSendText != "go now" {
		t.Errorf("InterruptAndSend text = %q, want %q", mock.lastInterruptAndSendText, "go now")
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared, got %q", app.pendingSubmit)
	}
	// History MUST gain the queued text on the thinking branch as well, so
	// Up-arrow recall works identically regardless of streaming vs thinking
	// turn state at preempt time.
	if app.history == nil {
		t.Fatal("history must be initialized")
	}
	found := false
	for i := 0; i < app.history.Len(); i++ {
		if app.history.At(i) == "go now" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("history must contain the preempted text %q after Esc-preempt while thinking", "go now")
	}
}

func TestEsc_QueuedAndIdle_DispatchesSubmitMsg(t *testing.T) {
	mock := newFakeSessionBackend()
	app := queuedIdleApp(t, mock, "send it")

	updated, cmd := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if mock.interruptAndSendCalls != 0 {
		t.Errorf("InterruptAndSend must NOT be called when idle, got %d calls", mock.interruptAndSendCalls)
	}
	if mock.interruptCalled {
		t.Error("plain Interrupt must NOT be called when idle")
	}
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared after Esc-idle-submit, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("preview should be cleared, got %q", app.input.PendingPreview())
	}
	if cmd == nil {
		t.Fatal("Esc with queued + idle must return a cmd dispatching SubmitMsg")
	}
	sm, ok := findSubmitMsg(cmd)
	if !ok {
		t.Fatalf("expected SubmitMsg in returned cmd; got msg %T", cmd())
	}
	if sm.Text != "send it" {
		t.Errorf("SubmitMsg.Text = %q, want %q", sm.Text, "send it")
	}
}

func TestEsc_NoQueueStreaming_PlainInterrupt(t *testing.T) {
	// QUM-380 regression: with nothing queued and an active turn, Esc still
	// calls the plain Interrupt path (NOT InterruptAndSend).
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.turnState = TurnStreaming
	app.statusBar.SetTurnState(TurnStreaming)

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	_ = updated

	if !mock.interruptCalled {
		t.Error("plain Interrupt must still be called when nothing is queued (QUM-380)")
	}
	if mock.interruptAndSendCalls != 0 {
		t.Errorf("InterruptAndSend must NOT be called when nothing is queued, got %d calls", mock.interruptAndSendCalls)
	}
}

func TestEsc_QueuedRejectedInterrupt_AdapterStillEnqueues(t *testing.T) {
	// Contract: even when the preempt fails, the TUI calls InterruptAndSend
	// exactly once. The adapter is responsible for enqueuing the prompt as a
	// ClassInterrupt queue item regardless of whether Session.Interrupt
	// succeeds (see QUM-619 + ForceInterruptForDelivery). The TUI does NOT
	// re-stash on its own.
	mock := newFakeSessionBackend()
	mock.interruptAndSendErr = errInjected{}
	app := queuedStreamingApp(t, mock, "keep me")

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if mock.interruptAndSendCalls != 1 {
		t.Errorf("InterruptAndSend should still be called once on rejected interrupt, got %d", mock.interruptAndSendCalls)
	}
	if mock.lastInterruptAndSendText != "keep me" {
		t.Errorf("InterruptAndSend text = %q, want %q", mock.lastInterruptAndSendText, "keep me")
	}
	// pendingSubmit cleared in TUI; the queued text now lives in the adapter
	// queue. The user has the recall via history (Up arrow).
	if app.pendingSubmit != "" {
		t.Errorf("pendingSubmit should be cleared by the TUI even on rejected preempt; adapter owns the enqueue, got %q", app.pendingSubmit)
	}
	// Even when the interrupt was rejected, the queued text MUST land in
	// history so the user can recall it via Up-arrow as the "text never
	// lost" fallback path (independent of the adapter's enqueue ownership).
	if app.history == nil {
		t.Fatal("history must be initialized")
	}
	found := false
	for i := 0; i < app.history.Len(); i++ {
		if app.history.At(i) == "keep me" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("history must contain the preempted text %q even when interrupt was rejected (Up-arrow recall fallback)", "keep me")
	}
}

// errInjected is a sentinel error used to drive the rejected-interrupt branch.
type errInjected struct{}

func (errInjected) Error() string { return "injected interrupt failure" }

// --- Ctrl+C behaviour -------------------------------------------------------

func TestCtrlC_QueuedAndEmptyInput_RecallsToPrompt(t *testing.T) {
	mock := newFakeSessionBackend()
	app := queuedStreamingApp(t, mock, "edit me")
	app.input.SetValue("") // empty input

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if app.showConfirm {
		t.Error("Ctrl+C with queued msg + empty input must NOT show quit confirm")
	}
	if app.pendingSubmit != "" {
		t.Errorf("Ctrl+C should clear pendingSubmit, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("preview should be cleared, got %q", app.input.PendingPreview())
	}
	if got := app.input.Value(); got != "edit me" {
		t.Errorf("Ctrl+C with queued + empty input should recall to prompt, got input=%q want %q", got, "edit me")
	}
	if mock.interruptCalled || mock.interruptAndSendCalls != 0 {
		t.Error("Ctrl+C recall must NOT call any interrupt path")
	}
}

func TestCtrlC_QueuedAndNonEmptyInput_ClearsQueueOnly(t *testing.T) {
	// Refuse-to-clobber: the user is mid-composition. Ctrl+C only drops the
	// queue; the input buffer is left alone. Quit-confirm is NOT shown.
	mock := newFakeSessionBackend()
	app := queuedStreamingApp(t, mock, "queued thing")
	app.input.SetValue("composing more")

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if app.showConfirm {
		t.Error("Ctrl+C with queued msg + non-empty input must NOT show quit confirm")
	}
	if app.pendingSubmit != "" {
		t.Errorf("Ctrl+C should clear pendingSubmit, got %q", app.pendingSubmit)
	}
	if app.input.PendingPreview() != "" {
		t.Errorf("preview should be cleared, got %q", app.input.PendingPreview())
	}
	if got := app.input.Value(); got != "composing more" {
		t.Errorf("Ctrl+C with queued + non-empty input must NOT clobber input, got %q want %q", got, "composing more")
	}
}

func TestCtrlC_SecondTapAfterRecall_ClearsPrompt(t *testing.T) {
	// First Ctrl+C: recall queued -> input. Second Ctrl+C: existing clear-text
	// rung clears input.
	mock := newFakeSessionBackend()
	app := queuedStreamingApp(t, mock, "edit me")
	app.input.SetValue("")

	// Tap 1.
	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if app.input.Value() != "edit me" {
		t.Fatalf("setup: first Ctrl+C should recall queued; input=%q", app.input.Value())
	}
	if app.pendingSubmit != "" {
		t.Fatalf("setup: first Ctrl+C should clear pendingSubmit; got %q", app.pendingSubmit)
	}

	// Tap 2: clears the prompt via the existing clear-text rung.
	updated, _ = app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if app.input.Value() != "" {
		t.Errorf("second Ctrl+C should clear prompt, got %q", app.input.Value())
	}
	if app.showConfirm {
		t.Error("second Ctrl+C with non-empty input from recall must clear input, not show quit confirm")
	}
}

func TestCtrlC_NoQueueEmptyInput_ShowsQuitConfirm(t *testing.T) {
	// Regression: nothing queued + empty prompt + idle = quit-confirm
	// (unchanged from pre-QUM-630 behavior).
	mock := newFakeSessionBackend()
	app := readyAppWithBridge(t, mock)
	app.input.SetValue("")

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	app = updated.(AppModel)

	if !app.showConfirm {
		t.Error("Ctrl+C with no queue + empty input must still show quit confirm")
	}
}
