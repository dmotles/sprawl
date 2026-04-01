package messages

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/state"
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

func TestSend_ConsistentTimestamp(t *testing.T) {
	tmpDir := t.TempDir()

	err := Send(tmpDir, "alice", "bob", "timestamp test", "body")
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

	// Parse the nanosecond prefix from the ID (format: <unix-nano>.<sender>.<hex>)
	parts := strings.SplitN(msg.ID, ".", 3)
	if len(parts) < 3 {
		t.Fatalf("unexpected ID format: %q", msg.ID)
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("parsing nanosecond prefix from ID: %v", err)
	}
	idTime := time.Unix(0, nanos)

	tsTime, err := time.Parse(time.RFC3339, msg.Timestamp)
	if err != nil {
		t.Fatalf("parsing Timestamp field: %v", err)
	}

	// The ID nanosecond prefix and the Timestamp field must represent the same second.
	// This contract ensures Send() uses a single time.Now() call for both.
	if idTime.Unix() != tsTime.Unix() {
		t.Errorf("ID time and Timestamp time differ: ID=%v (%d), Timestamp=%v (%d)",
			idTime, idTime.Unix(), tsTime, tsTime.Unix())
	}
}

func TestInbox_SkipsNonJSONFiles(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(MessagesDir(tmpDir), "bob")
	newDir := filepath.Join(agentDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}

	// Write a valid JSON message file with .json extension
	validMsg := Message{
		ID:        "1000.alice.aabb",
		From:      "alice",
		To:        "bob",
		Subject:   "hello",
		Body:      "world",
		Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, newDir, "1000.alice.aabb", &validMsg)

	// Write a junk non-JSON file that should be skipped
	if err := os.WriteFile(filepath.Join(newDir, ".DS_Store"), []byte("\x00\x00\x00\x01Bud1"), 0644); err != nil {
		t.Fatalf("writing .DS_Store: %v", err)
	}

	msgs, err := Inbox(tmpDir, "bob")
	if err != nil {
		t.Fatalf("Inbox() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

// --- ResolvePrefix tests ---

func TestResolvePrefix_ExactMatch(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, newDir, "1000.alice.aabb", msg)

	got, err := ResolvePrefix(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("ResolvePrefix() unexpected error: %v", err)
	}
	if got != "1000.alice.aabb" {
		t.Errorf("ResolvePrefix() = %q, want %q", got, "1000.alice.aabb")
	}
}

func TestResolvePrefix_UniquePrefix(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}
	if err := os.MkdirAll(curDir, 0755); err != nil {
		t.Fatalf("creating cur dir: %v", err)
	}

	msg1 := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "first", Body: "body1", Timestamp: "2026-03-31T10:00:00Z",
	}
	msg2 := &Message{
		ID: "2000.charlie.ccdd", From: "charlie", To: "bob",
		Subject: "second", Body: "body2", Timestamp: "2026-03-31T11:00:00Z",
	}
	writeMessageFile(t, newDir, msg1.ID, msg1)
	writeMessageFile(t, curDir, msg2.ID, msg2)

	got, err := ResolvePrefix(tmpDir, agent, "1000")
	if err != nil {
		t.Fatalf("ResolvePrefix() unexpected error: %v", err)
	}
	if got != "1000.alice.aabb" {
		t.Errorf("ResolvePrefix() = %q, want %q", got, "1000.alice.aabb")
	}
}

func TestResolvePrefix_AmbiguousPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}

	msg1 := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "first", Body: "body1", Timestamp: "2026-03-31T10:00:00Z",
	}
	msg2 := &Message{
		ID: "1000.alice.ccdd", From: "alice", To: "bob",
		Subject: "second", Body: "body2", Timestamp: "2026-03-31T11:00:00Z",
	}
	writeMessageFile(t, newDir, msg1.ID, msg1)
	writeMessageFile(t, newDir, msg2.ID, msg2)

	_, err := ResolvePrefix(tmpDir, agent, "1000")
	if err == nil {
		t.Fatal("expected error for ambiguous prefix")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should contain 'ambiguous', got: %v", err)
	}
}

func TestResolvePrefix_NoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	_, err := ResolvePrefix(tmpDir, agent, "nonexistent")
	if err == nil {
		t.Fatal("expected error for no match")
	}
	if !strings.Contains(err.Error(), "no message found") {
		t.Errorf("error should contain 'no message found', got: %v", err)
	}
}

// --- MarkRead tests ---

func TestMarkRead_MovesFromNewToCur(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}
	if err := os.MkdirAll(curDir, 0755); err != nil {
		t.Fatalf("creating cur dir: %v", err)
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, newDir, msg.ID, msg)

	err := MarkRead(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("MarkRead() unexpected error: %v", err)
	}

	// File should no longer be in new/
	if _, err := os.Stat(filepath.Join(newDir, "1000.alice.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from new/")
	}

	// File should be in cur/
	data, err := os.ReadFile(filepath.Join(curDir, "1000.alice.aabb.json"))
	if err != nil {
		t.Fatalf("expected file in cur/: %v", err)
	}

	// Content should be preserved
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling message: %v", err)
	}
	if got.ID != "1000.alice.aabb" {
		t.Errorf("ID = %q, want %q", got.ID, "1000.alice.aabb")
	}
	if got.Subject != "hello" {
		t.Errorf("Subject = %q, want %q", got.Subject, "hello")
	}
}

func TestMarkRead_NotInNew(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	err := MarkRead(tmpDir, agent, "nonexistent")
	if err == nil {
		t.Fatal("expected error when message not in new/")
	}
}

// --- MarkUnread tests ---

func TestMarkUnread_MovesFromCurToNew(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}
	if err := os.MkdirAll(curDir, 0755); err != nil {
		t.Fatalf("creating cur dir: %v", err)
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, curDir, msg.ID, msg)

	err := MarkUnread(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("MarkUnread() unexpected error: %v", err)
	}

	// File should no longer be in cur/
	if _, err := os.Stat(filepath.Join(curDir, "1000.alice.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from cur/")
	}

	// File should be in new/
	data, err := os.ReadFile(filepath.Join(newDir, "1000.alice.aabb.json"))
	if err != nil {
		t.Fatalf("expected file in new/: %v", err)
	}

	// Content should be preserved
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshaling message: %v", err)
	}
	if got.ID != "1000.alice.aabb" {
		t.Errorf("ID = %q, want %q", got.ID, "1000.alice.aabb")
	}
	if got.Subject != "hello" {
		t.Errorf("Subject = %q, want %q", got.Subject, "hello")
	}
}

func TestMarkUnread_NotInCur(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	err := MarkUnread(tmpDir, agent, "nonexistent")
	if err == nil {
		t.Fatal("expected error when message not in cur/")
	}
}

// --- Archive tests ---

func TestArchive_FromNew(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	archiveDir := filepath.Join(agentDir, "archive")
	for _, sub := range []string{"new", "cur", "archive"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, newDir, msg.ID, msg)

	err := Archive(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("Archive() unexpected error: %v", err)
	}

	// File should no longer be in new/
	if _, err := os.Stat(filepath.Join(newDir, "1000.alice.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from new/")
	}

	// File should be in archive/
	if _, err := os.Stat(filepath.Join(archiveDir, "1000.alice.aabb.json")); err != nil {
		t.Errorf("expected file in archive/: %v", err)
	}
}

func TestArchive_FromCur(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	curDir := filepath.Join(agentDir, "cur")
	archiveDir := filepath.Join(agentDir, "archive")
	for _, sub := range []string{"new", "cur", "archive"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, curDir, msg.ID, msg)

	err := Archive(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("Archive() unexpected error: %v", err)
	}

	// File should no longer be in cur/
	if _, err := os.Stat(filepath.Join(curDir, "1000.alice.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from cur/")
	}

	// File should be in archive/
	if _, err := os.Stat(filepath.Join(archiveDir, "1000.alice.aabb.json")); err != nil {
		t.Errorf("expected file in archive/: %v", err)
	}
}

func TestArchive_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	err := Archive(tmpDir, agent, "nonexistent")
	if err == nil {
		t.Fatal("expected error when message not found")
	}
}

// --- ReadMessage tests ---

func TestReadMessage_FromNew_AutoMarksRead(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	curDir := filepath.Join(agentDir, "cur")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, newDir, msg.ID, msg)

	got, err := ReadMessage(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("ReadMessage() unexpected error: %v", err)
	}

	if got.ID != "1000.alice.aabb" {
		t.Errorf("ID = %q, want %q", got.ID, "1000.alice.aabb")
	}
	if got.Subject != "hello" {
		t.Errorf("Subject = %q, want %q", got.Subject, "hello")
	}
	if got.Dir != "cur" {
		t.Errorf("Dir = %q, want %q (should be auto-marked read)", got.Dir, "cur")
	}

	// File should have moved from new/ to cur/
	if _, err := os.Stat(filepath.Join(newDir, "1000.alice.aabb.json")); !os.IsNotExist(err) {
		t.Error("expected file to be gone from new/")
	}
	if _, err := os.Stat(filepath.Join(curDir, "1000.alice.aabb.json")); err != nil {
		t.Errorf("expected file in cur/: %v", err)
	}
}

func TestReadMessage_FromCur(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	curDir := filepath.Join(agentDir, "cur")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, curDir, msg.ID, msg)

	got, err := ReadMessage(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("ReadMessage() unexpected error: %v", err)
	}

	if got.Dir != "cur" {
		t.Errorf("Dir = %q, want %q", got.Dir, "cur")
	}

	// File should still be in cur/
	if _, err := os.Stat(filepath.Join(curDir, "1000.alice.aabb.json")); err != nil {
		t.Errorf("expected file to remain in cur/: %v", err)
	}
}

func TestReadMessage_FromArchive(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	archiveDir := filepath.Join(agentDir, "archive")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	msg := &Message{
		ID: "1000.alice.aabb", From: "alice", To: "bob",
		Subject: "hello", Body: "world", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, archiveDir, msg.ID, msg)

	got, err := ReadMessage(tmpDir, agent, "1000.alice.aabb")
	if err != nil {
		t.Fatalf("ReadMessage() unexpected error: %v", err)
	}

	if got.Dir != "archive" {
		t.Errorf("Dir = %q, want %q", got.Dir, "archive")
	}
}

func TestReadMessage_FromSent(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	sentDir := filepath.Join(agentDir, "sent")
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	msg := &Message{
		ID: "1000.bob.ccdd", From: "bob", To: "alice",
		Subject: "outgoing", Body: "sent message", Timestamp: "2026-03-31T10:00:00Z",
	}
	writeMessageFile(t, sentDir, msg.ID, msg)

	got, err := ReadMessage(tmpDir, agent, "1000.bob.ccdd")
	if err != nil {
		t.Fatalf("ReadMessage() unexpected error: %v", err)
	}

	if got.Dir != "sent" {
		t.Errorf("Dir = %q, want %q", got.Dir, "sent")
	}

	// File should still be in sent/ (no auto-mark-read move)
	if _, err := os.Stat(filepath.Join(sentDir, "1000.bob.ccdd.json")); err != nil {
		t.Errorf("expected file to remain in sent/: %v", err)
	}

	// File should NOT appear in cur/ (no mark-read behavior)
	curPath := filepath.Join(agentDir, "cur", "1000.bob.ccdd.json")
	if _, err := os.Stat(curPath); !os.IsNotExist(err) {
		t.Errorf("expected file NOT to be in cur/, but it exists")
	}
}

func TestReadMessage_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	_, err := ReadMessage(tmpDir, agent, "nonexistent")
	if err == nil {
		t.Fatal("expected error when message not found")
	}
}

// --- List tests ---

func TestList_All(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeMessageFile(t, filepath.Join(agentDir, "new"), "1000.alice.aa01", &Message{
		ID: "1000.alice.aa01", From: "alice", To: "bob",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "cur"), "2000.alice.aa02", &Message{
		ID: "2000.alice.aa02", From: "alice", To: "bob",
		Subject: "read msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "archive"), "3000.alice.aa03", &Message{
		ID: "3000.alice.aa03", From: "alice", To: "bob",
		Subject: "archived msg", Body: "body", Timestamp: "2026-03-31T12:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "sent"), "4000.bob.aa04", &Message{
		ID: "4000.bob.aa04", From: "bob", To: "alice",
		Subject: "sent msg", Body: "body", Timestamp: "2026-03-31T13:00:00Z",
	})

	// "all" or "" should return new/ + cur/ only, not archived or sent
	msgs, err := List(tmpDir, agent, "all")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages for 'all', got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.Dir == "archive" || m.Dir == "sent" {
			t.Errorf("List('all') should not include %s messages", m.Dir)
		}
	}
}

func TestList_Unread(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeMessageFile(t, filepath.Join(agentDir, "new"), "1000.alice.aa01", &Message{
		ID: "1000.alice.aa01", From: "alice", To: "bob",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "cur"), "2000.alice.aa02", &Message{
		ID: "2000.alice.aa02", From: "alice", To: "bob",
		Subject: "read msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})

	msgs, err := List(tmpDir, agent, "unread")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for 'unread', got %d", len(msgs))
	}
	if msgs[0].Dir != "new" {
		t.Errorf("expected Dir='new', got %q", msgs[0].Dir)
	}
}

func TestList_Read(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeMessageFile(t, filepath.Join(agentDir, "new"), "1000.alice.aa01", &Message{
		ID: "1000.alice.aa01", From: "alice", To: "bob",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "cur"), "2000.alice.aa02", &Message{
		ID: "2000.alice.aa02", From: "alice", To: "bob",
		Subject: "read msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})

	msgs, err := List(tmpDir, agent, "read")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for 'read', got %d", len(msgs))
	}
	if msgs[0].Dir != "cur" {
		t.Errorf("expected Dir='cur', got %q", msgs[0].Dir)
	}
}

func TestList_Archived(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeMessageFile(t, filepath.Join(agentDir, "archive"), "1000.alice.aa01", &Message{
		ID: "1000.alice.aa01", From: "alice", To: "bob",
		Subject: "archived msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "new"), "2000.alice.aa02", &Message{
		ID: "2000.alice.aa02", From: "alice", To: "bob",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})

	msgs, err := List(tmpDir, agent, "archived")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for 'archived', got %d", len(msgs))
	}
	if msgs[0].Dir != "archive" {
		t.Errorf("expected Dir='archive', got %q", msgs[0].Dir)
	}
}

func TestList_Sent(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	for _, sub := range []string{"new", "cur", "archive", "sent"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	writeMessageFile(t, filepath.Join(agentDir, "sent"), "1000.bob.aa01", &Message{
		ID: "1000.bob.aa01", From: "bob", To: "alice",
		Subject: "sent msg", Body: "body", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeMessageFile(t, filepath.Join(agentDir, "new"), "2000.alice.aa02", &Message{
		ID: "2000.alice.aa02", From: "alice", To: "bob",
		Subject: "new msg", Body: "body", Timestamp: "2026-03-31T11:00:00Z",
	})

	msgs, err := List(tmpDir, agent, "sent")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message for 'sent', got %d", len(msgs))
	}
	if msgs[0].Dir != "sent" {
		t.Errorf("expected Dir='sent', got %q", msgs[0].Dir)
	}
}

func TestList_InvalidFilter(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"

	_, err := List(tmpDir, agent, "bogus")
	if err == nil {
		t.Fatal("expected error for invalid filter")
	}
}

func TestList_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"

	msgs, err := List(tmpDir, agent, "all")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if msgs == nil {
		// Accept nil or empty slice, but check length
		msgs = []*Message{}
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestList_SortedByTimestamp(t *testing.T) {
	tmpDir := t.TempDir()
	agent := "bob"
	agentDir := filepath.Join(MessagesDir(tmpDir), agent)
	newDir := filepath.Join(agentDir, "new")
	if err := os.MkdirAll(newDir, 0755); err != nil {
		t.Fatalf("creating new dir: %v", err)
	}

	// Write messages out of order
	writeMessageFile(t, newDir, "3000.charlie.0003", &Message{
		ID: "3000.charlie.0003", From: "charlie", To: "bob",
		Subject: "third", Body: "3", Timestamp: "2026-03-31T12:00:00Z",
	})
	writeMessageFile(t, newDir, "1000.alice.0001", &Message{
		ID: "1000.alice.0001", From: "alice", To: "bob",
		Subject: "first", Body: "1", Timestamp: "2026-03-31T10:00:00Z",
	})
	writeMessageFile(t, newDir, "2000.bob.0002", &Message{
		ID: "2000.bob.0002", From: "bob", To: "bob",
		Subject: "second", Body: "2", Timestamp: "2026-03-31T11:00:00Z",
	})

	result, err := List(tmpDir, agent, "unread")
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

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

func TestSend_WritesWakeFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the agents directory so wake file can be written
	agentsDir := filepath.Join(tmpDir, ".dendra", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}

	err := Send(tmpDir, "alice", "bob", "hello there", "body")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	wakePath := filepath.Join(agentsDir, "bob.wake")
	data, err := os.ReadFile(wakePath)
	if err != nil {
		t.Fatalf("reading wake file: %v", err)
	}

	want := "New message from alice: hello there"
	if string(data) != want {
		t.Errorf("wake file content = %q, want %q", string(data), want)
	}
}

func TestSend_WakeFileOverwritten(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the agents directory so wake file can be written
	agentsDir := filepath.Join(tmpDir, ".dendra", "agents")
	if err := os.MkdirAll(agentsDir, 0755); err != nil {
		t.Fatalf("creating agents dir: %v", err)
	}

	err := Send(tmpDir, "alice", "bob", "first subject", "body1")
	if err != nil {
		t.Fatalf("Send() first unexpected error: %v", err)
	}

	err = Send(tmpDir, "charlie", "bob", "second subject", "body2")
	if err != nil {
		t.Fatalf("Send() second unexpected error: %v", err)
	}

	wakePath := filepath.Join(agentsDir, "bob.wake")
	data, err := os.ReadFile(wakePath)
	if err != nil {
		t.Fatalf("reading wake file: %v", err)
	}

	want := "New message from charlie: second subject"
	if string(data) != want {
		t.Errorf("wake file content = %q, want %q", string(data), want)
	}
}

func TestSend_WakeFileIgnoresErrors(t *testing.T) {
	tmpDir := t.TempDir()

	// Intentionally do NOT create .dendra/agents/ directory.
	// Send should still succeed; wake file write is best-effort.
	err := Send(tmpDir, "alice", "bob", "subj", "body")
	if err != nil {
		t.Fatalf("Send() should succeed even when wake file cannot be written, got error: %v", err)
	}

	// Verify message was still delivered
	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 message in new/, got %d", len(entries))
	}
}

func TestBroadcast_SendsToAllActiveAgents(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 3 agents: 2 active, 1 killed
	agents := []struct {
		name   string
		status string
	}{
		{"oak", "active"},
		{"pine", "active"},
		{"elm", "killed"},
	}
	for _, a := range agents {
		if err := state.SaveAgent(tmpDir, &state.AgentState{
			Name:   a.name,
			Status: a.status,
		}); err != nil {
			t.Fatalf("saving agent %s: %v", a.name, err)
		}
	}

	count, err := Broadcast(tmpDir, "external-sender", "announcement", "hello everyone")
	if err != nil {
		t.Fatalf("Broadcast() unexpected error: %v", err)
	}
	if count != 2 {
		t.Errorf("Broadcast() returned count %d, want 2", count)
	}

	// Verify oak and pine got messages
	for _, name := range []string{"oak", "pine"} {
		msgs, err := Inbox(tmpDir, name)
		if err != nil {
			t.Fatalf("Inbox(%s) error: %v", name, err)
		}
		if len(msgs) != 1 {
			t.Errorf("expected 1 message for %s, got %d", name, len(msgs))
		}
		if len(msgs) > 0 {
			if msgs[0].Subject != "announcement" {
				t.Errorf("%s message subject = %q, want %q", name, msgs[0].Subject, "announcement")
			}
		}
	}

	// Verify elm (killed) did NOT get a message
	msgs, err := Inbox(tmpDir, "elm")
	if err != nil {
		t.Fatalf("Inbox(elm) error: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages for killed agent elm, got %d", len(msgs))
	}
}

func TestBroadcast_ExcludesSender(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 2 active agents, one of which is the sender
	for _, name := range []string{"oak", "pine"} {
		if err := state.SaveAgent(tmpDir, &state.AgentState{
			Name:   name,
			Status: "active",
		}); err != nil {
			t.Fatalf("saving agent %s: %v", name, err)
		}
	}

	count, err := Broadcast(tmpDir, "oak", "hello", "body")
	if err != nil {
		t.Fatalf("Broadcast() unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("Broadcast() returned count %d, want 1 (should exclude sender)", count)
	}

	// oak should NOT have a message in inbox
	oakMsgs, err := Inbox(tmpDir, "oak")
	if err != nil {
		t.Fatalf("Inbox(oak) error: %v", err)
	}
	if len(oakMsgs) != 0 {
		t.Errorf("sender oak should not receive broadcast, got %d messages", len(oakMsgs))
	}

	// pine should have the message
	pineMsgs, err := Inbox(tmpDir, "pine")
	if err != nil {
		t.Fatalf("Inbox(pine) error: %v", err)
	}
	if len(pineMsgs) != 1 {
		t.Errorf("expected 1 message for pine, got %d", len(pineMsgs))
	}
}

func TestBroadcast_NoActiveAgents(t *testing.T) {
	tmpDir := t.TempDir()

	// No agent state files at all
	count, err := Broadcast(tmpDir, "sender", "subj", "body")
	if err != nil {
		t.Fatalf("Broadcast() unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("Broadcast() returned count %d, want 0", count)
	}
}

func TestBroadcast_ReturnsCount(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 4 active agents
	for _, name := range []string{"a1", "a2", "a3", "a4"} {
		if err := state.SaveAgent(tmpDir, &state.AgentState{
			Name:   name,
			Status: "active",
		}); err != nil {
			t.Fatalf("saving agent %s: %v", name, err)
		}
	}

	count, err := Broadcast(tmpDir, "external", "subj", "body")
	if err != nil {
		t.Fatalf("Broadcast() unexpected error: %v", err)
	}
	if count != 4 {
		t.Errorf("Broadcast() returned count %d, want 4", count)
	}
}

func TestBroadcast_WritesWakeFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create 2 active agents
	for _, name := range []string{"oak", "pine"} {
		if err := state.SaveAgent(tmpDir, &state.AgentState{
			Name:   name,
			Status: "active",
		}); err != nil {
			t.Fatalf("saving agent %s: %v", name, err)
		}
	}

	_, err := Broadcast(tmpDir, "sender", "wake-test", "body")
	if err != nil {
		t.Fatalf("Broadcast() unexpected error: %v", err)
	}

	agentsDir := filepath.Join(tmpDir, ".dendra", "agents")
	for _, name := range []string{"oak", "pine"} {
		wakePath := filepath.Join(agentsDir, name+".wake")
		data, err := os.ReadFile(wakePath)
		if err != nil {
			t.Fatalf("reading wake file for %s: %v", name, err)
		}
		want := "New message from sender: wake-test"
		if string(data) != want {
			t.Errorf("wake file for %s = %q, want %q", name, string(data), want)
		}
	}
}

// writeMessageFile is a test helper that writes a Message as JSON into the given directory.
func writeMessageFile(t *testing.T, dir, filename string, msg *Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshaling message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename+".json"), data, 0644); err != nil {
		t.Fatalf("writing message file: %v", err)
	}
}

func TestSend_WithNotify_RootRecipientCallsNotify(t *testing.T) {
	tmpDir := t.TempDir()

	var calledFrom, calledSubject string
	notifyCalled := false
	notify := func(from, subject string) {
		notifyCalled = true
		calledFrom = from
		calledSubject = subject
	}

	err := Send(tmpDir, "alice", "root", "urgent", "please read", WithNotify(notify))
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	if !notifyCalled {
		t.Fatal("expected notify callback to be called for root recipient")
	}
	if calledFrom != "alice" {
		t.Errorf("notify from = %q, want %q", calledFrom, "alice")
	}
	if calledSubject != "urgent" {
		t.Errorf("notify subject = %q, want %q", calledSubject, "urgent")
	}

	// Also verify the message was delivered
	newDir := filepath.Join(MessagesDir(tmpDir), "root", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}
}

func TestSend_WithNotify_AnyRecipientCallsNotify(t *testing.T) {
	tmpDir := t.TempDir()

	notifyCalled := false
	notify := func(from, subject string) {
		notifyCalled = true
	}

	err := Send(tmpDir, "alice", "bob", "hello", "world", WithNotify(notify))
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	if !notifyCalled {
		t.Fatal("expected notify callback to be called regardless of recipient name")
	}

	// Verify message was still delivered
	newDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}
}

func TestSend_WithoutNotify_StillWorks(t *testing.T) {
	tmpDir := t.TempDir()

	// Send to root WITHOUT any options -- backward compatibility
	err := Send(tmpDir, "alice", "root", "hello", "world")
	if err != nil {
		t.Fatalf("Send() unexpected error: %v", err)
	}

	newDir := filepath.Join(MessagesDir(tmpDir), "root", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}
}

func TestSend_NotifyPanicDoesNotBreakSend(t *testing.T) {
	tmpDir := t.TempDir()

	notify := func(from, subject string) {
		panic("notification system exploded")
	}

	err := Send(tmpDir, "alice", "root", "urgent", "body", WithNotify(notify))
	if err != nil {
		t.Fatalf("Send() should return nil even when notify panics, got: %v", err)
	}

	// Verify message was still delivered despite panic
	newDir := filepath.Join(MessagesDir(tmpDir), "root", "new")
	entries, err := os.ReadDir(newDir)
	if err != nil {
		t.Fatalf("reading new dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in new/, got %d", len(entries))
	}
}

func TestSend_SentCopyFailureDoesNotReturnError(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-create the sender's messages directory as a read-only directory
	// so that MkdirAll for sent/ will fail.
	senderDir := filepath.Join(MessagesDir(tmpDir), "alice")
	if err := os.MkdirAll(senderDir, 0755); err != nil {
		t.Fatalf("setup: creating sender dir: %v", err)
	}
	// Make sender dir read-only so creating sent/ subdirectory fails.
	if err := os.Chmod(senderDir, 0555); err != nil {
		t.Fatalf("setup: chmod sender dir: %v", err)
	}
	t.Cleanup(func() {
		// Restore permissions so t.TempDir() cleanup can remove it.
		os.Chmod(senderDir, 0755)
	})

	// Send should succeed (delivery to recipient) even though sent copy fails.
	err := Send(tmpDir, "alice", "bob", "hello", "world")
	if err != nil {
		t.Fatalf("Send() returned error %v; want nil (sent copy failure should be ignored)", err)
	}

	// Verify the message was delivered to the recipient's new/ directory.
	recipientNewDir := filepath.Join(MessagesDir(tmpDir), "bob", "new")
	entries, err := os.ReadDir(recipientNewDir)
	if err != nil {
		t.Fatalf("reading recipient new/ dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in recipient new/, got %d", len(entries))
	}

	// Verify no sent copy was created (since the directory was read-only).
	sentDir := filepath.Join(senderDir, "sent")
	_, err = os.Stat(sentDir)
	if err == nil {
		t.Error("expected sent/ directory to not exist (should have failed to create)")
	}
}
