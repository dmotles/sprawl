package messages

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Send delivers a message from one agent to another using Maildir-style atomic writes.
func Send(dendraRoot, from, to, subject, body string) error {
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

	id := fmt.Sprintf("%d.%s.%s", time.Now().UnixNano(), from, hexSuffix)

	msg := &Message{
		ID:        id,
		From:      from,
		To:        to,
		Subject:   subject,
		Body:      body,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
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
			if entry.IsDir() {
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
