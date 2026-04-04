// Package main demonstrates interacting with Claude Code's stream-json protocol.
//
// This prototype launches the Claude Code CLI with --output-format stream-json
// (and optionally --input-format stream-json) and parses the NDJSON message
// stream. It supports both single-turn and multi-turn conversations.
//
// Usage:
//
//	go run ./docs/research/stream-json-prototype/ -mode single "Say hello"
//	go run ./docs/research/stream-json-prototype/ -mode multi
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// StreamMessage represents the top-level envelope for all stream-json messages.
// The Type field determines which subfields are populated.
type StreamMessage struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`

	// For type=system (init message)
	SessionID      string   `json:"session_id,omitempty"`
	CWD            string   `json:"cwd,omitempty"`
	Tools          []string `json:"tools,omitempty"`
	Model          string   `json:"model,omitempty"`
	PermissionMode string   `json:"permissionMode,omitempty"`
	ClaudeVersion  string   `json:"claude_code_version,omitempty"`
	APIKeySource   string   `json:"apiKeySource,omitempty"`

	// For type=assistant
	Message *AssistantMessage `json:"message,omitempty"`
	Error   string            `json:"error,omitempty"`

	// For type=result
	IsError    bool    `json:"is_error,omitempty"`
	Result     string  `json:"result,omitempty"`
	StopReason string  `json:"stop_reason,omitempty"`
	DurationMs int     `json:"duration_ms,omitempty"`
	NumTurns   int     `json:"num_turns,omitempty"`
	TotalCost  float64 `json:"total_cost_usd,omitempty"`

	// For type=rate_limit_event
	RateLimitInfo *RateLimitInfo `json:"rate_limit_info,omitempty"`

	// For type=stream_event (with --include-partial-messages)
	Event json.RawMessage `json:"event,omitempty"`

	// For type=user (tool results echoed back)
	// Uses same Message field as assistant

	// Common
	UUID string `json:"uuid,omitempty"`
}

// AssistantMessage represents the Claude API message structure.
type AssistantMessage struct {
	ID         string          `json:"id,omitempty"`
	Model      string          `json:"model,omitempty"`
	Role       string          `json:"role,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"` // Can be string or []ContentBlock
	StopReason *string         `json:"stop_reason"`
	Usage      *Usage          `json:"usage,omitempty"`
}

// ContentBlock represents a single content block in a message.
type ContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// Usage tracks token usage for a message.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// RateLimitInfo contains rate limit status information.
type RateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
}

// InputMessage is the format for sending messages via --input-format stream-json.
type InputMessage struct {
	Type            string       `json:"type"`
	Message         InputContent `json:"message"`
	ParentToolUseID *string      `json:"parent_tool_use_id"`
	SessionID       *string      `json:"session_id"`
}

// InputContent is the inner message content for input messages.
type InputContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func main() {
	mode := "single"
	prompt := "Say exactly 'hello from Go prototype' and nothing else."

	// Simple arg parsing
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-mode":
			if i+1 < len(args) {
				mode = args[i+1]
				i++
			}
		default:
			prompt = args[i]
		}
	}

	switch mode {
	case "single":
		runSingleTurn(prompt)
	case "multi":
		runMultiTurn()
	default:
		log.Fatalf("Unknown mode: %s (use 'single' or 'multi')", mode)
	}
}

// runSingleTurn demonstrates piping a prompt to claude -p with stream-json output.
func runSingleTurn(prompt string) {
	fmt.Println("=== Single-turn stream-json demo ===")
	fmt.Printf("Prompt: %s\n\n", prompt)

	cmd := exec.Command("claude", "-p",
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--model", "sonnet",
		prompt,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Failed to get stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start claude: %v", err)
	}

	readStream(stdout)

	if err := cmd.Wait(); err != nil {
		log.Printf("Claude exited with error: %v", err)
	}
}

// runMultiTurn demonstrates a persistent conversation using bidirectional stream-json.
func runMultiTurn() {
	fmt.Println("=== Multi-turn stream-json demo ===")

	cmd := exec.Command("claude", "-p",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--model", "sonnet",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Fatalf("Failed to get stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Failed to get stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatalf("Failed to start claude: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	prompts := []string{
		"Remember the number 42. Say only 'remembered'.",
		"What number did I ask you to remember? Reply with just the number.",
	}

	for i, prompt := range prompts {
		fmt.Printf("\n--- Turn %d ---\n", i+1)
		fmt.Printf("Sending: %s\n", prompt)

		msg := InputMessage{
			Type: "user",
			Message: InputContent{
				Role:    "user",
				Content: prompt,
			},
		}

		data, err := json.Marshal(msg)
		if err != nil {
			log.Fatalf("Failed to marshal input: %v", err)
		}

		if _, err := fmt.Fprintf(stdin, "%s\n", data); err != nil {
			log.Fatalf("Failed to write to stdin: %v", err)
		}

		// Read until we get a result message
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			var msg StreamMessage
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				log.Printf("Failed to parse JSON: %v (line: %s)", err, truncate(line, 100))
				continue
			}

			printMessage(msg)

			if msg.Type == "result" {
				fmt.Printf("  Result text: %s\n", strings.TrimSpace(msg.Result))
				fmt.Printf("  Cost: $%.6f | Duration: %dms | Turns: %d\n",
					msg.TotalCost, msg.DurationMs, msg.NumTurns)
				break
			}
		}

		// Small delay between turns
		if i < len(prompts)-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Close stdin to let claude exit
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		log.Printf("Claude exited with error: %v", err)
	}

	fmt.Println("\n=== Demo complete ===")
}

// readStream reads and prints all messages from a stream-json output.
func readStream(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB buffer

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var msg StreamMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("Failed to parse JSON: %v (line: %s)", err, truncate(line, 100))
			continue
		}

		printMessage(msg)

		if msg.Type == "result" {
			fmt.Printf("  Result text: %s\n", strings.TrimSpace(msg.Result))
			fmt.Printf("  Cost: $%.6f | Duration: %dms | Turns: %d\n",
				msg.TotalCost, msg.DurationMs, msg.NumTurns)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Scanner error: %v", err)
	}
}

// printMessage prints a human-readable summary of a stream message.
func printMessage(msg StreamMessage) {
	switch msg.Type {
	case "system":
		fmt.Printf("[%s/%s] session=%s model=%s version=%s\n",
			msg.Type, msg.Subtype, msg.SessionID, msg.Model, msg.ClaudeVersion)

	case "assistant":
		text := extractText(msg.Message)
		tools := extractToolUses(msg.Message)
		if text != "" {
			fmt.Printf("[assistant] text: %s\n", truncate(strings.TrimSpace(text), 200))
		}
		for _, t := range tools {
			fmt.Printf("[assistant] tool_use: %s\n", t)
		}
		if msg.Error != "" {
			fmt.Printf("[assistant] ERROR: %s\n", msg.Error)
		}

	case "user":
		fmt.Printf("[user] (tool result echoed back)\n")

	case "result":
		status := "success"
		if msg.IsError {
			status = "ERROR"
		}
		fmt.Printf("[result/%s] stop=%s\n", status, msg.StopReason)

	case "rate_limit_event":
		if msg.RateLimitInfo != nil {
			fmt.Printf("[rate_limit] status=%s type=%s\n",
				msg.RateLimitInfo.Status, msg.RateLimitInfo.RateLimitType)
		}

	case "stream_event":
		fmt.Printf("[stream_event] (partial message)\n")

	default:
		fmt.Printf("[%s] (unknown type)\n", msg.Type)
	}
}

// extractText pulls text content from an assistant message.
func extractText(m *AssistantMessage) string {
	if m == nil {
		return ""
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		// Try as plain string
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			return ""
		}
		return s
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

// extractToolUses pulls tool use names from an assistant message.
func extractToolUses(m *AssistantMessage) []string {
	if m == nil {
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil
	}
	var tools []string
	for _, b := range blocks {
		if b.Type == "tool_use" {
			tools = append(tools, b.Name)
		}
	}
	return tools
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
