package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-631: the UnifiedRuntime must publish frames belonging to an autonomous
// (harness-initiated) turn onto its EventBus so subscribers (supervisor → TUI)
// can render harness-initiated turns. New() installs an autonomous-frame
// handler on the concrete backend *session (via type assertion, mirroring the
// QUM-602 SetTerminalErrorHandler wiring); each autonomous frame is published
// as EventProtocolMessage and the autonomous result additionally publishes
// EventTurnCompleted.
//
// FAILS today: backend has no SetAutonomousFrameHandler, so New() installs
// nothing and the bus never sees autonomous frames.

// autonomousText pulls the first text block out of an assistant
// protocol.Message carried on an EventProtocolMessage.
func autonomousText(t *testing.T, msg *protocol.Message) string {
	t.Helper()
	var am protocol.AssistantMessage
	if err := protocol.ParseAs(msg, &am); err != nil {
		t.Fatalf("ParseAs(AssistantMessage): %v", err)
	}
	var inner struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(am.Content, &inner); err != nil {
		t.Fatalf("unmarshal assistant content: %v", err)
	}
	for _, c := range inner.Content {
		if c.Type == "text" {
			return c.Text
		}
	}
	return ""
}

func TestUnifiedRuntime_AutonomousFrames_PublishedToEventBus(t *testing.T) {
	transport := newScriptedTransport()
	session := backend.NewSession(transport, backend.SessionConfig{SessionID: "sess-auto"})
	t.Cleanup(func() { _ = session.Close() })

	// New() installs the autonomous-frame handler on the session. The turn
	// loop is intentionally NOT started — we only exercise the autonomous
	// (harness-initiated) path, which flows through the reader, not StartTurn.
	rt := New(RuntimeConfig{
		Name:    "agent-auto",
		Session: session,
	})

	ch, unsub := rt.EventBus().SubscribeNamed("auto-test", 16)
	defer unsub()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := session.Start(ctx); err != nil {
		t.Fatalf("session.Start: %v", err)
	}

	// Feed an autonomous turn: no Enqueue, no StartTurn.
	transport.feed(t, `{"type":"system","subtype":"init","session_id":"sess-auto"}`)
	transport.feed(t, `{"type":"assistant","uuid":"a-1","message":{"role":"assistant","content":[{"type":"text","text":"auto-reply"}]}}`)
	transport.feed(t, `{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":1,"total_cost_usd":0.01}`)

	var (
		sawAssistant bool
		sawCompleted bool
	)
	deadline := time.After(2 * time.Second)
	for !sawAssistant || !sawCompleted {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("EventBus channel closed before autonomous events arrived")
			}
			switch ev.Type {
			case EventProtocolMessage:
				if ev.Message != nil && ev.Message.Type == "assistant" {
					if txt := autonomousText(t, ev.Message); txt == "auto-reply" {
						sawAssistant = true
					}
				}
			case EventTurnCompleted:
				sawCompleted = true
			}
		case <-deadline:
			t.Fatalf("timeout waiting for autonomous events on EventBus (sawAssistant=%v, sawCompleted=%v)", sawAssistant, sawCompleted)
		}
	}
}
