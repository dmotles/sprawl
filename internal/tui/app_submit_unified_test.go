package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-828: the TUI submit path is unified — every human submit is written to
// the CLI stdin (priority next, tracked in the outstanding map) regardless of
// turn state. The legacy single-slot `pendingSubmit` busy branch is gone; the
// `⏳ N queued` indicator is the sole projection of queued state; the user
// bubble renders on consume (Strategy B), not on submit; Esc is a bare,
// contentless interrupt that leaves the queue intact.

// busyTrackingApp returns a ready, sized, mid-turn (TurnStreaming) AppModel
// whose fake bridge mints distinct uuids per SendMessage so the full
// submit→track→consume flow is exercisable.
func busyTrackingApp(t *testing.T) (AppModel, *fakeSessionBackend) {
	t.Helper()
	fake := newFakeSessionBackend()
	fake.trackSends = true
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app := resized.(AppModel)
	app.turnState = TurnStreaming
	return app, fake
}

// submitThroughBridge drives one SubmitMsg through the reducer and then feeds
// the resulting UserMessageSentMsg back in, mirroring the real bubbletea loop
// (SubmitMsg → bridge.SendMessage cmd → UserMessageSentMsg).
func submitThroughBridge(t *testing.T, app AppModel, text string) AppModel {
	t.Helper()
	updated, cmd := app.Update(SubmitMsg{Text: text})
	app = updated.(AppModel)
	if cmd == nil {
		return app
	}
	msg := cmd()
	updated, _ = app.Update(msg)
	return updated.(AppModel)
}

// 1. A submit while busy must write to stdin (call bridge.SendMessage) and be
// tracked in queuedUser — NOT stashed in a Go-side single slot.
func TestSubmit_WhileBusy_WritesToStdinAndTracks(t *testing.T) {
	app, fake := busyTrackingApp(t)

	updated, cmd := app.Update(SubmitMsg{Text: "busy msg"})
	app = updated.(AppModel)

	if fake.sendCalls != 1 {
		t.Fatalf("SendMessage calls = %d, want 1 (busy submit must write to stdin)", fake.sendCalls)
	}
	if fake.lastSent != "busy msg" {
		t.Errorf("SendMessage text = %q, want %q", fake.lastSent, "busy msg")
	}
	if cmd == nil {
		t.Fatal("busy submit must return a cmd (the stdin write), got nil")
	}
	updated, _ = app.Update(cmd())
	app = updated.(AppModel)
	if app.queuedUserCount() != 1 {
		t.Errorf("queuedUserCount = %d, want 1 after busy submit is tracked", app.queuedUserCount())
	}
}

// 2. Multiple submits while busy grow the queue — no single-slot replace. This
// is the regression QUM-828 fixes.
func TestSubmit_MultipleWhileBusy_QueueGrows(t *testing.T) {
	app, _ := busyTrackingApp(t)

	app = submitThroughBridge(t, app, "first")
	app = submitThroughBridge(t, app, "second")
	app = submitThroughBridge(t, app, "third")

	if app.queuedUserCount() != 3 {
		t.Fatalf("queuedUserCount = %d, want 3 (each busy submit queues; no replace)", app.queuedUserCount())
	}
	if !strings.Contains(stripAnsi(app.input.View()), "3 queued") {
		t.Errorf("input should show the ⏳ 3 queued indicator; got:\n%s", stripAnsi(app.input.View()))
	}
}

// 3. Strategy B: the user bubble is NOT rendered on submit; it appears only
// when the consumption ack arrives, landing after the in-flight assistant
// content (correct conversational ordering).
func TestRender_OnConsume_NotOnSubmit(t *testing.T) {
	app, _ := busyTrackingApp(t)

	// Plant an in-flight assistant chunk so we can assert ordering.
	app.rootBuf().AppendAssistantChunk("streaming answer")

	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1", Text: "queued while busy"})
	app = updated.(AppModel)

	if userItemPresent(app, "queued while busy") {
		t.Fatal("user bubble must NOT render on submit (Strategy B render-on-consume)")
	}

	updated, _ = app.Update(UserMessageConsumedMsg{UUID: "u1"})
	app = updated.(AppModel)

	if !userItemPresent(app, "queued while busy") {
		t.Fatal("user bubble must render when the consumption ack arrives")
	}
	// Ordering: the user item must land AFTER the in-flight assistant item.
	items := app.viewportFor(app.rootAgent).ChatList().Items()
	userIdx, asstIdx := -1, -1
	for i, it := range items {
		if u, ok := it.(*UserItem); ok && u.Text() == "queued while busy" {
			userIdx = i
		}
		if _, ok := it.(*AssistantTextItem); ok {
			asstIdx = i
		}
	}
	if asstIdx == -1 || userIdx == -1 || userIdx < asstIdx {
		t.Errorf("user bubble (idx %d) must follow the in-flight assistant item (idx %d)", userIdx, asstIdx)
	}
}

// 4. Esc during a turn is a bare contentless interrupt: it aborts the model
// turn and leaves the queued messages intact. The content-carrying preempt
// path (InterruptAndSend) must not fire.
func TestEsc_DuringTurn_AbortsLeavesQueueIntact(t *testing.T) {
	app, fake := busyTrackingApp(t)
	app.observedAgent = app.rootAgent
	app = submitThroughBridge(t, app, "queued one")
	app = submitThroughBridge(t, app, "queued two")
	if app.queuedUserCount() != 2 {
		t.Fatalf("setup: queuedUserCount = %d, want 2", app.queuedUserCount())
	}

	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if fake.interruptCalls != 1 {
		t.Errorf("bare Interrupt calls = %d, want 1 on Esc-during-turn", fake.interruptCalls)
	}
	// The content-carrying preempt path (InterruptAndSend) is deleted from the
	// SessionBackend interface entirely — its absence is the compile-time
	// guarantee that Esc can never carry content.
	if app.queuedUserCount() != 2 {
		t.Errorf("queuedUserCount = %d, want 2 (queue survives the abort)", app.queuedUserCount())
	}
}

// 4b. Isolated Esc contract: with a queue already populated (seeded directly,
// independent of the submit path), Esc-during-turn fires the bare Interrupt
// only, never the content-carrying preempt, and the queue is untouched. Guards
// the contract separately from the submit-tracking path so a regression in one
// can't mask the other.
func TestEsc_DuringTurn_BareInterruptOnly_QueueUntouched(t *testing.T) {
	app, fake := busyTrackingApp(t)
	app.observedAgent = app.rootAgent
	updated, _ := app.Update(UserMessageSentMsg{UUID: "u1", Text: "one"})
	app = updated.(AppModel)
	updated, _ = app.Update(UserMessageSentMsg{UUID: "u2", Text: "two"})
	app = updated.(AppModel)
	if app.queuedUserCount() != 2 {
		t.Fatalf("setup: queuedUserCount = %d, want 2", app.queuedUserCount())
	}

	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	app = updated.(AppModel)

	if fake.interruptCalls != 1 {
		t.Errorf("bare Interrupt calls = %d, want 1", fake.interruptCalls)
	}
	if app.queuedUserCount() != 2 {
		t.Errorf("queuedUserCount = %d, want 2 (queue survives the abort)", app.queuedUserCount())
	}
}

// 5. Recall (Ctrl+U) operates on a genuinely multi-entry queue built from
// normal busy typing.
func TestRecall_MultiEntry_FromBusyTyping(t *testing.T) {
	app, fake := busyTrackingApp(t)
	app.observedAgent = app.rootAgent
	app = submitThroughBridge(t, app, "alpha")
	app = submitThroughBridge(t, app, "beta")
	if app.queuedUserCount() != 2 {
		t.Fatalf("setup: queuedUserCount = %d, want 2 (recall needs a real multi-entry queue)", app.queuedUserCount())
	}

	updated, _ := app.Update(ctrlKey('u'))
	_ = updated
	if fake.recallCalls != 1 {
		t.Errorf("recallCalls = %d, want 1 for Ctrl+U on a busy-typed queue", fake.recallCalls)
	}
}

// 5b. Send-all-now (Ctrl+G) operates on a genuinely multi-entry queue built
// from normal busy typing.
func TestSendAllNow_MultiEntry_FromBusyTyping(t *testing.T) {
	app, fake := busyTrackingApp(t)
	app.observedAgent = app.rootAgent
	app = submitThroughBridge(t, app, "alpha")
	app = submitThroughBridge(t, app, "beta")
	if app.queuedUserCount() != 2 {
		t.Fatalf("setup: queuedUserCount = %d, want 2", app.queuedUserCount())
	}

	updated, _ := app.Update(ctrlKey('g'))
	_ = updated
	if fake.sendAllNowCalls != 1 {
		t.Errorf("sendAllNowCalls = %d, want 1 for Ctrl+G on a busy-typed queue", fake.sendAllNowCalls)
	}
}

// 6. A session restart drops the queue: clear queuedUser/queuedText, sync the
// indicator, and surface the banner.
func TestRestart_ClearsQueuedIndicator(t *testing.T) {
	app, _ := busyTrackingApp(t)
	app = submitThroughBridge(t, app, "will be dropped")
	app = submitThroughBridge(t, app, "also dropped")
	if app.queuedUserCount() != 2 {
		t.Fatalf("setup: queuedUserCount = %d, want 2", app.queuedUserCount())
	}

	updated, _ := app.Update(SessionRestartingMsg{Reason: "x"})
	app = updated.(AppModel)

	if app.queuedUserCount() != 0 {
		t.Errorf("queuedUserCount = %d, want 0 after session restart", app.queuedUserCount())
	}
	if strings.Contains(stripAnsi(app.input.View()), "queued") {
		t.Errorf("queued indicator should be cleared after restart; got:\n%s", stripAnsi(app.input.View()))
	}
	if !statusBarContains(app, "queued message") {
		t.Errorf("status bar should carry the dropped-queue banner after restart; got:\n%s", stripAnsi(app.statusBar.View()))
	}
}

// userItemPresent reports whether the root agent's ChatList contains a user
// bubble with the given text.
func userItemPresent(app AppModel, text string) bool {
	for _, it := range app.viewportFor(app.rootAgent).ChatList().Items() {
		if u, ok := it.(*UserItem); ok && u.Text() == text {
			return true
		}
	}
	return false
}
