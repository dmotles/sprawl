package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/protocol"
)

// BridgeSession is the interface that the bridge uses to interact with the
// host session. This matches the methods on host.Session that the bridge needs.
type BridgeSession interface {
	Initialize(ctx context.Context) error
	SendUserMessage(ctx context.Context, prompt string) (<-chan *protocol.Message, error)
	Close() error
}

// Bridge adapts a host session into Bubble Tea commands and messages.
// It converts protocol events from the session into tea.Msg types
// that the TUI model can handle.
type Bridge struct {
	session   BridgeSession
	ctx       context.Context
	events    <-chan *protocol.Message
	sessionID string
}

// NewBridge creates a new Bridge wrapping the given session.
func NewBridge(ctx context.Context, session BridgeSession) *Bridge {
	return &Bridge{
		session: session,
		ctx:     ctx,
	}
}

// SetSessionID stores the Claude session ID for this bridge so the TUI can
// display it (e.g. in the status bar) after Initialize. Separate from
// construction because the ID is decided during session preparation, which
// happens alongside (not inside) Bridge creation.
func (b *Bridge) SetSessionID(id string) {
	b.sessionID = id
}

// SessionID returns the Claude session ID set via SetSessionID, or "" if unset.
func (b *Bridge) SessionID() string {
	return b.sessionID
}

// Initialize returns a tea.Cmd that initializes the session.
// On success it returns SessionInitializedMsg; on failure, SessionErrorMsg.
func (b *Bridge) Initialize() tea.Cmd {
	return func() tea.Msg {
		if err := b.session.Initialize(b.ctx); err != nil {
			return SessionErrorMsg{Err: fmt.Errorf("initializing session: %w", err)}
		}
		return SessionInitializedMsg{}
	}
}

// SendMessage returns a tea.Cmd that sends a user message to the session.
// On success it stores the events channel and returns UserMessageSentMsg.
// On failure it returns SessionErrorMsg.
func (b *Bridge) SendMessage(text string) tea.Cmd {
	return func() tea.Msg {
		events, err := b.session.SendUserMessage(b.ctx, text)
		if err != nil {
			return SessionErrorMsg{Err: fmt.Errorf("sending message: %w", err)}
		}
		b.events = events
		return UserMessageSentMsg{}
	}
}

// WaitForEvent returns a tea.Cmd that reads the next event from the session's
// event channel and converts it to the appropriate tea.Msg.
// If no events channel is active, returns SessionErrorMsg.
func (b *Bridge) WaitForEvent() tea.Cmd {
	return func() tea.Msg {
		if b.events == nil {
			return SessionErrorMsg{Err: fmt.Errorf("no active event stream")}
		}

		select {
		case msg, ok := <-b.events:
			if !ok {
				return SessionErrorMsg{Err: io.EOF}
			}
			result := mapProtocolMessage(msg)
			if result == nil {
				// Unknown message type — skip and wait for next
				return b.WaitForEvent()()
			}
			return result
		case <-b.ctx.Done():
			return SessionErrorMsg{Err: b.ctx.Err()}
		}
	}
}

// Close shuts down the bridge by closing the underlying session.
func (b *Bridge) Close() error {
	return b.session.Close()
}

// contentBlock represents a single content block in an assistant message.
type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	ID    string          `json:"id,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// assistantContent is used to parse the "message" field of an assistant message.
type assistantContent struct {
	Content []contentBlock `json:"content"`
}

// mapProtocolMessage converts a protocol.Message into the appropriate tea.Msg.
// Returns nil for unrecognized message types.
func mapProtocolMessage(msg *protocol.Message) tea.Msg {
	switch msg.Type {
	case "assistant":
		return mapAssistantMessage(msg)
	case "result":
		return mapResultMessage(msg)
	case "system":
		// System messages (init, session_state_changed, etc.) are informational
		// during the event stream. The session initialization is handled by
		// Bridge.Initialize(). Return nil to skip and wait for the next message.
		return nil
	default:
		return nil
	}
}

func mapAssistantMessage(msg *protocol.Message) tea.Msg {
	var am protocol.AssistantMessage
	if err := json.Unmarshal(msg.Raw, &am); err != nil {
		return nil
	}

	var content assistantContent
	if err := json.Unmarshal(am.Content, &content); err != nil {
		return nil
	}

	// Return the first significant content block.
	for _, block := range content.Content {
		switch block.Type {
		case "text":
			return AssistantTextMsg{Text: block.Text}
		case "tool_use":
			return ToolCallMsg{
				ToolName:  block.Name,
				ToolID:    block.ID,
				Approved:  true, // Session auto-approves tool calls
				Input:     summarizeToolInput(block.Name, block.Input),
				FullInput: expandToolInput(block.Name, block.Input),
			}
		}
	}

	return nil
}

// summarizeToolInput extracts a concise description from tool input JSON.
func summarizeToolInput(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}

	// Extract the most relevant field based on tool name.
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return truncateString(cmd, 120)
		}
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			return path
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return pattern
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			return pattern
		}
	}

	// Fallback: compact JSON, truncated.
	compact, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return truncateString(string(compact), 120)
}

// expandToolInput renders the un-truncated form of a tool's input for the
// TUI's expanded-tool-call view (QUM-335). Bash returns the verbatim
// `command` value (newlines preserved) so the user can read complex
// one-liners. Every other tool — including Read/Edit/Write/Glob/Grep — is
// rendered as pretty-printed JSON (one key per line) so all parameters are
// visible, not just the summary field. Returns "" when input is empty or
// unparseable.
func expandToolInput(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var input map[string]interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return ""
	}

	if toolName == "Bash" {
		if cmd, ok := input["command"].(string); ok {
			return cmd
		}
	}

	pretty, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return ""
	}
	return string(pretty)
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

func mapResultMessage(msg *protocol.Message) tea.Msg {
	var rm protocol.ResultMessage
	if err := json.Unmarshal(msg.Raw, &rm); err != nil {
		return nil
	}

	return SessionResultMsg{
		Result:       rm.Result,
		IsError:      rm.IsError,
		DurationMs:   rm.DurationMs,
		NumTurns:     rm.NumTurns,
		TotalCostUsd: rm.TotalCostUsd,
	}
}
