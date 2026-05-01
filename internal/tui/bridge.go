package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/protocol"
)

// BridgeSession is the interface that the bridge uses to interact with the
// host session. This matches the methods on host.Session that the bridge needs.
type BridgeSession interface {
	Initialize(ctx context.Context) error
	SendUserMessage(ctx context.Context, prompt string) (<-chan *protocol.Message, error)
	Interrupt(ctx context.Context) error
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

// Interrupt returns a tea.Cmd that sends an interrupt request to the session.
// The result is delivered as InterruptResultMsg.
func (b *Bridge) Interrupt() tea.Cmd {
	return func() tea.Msg {
		err := b.session.Interrupt(b.ctx)
		return InterruptResultMsg{Err: err}
	}
}

// Close shuts down the bridge by closing the underlying session.
func (b *Bridge) Close() error {
	return b.session.Close()
}

// contentBlock represents a single content block in an assistant or user
// message. tool_use blocks (assistant) carry Name + ID + Input; tool_result
// blocks (user) carry ToolUseID + Content + IsError. (QUM-336)
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Name      string          `json:"name,omitempty"`
	ID        string          `json:"id,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// assistantContent is used to parse the "message" field of an assistant message.
// The Anthropic API message object contains both `content` (array of blocks)
// and `usage` (token counts); we parse both.
type assistantContent struct {
	Content []contentBlock  `json:"content"`
	Usage   *protocol.Usage `json:"usage,omitempty"`
}

// MapProtocolMessage is the exported wrapper around mapProtocolMessage so
// other packages (notably internal/runtime's TUIAdapter — QUM-397) can reuse
// the protocol-to-tea.Msg mapping without duplicating the logic.
func MapProtocolMessage(msg *protocol.Message) tea.Msg {
	return mapProtocolMessage(msg)
}

// mapProtocolMessage converts a protocol.Message into the appropriate tea.Msg.
// Returns nil for unrecognized message types.
func mapProtocolMessage(msg *protocol.Message) tea.Msg {
	switch msg.Type {
	case "assistant":
		return mapAssistantMessage(msg)
	case "user":
		return mapUserMessage(msg)
	case "result":
		return mapResultMessage(msg)
	case "system":
		// QUM-385: system/init carries the model name, from which we derive the
		// context window limit. Other system subtypes are still skipped.
		if msg.Subtype == "init" {
			var si protocol.SystemInit
			if err := json.Unmarshal(msg.Raw, &si); err == nil && si.Model != "" {
				return SessionModelMsg{Model: si.Model}
			}
		}
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

	// QUM-386: collect ALL content blocks instead of returning the first.
	var msgs []tea.Msg
	for _, block := range content.Content {
		switch block.Type {
		case "text":
			msgs = append(msgs, AssistantTextMsg{Text: block.Text})
		case "tool_use":
			msgs = append(msgs, ToolCallMsg{
				ToolName:  block.Name,
				ToolID:    block.ID,
				Approved:  true, // Session auto-approves tool calls
				Input:     summarizeToolInput(block.Name, block.Input),
				FullInput: expandToolInput(block.Name, block.Input),
			})
		}
	}
	// QUM-385: emit token usage alongside content blocks so the status bar
	// can track context window consumption.
	if content.Usage != nil {
		msgs = append(msgs, SessionUsageMsg{
			InputTokens:  content.Usage.InputTokens,
			OutputTokens: content.Usage.OutputTokens,
		})
	}

	if len(msgs) == 0 {
		return nil
	}
	return AssistantContentMsg{Msgs: msgs}
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

// userMessageEnvelope mirrors the wire shape of a `user` protocol message.
// `Content` is json.RawMessage because Claude Code sends either a plain
// string (echo of a typed user prompt — already rendered locally; we ignore
// it) or an array of content blocks (used for tool_result delivery).
type userMessageEnvelope struct {
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// mapUserMessage extracts the first tool_result content block from a `user`
// message and emits ToolResultMsg. Returns nil for plain-string user echoes
// or block arrays that contain no tool_result. (QUM-336)
func mapUserMessage(msg *protocol.Message) tea.Msg {
	var env userMessageEnvelope
	if err := json.Unmarshal(msg.Raw, &env); err != nil {
		return nil
	}
	if len(env.Message.Content) == 0 {
		return nil
	}
	// Plain-string content (user prompt echo) — start of the JSON value will
	// be `"`. Skip; the InputModel already rendered the typed prompt via
	// SubmitMsg.
	if env.Message.Content[0] == '"' {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(env.Message.Content, &blocks); err != nil {
		return nil
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		return ToolResultMsg{
			ToolID:  b.ToolUseID,
			Content: flattenToolResultContent(b.Content),
			IsError: b.IsError,
		}
	}
	return nil
}

// flattenToolResultContent decodes the polymorphic `content` field of a
// tool_result block. The Anthropic protocol allows it to be either a plain
// string or an array of `{type:"text", text:"..."}` blocks; the latter form
// is joined with newlines so a single Result string can carry multi-block
// output.
func flattenToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
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
