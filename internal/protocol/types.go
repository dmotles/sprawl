// Package protocol implements the stream-json NDJSON protocol for communicating
// with Claude Code via stdin/stdout. It provides Go types for all message types,
// an NDJSON reader for parsing stdout messages, and an NDJSON writer for sending
// stdin messages.
package protocol

import "encoding/json"

// --- Output messages (from Claude Code stdout) ---

// Message is the top-level envelope for all stream-json output messages.
// Discriminate on Type field, then Subtype where applicable.
// Use ParseAs to deserialize Raw into a specific message type.
type Message struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	UUID      string          `json:"uuid,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Raw       json.RawMessage `json:"-"` // Full JSON line, populated by Reader
}

// SystemInit is the first message emitted after launch (type=system, subtype=init).
type SystemInit struct {
	Type           string   `json:"type"`
	Subtype        string   `json:"subtype"`
	SessionID      string   `json:"session_id,omitempty"`
	UUID           string   `json:"uuid,omitempty"`
	CWD            string   `json:"cwd"`
	Tools          []string `json:"tools"`
	Model          string   `json:"model"`
	PermissionMode string   `json:"permissionMode"`
	ClaudeVersion  string   `json:"claude_code_version"`
	APIKeySource   string   `json:"apiKeySource"`
}

// TaskNotification is a harness task lifecycle frame (type=system,
// subtype=task_notification) emitted when a background task completes. The
// Summary is a human-readable one-liner used to render an auto-continue
// trigger marker in the TUI (QUM-634).
type TaskNotification struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	TaskID  string `json:"task_id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// AssistantMessage contains a complete assistant turn (type=assistant).
// The Content field holds the Anthropic API message object as raw JSON.
type AssistantMessage struct {
	Type            string          `json:"type"`
	UUID            string          `json:"uuid,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	Content         json.RawMessage `json:"message"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
}

// ResultMessage is emitted when a turn completes (type=result).
type ResultMessage struct {
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype,omitempty"`
	UUID         string   `json:"uuid,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	Result       string   `json:"result,omitempty"`
	IsError      bool     `json:"is_error"`
	DurationMs   int      `json:"duration_ms"`
	NumTurns     int      `json:"num_turns"`
	TotalCostUsd float64  `json:"total_cost_usd"`
	StopReason   string   `json:"stop_reason,omitempty"`
	Errors       []string `json:"errors,omitempty"`
}

// SessionStateChanged signals idle/running/requires_action
// (type=system, subtype=session_state_changed).
type SessionStateChanged struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	UUID      string `json:"uuid,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	State     string `json:"state"`
}

// ControlRequest is a permission or hook callback request from Claude Code
// (type=control_request). The Request field holds the request payload as raw JSON.
type ControlRequest struct {
	Type      string          `json:"type"`
	UUID      string          `json:"uuid,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}

// RateLimitEvent contains rate limit status (type=rate_limit_event).
type RateLimitEvent struct {
	Type          string         `json:"type"`
	UUID          string         `json:"uuid,omitempty"`
	SessionID     string         `json:"session_id,omitempty"`
	RateLimitInfo *RateLimitInfo `json:"rate_limit_info"`
}

// RateLimitInfo holds the details of a rate limit event.
type RateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`
}

// StreamEvent contains token-level deltas (type=stream_event).
// Only present when Claude Code is launched with --include-partial-messages.
type StreamEvent struct {
	Type            string          `json:"type"`
	UUID            string          `json:"uuid,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	Event           json.RawMessage `json:"event"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
}

// ParseUsage extracts the inline usage and model from an AssistantMessage's
// Content blob. Returns (nil, "", nil) if Content is empty (no usage data to
// extract); a non-nil error indicates malformed JSON.
func (m *AssistantMessage) ParseUsage() (*Usage, string, error) {
	if len(m.Content) == 0 {
		return nil, "", nil
	}
	var inner struct {
		Model string `json:"model"`
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(m.Content, &inner); err != nil {
		return nil, "", err
	}
	return inner.Usage, inner.Model, nil
}

// Usage contains token consumption metrics from an Anthropic API response.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// --- Input messages (to Claude Code stdin) ---

// UserMessage is sent on stdin to submit a new prompt.
//
// Priority, UUID, and SessionID are optional (omitempty) so the existing
// SendUserMessage wire shape is byte-identical when they are unset.
// Priority is the command-queue priority (now|next|later, CLI default
// "next"); UUID is a stable per-message id the CLI echoes back on the
// isReplay frame when launched with --replay-user-messages.
type UserMessage struct {
	Type            string       `json:"type"`
	Message         MessageParam `json:"message"`
	ParentToolUseID *string      `json:"parent_tool_use_id"`
	Priority        string       `json:"priority,omitempty"`
	UUID            string       `json:"uuid,omitempty"`
	SessionID       string       `json:"session_id,omitempty"`
}

// UserFrame is an inbound (stdout) user frame. When the CLI is launched with
// --replay-user-messages it re-emits each consumed stdin user message with
// the original uuid and isReplay:true. The replay flag is camelCase on the
// wire (isReplay), distinct from the snake_case session_id.
type UserFrame struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	IsReplay  bool   `json:"isReplay,omitempty"`
}

// SystemNotification is a CLI-native toast/status frame (type=system,
// subtype=notification). Its priority enum (low|medium|high|immediate) is
// distinct from the command-queue priority on UserMessage.
type SystemNotification struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Key       string `json:"key"`
	Text      string `json:"text"`
	Priority  string `json:"priority"`
	Color     string `json:"color,omitempty"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

// MessageParam is the inner message content for user input messages,
// following the Anthropic API MessageParam format.
type MessageParam struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// InterruptRequest is sent on stdin to cancel the current turn.
// Wire format: {"type":"control_request","request_id":"<id>","request":{"subtype":"interrupt"}}
type InterruptRequest struct {
	Type      string                `json:"type"`
	RequestID string                `json:"request_id"`
	Request   InterruptRequestInner `json:"request"`
}

// InterruptRequestInner holds the request payload for an InterruptRequest.
type InterruptRequestInner struct {
	Subtype string `json:"subtype"` // always "interrupt"
}

// CancelAsyncMessageRequest is sent on stdin to drop a pending async user
// message from the CLI command queue by uuid. Mirrors InterruptRequest.
// The inner key is message_uuid (NOT uuid) per CLI 2.1.173 — the user
// message and its isReplay echo key on uuid, but the cancel keys on
// message_uuid.
// Wire format:
// {"type":"control_request","request_id":"<id>","request":{"subtype":"cancel_async_message","message_uuid":"<uuid>"}}
type CancelAsyncMessageRequest struct {
	Type      string                         `json:"type"`
	RequestID string                         `json:"request_id"`
	Request   CancelAsyncMessageRequestInner `json:"request"`
}

// CancelAsyncMessageRequestInner holds the request payload for a
// CancelAsyncMessageRequest.
type CancelAsyncMessageRequestInner struct {
	Subtype     string `json:"subtype"` // always "cancel_async_message"
	MessageUUID string `json:"message_uuid"`
}

// CancelAsyncMessageAck is the control_response payload for a
// cancel_async_message request. Cancelled==false means the message was
// already dequeued for execution (treat as gone, never "still queued").
type CancelAsyncMessageAck struct {
	Cancelled bool `json:"cancelled"`
}

// ControlResponse is sent on stdin to respond to a ControlRequest.
type ControlResponse struct {
	Type     string               `json:"type"`
	Response ControlResponseInner `json:"response"`
}

// ControlResponseInner holds the response payload for a ControlResponse.
type ControlResponseInner struct {
	Subtype   string `json:"subtype"`
	RequestID string `json:"request_id"`
	Error     string `json:"error,omitempty"`
	Response  any    `json:"response,omitempty"`
}
