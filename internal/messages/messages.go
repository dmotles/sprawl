package messages

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Message represents a message between agents.
type Message struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Dir       string `json:"-"`
}

// MessagesDir returns the path to the messages directory under the dendra root.
func MessagesDir(dendraRoot string) string {
	return filepath.Join(dendraRoot, ".dendra", "messages")
}

// NotifyFunc is called after successful delivery when the recipient is "root".
// It is best-effort — errors and panics are swallowed.
type NotifyFunc func(from, subject string)

type sendOptions struct {
	notify NotifyFunc
}

// SendOption configures optional behavior for Send.
type SendOption func(*sendOptions)

// WithNotify registers a notification callback invoked when the recipient is "root".
func WithNotify(fn NotifyFunc) SendOption {
	return func(o *sendOptions) {
		o.notify = fn
	}
}

// Send delivers a message from one agent to another using Maildir-style atomic writes.
func Send(dendraRoot, from, to, subject, body string, opts ...SendOption) error {
	if from == "" {
		return fmt.Errorf("sender (from) must not be empty")
	}
	if to == "" {
		return fmt.Errorf("recipient (to) must not be empty")
	}

	agentDir := filepath.Join(MessagesDir(dendraRoot), to)
	for _, sub := range []string{"tmp", "new", "cur", "archive"} {
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", sub, err)
		}
	}

	// Generate random hex suffix
	suffixBytes := make([]byte, 4)
	if _, err := rand.Read(suffixBytes); err != nil {
		return fmt.Errorf("generating random suffix: %w", err)
	}
	hexSuffix := hex.EncodeToString(suffixBytes)

	now := time.Now()
	id := fmt.Sprintf("%d.%s.%s", now.UnixNano(), from, hexSuffix)

	msg := &Message{
		ID:        id,
		From:      from,
		To:        to,
		Subject:   subject,
		Body:      body,
		Timestamp: now.UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}

	filename := id + ".json"
	tmpPath := filepath.Join(agentDir, "tmp", filename)
	newPath := filepath.Join(agentDir, "new", filename)

	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("writing tmp file: %w", err)
	}

	if err := os.Rename(tmpPath, newPath); err != nil {
		return fmt.Errorf("moving message to new/: %w", err)
	}

	// Write a copy to sender's sent/ directory for outbox tracking.
	sentDir := filepath.Join(MessagesDir(dendraRoot), from, "sent")
	if err := os.MkdirAll(sentDir, 0755); err != nil {
		return fmt.Errorf("creating sender sent directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sentDir, filename), data, 0644); err != nil {
		return fmt.Errorf("writing sent copy: %w", err)
	}

	// Best-effort root notification
	var sopts sendOptions
	for _, o := range opts {
		o(&sopts)
	}
	if to == "root" && sopts.notify != nil {
		func() {
			defer func() { recover() }()
			sopts.notify(from, subject)
		}()
	}

	return nil
}

// Inbox returns all messages for an agent from both new/ and cur/ directories,
// sorted by timestamp ascending.
func Inbox(dendraRoot, agent string) ([]*Message, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)

	var result []*Message

	for _, dir := range []string{"new", "cur"} {
		dirPath := filepath.Join(agentDir, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s directory: %w", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dirPath, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("reading message file %s: %w", entry.Name(), err)
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				return nil, fmt.Errorf("unmarshaling message %s: %w", entry.Name(), err)
			}
			msg.Dir = dir
			result = append(result, &msg)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, result[i].Timestamp)
		tj, _ := time.Parse(time.RFC3339, result[j].Timestamp)
		return ti.Before(tj)
	})

	return result, nil
}

// ResolvePrefix finds a full message ID from a prefix by scanning new/, cur/, archive/, sent/ directories.
// Returns the full ID if exactly one match found.
func ResolvePrefix(dendraRoot, agent, prefix string) (string, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	matches := make(map[string]bool)

	for _, dir := range []string{"new", "cur", "archive", "sent"} {
		dirPath := filepath.Join(agentDir, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("reading %s directory: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			id := strings.TrimSuffix(entry.Name(), ".json")
			if strings.HasPrefix(id, prefix) {
				matches[id] = true
			}
		}
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("no message found matching prefix %q", prefix)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous prefix %q: matches %d messages", prefix, len(matches))
	}

	for id := range matches {
		return id, nil
	}
	return "", nil // unreachable
}

// MarkRead moves a message from new/ to cur/.
func MarkRead(dendraRoot, agent, msgID string) error {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	srcPath := filepath.Join(agentDir, "new", msgID+".json")
	dstPath := filepath.Join(agentDir, "cur", msgID+".json")

	if err := os.MkdirAll(filepath.Join(agentDir, "cur"), 0755); err != nil {
		return fmt.Errorf("creating cur directory: %w", err)
	}

	if err := os.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("marking message as read: %w", err)
	}
	return nil
}

// MarkUnread moves a message from cur/ to new/.
func MarkUnread(dendraRoot, agent, msgID string) error {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	srcPath := filepath.Join(agentDir, "cur", msgID+".json")
	dstPath := filepath.Join(agentDir, "new", msgID+".json")

	if err := os.MkdirAll(filepath.Join(agentDir, "new"), 0755); err != nil {
		return fmt.Errorf("creating new directory: %w", err)
	}

	if err := os.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("marking message as unread: %w", err)
	}
	return nil
}

// Archive moves a message from new/ or cur/ to archive/.
func Archive(dendraRoot, agent, msgID string) error {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	filename := msgID + ".json"
	dstPath := filepath.Join(agentDir, "archive", filename)

	if err := os.MkdirAll(filepath.Join(agentDir, "archive"), 0755); err != nil {
		return fmt.Errorf("creating archive directory: %w", err)
	}

	// Try new/ first, then cur/
	srcPath := filepath.Join(agentDir, "new", filename)
	if err := os.Rename(srcPath, dstPath); err == nil {
		return nil
	}

	srcPath = filepath.Join(agentDir, "cur", filename)
	if err := os.Rename(srcPath, dstPath); err != nil {
		return fmt.Errorf("archiving message: not found in new/ or cur/")
	}
	return nil
}

// ReadMessage reads a message from any directory (new/, cur/, archive/), returns it.
// If found in new/, auto-marks as read by moving to cur/.
func ReadMessage(dendraRoot, agent, msgID string) (*Message, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	filename := msgID + ".json"

	for _, dir := range []string{"new", "cur", "archive"} {
		filePath := filepath.Join(agentDir, dir, filename)
		data, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading message file: %w", err)
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, fmt.Errorf("unmarshaling message: %w", err)
		}

		if dir == "new" {
			// Auto-mark as read
			if err := MarkRead(dendraRoot, agent, msgID); err != nil {
				return nil, fmt.Errorf("auto-marking message as read: %w", err)
			}
			msg.Dir = "cur"
		} else {
			msg.Dir = dir
		}

		return &msg, nil
	}

	return nil, fmt.Errorf("message %q not found", msgID)
}

// List returns messages filtered by the given filter.
func List(dendraRoot, agent, filter string) ([]*Message, error) {
	var dirs []string
	switch filter {
	case "", "all":
		dirs = []string{"new", "cur"}
	case "unread":
		dirs = []string{"new"}
	case "read":
		dirs = []string{"cur"}
	case "archived":
		dirs = []string{"archive"}
	case "sent":
		dirs = []string{"sent"}
	default:
		return nil, fmt.Errorf("invalid filter %q: must be one of all, unread, read, archived, sent", filter)
	}

	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	var result []*Message

	for _, dir := range dirs {
		dirPath := filepath.Join(agentDir, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s directory: %w", dir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dirPath, entry.Name()))
			if err != nil {
				return nil, fmt.Errorf("reading message file %s: %w", entry.Name(), err)
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				return nil, fmt.Errorf("unmarshaling message %s: %w", entry.Name(), err)
			}
			msg.Dir = dir
			result = append(result, &msg)
		}
	}

	sort.Slice(result, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, result[i].Timestamp)
		tj, _ := time.Parse(time.RFC3339, result[j].Timestamp)
		return ti.Before(tj)
	})

	return result, nil
}
