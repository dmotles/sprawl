package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const autoSummarizePrompt = `You are summarizing a Claude Code session that ended without a proper handoff.
Analyze the following session transcript and produce a concise markdown summary covering:
1. What was being worked on
2. What was accomplished
3. What was in progress or incomplete
4. Any important decisions or findings

Keep the summary under 500 words. Use markdown headers.

<session-transcript>
%s
</session-transcript>`

// EncodeCWDForClaude encodes a CWD path for use in Claude's project directory naming.
// It replaces / with - and . with -.
func EncodeCWDForClaude(cwd string) string {
	result := strings.ReplaceAll(cwd, "/", "-")
	result = strings.ReplaceAll(result, ".", "-")
	return result
}

// SessionLogPath returns the path to a Claude session JSONL log file.
func SessionLogPath(homeDir, cwd, sessionID string) string {
	return filepath.Join(homeDir, ".claude", "projects", EncodeCWDForClaude(cwd), sessionID+".jsonl")
}

// ReadSessionLog reads a Claude session JSONL file and returns a formatted transcript.
// It keeps only the last maxMessages messages and truncates the result to maxBytes.
func ReadSessionLog(path string, maxMessages int, maxBytes int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("opening session log: %w", err)
	}
	defer f.Close()

	type message struct {
		role    string
		content string
	}

	var messages []message

	scanner := bufio.NewScanner(f)
	// Increase buffer for potentially large lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip malformed lines
		}

		msgType, _ := entry["type"].(string)
		if msgType != "user" && msgType != "assistant" {
			continue
		}

		msgObj, ok := entry["message"].(map[string]any)
		if !ok {
			continue
		}

		role, _ := msgObj["role"].(string)
		if role != "user" && role != "assistant" {
			continue
		}

		var content string
		switch c := msgObj["content"].(type) {
		case string:
			content = c
		case []any:
			var parts []string
			for _, block := range c {
				blockMap, ok := block.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := blockMap["type"].(string)
				switch blockType {
				case "text":
					text, _ := blockMap["text"].(string)
					parts = append(parts, text)
				case "tool_use":
					name, _ := blockMap["name"].(string)
					parts = append(parts, fmt.Sprintf("[tool: %s]", name))
				}
			}
			content = strings.Join(parts, "\n")
		default:
			continue
		}

		messages = append(messages, message{role: role, content: content})
	}

	if len(messages) == 0 {
		return "", nil
	}

	// Keep only last maxMessages
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}

	// Build formatted lines per message, then apply byte limit by dropping
	// whole messages from the front to avoid cutting mid-message or mid-rune.
	formatted := make([]string, len(messages))
	totalBytes := 0
	for i, m := range messages {
		formatted[i] = fmt.Sprintf("%s: %s\n", m.role, m.content)
		totalBytes += len(formatted[i])
	}

	// Drop messages from the front until within byte budget.
	start := 0
	for start < len(formatted) && totalBytes > maxBytes {
		totalBytes -= len(formatted[start])
		start++
	}

	if start >= len(formatted) {
		return "", nil
	}

	var b strings.Builder
	for i := start; i < len(formatted); i++ {
		b.WriteString(formatted[i])
		b.WriteString("\n")
	}

	return b.String(), nil
}

// HasSessionSummary checks if a session summary file exists for the given session ID.
func HasSessionSummary(sprawlRoot, sessionID string) (bool, error) {
	dir := sessionsDir(sprawlRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading sessions directory: %w", err)
	}

	// Check for new-format (sessionID.md) or old-format (*_sessionID.md) files.
	target := sessionID + ".md"
	suffix := "_" + sessionID + ".md"
	for _, e := range entries {
		if e.Name() == target || strings.HasSuffix(e.Name(), suffix) {
			return true, nil
		}
	}
	return false, nil
}

// AutoSummarize detects a missed handoff and auto-summarizes the session log.
func AutoSummarize(ctx context.Context, sprawlRoot, cwd, homeDir, sessionID string, invoker ClaudeInvoker) (bool, error) {
	exists, err := HasSessionSummary(sprawlRoot, sessionID)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	logPath := SessionLogPath(homeDir, cwd, sessionID)
	transcript, err := ReadSessionLog(logPath, 50, 100*1024)
	if err != nil {
		return false, err
	}
	if transcript == "" {
		return false, nil
	}

	prompt := fmt.Sprintf(autoSummarizePrompt, transcript)
	response, err := invoker.Invoke(ctx, prompt)
	if err != nil {
		return false, err
	}

	session := Session{
		SessionID:    sessionID,
		Timestamp:    time.Now().UTC(),
		Handoff:      false,
		AgentsActive: []string{},
	}
	if err := WriteSessionSummary(sprawlRoot, session, response); err != nil {
		return false, err
	}

	return true, nil
}
