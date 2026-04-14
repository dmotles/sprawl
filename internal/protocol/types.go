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

// --- Input messages (to Claude Code stdin) ---

// UserMessage is sent on stdin to submit a new prompt.
type UserMessage struct {
	Type            string       `json:"type"`
	Message         MessageParam `json:"message"`
	ParentToolUseID *string      `json:"parent_tool_use_id"`
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
