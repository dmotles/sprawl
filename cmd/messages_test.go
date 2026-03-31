package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/dendra/internal/messages"
)

func newTestMessagesDeps(t *testing.T) (*messagesDeps, string) {
	t.Helper()
	tmpDir := t.TempDir()
	deps := &messagesDeps{
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "alice"
			}
			return ""
		},
	}
	return deps, tmpDir
}

func TestMessagesSend_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	err := runMessagesSend(deps, "bob", "hello", "world")
	if err != nil {
		t.Fatalf("runMessagesSend() unexpected error: %v", err)
	}

	// Verify message landed in bob's new/ directory
	newDir := filepath.Join(messages.MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}

	// Verify the message content
	data, err := os.ReadFile(filepath.Join(newDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading message file: %v", err)
	}
	var msg messages.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshaling message: %v", err)
	}
	if msg.From != "alice" {
		t.Errorf("From = %q, want %q", msg.From, "alice")
	}
	if msg.To != "bob" {
		t.Errorf("To = %q, want %q", msg.To, "bob")
	}
	if msg.Subject != "hello" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "hello")
	}
	if msg.Body != "world" {
		t.Errorf("Body = %q, want %q", msg.Body, "world")
	}
}

func TestMessagesSend_MissingAgentIdentity(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runMessagesSend(deps, "bob", "hello", "world")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestMessagesSend_MissingDendraRoot(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "alice"
		}
		return ""
	}

	err := runMessagesSend(deps, "bob", "hello", "world")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

func TestMessagesInbox_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	// Pre-populate messages for alice
	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}
	if err := os.MkdirAll(curDir, 0755); err != nil {
		t.Fatalf("creating cur dir: %v", err)
	}

	// Write 2 new messages and 1 read message
	writeTestMessage(t, newDir, "1000.bob.aa01", &messages.Message{
		ID: "1000.bob.aa01", From: "bob", To: "alice",
		Subject: "new1", Body: "body1", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeTestMessage(t, newDir, "2000.charlie.aa02", &messages.Message{
		ID: "2000.charlie.aa02", From: "charlie", To: "alice",
		Subject: "new2", Body: "body2", Timestamp: "2026-03-31T11:00:00Z",
	})
	writeTestMessage(t, curDir, "500.dave.aa03", &messages.Message{
		ID: "500.dave.aa03", From: "dave", To: "alice",
		Subject: "read1", Body: "body3", Timestamp: "2026-03-31T09:00:00Z",
	})

	msgs, newCount, readCount, err := runMessagesInbox(deps)
	if err != nil {
		t.Fatalf("runMessagesInbox() unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Errorf("expected 3 messages, got %d", len(msgs))
	}
	if newCount != 2 {
		t.Errorf("newCount = %d, want 2", newCount)
	}
	if readCount != 1 {
		t.Errorf("readCount = %d, want 1", readCount)
	}
}

func TestMessagesInbox_Empty(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)

	msgs, newCount, readCount, err := runMessagesInbox(deps)
	if err != nil {
		t.Fatalf("runMessagesInbox() unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
	if newCount != 0 {
		t.Errorf("newCount = %d, want 0", newCount)
	}
	if readCount != 0 {
		t.Errorf("readCount = %d, want 0", readCount)
	}
}

func TestMessagesInbox_MissingAgentIdentity(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	_, _, _, err := runMessagesInbox(deps)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestMessagesInbox_MissingDendraRoot(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_AGENT_IDENTITY" {
			return "alice"
		}
		return ""
	}

	_, _, _, err := runMessagesInbox(deps)
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}
}

// writeTestMessage is a test helper that writes a Message as JSON into the given directory.
func writeTestMessage(t *testing.T, dir, filename string, msg *messages.Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename+".json"), data, 0644); err != nil {
		t.Fatalf("writing message file: %v", err)
	}
}
