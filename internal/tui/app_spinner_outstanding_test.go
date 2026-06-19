package tui

import "testing"

// QUM-831: the thinking/running spinner (the QUM-796 sparkle row above the
// prompt input) must derive from OUTSTANDING WORK, not just the result block.
// busy = (turnState != TurnIdle) OR (outstanding pending-zone non-empty). It
// stays lit while a queued message is processed (incl. before its first
// content frame) and between result blocks while messages remain unconsumed,
// and clears ONLY at true idle (no in-flight turn AND outstanding empty).
//
// Spinner-visible assertion: sparkleRow(true) != "" (root path, input visible).

// Sanity: a fresh send arms the spinner (TurnStreaming at send time). Passes
// on today's code; guards against a regression in the base case.
func TestSpinner_LitOnSend(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	if app.sparkleRow(true) == "" {
		t.Fatal("spinner not shown after send; want visible")
	}
}

// Window #2: a result block arrives while messages are still queued/unconsumed.
// finalizeTurn drops turnState to Idle, but outstanding work remains — the
// spinner must stay lit. RED on today's code (turnState-only derivation).
func TestSpinner_StaysAfterResultWhileQueued(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "u2", Text: "beta"})
	// Turn 1's result block lands while u1/u2 are still unconsumed.
	app = deliver(t, app, SessionResultMsg{})

	if app.rootBuf().ZoneUserCount() == 0 {
		t.Fatal("precondition: expected queued messages still outstanding")
	}
	if app.sparkleRow(true) == "" {
		t.Fatal("spinner cleared after result block while messages still queued; want visible")
	}
}

// Window #2 mid-drain: after consuming one of several queued messages and a
// result block, remaining queued messages keep the spinner lit between blocks.
func TestSpinner_PersistsAcrossDrainBetweenBlocks(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	app = deliver(t, app, UserMessageSentMsg{UUID: "u2", Text: "beta"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	app = deliver(t, app, SessionResultMsg{}) // block 1 done; u2 still queued

	if app.rootBuf().ZoneUserCount() == 0 {
		t.Fatal("precondition: expected u2 still outstanding")
	}
	if app.sparkleRow(true) == "" {
		t.Fatal("spinner cleared between result blocks while u2 still queued; want visible")
	}
}

// Window #1 (tail): the last queued message is consumed (leaving the zone
// empty) but its turn's first content frame has not arrived yet. The consume
// must re-arm the turn state so the spinner stays lit through the pre-content
// window. RED on today's code (settle drops count to 0, turnState still Idle).
func TestSpinner_LitOnTailConsumeBeforeContent(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	// Prior turn finalized (turnState→Idle) before the queued message's turn
	// opens — the reducer-level shape of "result block, then consume".
	app = deliver(t, app, SessionResultMsg{})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})

	if app.rootBuf().ZoneUserCount() != 0 {
		t.Fatal("precondition: expected zone empty after consuming the only queued message")
	}
	if app.turnState != TurnThinking {
		t.Fatalf("consume did not re-arm turn state; turnState = %v, want TurnThinking", app.turnState)
	}
	if app.sparkleRow(true) == "" {
		t.Fatal("spinner cleared on tail consume before content; want visible")
	}
}

// No false positive: once the last queued message is consumed AND its turn
// completes, the spinner clears at true idle.
func TestSpinner_ClearsAtTrueIdle(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	app = deliver(t, app, SessionResultMsg{})

	if app.rootBuf().ZoneUserCount() != 0 {
		t.Fatal("precondition: expected zone empty at true idle")
	}
	if app.turnState != TurnIdle {
		t.Fatalf("turnState = %v, want TurnIdle at true idle", app.turnState)
	}
	if app.sparkleRow(true) != "" {
		t.Fatalf("spinner still shown at true idle; want cleared, got %q", app.sparkleRow(true))
	}
}

// No stranded spinner on recall: a queued message that is cancelled (recalled)
// drops out of the zone and, with no in-flight turn, the spinner clears.
func TestSpinner_ClearsAfterRecallToEmpty(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	// A result block finalizes any send-time turn state to Idle...
	app = deliver(t, app, SessionResultMsg{})
	// ...then the queued message is recalled, emptying the zone.
	app = deliver(t, app, UserMessageCancelledMsg{UUID: "u1"})

	if app.rootBuf().ZoneUserCount() != 0 {
		t.Fatal("precondition: expected zone empty after recall")
	}
	if app.turnState != TurnIdle {
		t.Fatalf("turnState = %v, want TurnIdle after recall", app.turnState)
	}
	if app.sparkleRow(true) != "" {
		t.Fatalf("spinner stranded after recall to empty; want cleared, got %q", app.sparkleRow(true))
	}
}

// isBusy is the authoritative backstop: even if the watchdog force-finalizes
// turnState to Idle while queued work remains, isBusy stays true.
func TestSpinner_IsBusyTrueWhileQueuedDespiteIdleTurn(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "alpha"})
	app = deliver(t, app, SessionResultMsg{}) // turnState→Idle, u1 still queued

	if app.turnState != TurnIdle {
		t.Fatalf("turnState = %v, want TurnIdle", app.turnState)
	}
	if !app.isBusy() {
		t.Fatal("isBusy = false while a queued message is outstanding; want true")
	}
}
