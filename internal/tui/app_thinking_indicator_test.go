package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

var errTestNow = errors.New("send-all-now boom")

// QUM-858: hitting Ctrl+G (send-all-now) with queued prompts while the turn is
// idle must optimistically flip turnState to TurnThinking so the in-turn
// indicator lights immediately — before the CLI's isReplay echo lands.
func TestCtrlG_SendAllNow_OptimisticThinkingFlipWhenQueued(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.observedAgent = app.rootAgent

	// A queued prompt is pending in the zone; the turn is idle.
	app.rootBuf().ZoneAddUser("u1", "queued prompt")
	if app.turnState != TurnIdle {
		t.Fatalf("precondition: turnState = %v, want TurnIdle", app.turnState)
	}

	updated, _ := app.Update(ctrlKey('g'))
	app = updated.(AppModel)

	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v after Ctrl+G with queued prompt, want TurnThinking", app.turnState)
	}
	if fake.sendAllNowCalls != 1 {
		t.Errorf("sendAllNowCalls = %d, want 1", fake.sendAllNowCalls)
	}
}

// QUM-858: Ctrl+G on an empty queue is a documented no-op (no turn starts, no
// finalizeTurn ever runs). It must NOT flip turnState, or the indicator would
// spin forever with nothing in flight.
func TestCtrlG_SendAllNow_EmptyQueue_NoFlip(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.observedAgent = app.rootAgent

	if app.queuedUserCount() != 0 {
		t.Fatalf("precondition: queuedUserCount = %d, want 0", app.queuedUserCount())
	}

	updated, _ := app.Update(ctrlKey('g'))
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after empty-queue Ctrl+G, want TurnIdle (no flip)", app.turnState)
	}
}

// QUM-858: the optimistic flip is guarded on TurnIdle so an in-flight Streaming
// turn is never stomped back to Thinking.
func TestCtrlG_SendAllNow_DoesNotStompStreaming(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.observedAgent = app.rootAgent
	app.rootBuf().ZoneAddUser("u1", "queued prompt")
	app.setTurnState(TurnStreaming)

	updated, _ := app.Update(ctrlKey('g'))
	app = updated.(AppModel)

	if app.turnState != TurnStreaming {
		t.Errorf("turnState = %v after Ctrl+G during streaming, want TurnStreaming (not stomped)", app.turnState)
	}
}

// QUM-858: if SendAllNow fails, no turn ever opens, so no
// EventInterrupted/EventTurnStarted/finalizeTurn fires to reset the optimistic
// TurnThinking flip. The error leg of SendAllNowResultMsg must reset it so the
// spinner doesn't stay lit and idle-gated reducers aren't blocked.
func TestSendAllNowResultMsg_Error_ResetsOptimisticFlip(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.setTurnState(TurnThinking) // the optimistic flip from a prior Ctrl+G

	updated, _ := app.Update(SendAllNowResultMsg{Err: errTestNow})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("turnState = %v after failed SendAllNow, want TurnIdle (flip reset)", app.turnState)
	}
}

// QUM-858: the error-leg reset is guarded on TurnThinking so a genuine in-flight
// Streaming turn is not stomped back to Idle by a concurrent SendAllNow failure.
func TestSendAllNowResultMsg_Error_DoesNotStompStreaming(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.setTurnState(TurnStreaming)

	updated, _ := app.Update(SendAllNowResultMsg{Err: errTestNow})
	app = updated.(AppModel)

	if app.turnState != TurnStreaming {
		t.Errorf("turnState = %v after failed SendAllNow during streaming, want TurnStreaming (not stomped)", app.turnState)
	}
}

// QUM-858 durable half: EventTurnStarted (translated to TurnStartedMsg) flips a
// TurnIdle TUI to TurnThinking so the pre-content window of a freshly-opened
// turn (send-all-now replacement, autonomous, or QUM-640 continuation) shows the
// indicator. It must also re-arm the event pump.
func TestTurnStartedMsg_FlipsThinkingWhenIdle(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	if app.turnState != TurnIdle {
		t.Fatalf("precondition: turnState = %v, want TurnIdle", app.turnState)
	}

	updated, cmd := app.Update(TurnStartedMsg{})
	app = updated.(AppModel)

	if app.turnState != TurnThinking {
		t.Errorf("turnState = %v after TurnStartedMsg, want TurnThinking", app.turnState)
	}
	// Bus-delivered reducers MUST re-arm WaitForEvent or the pump parks (QUM-826).
	if cmd == nil {
		t.Error("TurnStartedMsg reducer returned nil cmd, want a WaitForEvent re-arm")
	}
}

// QUM-858: the durable-half flip is guarded on TurnIdle so it does not stomp an
// in-flight Streaming turn back to Thinking.
func TestTurnStartedMsg_DoesNotStompStreaming(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.setTurnState(TurnStreaming)

	updated, _ := app.Update(TurnStartedMsg{})
	app = updated.(AppModel)

	if app.turnState != TurnStreaming {
		t.Errorf("turnState = %v after TurnStartedMsg during streaming, want TurnStreaming (not stomped)", app.turnState)
	}
}

// QUM-858: a TurnStartedMsg surfaced from a CHILD stream is wrapped in
// ChildStreamMsg and routed to applyChildStreamInner (a no-op for this type). It
// must never touch the ROOT turnState.
func TestChildStreamTurnStarted_DoesNotFlipRoot(t *testing.T) {
	fake := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, fake)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	if app.turnState != TurnIdle {
		t.Fatalf("precondition: turnState = %v, want TurnIdle", app.turnState)
	}
	// Match the live-generation guard (Agent + Epoch) so the message actually
	// routes through applyChildStreamInner instead of being dropped as stale —
	// otherwise this test would pass vacuously. childAdapterEpoch defaults to 0.
	app.childAdapterAgent = "some-child"

	updated, _ := app.Update(ChildStreamMsg{Agent: "some-child", Epoch: 0, Inner: TurnStartedMsg{}})
	app = updated.(AppModel)

	if app.turnState != TurnIdle {
		t.Errorf("root turnState = %v after child TurnStartedMsg, want TurnIdle (untouched)", app.turnState)
	}
}
