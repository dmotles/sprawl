package tui

import (
	"encoding/json"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/dmotles/sprawl/internal/protocol"
)

// sidechainVisibleEnv re-enables rendering of sidechain-internal frames.
// DEBUG ONLY — suppression is the default and the shipped behavior (QUM-928).
const sidechainVisibleEnv = "SPRAWL_SHOW_SIDECHAIN"

// sidechainVisible is read ONCE at package init, since the environment is fixed
// for the life of the process. Consequence for tests: t.Setenv does NOT affect
// it — flip this var directly (see withSidechainVisible in the test helpers).
//
// It gates both the live mapping and the replay rehydrate path so the two can
// never drift: whatever renders live must also render on reload.
var sidechainVisible = sidechainVisibleFromEnv(os.Getenv(sidechainVisibleEnv))

// sidechainVisibleFromEnv is the parsing rule, split out so it can be tested
// without depending on the ambient environment. Exactly "1" enables the hatch;
// everything else (unset, "0", "true", "yes") leaves suppression on, because the
// default must be suppressed and a typo must not silently un-suppress.
func sidechainVisibleFromEnv(v string) bool { return v == "1" }

// isSidechainFrame reports whether a frame originated inside a sidechain (a
// Claude in-process Agent-tool spawn: Explore, Plan, oracle, …) and must
// therefore be kept out of the chat stream.
//
// Attribution is SOLELY a non-empty parent_tool_use_id. isSidechain is never set
// on the stream-json wire and session_id is identical to the main thread, so
// this is the only usable discriminator. The parent Agent tool_use itself
// carries no parent_tool_use_id, which is what makes suppression a clean single
// predicate — the Agent row survives while all of its internals vanish.
//
// !! DO NOT hoist this check to the MapProtocolMessage switch head !!
// A future reader will want to tidy it into one top-level early-return. That
// would be a BUG: tool_progress frames reuse parent_tool_use_id with unrelated
// semantics — they are MCP heartbeats, where tool_use_id is "<id>-heartbeat-0"
// and parent_tool_use_id is the tool's OWN id (measured: 1,011 such frames
// across 660 wire logs). Suppressing by frame type, inside the assistant and
// user mappers only, is deliberate.
func isSidechainFrame(parentToolUseID string) bool {
	return parentToolUseID != "" && !sidechainVisible
}

// contentBlock represents a single content block in an assistant or user
// message. tool_use blocks (assistant) carry Name + ID + Input; tool_result
// blocks (user) carry ToolUseID + Content + IsError. (QUM-336)
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
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

// MapProtocolMessage converts a protocol.Message into the appropriate tea.Msg.
// Returns nil for unrecognized message types. Exported so other packages
// (notably internal/tuiruntime's TUIAdapter — QUM-397) can reuse the
// protocol-to-tea.Msg mapping without duplicating the logic.
func MapProtocolMessage(msg *protocol.Message) tea.Msg {
	switch msg.Type {
	case "assistant":
		return mapAssistantMessage(msg)
	case "user":
		return mapUserMessage(msg)
	case "result":
		return mapResultMessage(msg)
	case "system":
		// QUM-928: task_notification is deliberately NOT mapped.
		//
		// It used to drive the live "↻ auto-continued" marker. Two measured facts
		// retired that. (1) QUM-914 routed any notification carrying a tool_use_id
		// to a sidechain-completion msg, on the premise that tool_use_id marks a
		// sidechain — but across 660 wire logs ALL 4,014 notifications carry one and
		// only 8% come from Agent (79% are FOREGROUND Bash), so that premise is
		// false and the branch swallowed every notification. (2) Decisively,
		// QUM-929 deleted sprawl's [auto-continue] injection: sprawl no longer
		// auto-continues anything, so a marker reading "auto-continued" on a
		// foreground Bash completion would assert something that never happened —
		// worse than no marker, since dead code renders nothing while that renders a
		// falsehood. The wire offers no field to scope it to background tasks, so a
		// narrower live variant is not implementable.
		//
		// The REPLAY marker stays: historical logs contain real injections, and
		// replay.go classifies runtime.AutoContinuePrefix into MessageAutoTrigger.
		// That asymmetry is intentional — old sessions have injections, new ones
		// cannot. Task telemetry is unaffected (it observes the frame in
		// internal/runtime, not here).
		// QUM-865: system/compact_boundary marks a context-compaction event
		// (manual /compact or automatic). Map it to a first-party banner msg
		// carrying the token counts + trigger; the giant isCompactSummary user
		// frame that follows is suppressed separately (live: mapUserMessage
		// never renders it as a bubble; replay: scanTranscript skips it).
		//
		// Session-id assumption (QUM-865 deferred guard): sprawl captures the
		// session id ONCE at launch and does not detect a mid-stream session-id
		// change. Our runtime provably does not fork the session id on
		// compaction (the compact_boundary frame carries the same session_id),
		// so exit/re-enter resumes the compacted session correctly. If a future
		// backend forks the session id mid-stream, a follow-up must add a guard
		// (repoint wire log + persist new id + notify the TUI).
		if msg.Subtype == "compact_boundary" {
			var cb protocol.CompactBoundary
			if err := json.Unmarshal(msg.Raw, &cb); err != nil {
				return nil
			}
			return CompactBoundaryMsg{
				Trigger:    cb.CompactMetadata.Trigger,
				PreTokens:  cb.CompactMetadata.PreTokens,
				PostTokens: cb.CompactMetadata.PostTokens,
			}
		}
		// QUM-867: system/status frames report the compaction lifecycle around a
		// /compact command. compact_result:"failed" surfaces a transient error
		// toast; status:"compacting" surfaces a transient in-progress label;
		// compact_result:"success" is ignored (the QUM-865 compact_boundary banner
		// already covers the success path — no duplicate noise). Naturally inert on
		// backends that never emit the frame, mirroring the compact_boundary branch
		// (SupportsCompactCommand gating happens at command-availability time).
		if msg.Subtype == "status" {
			var cs protocol.CompactStatus
			if err := json.Unmarshal(msg.Raw, &cs); err != nil {
				return nil
			}
			if cs.CompactResult == "failed" {
				return CompactFailedMsg{Error: cs.CompactError}
			}
			if cs.Status == "compacting" {
				return CompactingStatusMsg{}
			}
			return nil
		}
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

	parentToolUseID := ""
	if am.ParentToolUseID != nil {
		parentToolUseID = *am.ParentToolUseID
	}

	// QUM-928: drop the whole frame if it came from a sidechain. This also
	// discards its SessionUsageMsg, deliberately: the status-bar gauge measures
	// the MAIN thread's context window, and a sidechain runs in a separate
	// context, so folding its usage in inflated the gauge. Cost/usage telemetry
	// is unaffected — that rides a separate subscriber (internal/usage).
	if isSidechainFrame(parentToolUseID) {
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
		case "thinking":
			// QUM-677 S7: every thinking block produces a ThinkingMsg
			// (including empty bodies — Claude/Opus redacts the body
			// server-side, leaving `thinking==""`). The marker is a count,
			// not the text, so empty arrivals are still meaningful.
			msgs = append(msgs, ThinkingMsg{Text: block.Thinking})
		case "tool_use":
			headerArg, headerParams := FormatToolHeader(block.Name, block.Input)
			msgs = append(msgs, ToolCallMsg{
				ToolName:        block.Name,
				ToolID:          block.ID,
				Approved:        true, // Session auto-approves tool calls
				Input:           summarizeToolInput(block.Name, block.Input),
				FullInput:       expandToolInput(block.Name, block.Input),
				HeaderArg:       headerArg,
				HeaderParams:    headerParams,
				ParentToolUseID: parentToolUseID,
			})
		}
	}
	// QUM-385: emit token usage alongside content blocks so the status bar
	// can track context window consumption.
	if content.Usage != nil {
		msgs = append(msgs, SessionUsageMsg{
			InputTokens:              content.Usage.InputTokens,
			OutputTokens:             content.Usage.OutputTokens,
			CacheReadInputTokens:     content.Usage.CacheReadInputTokens,
			CacheCreationInputTokens: content.Usage.CacheCreationInputTokens,
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
	// ParentToolUseID is non-nil on a sidechain's inner tool_result frames.
	// protocol.UserFrame does not carry it, so it is parsed here (QUM-928).
	ParentToolUseID *string `json:"parent_tool_use_id"`
}

// mapUserMessage extracts the first tool_result content block from a `user`
// message and emits ToolResultMsg. Returns nil for plain-string user echoes
// or block arrays that contain no tool_result. (QUM-336)
func mapUserMessage(msg *protocol.Message) tea.Msg {
	var env userMessageEnvelope
	if err := json.Unmarshal(msg.Raw, &env); err != nil {
		return nil
	}
	// QUM-928: a sidechain's inner tool_result frames carry parent_tool_use_id.
	// Checked ahead of both the plain-string and tool_result arms.
	if env.ParentToolUseID != nil && isSidechainFrame(*env.ParentToolUseID) {
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
