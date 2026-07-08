package tui

import (
	"strings"
	"testing"
)

// AttachMsg dispatches to the bridge's SendAttachment with the parsed paths and
// prompt (QUM-860).
func TestApp_AttachMsg_DispatchesSendAttachment(t *testing.T) {
	app, fake := idleTrackingApp(t)
	app = deliver(t, app, AttachMsg{Paths: []string{"/tmp/a.png"}, Prompt: "look"})
	// The reducer returns bridge.SendAttachment(...); the fake increments
	// synchronously when the cmd closure is built.
	_, cmd := app.Update(AttachMsg{Paths: []string{"/tmp/b.png"}, Prompt: "again"})
	if cmd == nil {
		t.Fatal("AttachMsg should return a dispatch cmd")
	}
	_ = cmd()
	if fake.attachCalls == 0 {
		t.Fatal("SendAttachment was not called")
	}
	if fake.lastAttachPrompt != "again" {
		t.Errorf("lastAttachPrompt = %q, want %q", fake.lastAttachPrompt, "again")
	}
	if len(fake.lastAttachPaths) != 1 || fake.lastAttachPaths[0] != "/tmp/b.png" {
		t.Errorf("lastAttachPaths = %v, want [/tmp/b.png]", fake.lastAttachPaths)
	}
}

// QUM-860 follow-up: a locally-rejected /attach (unsupported/missing/unreadable/
// too-large) must NOT leave a phantom "Thinking…" spinner lit. AttachMsg
// optimistically flips Idle→Thinking; the reject arrives as an AttachRejectedMsg
// and writes no turn, so its reducer must unwind that flip back to Idle while
// still surfacing the error toast.
func TestApp_AttachReject_ClearsPhantomThinking(t *testing.T) {
	app, _ := idleTrackingApp(t)
	// The optimistic flip: dispatching /attach lights the spinner.
	app = deliver(t, app, AttachMsg{Paths: []string{"/tmp/bad.txt"}, Prompt: "look"})
	if app.turnState != TurnThinking {
		t.Fatalf("AttachMsg should optimistically flip to TurnThinking, got %v", app.turnState)
	}
	// The reject: SendAttachment's local validation failure returns an
	// AttachRejectedMsg and no turn. The spinner must clear back to Idle.
	app = deliver(t, app, AttachRejectedMsg{Toast: Toast{Text: "unsupported format", Style: ToastError}})
	if app.turnState != TurnIdle {
		t.Errorf("rejected /attach must return turnState to TurnIdle (no phantom Thinking), got %v", app.turnState)
	}
	if app.toasts.Empty() {
		t.Error("the rejection toast must still render")
	}
}

// QUM-860 follow-up: the reject-path spinner reset is guarded on TurnThinking so
// it can never stomp a genuine in-flight turn. An AttachRejectedMsg arriving
// while a real turn is streaming (e.g. /attach dispatched behind an active turn)
// must leave TurnStreaming intact.
func TestApp_AttachReject_DoesNotStompStreaming(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{UUID: "u1", Text: "hi"}) // → TurnStreaming
	if app.turnState != TurnStreaming {
		t.Fatalf("setup: expected TurnStreaming, got %v", app.turnState)
	}
	app = deliver(t, app, AttachRejectedMsg{Toast: Toast{Text: "too large", Style: ToastError}})
	if app.turnState != TurnStreaming {
		t.Errorf("AttachRejectedMsg must not stomp an in-flight TurnStreaming, got %v", app.turnState)
	}
}

// QUM-860 follow-up: a VALID /attach's spinner must behave identically to a
// normal typed turn — Thinking on dispatch, Streaming once the frame is sent.
func TestApp_AttachValid_SpinnerMatchesTypedTurn(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, AttachMsg{Paths: []string{"/tmp/a.png"}, Prompt: "look"})
	if app.turnState != TurnThinking {
		t.Fatalf("valid /attach should show Thinking on dispatch, got %v", app.turnState)
	}
	app = deliver(t, app, UserMessageSentMsg{
		UUID:        "u1",
		Text:        "look",
		Attachments: []AttachmentChip{{Name: "a.png", MediaType: "image/png", Size: "1 KB"}},
	})
	if app.turnState != TurnStreaming {
		t.Errorf("valid /attach should advance to TurnStreaming once sent, got %v", app.turnState)
	}
}

// AttachMsg with no paths is a no-op (never dispatches).
func TestApp_AttachMsg_NoPaths_NoOp(t *testing.T) {
	app, fake := idleTrackingApp(t)
	_, cmd := app.Update(AttachMsg{Paths: nil, Prompt: "x"})
	if cmd != nil {
		if _ = cmd(); fake.attachCalls != 0 {
			t.Error("AttachMsg with no paths must not dispatch SendAttachment")
		}
	}
}

// A UserMessageSentMsg carrying attachments renders a user bubble with a chip
// (never the system-notification path) and marks an attachment turn in flight.
func TestApp_UserMessageSent_WithAttachments_RendersChip(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{
		UUID: "u1",
		Text: "what is this",
		Attachments: []AttachmentChip{
			{Name: "mock.png", MediaType: "image/png", Size: "320 KB"},
		},
	})
	cl := rootChat(app)
	out := stripANSI(cl.Render(90))
	if !strings.Contains(out, "mock.png") || !strings.Contains(out, "📎") {
		t.Errorf("expected chip in render, got:\n%s", out)
	}
	if app.attachTurnInFlight {
		t.Error("attachTurnInFlight should NOT be set until the attach turn is consumed")
	}
	// Settle relocates it and the chip is still present exactly once; the
	// consume also arms the in-flight marker (the turn is now executing).
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	if !app.attachTurnInFlight {
		t.Error("attachTurnInFlight should be set once the attach turn is consumed")
	}
	settled := stripANSI(rootChat(app).Render(90))
	if strings.Count(settled, "mock.png") != 1 {
		t.Errorf("chip should render exactly once after settle, got:\n%s", settled)
	}
}

// An is_error SessionResultMsg for an attachment turn surfaces a non-empty
// attachment-framed error dialog — never a blank "Session Error" (QUM-860).
func TestApp_AttachTurn_APIRejection_SurfacesNonEmptyError(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{
		UUID:        "u1",
		Text:        "look",
		Attachments: []AttachmentChip{{Name: "big.png", MediaType: "image/png", Size: "9 MB"}},
	})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"}) // attach turn starts executing
	// API rejects with an empty result string (the dead-turn case).
	app = deliver(t, app, SessionResultMsg{IsError: true, Result: ""})
	if !app.showError {
		t.Fatal("expected error dialog to show for a rejected attachment turn")
	}
	if strings.TrimSpace(app.errorDialog.err.Error()) == "" {
		t.Error("attachment rejection error must be non-empty (not a blank Session Error)")
	}
	if !strings.Contains(app.errorDialog.err.Error(), "attachment") {
		t.Errorf("error should mention attachment, got %q", app.errorDialog.err.Error())
	}
	if app.attachTurnInFlight {
		t.Error("attachTurnInFlight should be cleared after the terminal result")
	}
}

// Regression (reviewer Finding 1): an earlier plain turn's error must NOT be
// mislabeled as an attachment rejection just because a later attach turn was
// queued. The marker arms only when the attach turn is actually consumed, so a
// plain turn A that errors while attach turn B is still queued is framed
// verbatim.
func TestApp_QueuedAttachDoesNotMislabelEarlierTurnError(t *testing.T) {
	app, _ := idleTrackingApp(t)
	// Plain turn A and attach turn B both queued (B behind A).
	app = deliver(t, app, UserMessageSentMsg{UUID: "uA", Text: "plain question"})
	app = deliver(t, app, UserMessageSentMsg{
		UUID:        "uB",
		Text:        "look",
		Attachments: []AttachmentChip{{Name: "b.png", MediaType: "image/png", Size: "1 KB"}},
	})
	// A executes and errors before B is ever consumed.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "uA"})
	app = deliver(t, app, SessionResultMsg{IsError: true, Result: "unrelated A error"})
	if app.errorDialog.err.Error() != "unrelated A error" {
		t.Errorf("plain turn A error mislabeled: %q", app.errorDialog.err.Error())
	}

	// Now B executes and is rejected — THAT error is attachment-framed.
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "uB"})
	app = deliver(t, app, SessionResultMsg{IsError: true, Result: "image too large"})
	if !strings.Contains(app.errorDialog.err.Error(), "attachment") {
		t.Errorf("attach turn B rejection should be attachment-framed, got %q", app.errorDialog.err.Error())
	}
}

// An interrupted attach turn (InterruptCompletedMsg, not SessionResultMsg) must
// clear the in-flight marker so it can't mislabel the following turn's error.
func TestApp_AttachTurnInterrupt_ClearsMarker(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, UserMessageSentMsg{
		UUID:        "u1",
		Text:        "look",
		Attachments: []AttachmentChip{{Name: "a.png", MediaType: "image/png", Size: "1 KB"}},
	})
	app = deliver(t, app, UserMessageConsumedMsg{UUID: "u1"})
	if !app.attachTurnInFlight {
		t.Fatal("setup: marker should be armed after consume")
	}
	app = deliver(t, app, InterruptCompletedMsg{})
	if app.attachTurnInFlight {
		t.Error("interrupt must clear attachTurnInFlight")
	}
}

// A non-attachment error result keeps the existing behavior (raw result text).
func TestApp_NonAttachError_UnchangedFraming(t *testing.T) {
	app, _ := idleTrackingApp(t)
	app = deliver(t, app, SessionResultMsg{IsError: true, Result: "boom"})
	if !app.showError {
		t.Fatal("expected error dialog")
	}
	if app.errorDialog.err.Error() != "boom" {
		t.Errorf("non-attach error = %q, want %q (unchanged)", app.errorDialog.err.Error(), "boom")
	}
}
