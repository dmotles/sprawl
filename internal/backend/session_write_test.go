package backend

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
)

// QUM-817 (Slice 2): "send a message" is a direct stdin write of a
// protocol.UserMessage with a priority + uuid. It does NOT open a per-turn
// subscriber or gate on currentTurn — the CLI owns queuing and the single
// reader observes the resulting frames (incl. the isReplay echo).

func TestSession_WriteUserMessage_WritesPriorityAndUUID(t *testing.T) {
	transport := newMockManagedTransport()
	session := NewSession(transport, SessionConfig{SessionID: "sess-1"})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := session.WriteUserMessage(ctx, protocol.UserMessage{
		Type:      "user",
		Message:   protocol.MessageParam{Role: "user", Content: "hello"},
		Priority:  "next",
		UUID:      "u-1",
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("WriteUserMessage error: %v", err)
	}

	var sent any
	select {
	case sent = <-transport.sendCh:
	case <-time.After(time.Second):
		t.Fatal("WriteUserMessage did not write to the transport")
	}

	data, err := json.Marshal(sent)
	if err != nil {
		t.Fatalf("marshal sent: %v", err)
	}
	var parsed struct {
		Type     string `json:"type"`
		Priority string `json:"priority"`
		UUID     string `json:"uuid"`
		Message  struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal sent: %v", err)
	}
	if parsed.Type != "user" {
		t.Errorf("type = %q, want user", parsed.Type)
	}
	if parsed.Priority != "next" {
		t.Errorf("priority = %q, want next", parsed.Priority)
	}
	if parsed.UUID != "u-1" {
		t.Errorf("uuid = %q, want u-1", parsed.UUID)
	}
	if parsed.Message.Content != "hello" {
		t.Errorf("content = %q, want hello", parsed.Message.Content)
	}

	// It must NOT open a turn (no subscriber / currentTurn allocated).
	if session.InTurn() {
		t.Error("WriteUserMessage opened a turn (InTurn=true), want false")
	}
}
