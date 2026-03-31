//go:build integration

package protocol_test

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/dendra/internal/protocol"
)

// readResult holds a message or error from the reader goroutine.
type readResult struct {
	msg *protocol.Message
	err error
}

// claudeProcess wraps a running Claude Code process with protocol readers/writers.
// A single background goroutine reads all stdout messages into the msgs channel,
// ensuring there is never more than one concurrent reader on the protocol.Reader.
type claudeProcess struct {
	cmd    *exec.Cmd
	writer *protocol.Writer
	stdin  io.WriteCloser
	msgs   <-chan readResult
}

// claudeOption configures how Claude Code is started.
type claudeOption func(*claudeConfig)

type claudeConfig struct {
	env       []string
	multiTurn bool
	prompt    string
}

// withEnv adds an environment variable to the Claude process.
func withEnv(key, value string) claudeOption {
	return func(c *claudeConfig) {
		c.env = append(c.env, fmt.Sprintf("%s=%s", key, value))
	}
}

// withMultiTurn enables bidirectional streaming mode.
func withMultiTurn() claudeOption {
	return func(c *claudeConfig) {
		c.multiTurn = true
	}
}

// withPrompt sets the single-turn prompt.
func withPrompt(prompt string) claudeOption {
	return func(c *claudeConfig) {
		c.prompt = prompt
	}
}

// startClaude starts a Claude Code process with stream-json protocol and returns
// a claudeProcess. A single goroutine reads stdout into a channel. Cleanup is
// registered via t.Cleanup.
func startClaude(t *testing.T, opts ...claudeOption) *claudeProcess {
	t.Helper()

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude binary not found in PATH, skipping integration test")
	}

	cfg := &claudeConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	args := []string{
		"--output-format", "stream-json",
		"--verbose",
		"--model", "sonnet",
		"--setting-sources", "user,project",
		"--dangerously-skip-permissions",
	}

	if cfg.multiTurn {
		args = append(args, "--input-format", "stream-json")
		args = append(args, "-p")
	} else if cfg.prompt != "" {
		args = append(args, "-p", cfg.prompt)
	}

	cmd := exec.Command(claudePath, args...)

	if len(cfg.env) > 0 {
		cmd.Env = append(cmd.Environ(), cfg.env...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("failed to create stdin pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start claude: %v", err)
	}

	// Single reader goroutine feeds all messages into a channel.
	// It exits when stdout is closed (process killed or stdin closed).
	reader := protocol.NewReader(stdout)
	ch := make(chan readResult, 64)
	go func() {
		defer close(ch)
		for {
			msg, err := reader.Next()
			ch <- readResult{msg: msg, err: err}
			if err != nil {
				return
			}
		}
	}()

	cp := &claudeProcess{
		cmd:    cmd,
		writer: protocol.NewWriter(stdin),
		stdin:  stdin,
		msgs:   ch,
	}

	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	return cp
}

// readUntilResult reads messages from the process's channel until a "result" type
// message is received or the timeout expires. Returns all messages including the
// result message.
func readUntilResult(t *testing.T, cp *claudeProcess, timeout time.Duration) []*protocol.Message {
	t.Helper()

	var messages []*protocol.Message
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			t.Fatalf("readUntilResult timed out after %v; collected %d messages", timeout, len(messages))
			return nil
		case rr, ok := <-cp.msgs:
			if !ok {
				t.Fatalf("message channel closed before result message; collected %d messages", len(messages))
				return nil
			}
			if rr.err != nil {
				if rr.err == io.EOF {
					t.Fatalf("unexpected EOF before result message; collected %d messages", len(messages))
				}
				t.Fatalf("reader error: %v", rr.err)
				return nil
			}
			t.Logf("received message: type=%q subtype=%q session_id=%q raw=%s",
				rr.msg.Type, rr.msg.Subtype, rr.msg.SessionID, string(rr.msg.Raw))
			messages = append(messages, rr.msg)
			if rr.msg.Type == "result" {
				return messages
			}
		}
	}
}

func TestIntegration_SingleTurn(t *testing.T) {
	cp := startClaude(t, withPrompt("Say exactly 'pong' and nothing else."))

	messages := readUntilResult(t, cp, 30*time.Second)

	if len(messages) == 0 {
		t.Fatal("received no messages")
	}

	// First message should be system/init
	first := messages[0]
	if first.Type != "system" || first.Subtype != "init" {
		t.Errorf("first message type=%q subtype=%q, want type=system subtype=init", first.Type, first.Subtype)
	}

	var init protocol.SystemInit
	if err := protocol.ParseAs(first, &init); err != nil {
		t.Fatalf("ParseAs(SystemInit) error: %v", err)
	}
	if init.SessionID == "" {
		t.Error("SystemInit.SessionID is empty, expected non-empty")
	}
	if !strings.Contains(strings.ToLower(init.Model), "sonnet") {
		t.Errorf("SystemInit.Model = %q, expected to contain 'sonnet'", init.Model)
	}
	if len(init.Tools) == 0 {
		t.Error("SystemInit.Tools is empty, expected non-empty")
	}

	// Verify at least one assistant message exists
	hasAssistant := false
	for _, msg := range messages {
		if msg.Type == "assistant" {
			hasAssistant = true
			break
		}
	}
	if !hasAssistant {
		t.Error("no assistant message found in message stream")
	}

	// Last message should be result with pong
	last := messages[len(messages)-1]
	if last.Type != "result" {
		t.Fatalf("last message type=%q, want result", last.Type)
	}

	var result protocol.ResultMessage
	if err := protocol.ParseAs(last, &result); err != nil {
		t.Fatalf("ParseAs(ResultMessage) error: %v", err)
	}
	if result.IsError {
		t.Errorf("ResultMessage.IsError = true, want false; errors: %v", result.Errors)
	}
	if !strings.Contains(strings.ToLower(result.Result), "pong") {
		t.Errorf("ResultMessage.Result = %q, expected to contain 'pong' (case-insensitive)", result.Result)
	}
}

func TestIntegration_MultiTurn(t *testing.T) {
	cp := startClaude(t, withMultiTurn())

	// Turn 1: establish a secret word
	if err := cp.writer.SendUserMessage("Remember the secret word is 'banana'. Say only 'ok'."); err != nil {
		t.Fatalf("SendUserMessage turn 1: %v", err)
	}

	turn1Messages := readUntilResult(t, cp, 30*time.Second)

	// Capture session ID from init message
	var sessionID string
	for _, msg := range turn1Messages {
		if msg.Type == "system" && msg.Subtype == "init" {
			sessionID = msg.SessionID
			break
		}
	}
	if sessionID == "" {
		// Session ID might be on other messages
		for _, msg := range turn1Messages {
			if msg.SessionID != "" {
				sessionID = msg.SessionID
				break
			}
		}
	}
	if sessionID == "" {
		t.Fatal("no session ID found in turn 1 messages")
	}
	t.Logf("captured session ID: %s", sessionID)

	// Turn 2: ask for the secret word
	if err := cp.writer.SendUserMessage("What was the secret word? Reply with just the word."); err != nil {
		t.Fatalf("SendUserMessage turn 2: %v", err)
	}

	turn2Messages := readUntilResult(t, cp, 30*time.Second)

	// Verify turn 2 result contains banana
	last := turn2Messages[len(turn2Messages)-1]
	if last.Type != "result" {
		t.Fatalf("last message of turn 2 type=%q, want result", last.Type)
	}

	var result protocol.ResultMessage
	if err := protocol.ParseAs(last, &result); err != nil {
		t.Fatalf("ParseAs(ResultMessage) error: %v", err)
	}
	if !strings.Contains(strings.ToLower(result.Result), "banana") {
		t.Errorf("turn 2 ResultMessage.Result = %q, expected to contain 'banana' (case-insensitive)", result.Result)
	}

	// Verify session ID is consistent across turns
	for _, msg := range turn2Messages {
		if msg.SessionID != "" && msg.SessionID != sessionID {
			t.Errorf("turn 2 message session_id=%q, want %q (consistent with turn 1)", msg.SessionID, sessionID)
			break
		}
	}
}

func TestIntegration_SessionStateEvents(t *testing.T) {
	cp := startClaude(t,
		withMultiTurn(),
		withEnv("CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS", "1"),
	)

	if err := cp.writer.SendUserMessage("Say 'hello'"); err != nil {
		t.Fatalf("SendUserMessage: %v", err)
	}

	messages := readUntilResult(t, cp, 30*time.Second)

	// Verify at least one session_state_changed event
	var foundStateEvent bool
	for _, msg := range messages {
		if msg.Type == "system" && msg.Subtype == "session_state_changed" {
			foundStateEvent = true

			var ssc protocol.SessionStateChanged
			if err := protocol.ParseAs(msg, &ssc); err != nil {
				t.Fatalf("ParseAs(SessionStateChanged) error: %v", err)
			}

			validStates := map[string]bool{
				"idle":            true,
				"running":         true,
				"requires_action": true,
			}
			if !validStates[ssc.State] {
				t.Errorf("SessionStateChanged.State = %q, want one of idle, running, requires_action", ssc.State)
			}
			t.Logf("session state event: state=%q", ssc.State)
		}
	}

	if !foundStateEvent {
		t.Error("no session_state_changed event found in message stream")
	}
}
