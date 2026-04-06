package messages

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dmotles/sprawl/internal/state"
)

// NowFunc is the time source used by the messages package. Override in tests for determinism.
var NowFunc = time.Now

// RandReader is the randomness source used by the messages package. Override in tests for determinism.
var RandReader = rand.Reader

// Message represents a message between agents.
type Message struct {
	ID        string `json:"id"`
	ShortID   string `json:"shortId,omitempty"`
	From      string `json:"from"`
	To        string `json:"to"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Timestamp string `json:"timestamp"`
	Dir       string `json:"-"`
}

// MessagesDir returns the path to the messages directory under the sprawl root.
func MessagesDir(dendraRoot string) string { //nolint:revive // stuttering name is part of public API
	return filepath.Join(dendraRoot, ".sprawl", "messages")
}

// NotifyFunc is called after successful delivery when provided via WithNotify.
// It receives the sender name, subject, and the generated message ID so callers
// can construct actionable instructions (e.g. "sprawl messages read <id>").
// It is best-effort — errors and panics are swallowed.
type NotifyFunc func(from, subject, msgID string)

type sendOptions struct {
	notify NotifyFunc
}

// SendOption configures optional behavior for Send.
type SendOption func(*sendOptions)

// WithNotify registers a notification callback invoked after successful delivery.
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
		if err := os.MkdirAll(filepath.Join(agentDir, sub), 0o755); err != nil { //nolint:gosec // G301: world-readable message dirs are intentional
			return fmt.Errorf("creating directory %s: %w", sub, err)
		}
	}

	// Generate random hex suffix
	suffixBytes := make([]byte, 4)
	if _, err := rand.Read(suffixBytes); err != nil {
		return fmt.Errorf("generating random suffix: %w", err)
	}
	hexSuffix := hex.EncodeToString(suffixBytes)

	now := NowFunc()
	id := fmt.Sprintf("%d.%s.%s", now.UnixNano(), from, hexSuffix)

	shortID, err := generateShortID(agentDir)
	if err != nil {
		return fmt.Errorf("generating short ID: %w", err)
	}

	msg := &Message{
		ID:        id,
		ShortID:   shortID,
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

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil { //nolint:gosec // G306: world-readable message files are intentional
		return fmt.Errorf("writing tmp file: %w", err)
	}

	if err := os.Rename(tmpPath, newPath); err != nil {
		return fmt.Errorf("moving message to new/: %w", err)
	}

	// Best-effort copy to sender's sent/ directory for outbox tracking.
	// The sent copy is advisory — delivery already succeeded above, so we
	// silently ignore errors to avoid returning a misleading failure that
	// could cause callers to retry (and duplicate) a delivered message.
	sentDir := filepath.Join(MessagesDir(dendraRoot), from, "sent")
	if err := os.MkdirAll(sentDir, 0o755); err == nil { //nolint:gosec // G301: world-readable sent dir is intentional
		_ = os.WriteFile(filepath.Join(sentDir, filename), data, 0o644) //nolint:gosec // G306: world-readable message files are intentional
	}

	// Best-effort wake file to notify the recipient agent.
	wakePath := filepath.Join(dendraRoot, ".sprawl", "agents", to+".wake")
	wakeMsg := fmt.Sprintf("New message from %s: %s", from, subject)
	_ = os.WriteFile(wakePath, []byte(wakeMsg), 0o644) //nolint:gosec // G306: world-readable wake file is intentional

	// Best-effort root notification
	var sopts sendOptions
	for _, o := range opts {
		o(&sopts)
	}
	if sopts.notify != nil {
		func() {
			defer func() { recover() }() //nolint:errcheck // intentional panic recovery
			notifyID := shortID
			if notifyID == "" {
				notifyID = id
			}
			sopts.notify(from, subject, notifyID)
		}()
	}

	return nil
}

// Inbox returns all messages for an agent from both new/ and cur/ directories,
// sorted by timestamp ascending.
func Inbox(dendraRoot, agent string) ([]*Message, error) {
	return List(dendraRoot, agent, "all")
}

// Sent returns all messages in the agent's sent/ outbox, sorted by timestamp ascending.
func Sent(dendraRoot, agent string) ([]*Message, error) {
	return List(dendraRoot, agent, "sent")
}

// ResolvePrefix finds a full message ID from a prefix by scanning new/, cur/, archive/, sent/ directories.
// It first attempts to match by ShortID (exact match inside message JSON), then falls back to
// filename-based prefix matching for long IDs. Returns the full ID if exactly one match found.
func ResolvePrefix(dendraRoot, agent, prefix string) (string, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)

	// Pass 1: match by ShortID (read JSON, compare ShortID field)
	shortIDMatches := make(map[string]bool)
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
			data, err := os.ReadFile(filepath.Join(dirPath, entry.Name()))
			if err != nil {
				continue
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.ShortID != "" && msg.ShortID == prefix {
				shortIDMatches[msg.ID] = true
			}
		}
	}

	if len(shortIDMatches) == 1 {
		for id := range shortIDMatches {
			return id, nil
		}
	}
	if len(shortIDMatches) > 1 {
		return "", fmt.Errorf("ambiguous prefix %q: matches %d messages", prefix, len(shortIDMatches))
	}

	// Pass 2: fallback to filename-based prefix matching
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

	if err := os.MkdirAll(filepath.Join(agentDir, "cur"), 0o755); err != nil { //nolint:gosec // G301: world-readable message dirs are intentional
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

	if err := os.MkdirAll(filepath.Join(agentDir, "new"), 0o755); err != nil { //nolint:gosec // G301: world-readable message dirs are intentional
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

	if err := os.MkdirAll(filepath.Join(agentDir, "archive"), 0o755); err != nil { //nolint:gosec // G301: world-readable message dirs are intentional
		return fmt.Errorf("creating archive directory: %w", err)
	}

	// Try new/ first, then cur/
	newPath := filepath.Join(agentDir, "new", filename)
	errNew := os.Rename(newPath, dstPath)
	if errNew == nil {
		return nil
	}

	curPath := filepath.Join(agentDir, "cur", filename)
	errCur := os.Rename(curPath, dstPath)
	if errCur == nil {
		return nil
	}

	if os.IsNotExist(errNew) && os.IsNotExist(errCur) {
		return fmt.Errorf("archiving message: not found in new/ or cur/")
	}
	// Return whichever error is not a simple "not found" — prefer errNew.
	if !os.IsNotExist(errNew) {
		return fmt.Errorf("archiving message from new/: %w", errNew)
	}
	return fmt.Errorf("archiving message from cur/: %w", errCur)
}

// archiveFromDirs moves all .json files from the given directories into archive/.
// It continues on failure and returns the count of successful archives plus any error.
func archiveFromDirs(agentDir string, dirs []string) (int, error) {
	archiveDir := filepath.Join(agentDir, "archive")
	if err := os.MkdirAll(archiveDir, 0o755); err != nil { //nolint:gosec // G301: world-readable message dirs are intentional
		return 0, fmt.Errorf("creating archive directory: %w", err)
	}

	count := 0
	var errs []string
	for _, dir := range dirs {
		dirPath := filepath.Join(agentDir, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return count, fmt.Errorf("reading %s directory: %w", dir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			src := filepath.Join(dirPath, entry.Name())
			dst := filepath.Join(archiveDir, entry.Name())
			if err := os.Rename(src, dst); err != nil {
				errs = append(errs, fmt.Sprintf("%s/%s: %v", dir, entry.Name(), err))
				continue
			}
			count++
		}
	}
	if len(errs) > 0 {
		return count, fmt.Errorf("partial archive failure (%d archived, %d errors): %s",
			count, len(errs), strings.Join(errs, "; "))
	}
	return count, nil
}

// ArchiveAll archives all messages from new/ and cur/ directories, returning the count.
func ArchiveAll(dendraRoot, agent string) (int, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	return archiveFromDirs(agentDir, []string{"new", "cur"})
}

// ArchiveRead archives only read messages from cur/ directory, returning the count.
func ArchiveRead(dendraRoot, agent string) (int, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	return archiveFromDirs(agentDir, []string{"cur"})
}

// ReadMessage reads a message from any directory (new/, cur/, archive/, sent/), returns it.
// If found in new/, auto-marks as read by moving to cur/.
// Messages in sent/ are returned as-is (no auto-mark-read).
func ReadMessage(dendraRoot, agent, msgID string) (*Message, error) {
	agentDir := filepath.Join(MessagesDir(dendraRoot), agent)
	filename := msgID + ".json"

	for _, dir := range []string{"new", "cur", "archive", "sent"} {
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

// readMessagesFromDirs scans the given directories under agentDir, reads all
// .json message files, and returns them sorted by timestamp ascending.
func readMessagesFromDirs(agentDir string, dirs []string) ([]*Message, error) {
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
				continue // file may have been removed between ReadDir and ReadFile
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue // skip corrupt JSON files
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

	return readMessagesFromDirs(filepath.Join(MessagesDir(dendraRoot), agent), dirs)
}

// Broadcast sends a message to all active agents (excluding the sender).
// Returns the number of recipients.
func Broadcast(dendraRoot, sender, subject, body string) (int, error) {
	if sender == "" {
		return 0, fmt.Errorf("sender must not be empty")
	}

	agents, err := state.ListAgents(dendraRoot)
	if err != nil {
		return 0, fmt.Errorf("listing agents: %w", err)
	}

	count := 0
	var errs []string
	for _, agent := range agents {
		if agent.Status != "active" || agent.Name == sender {
			continue
		}
		if err := Send(dendraRoot, sender, agent.Name, subject, body); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", agent.Name, err))
			continue
		}
		count++
	}
	if len(errs) > 0 {
		return count, fmt.Errorf("partial broadcast failure (%d/%d succeeded): %s", count, count+len(errs), strings.Join(errs, "; "))
	}
	return count, nil
}

// collectExistingShortIDs scans new/, cur/, archive/ directories under agentDir
// and returns a set of short IDs already in use.
func collectExistingShortIDs(agentDir string) map[string]bool {
	existing := make(map[string]bool)
	for _, dir := range []string{"new", "cur", "archive"} {
		dirPath := filepath.Join(agentDir, dir)
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dirPath, entry.Name()))
			if err != nil {
				continue
			}
			var msg Message
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}
			if msg.ShortID != "" {
				existing[msg.ShortID] = true
			}
		}
	}
	return existing
}

// generateShortID creates a unique short identifier for a message within the
// given agent directory. It tries 3-character IDs first, falling back to
// 4-character IDs if collisions occur.
func generateShortID(agentDir string) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	const maxAttempts = 10

	existing := collectExistingShortIDs(agentDir)

	// Try 3-char IDs first
	for range maxAttempts {
		candidate := randomString(3, charset)
		if !existing[candidate] {
			return candidate, nil
		}
	}

	// Fallback to 4-char IDs
	for range maxAttempts {
		candidate := randomString(4, charset)
		if !existing[candidate] {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("failed to generate unique short ID after %d attempts", maxAttempts*2)
}

// randomString generates a random string of the given length using characters from charset.
func randomString(length int, charset string) string {
	b := make([]byte, length)
	buf := make([]byte, length)
	if _, err := io.ReadFull(RandReader, buf); err != nil {
		return string(buf[:length])
	}
	for i := range b {
		b[i] = charset[int(buf[i])%len(charset)]
	}
	return string(b)
}
