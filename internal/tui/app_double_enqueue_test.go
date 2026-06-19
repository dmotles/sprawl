package tui

import "testing"

// QUM-832 (part b) — regression guard: exactly ONE submit while busy yields
// exactly ONE stdin write (enqueue) and, after the consume ack, exactly ONE
// rendered user bubble. The live forensic (Linear QUM-832, build 3aa9d19 /
// QUM-830, pre-833) showed a busy follow-up "message 2" enqueued TWICE
// (~0.8s apart) before deduping to one turn. RCA: that build predates the
// QUM-828 unified submit path, which deleted the `turnState != TurnIdle ->
// pendingSubmit` dual-submit branch; today SubmitMsg → exactly one
// bridge.SendMessage → one WriteUserPrompt → one stdin frame. This guard pins
// the full chain one-submit → one-enqueue → one-bubble so a reintroduced
// double-write goes red.
func TestSubmit_WhileBusy_OneEnqueueOneBubble(t *testing.T) {
	app, fake := busyTrackingApp(t)

	app = submitThroughBridge(t, app, "message 2")

	if fake.sendCalls != 1 {
		t.Fatalf("SendMessage calls = %d, want 1 (one submit = one enqueue)", fake.sendCalls)
	}
	if app.queuedUserCount() != 1 {
		t.Fatalf("queuedUserCount = %d, want 1 (one pending bubble)", app.queuedUserCount())
	}

	// The consume ack settles the pending bubble into the committed transcript
	// exactly once — a double enqueue would surface as a second (phantom) bubble.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})

	cl := rootChat(app)
	if n := countUserItems(cl); n != 1 {
		t.Fatalf("committed user bubbles = %d, want exactly 1 (no double-render)", n)
	}
	if app.queuedUserCount() != 0 {
		t.Errorf("queuedUserCount = %d after consume, want 0 (settled out of the zone)", app.queuedUserCount())
	}
}
