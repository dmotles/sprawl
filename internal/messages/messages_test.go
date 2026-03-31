package messages

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMessagesDir(t *testing.T) {
	got := MessagesDir("/home/user/project")
	want := "/home/user/project/.dendra/messages"
	if got != want {
		t.Errorf("MessagesDir() = %q, want %q", got, want)
	}
}

func TestSend_CreatesMessageInNewDir(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "hello", "world")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}

	// tmp/ should be empty (atomic rename means no leftover)
	tmpMsgDir := filepath.Join(MessagesDir(tmpDir), "bob", "tmp")
	tmpEntries, err := os.ReadDir(tmpMsgDir)
	if err != nil {
		t.Fatalf("reading tmp dir: %v", err)
	}
	if len(tmpEntries) != 0 {
		t.Errorf("expected 0 files in tmp/, got %d", len(tmpEntries))
	}
}

func TestSend_MessageIsValidJSON(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "test subject", "test body")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(newDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading message file: %v", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshaling message: %v", err)
	}

	if msg.From != "alice" {
		t.Errorf("From = %q, want %q", msg.From, "alice")
	}
	if msg.To != "bob" {
		t.Errorf("To = %q, want %q", msg.To, "bob")
	}
	if msg.Subject != "test subject" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "test subject")
	}
	if msg.Body != "test body" {
		t.Errorf("Body = %q, want %q", msg.Body, "test body")
	}
	if msg.ID == "" {
		t.Error("ID should not be empty")
	}
	if msg.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}
	// Verify timestamp is valid RFC3339
	if _, err := time.Parse(time.RFC3339, msg.Timestamp); err != nil {
		t.Errorf("Timestamp %q is not valid RFC3339: %v", msg.Timestamp, err)
	}
}

func TestSend_MessageIDFormat(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "subj", "body")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(newDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading message file: %v", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshaling message: %v", err)
	}

	// ID format: <unix-nano>.<sender>.<hex-suffix>
	pattern := regexp.MustCompile(`^\d+\.\w+\.[a-f0-9]+$`)
	if !pattern.MatchString(msg.ID) {
		t.Errorf("ID %q does not match expected pattern <unix-nano>.<sender>.<hex>", msg.ID)
	}

	// ID should contain the sender name
	if !strings.Contains(msg.ID, "alice") {
		t.Errorf("ID %q should contain sender name 'alice'", msg.ID)
	}
}

func TestSend_ConcurrentSends(t *testing.T) {
	tmpDir := t.TempDir()

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = Send(tmpDir, "alice", "bob", "concurrent", "message")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Send() error: %v", i, err)
		}
	}

	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != n {
		t.Errorf("expected %d files in new/, got %d", n, len(entries))
	}

	// All IDs should be distinct
	ids := make(map[string]bool)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(newDir, e.Name()))
		if err != nil {
			t.Fatalf("reading file: %v", err)
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshaling: %v", err)
		}
		if ids[msg.ID] {
			t.Errorf("duplicate message ID: %s", msg.ID)
		}
		ids[msg.ID] = true
	}

	// Sender's sent/ directory should contain exactly n files with distinct IDs
	sentDir := filepath.Join(MessagesDir(tmpDir), "alice", "sent")
	sentEntries, err := os.ReadDir(sentDir)
	if err != nil {
		t.Fatalf("reading sent dir: %v", err)
	}
	if len(sentEntries) != n {
		t.Errorf("expected %d files in alice/sent/, got %d", n, len(sentEntries))
	}

	sentIDs := make(map[string]bool)
	for _, e := range sentEntries {
		data, err := os.ReadFile(filepath.Join(sentDir, e.Name()))
		if err != nil {
			t.Fatalf("reading sent file: %v", err)
		}
		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshaling sent message: %v", err)
		}
		if sentIDs[msg.ID] {
			t.Errorf("duplicate sent message ID: %s", msg.ID)
		}
		sentIDs[msg.ID] = true
	}
}

func TestSend_CreatesSentCopy(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "subj", "body")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	sentDir := filepath.Join(MessagesDir(tmpDir), "alice", "sent")
	entries, err := os.ReadDir(sentDir)
	if err != nil {
		t.Fatalf("reading sent dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in alice/sent/, got %d", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(sentDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("reading sent message file: %v", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshaling sent message: %v", err)
	}

	if msg.From != "alice" {
		t.Errorf("From = %q, want %q", msg.From, "alice")
	}
	if msg.To != "bob" {
		t.Errorf("To = %q, want %q", msg.To, "bob")
	}
	if msg.Subject != "subj" {
		t.Errorf("Subject = %q, want %q", msg.Subject, "subj")
	}
	if msg.Body != "body" {
		t.Errorf("Body = %q, want %q", msg.Body, "body")
	}
	if msg.ID == "" {
		t.Error("ID should not be empty")
	}
	if msg.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}
}

func TestSend_SentCopyMatchesDelivered(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "subj", "body")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	// Read the delivered copy from bob/new/
	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	newEntries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(newEntries) != 1 {
		t.Fatalf("expected 1 file in bob/new/, got %d", len(newEntries))
	}
	deliveredData, err := os.ReadFile(filepath.Join(newDir, newEntries[0].Name()))
	if err != nil {
		t.Fatalf("reading delivered message: %v", err)
	}

	// Read the sent copy from alice/sent/
	sentDir := filepath.Join(MessagesDir(tmpDir), "alice", "sent")
	sentEntries, err := os.ReadDir(sentDir)
	if err != nil {
		t.Fatalf("reading sent dir: %v", err)
	}
	if len(sentEntries) != 1 {
		t.Fatalf("expected 1 file in alice/sent/, got %d", len(sentEntries))
	}
	sentData, err := os.ReadFile(filepath.Join(sentDir, sentEntries[0].Name()))
	if err != nil {
		t.Fatalf("reading sent message: %v", err)
	}

	if !bytes.Equal(deliveredData, sentData) {
		t.Errorf("sent copy does not match delivered copy\ndelivered: %s\nsent: %s", deliveredData, sentData)
	}
}

func TestInbox_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	msgs, err := Inbox(tmpDir, "nonexistent-agent")
	if err != nil {
		t.Fatalf("Inbox() unexpected error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestInbox_NewAndCurMessages(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(MessagesDir(tmpDir), "bob")

	// Create new/ and cur/ directories
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}
	if err := os.MkdirAll(curDir, 0755); err != nil {
		t.Fatalf("creating cur dir: %v", err)
	}

	// Write a message in new/
	newMsg := Message{
		ID:        "1000.alice.aabb",
		From:      "alice",
		To:        "bob",
		Subject:   "new message",
		Body:      "this is new",
		Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, newDir, "1000.alice.aabb", &newMsg)

	// Write a message in cur/
	curMsg := Message{
		ID:        "900.charlie.ccdd",
		From:      "charlie",
		To:        "bob",
		Subject:   "read message",
		Body:      "this was read",
		Timestamp: "2026-03-31T09:00:00Z",
	}
	writeMessageFile(t, curDir, "900.charlie.ccdd", &curMsg)

	msgs, err := Inbox(tmpDir, "bob")
	if err != nil {
		t.Fatalf("Inbox() unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}

	// Check Dir tags
	foundNew := false
	foundCur := false
	for _, m := range msgs {
		if m.ID == "1000.alice.aabb" && m.Dir == "new" {
			foundNew = true
		}
		if m.ID == "900.charlie.ccdd" && m.Dir == "cur" {
			foundCur = true
		}
	}
	if !foundNew {
		t.Error("expected to find new/ message with Dir='new'")
	}
	if !foundCur {
		t.Error("expected to find cur/ message with Dir='cur'")
	}
}

func TestInbox_SortedByTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(MessagesDir(tmpDir), "bob")
	newDir := filepath.Join(agentDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}

	// Write messages out of order
	msgs := []Message{
		{
			ID:        "3000.charlie.0003",
			From:      "charlie",
			To:        "bob",
			Subject:   "third",
			Body:      "3",
			Timestamp: "2026-03-31T12:00:00Z",
		},
		{
			ID:        "1000.alice.0001",
			From:      "alice",
			To:        "bob",
			Subject:   "first",
			Body:      "1",
			Timestamp: "2026-03-31T10:00:00Z",
		},
		{
			ID:        "2000.bob.0002",
			From:      "bob",
			To:        "bob",
			Subject:   "second",
			Body:      "2",
			Timestamp: "2026-03-31T11:00:00Z",
		},
	}

	for _, m := range msgs {
		writeMessageFile(t, newDir, m.ID, &m)
	}

	result, err := Inbox(tmpDir, "bob")
	if err != nil {
		t.Fatalf("Inbox() unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// Should be sorted ascending by timestamp
	if result[0].Subject != "first" {
		t.Errorf("result[0].Subject = %q, want %q", result[0].Subject, "first")
	}
	if result[1].Subject != "second" {
		t.Errorf("result[1].Subject = %q, want %q", result[1].Subject, "second")
	}
	if result[2].Subject != "third" {
		t.Errorf("result[2].Subject = %q, want %q", result[2].Subject, "third")
	}
}

func TestSend_CreatesDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "subj", "body")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	agentDir := filepath.Join(MessagesDir(tmpDir), "bob")
	for _, sub := range []string{"tmp", "new", "cur", "archive"} {
		dir := filepath.Join(agentDir, sub)
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %s to be a directory", sub)
		}
	}

	// Sender's sent/ directory should also be created
	senderSentDir := filepath.Join(MessagesDir(tmpDir), "alice", "sent")
	info, err := os.Stat(senderSentDir)
	if err != nil {
		t.Errorf("expected sender's sent/ directory to exist: %v", err)
	} else if !info.IsDir() {
		t.Errorf("expected alice/sent/ to be a directory")
	}
}

func TestSend_EmptyRecipient(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "", "subj", "body")
	if err == nil {
		t.Fatal("expected error for empty recipient")
	}
	if !strings.Contains(err.Error(), "recipient") && !strings.Contains(err.Error(), "to") {
		t.Errorf("error should mention recipient/to, got: %v", err)
	}
}

func TestSend_EmptySender(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "", "bob", "subj", "body")
	if err == nil {
		t.Fatal("expected error for empty sender")
	}
	if !strings.Contains(err.Error(), "sender") && !strings.Contains(err.Error(), "from") {
		t.Errorf("error should mention sender/from, got: %v", err)
	}
}

// writeMessageFile is a test helper that writes a Message as JSON into the given directory.
func writeMessageFile(t *testing.T, dir, filename string, msg *Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0644); err != nil {
		t.Fatalf("writing message file: %v", err)
	}
}
