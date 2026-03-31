package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dmotles/dendra/internal/messages"
	"github.com/dmotles/dendra/internal/state"
	"github.com/dmotles/dendra/internal/tmux"
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
		stdout: &bytes.Buffer{},
		stderr: &bytes.Buffer{},
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

// --- runMessagesRead tests ---

func TestMessagesRead_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	// Pre-populate a message in alice's new/ directory
	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, newDir, "1000.bob.aabb", &messages.Message{
		ID: "1000.bob.aabb", From: "bob", To: "alice",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	})

	msg, err := runMessagesRead(deps, "1000.bob.aabb")
	if err != nil {
		t.Fatalf("runMessagesRead() unexpected error: %v", err)
	}

	if msg.ID != "1000.bob.aabb" {
		t.Errorf("ID = %q, want %q", msg.ID, "1000.bob.aabb")
	}
	if msg.From != "bob" {
		t.Errorf("From = %q, want %q", msg.From, "bob")
	}
	if msg.Subject != "hello" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "hello")
	}
	if msg.Body != "world" {
		t.Errorf("Body = %q, want %q", msg.Body, "world")
	}

	// Should have been auto-marked read (moved to cur/)
	if _, err := os.Stat(filepath.Join(newDir, "1000.bob.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from new/")
	}
	if _, err := os.Stat(filepath.Join(curDir, "1000.bob.aabb.json")); err != nil {
		t.Errorf("expected file in cur/: %v", err)
	}
}

func TestMessagesRead_NotFound(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	// Create empty directories
	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	_, err := runMessagesRead(deps, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent message")
	}
}

func TestMessagesRead_PrefixMatch(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "new"), "1000.bob.aabb", &messages.Message{
		ID: "1000.bob.aabb", From: "bob", To: "alice",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	})

	msg, err := runMessagesRead(deps, "1000")
	if err != nil {
		t.Fatalf("runMessagesRead() unexpected error: %v", err)
	}
	if msg.ID != "1000.bob.aabb" {
		t.Errorf("ID = %q, want %q", msg.ID, "1000.bob.aabb")
	}
}

func TestMessagesRead_MissingEnvVars(t *testing.T) {
	// Missing DENDRA_ROOT
	deps := &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_AGENT_IDENTITY" {
				return "alice"
			}
			return ""
		},
	}
	_, err := runMessagesRead(deps, "1000")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}

	// Missing DENDRA_AGENT_IDENTITY
	deps = &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return "/tmp/test"
			}
			return ""
		},
	}
	_, err = runMessagesRead(deps, "1000")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

func TestMessagesBroadcast_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	// Create 2 active agents that are not the sender (alice)
	for _, name := range []string{"bob", "charlie"} {
		if err := state.SaveAgent(tmpDir, &state.AgentState{
			Name:   name,
			Status: "active",
		}); err != nil {
			t.Fatalf("saving agent %s: %v", name, err)
		}
	}

	err := runMessagesBroadcast(deps, "announcement", "hello everyone")
	if err != nil {
		t.Fatalf("runMessagesBroadcast() unexpected error: %v", err)
	}

	// Verify both agents received the message
	for _, name := range []string{"bob", "charlie"} {
		newDir := filepath.Join(messages.MessagesDir(tmpDir), name, "new")
		entries, err := os.ReadDir(newDir)
		if err != nil {
			t.Fatalf("reading new dir for %s: %v", name, err)
		}
		if len(entries) != 1 {
			t.Errorf("expected 1 message for %s, got %d", name, len(entries))
		}
	}
}

func TestMessagesBroadcast_MissingAgentIdentity(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)
	deps.getenv = func(key string) string {
		if key == "DENDRA_ROOT" {
			return "/tmp/test"
		}
		return ""
	}

	err := runMessagesBroadcast(deps, "subj", "body")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
	if !strings.Contains(err.Error(), "DENDRA_AGENT_IDENTITY") {
		t.Errorf("error should mention DENDRA_AGENT_IDENTITY, got: %v", err)
	}
}

// --- runMessagesList tests ---

func TestMessagesList_All(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "new"), "1000.bob.aa01", &messages.Message{
		ID: "1000.bob.aa01", From: "bob", To: "alice",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeTestMessage(t, filepath.Join(agentDir, "cur"), "2000.bob.aa02", &messages.Message{
		ID: "2000.bob.aa02", From: "bob", To: "alice",
		Subject: "read msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})
	writeTestMessage(t, filepath.Join(agentDir, "archive"), "3000.bob.aa03", &messages.Message{
		ID: "3000.bob.aa03", From: "bob", To: "alice",
		Subject: "archived msg", Body: "body", Timestamp: "2026-03-31T12:00:00Z",
	})

	msgs, err := runMessagesList(deps, "all")
	if err != nil {
		t.Fatalf("runMessagesList() unexpected error: %v", err)
	}
	// "all" should return new/ + cur/ only
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages for 'all', got %d", len(msgs))
	}
}

func TestMessagesList_Unread(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "new"), "1000.bob.aa01", &messages.Message{
		ID: "1000.bob.aa01", From: "bob", To: "alice",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeTestMessage(t, filepath.Join(agentDir, "cur"), "2000.bob.aa02", &messages.Message{
		ID: "2000.bob.aa02", From: "bob", To: "alice",
		Subject: "read msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})

	msgs, err := runMessagesList(deps, "unread")
	if err != nil {
		t.Fatalf("runMessagesList() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for 'unread', got %d", len(msgs))
	}
	if msgs[0].Dir != "new" {
		t.Errorf("expected Dir='new', got %q", msgs[0].Dir)
	}
}

func TestMessagesList_Sent(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "sent"), "1000.alice.aa01", &messages.Message{
		ID: "1000.alice.aa01", From: "alice", To: "bob",
		Subject: "sent msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})

	msgs, err := runMessagesList(deps, "sent")
	if err != nil {
		t.Fatalf("runMessagesList() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for 'sent', got %d", len(msgs))
	}
	if msgs[0].Dir != "sent" {
		t.Errorf("expected Dir='sent', got %q", msgs[0].Dir)
	}
}

func TestMessagesList_DefaultFilter(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "new"), "1000.bob.aa01", &messages.Message{
		ID: "1000.bob.aa01", From: "bob", To: "alice",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})

	// Empty string should work like "all"
	msgs, err := runMessagesList(deps, "")
	if err != nil {
		t.Fatalf("runMessagesList() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message for default filter, got %d", len(msgs))
	}
}

func TestMessagesList_InvalidFilter(t *testing.T) {
	deps, _ := newTestMessagesDeps(t)

	_, err := runMessagesList(deps, "bogus")
	if err == nil {
		t.Fatal("expected error for invalid filter")
	}
}

func TestMessagesList_MissingEnvVars(t *testing.T) {
	// Missing DENDRA_ROOT
	deps := &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_AGENT_IDENTITY" {
				return "alice"
			}
			return ""
		},
	}
	_, err := runMessagesList(deps, "all")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}

	// Missing DENDRA_AGENT_IDENTITY
	deps = &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return "/tmp/test"
			}
			return ""
		},
	}
	_, err = runMessagesList(deps, "all")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
}

// --- runMessagesArchive tests ---

func TestMessagesArchive_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "new"), "1000.bob.aabb", &messages.Message{
		ID: "1000.bob.aabb", From: "bob", To: "alice",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	})

	err := runMessagesArchive(deps, "1000.bob.aabb")
	if err != nil {
		t.Fatalf("runMessagesArchive() unexpected error: %v", err)
	}

	// Verify message is in archive/
	archiveDir := filepath.Join(agentDir, "archive")
	if _, err := os.Stat(filepath.Join(archiveDir, "1000.bob.aabb.json")); err != nil {
		t.Errorf("expected file in archive/: %v", err)
	}

	// Verify message is gone from new/
	newDir := filepath.Join(agentDir, "new")
	if _, err := os.Stat(filepath.Join(newDir, "1000.bob.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from new/")
	}
}

func TestMessagesArchive_NotFound(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	err := runMessagesArchive(deps, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent message")
	}
}

func TestMessagesArchive_MissingEnvVars(t *testing.T) {
	// Missing DENDRA_ROOT
	deps := &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_AGENT_IDENTITY" {
				return "alice"
			}
			return ""
		},
	}
	err := runMessagesArchive(deps, "1000")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}

	// Missing DENDRA_AGENT_IDENTITY
	deps = &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return "/tmp/test"
			}
			return ""
		},
	}
	err = runMessagesArchive(deps, "1000")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
}

// --- runMessagesUnread tests ---

func TestMessagesUnread_HappyPath(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeTestMessage(t, filepath.Join(agentDir, "cur"), "1000.bob.aabb", &messages.Message{
		ID: "1000.bob.aabb", From: "bob", To: "alice",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	})

	err := runMessagesUnread(deps, "1000.bob.aabb")
	if err != nil {
		t.Fatalf("runMessagesUnread() unexpected error: %v", err)
	}

	// Verify message is in new/
	newDir := filepath.Join(agentDir, "new")
	if _, err := os.Stat(filepath.Join(newDir, "1000.bob.aabb.json")); err != nil {
		t.Errorf("expected file in new/: %v", err)
	}

	// Verify message is gone from cur/
	curDir := filepath.Join(agentDir, "cur")
	if _, err := os.Stat(filepath.Join(curDir, "1000.bob.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from cur/")
	}
}

func TestMessagesUnread_NotInCur(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	err := runMessagesUnread(deps, "nonexistent")
	if err == nil {
		t.Fatal("expected error when message not in cur/")
	}
}

func TestMessagesUnread_MissingEnvVars(t *testing.T) {
	// Missing DENDRA_ROOT
	deps := &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_AGENT_IDENTITY" {
				return "alice"
			}
			return ""
		},
	}
	err := runMessagesUnread(deps, "1000")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_ROOT")
	}
	if !strings.Contains(err.Error(), "DENDRA_ROOT") {
		t.Errorf("error should mention DENDRA_ROOT, got: %v", err)
	}

	// Missing DENDRA_AGENT_IDENTITY
	deps = &messagesDeps{
		getenv: func(key string) string {
			if key == "DENDRA_ROOT" {
				return "/tmp/test"
			}
			return ""
		},
	}
	err = runMessagesUnread(deps, "1000")
	if err == nil {
		t.Fatal("expected error for missing DENDRA_AGENT_IDENTITY")
	}
}

func TestFormatInboxTable_MixedNewAndRead(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{
		{ID: "1", From: "bob", To: "alice", Subject: "subj1", Body: "body1", Timestamp: "2026-03-31T10:00:00Z", Dir: "new"},
		{ID: "2", From: "charlie", To: "alice", Subject: "subj2", Body: "body2", Timestamp: "2026-03-31T11:00:00Z", Dir: "cur"},
	}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	if !strings.Contains(output, "NEW") {
		t.Errorf("expected output to contain 'NEW' for dir=new, got:\n%s", output)
	}
	if !strings.Contains(output, "read") {
		t.Errorf("expected output to contain 'read' for dir=cur, got:\n%s", output)
	}
}

func TestFormatInboxTable_IncludesTimestamp(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{
		{ID: "1", From: "bob", To: "alice", Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:05:00Z", Dir: "new"},
	}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	// Should contain human-readable timestamp, not raw RFC3339
	if !strings.Contains(output, "2026-03-31 10:05") {
		t.Errorf("expected formatted timestamp '2026-03-31 10:05' in output, got:\n%s", output)
	}
	if strings.Contains(output, "T10:05:00Z") {
		t.Errorf("output should not contain raw RFC3339 timestamp, got:\n%s", output)
	}
}

func TestFormatInboxTable_EmptyList(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	if output != "" {
		t.Errorf("expected no output for empty message list, got:\n%q", output)
	}
}

func TestFormatInboxTable_SubjectNotBody(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{
		{ID: "1", From: "bob", To: "alice", Subject: "important subject", Body: "secret body content", Timestamp: "2026-03-31T10:00:00Z", Dir: "new"},
	}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	if !strings.Contains(output, "important subject") {
		t.Errorf("expected output to contain subject, got:\n%s", output)
	}
	if strings.Contains(output, "secret body content") {
		t.Errorf("output should not contain message body, got:\n%s", output)
	}
}

func TestFormatInboxTable_ColumnsAlignWithVaryingNames(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{
		{ID: "1", From: "a", To: "alice", Subject: "subj-a", Body: "body", Timestamp: "2026-03-31T10:00:00Z", Dir: "new"},
		{ID: "2", From: "engineering-lead", To: "alice", Subject: "subj-eng", Body: "body", Timestamp: "2026-03-31T11:00:00Z", Dir: "new"},
		{ID: "3", From: "root", To: "alice", Subject: "subj-root", Body: "body", Timestamp: "2026-03-31T12:00:00Z", Dir: "new"},
	}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), output)
	}

	subjects := []string{"subj-a", "subj-eng", "subj-root"}
	offsets := make([]int, 3)
	for i, line := range lines {
		idx := strings.Index(line, subjects[i])
		if idx < 0 {
			t.Fatalf("line %d: expected to find subject %q in %q", i, subjects[i], line)
		}
		offsets[i] = idx
	}

	// All subject columns must start at the same byte offset for proper alignment.
	// With fixed-width %-12s, "engineering-lead" (16 chars) overflows the field,
	// pushing its subject column further right than the others.
	if offsets[0] != offsets[1] || offsets[1] != offsets[2] {
		t.Errorf("subject columns are not aligned: offsets = %v\noutput:\n%s", offsets, output)
	}
}

func TestFormatInboxTable_SingleMessage_NoExcessivePadding(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{
		{ID: "1", From: "bob", To: "alice", Subject: "important subject", Body: "body", Timestamp: "2026-03-31T10:00:00Z", Dir: "new"},
	}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	// With dynamic column widths, the gap between "bob" and "important subject"
	// should be reasonable (not padded to a fixed 12-char field width).
	// The old %-12s format produces "bob         important subject" (9 extra spaces).
	// With tabwriter, there should be no run of 6+ consecutive spaces between them.
	bobIdx := strings.Index(output, "bob")
	subjIdx := strings.Index(output, "important subject")
	if bobIdx < 0 || subjIdx < 0 {
		t.Fatalf("expected both 'bob' and 'important subject' in output, got:\n%q", output)
	}
	gap := subjIdx - (bobIdx + len("bob"))
	if gap > 5 {
		t.Errorf("excessive padding between From and Subject: %d spaces (expected ≤5), got:\n%q", gap, output)
	}
}

func TestFormatInboxTable_AllColumnsPresent(t *testing.T) {
	var buf bytes.Buffer
	msgs := []*messages.Message{
		{ID: "1", From: "alice", To: "bob", Subject: "first topic", Body: "body1", Timestamp: "2026-03-31T09:30:00Z", Dir: "new"},
		{ID: "2", From: "charlie", To: "bob", Subject: "second topic", Body: "body2", Timestamp: "2026-03-31T14:45:00Z", Dir: "cur"},
	}

	formatInboxTable(&buf, msgs)
	output := buf.String()

	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), output)
	}

	// Line 1: NEW status, formatted timestamp, from, subject
	if !strings.Contains(lines[0], "NEW") {
		t.Errorf("line 0: expected status 'NEW', got:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "2026-03-31 09:30") {
		t.Errorf("line 0: expected formatted timestamp '2026-03-31 09:30', got:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "alice") {
		t.Errorf("line 0: expected from 'alice', got:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "first topic") {
		t.Errorf("line 0: expected subject 'first topic', got:\n%s", lines[0])
	}

	// Line 2: read status, formatted timestamp, from, subject
	if !strings.Contains(lines[1], "read") {
		t.Errorf("line 1: expected status 'read', got:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], "2026-03-31 14:45") {
		t.Errorf("line 1: expected formatted timestamp '2026-03-31 14:45', got:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], "charlie") {
		t.Errorf("line 1: expected from 'charlie', got:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], "second topic") {
		t.Errorf("line 1: expected subject 'second topic', got:\n%s", lines[1])
	}
}

func TestMessagesInbox_OutputRouting(t *testing.T) {
	deps, tmpDir := newTestMessagesDeps(t)

	// Pre-populate a message for alice
	agentDir := filepath.Join(messages.MessagesDir(tmpDir), "alice")
	newDir := filepath.Join(agentDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}
	writeTestMessage(t, newDir, "1000.bob.aa01", &messages.Message{
		ID: "1000.bob.aa01", From: "bob", To: "alice",
		Subject: "hello subject", Body: "hello body", Timestamp: "2026-03-31T10:00:00Z",
	})

	// runMessagesInboxDisplay should write summary to stderr and table to stdout
	err := runMessagesInboxDisplay(deps)
	if err != nil {
		t.Fatalf("runMessagesInboxDisplay() unexpected error: %v", err)
	}

	stdoutBuf := deps.stdout.(*bytes.Buffer)
	stderrBuf := deps.stderr.(*bytes.Buffer)

	stdoutOutput := stdoutBuf.String()
	stderrOutput := stderrBuf.String()

	// Message table lines should be on stdout
	if !strings.Contains(stdoutOutput, "hello subject") {
		t.Errorf("expected message table on stdout, got stdout:\n%s", stdoutOutput)
	}

	// Summary should be on stderr
	if !strings.Contains(stderrOutput, "Inbox:") {
		t.Errorf("expected summary line on stderr, got stderr:\n%s", stderrOutput)
	}

	// Summary should NOT be on stdout
	if strings.Contains(stdoutOutput, "Inbox:") {
		t.Errorf("summary line should not be on stdout, got:\n%s", stdoutOutput)
	}

	// Message body should not appear anywhere in output
	if strings.Contains(stdoutOutput, "hello body") {
		t.Errorf("message body should not appear on stdout, got:\n%s", stdoutOutput)
	}
	if strings.Contains(stderrOutput, "hello body") {
		t.Errorf("message body should not appear on stderr, got:\n%s", stderrOutput)
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

// mockTmuxRunner records calls to SendKeys for test assertions.
type mockTmuxRunner struct {
	sendKeysCalls []sendKeysCall
}

type sendKeysCall struct {
	sessionName string
	windowName  string
	keys        string
}

func (m *mockTmuxRunner) HasSession(name string) bool                       { return false }
func (m *mockTmuxRunner) NewSession(name string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *mockTmuxRunner) NewSessionWithWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *mockTmuxRunner) NewWindow(sessionName, windowName string, env map[string]string, shellCmd string) error {
	return nil
}
func (m *mockTmuxRunner) KillWindow(sessionName, windowName string) error { return nil }
func (m *mockTmuxRunner) ListWindowPIDs(sessionName, windowName string) ([]int, error) {
	return nil, nil
}
func (m *mockTmuxRunner) SendKeys(sessionName, windowName string, keys string) error {
	m.sendKeysCalls = append(m.sendKeysCalls, sendKeysCall{sessionName, windowName, keys})
	return nil
}
func (m *mockTmuxRunner) Attach(name string) error { return nil }

func TestRunMessagesSend_NotifiesRootViaTmux(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTmuxRunner{}
	deps := &messagesDeps{
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "worker-1"
			}
			return ""
		},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		tmuxRunner: mock,
	}

	err := runMessagesSend(deps, "root", "build done", "all tests pass")
	if err != nil {
		t.Fatalf("runMessagesSend() unexpected error: %v", err)
	}

	if len(mock.sendKeysCalls) != 1 {
		t.Fatalf("expected 1 SendKeys call, got %d", len(mock.sendKeysCalls))
	}

	call := mock.sendKeysCalls[0]
	if call.sessionName != tmux.RootSessionName {
		t.Errorf("SendKeys session = %q, want %q", call.sessionName, tmux.RootSessionName)
	}
	if call.windowName != tmux.RootWindowName {
		t.Errorf("SendKeys window = %q, want %q", call.windowName, tmux.RootWindowName)
	}

	wantNotification := "[inbox] Message from worker-1: build done"
	if !strings.Contains(call.keys, wantNotification) {
		t.Errorf("SendKeys keys = %q, want it to contain %q", call.keys, wantNotification)
	}
}

func TestRunMessagesSend_NonRootNoTmuxNotification(t *testing.T) {
	tmpDir := t.TempDir()
	mock := &mockTmuxRunner{}
	deps := &messagesDeps{
		getenv: func(key string) string {
			switch key {
			case "DENDRA_ROOT":
				return tmpDir
			case "DENDRA_AGENT_IDENTITY":
				return "worker-1"
			}
			return ""
		},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		tmuxRunner: mock,
	}

	err := runMessagesSend(deps, "bob", "hello", "world")
	if err != nil {
		t.Fatalf("runMessagesSend() unexpected error: %v", err)
	}

	if len(mock.sendKeysCalls) != 0 {
		t.Errorf("expected 0 SendKeys calls for non-root recipient, got %d", len(mock.sendKeysCalls))
	}
}
