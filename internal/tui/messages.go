package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// toolCallInputPrefix is the cell width of the `"│ "` gutter rendered before
// each wrapped line of a tool-call input block. Subtracted from the content
// width when deciding the wrap column.
const toolCallInputPrefix = 2

// StreamingCursor is the character shown at the end of an in-progress
// assistant message.
const StreamingCursor = "▍"

// NewContentIndicator is shown by ChatRegion when auto-scroll is off and new
// content exists below.
const NewContentIndicator = "↓ New content below ↓"

// MessageType identifies the kind of conversation entry.
type MessageType int

const (
	MessageUser MessageType = iota
	MessageAssistant
	MessageToolCall
	MessageStatus
	MessageError
	// MessageSystem is system-injected content (e.g. inbox-drain body
	// surfaced into the conversation buffer by InboxDrainMsg). Pre-S6 the
	// viewport renderer drew this with a mail glyph; post-S6 it is a log-
	// only entry (S5 contract violator routed to the status bar). (QUM-338)
	MessageSystem
	// MessageSystemNotification is a supervisor-injected
	// `<system-notification>` user-role message (QUM-557).
	MessageSystemNotification
	// MessageBanner is a session banner (ASCII art + tagline). Log-only
	// post-S6.
	MessageBanner
	// MessageAutoTrigger is the synthetic header drawn before an
	// autonomous (harness-initiated) turn's assistant response so the user
	// sees WHY weave responded. Content is the task_notification summary
	// (QUM-634).
	MessageAutoTrigger
)

// MessageEntry is a single item in the conversation buffer log. Pre-S6 this
// was the central data structure rendered by the viewport's renderMessages
// walk; post-S6 it is the back-compat snapshot type returned by
// ViewportModel.GetMessages and consumed by ChatList.Reset for replay/
// preload/resync. Render logic lives entirely in ChatList + Item types.
type MessageEntry struct {
	Type      MessageType
	Content   string
	Complete  bool
	Approved  bool   // MessageToolCall only
	ToolInput string // concise tool input summary (MessageToolCall only)
	// ToolInputFull is the un-truncated multi-line representation of the
	// raw tool input — surfaced when the global expand-tool-inputs flag is
	// on (QUM-335).
	ToolInputFull string
	// ToolID is the tool_use_id from Claude's protocol — used by
	// ChatList.MarkToolResult and the legacy log's MarkToolResult to find
	// the matching entry when a tool_result event arrives. (QUM-336)
	ToolID string
	// Pending is true while a tool call is in flight (no tool_result yet).
	Pending bool
	// Failed is true when the corresponding tool_result arrived with
	// is_error=true. (QUM-336)
	Failed bool
	// Result is the raw tool result text. (QUM-336)
	Result string
	// Depth is the nesting level of a sidechain tool call. (QUM-379)
	Depth int
	// ParentToolID is the ToolID of the enclosing Agent tool call. (QUM-386)
	ParentToolID string
	// Interrupt drives the renderer's color/glyph choice within the
	// MessageSystemNotification class. (QUM-557 / QUM-562)
	Interrupt bool
	// NotificationType is the parsed `type` attribute on
	// MessageSystemNotification entries (QUM-562).
	NotificationType string
	// HeaderArg is the per-tool main argument inlined on the compact header
	// line (QUM-419). MessageToolCall only.
	HeaderArg string
	// HeaderParams is the ordered list of secondary k=v pairs displayed
	// after HeaderArg (QUM-419). MessageToolCall only.
	HeaderParams []KVPair
}

// notificationGlyphAndStyle selects the (glyph, style) pair for a
// MessageSystemNotification entry, branching first on NotificationType
// (QUM-562 status_change vs message-class) and then on Interrupt within
// the message class.
func notificationGlyphAndStyle(theme *Theme, msg MessageEntry) (glyph string, style lipgloss.Style) {
	switch msg.NotificationType {
	case NotificationKindStatusChange, NotificationKindLivenessCheck:
		// QUM-730: liveness_check shares the status_change visual treatment
		// (KISS — distinct from the mail glyph but no bespoke styling).
		return "◉", theme.StatusChangeText
	default: // NotificationKindMessage and any unknown/legacy value
		if msg.Interrupt {
			return "⚡", theme.InterruptText
		}
		return "✉", theme.NotificationText
	}
}

// formatSystemMessage prepares a system-message body for rendering by:
//  1. normalizing CRLF / lone CR into LF,
//  2. collapsing runs of >=2 consecutive blank (whitespace-only) lines down
//     to exactly one blank line (QUM-401),
//  3. dropping leading and trailing blank lines, and
//  4. soft-wrapping each non-blank line at word boundaries using
//     ansi.Wordwrap so long messages don't escape the viewport.
//
// Word-wrap is skipped when the wrap budget would be <1.
func formatSystemMessage(content string, width int) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	rawLines := strings.Split(content, "\n")
	out := make([]string, 0, len(rawLines))
	prevBlank := true // start as "blank" so leading blanks are dropped
	wrapBudget := width - 4
	for _, ln := range rawLines {
		if strings.TrimSpace(ln) == "" {
			if prevBlank {
				continue
			}
			out = append(out, "")
			prevBlank = true
			continue
		}
		if wrapBudget > 0 {
			wrapped := ansi.Wordwrap(ln, wrapBudget, "")
			out = append(out, strings.Split(wrapped, "\n")...)
		} else {
			out = append(out, ln)
		}
		prevBlank = false
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// previewResultLines splits result on newlines, drops empty/whitespace-only
// entries, returns up to maxLines truncated to width cells, and the count
// of remaining (non-empty) source lines that did not fit. width <= 0
// disables truncation. maxLines < 0 means "no cap" (QUM-343 expanded form).
func previewResultLines(result string, maxLines, width int) ([]string, int) {
	result = strings.ReplaceAll(result, "\r\n", "\n")
	result = strings.ReplaceAll(result, "\r", "\n")
	var nonEmpty []string
	for _, ln := range strings.Split(result, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, ln)
	}
	if len(nonEmpty) == 0 {
		return nil, 0
	}
	take := maxLines
	if maxLines < 0 || len(nonEmpty) < take {
		take = len(nonEmpty)
	}
	out := make([]string, 0, take)
	for i := 0; i < take; i++ {
		ln := nonEmpty[i]
		if width > 0 {
			ln = ansi.Truncate(ln, width, "…")
		}
		out = append(out, ln)
	}
	return out, len(nonEmpty) - take
}

// wrapToolInput prepares a tool-input string for rendering inside the tool
// block. Carriage returns are dropped; each logical line is wrapped to at
// most width cells. When width <= 0 the input is returned as-is.
func wrapToolInput(input string, width int) []string {
	input = strings.ReplaceAll(input, "\r", "")
	if width <= 0 {
		return strings.Split(input, "\n")
	}
	var out []string
	for _, ln := range strings.Split(input, "\n") {
		wrapped := ansi.Wrap(ln, width, "")
		if wrapped == "" {
			out = append(out, "")
			continue
		}
		out = append(out, strings.Split(wrapped, "\n")...)
	}
	return out
}

// systemNotification* constants are the literal wrapping tokens used by the
// supervisor's notification-injection path (QUM-555 / QUM-562). The TUI
// strips these wrappers at both live-append and replay entry points so the
// rendered viewport never shows the raw markup.
//
// QUM-562: the open tag is now a prefix-only match (`<system-notification`,
// no trailing `>`) because emitters append a `type="..."` attribute and
// optionally `interrupt="true"`. The parser scans attributes between the
// prefix and the first `>`.
const (
	systemNotificationOpenPrefix      = "<system-notification"
	systemNotificationCloseTag        = "</system-notification>"
	systemNotificationInterruptMarker = "[interrupt]"
)

// NotificationKind is the value of the `type` attribute on a
// `<system-notification>` wrapper. QUM-562: drives glyph + color selection
// in the viewport render switch. Untyped legacy tags default to
// NotificationKindMessage so pre-QUM-562 transcripts replay identically.
const (
	NotificationKindMessage      = "message"
	NotificationKindStatusChange = "status_change"
	// NotificationKindLivenessCheck is the QUM-730 supervisor heartbeat
	// liveness-check class. Same glyph/style as status_change for KISS —
	// distinct from "message" so the operator can see the heartbeat fired.
	NotificationKindLivenessCheck = "liveness_check"
)

// stripSystemNotificationTag peels ONE `<system-notification [attrs]>...
// </system-notification>` envelope from the START of the input. Surrounding
// whitespace is trimmed before matching; the inner body is returned verbatim
// (newlines preserved). The portion of the input AFTER the first closing tag
// is returned in `remaining` so callers can loop and peel additional
// envelopes — back-to-back notifications must render as distinct viewport
// entries, not a single block with raw tags leaking (QUM-574).
//
// Returns:
//   - body:        the inner content of the first envelope with wrapping tags
//     removed
//   - notifType:   the parsed `type` attribute (defaults to
//     NotificationKindMessage when absent or unrecognized —
//     YAGNI per QUM-562 design decision #5)
//   - isInterrupt: true iff `interrupt="true"` is set OR (back-compat) the
//     body starts with the literal `[interrupt]` marker
//   - remaining:   the input string after the first `</system-notification>`
//     close tag. Empty when the envelope is the only content. Untrimmed —
//     callers should re-feed it to peel additional envelopes; when ok=false
//     on a subsequent call, any non-whitespace residue can be surfaced as
//     plain system text.
//   - ok:          false when no leading tag is present or the open tag has
//     no matching close; in that case body is the original string,
//     remaining is empty, and notifType is empty.
//
// Attribute parsing is permissive: double or single quotes accepted,
// whitespace between attributes tolerated. The canonical emitters always
// produce double-quoted attributes.
func stripSystemNotificationTag(s string) (body, notifType string, isInterrupt bool, remaining string, ok bool) {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, systemNotificationOpenPrefix) {
		return s, "", false, "", false
	}
	rest := trimmed[len(systemNotificationOpenPrefix):]
	// Next char must be `>` (no attrs) or whitespace (attrs follow).
	if len(rest) == 0 {
		return s, "", false, "", false
	}
	if rest[0] != '>' && rest[0] != ' ' && rest[0] != '\t' {
		// e.g. `<system-notificationXXX>` — not our tag.
		return s, "", false, "", false
	}
	closeIdx := strings.IndexByte(rest, '>')
	if closeIdx < 0 {
		return s, "", false, "", false
	}
	attrSegment := rest[:closeIdx]
	afterOpen := rest[closeIdx+1:]
	// QUM-574: anchor on the FIRST `</system-notification>` so back-to-back
	// envelopes peel one-at-a-time. The previous `HasSuffix` anchor was
	// greedy and swallowed inner close+open tag pairs as part of a single
	// envelope's body, leaking raw markup to the viewport.
	endIdx := strings.Index(afterOpen, systemNotificationCloseTag)
	if endIdx < 0 {
		return s, "", false, "", false
	}
	innerBody := afterOpen[:endIdx]
	remaining = afterOpen[endIdx+len(systemNotificationCloseTag):]

	attrs := parseTagAttributes(attrSegment)
	notifType = attrs["type"]
	if notifType != NotificationKindMessage && notifType != NotificationKindStatusChange && notifType != NotificationKindLivenessCheck {
		// Unknown or missing type → fall back to message per the QUM-562
		// back-compat contract.
		notifType = NotificationKindMessage
	}
	isInterrupt = attrs["interrupt"] == "true" || strings.HasPrefix(innerBody, systemNotificationInterruptMarker)
	return innerBody, notifType, isInterrupt, remaining, true
}

// parseTagAttributes is a permissive `key="value"` / `key='value'` scanner
// for the attribute segment inside a `<system-notification ...>` open tag.
// Unrecognized syntax is silently dropped — callers fall back to default
// behavior rather than rejecting the whole tag. Attribute names are
// case-sensitive (the emitters always use lowercase).
func parseTagAttributes(seg string) map[string]string {
	out := make(map[string]string)
	i := 0
	for i < len(seg) {
		// Skip leading whitespace.
		for i < len(seg) && (seg[i] == ' ' || seg[i] == '\t') {
			i++
		}
		if i >= len(seg) {
			break
		}
		// Read key up to `=`.
		keyStart := i
		for i < len(seg) && seg[i] != '=' && seg[i] != ' ' && seg[i] != '\t' {
			i++
		}
		key := seg[keyStart:i]
		if key == "" || i >= len(seg) || seg[i] != '=' {
			// Malformed attribute (no `=` or empty key) — skip past
			// whitespace to look for the next one.
			for i < len(seg) && seg[i] != ' ' && seg[i] != '\t' {
				i++
			}
			continue
		}
		i++ // past `=`
		if i >= len(seg) {
			break
		}
		quote := seg[i]
		if quote != '"' && quote != '\'' {
			// Unquoted value — skip silently.
			for i < len(seg) && seg[i] != ' ' && seg[i] != '\t' {
				i++
			}
			continue
		}
		i++ // past opening quote
		valStart := i
		for i < len(seg) && seg[i] != quote {
			i++
		}
		if i >= len(seg) {
			break
		}
		out[key] = seg[valStart:i]
		i++ // past closing quote
	}
	return out
}

// --- QUM-527 slice 2c: question-queue messages ---

// QuestionsAvailableMsg signals that the question queue has been updated. The
// forwarder goroutine in cmd/enter.go fills Depth via PeekQuestions; the
// in-process QuestionConsumer.OnEnqueue path leaves Depth=0 and only carries
// Head — the AppModel handler is tolerant of either source.
type QuestionsAvailableMsg struct {
	Depth int
	Head  *supervisor.PendingQuestion
}

// QuestionAnsweredMsg is emitted by the QuestionModel when the user finalizes
// answers. The AppModel forwards Response to Supervisor.ResolveQuestion.
type QuestionAnsweredMsg struct {
	RequestID string
	Response  supervisor.QuestionResponse
}

// ShowQuestionMsg requests that the question modal be re-shown if a request is
// installed and no higher-priority modal is up.
type ShowQuestionMsg struct{}

// DismissQuestionMsg requests that the question modal be hidden.
//
// Hard=false is the QUM-538 soft-hide: visibility goes off but the request
// stays in the supervisor's queue with drafts intact; Ctrl-Q (or
// ShowQuestionMsg) re-opens with state preserved. Used by inside-modal Ctrl-Q.
//
// Hard=true is the QUM-611 cancel-and-unwedge path: AppModel calls
// Supervisor.CancelQuestion so the blocked MCP tool returns immediately and
// the caller's turn finalizes, then resets the modal (drafts discarded). Used
// by inside-modal plain Esc — the user-facing "I'm done with this question"
// exit. The status-bar hint advertises this affordance while the modal is
// hidden but the question is still pending.
type DismissQuestionMsg struct {
	Hard bool
}

// CancelQuestionMsg signals that the named request was cancelled upstream
// (e.g. by the supervisor's CancelByAgent path). The AppModel resets the
// active modal only when the RequestID matches.
type CancelQuestionMsg struct {
	RequestID string
	Reason    string
}

// AssistantContentMsg batches all content blocks from a single assistant
// message so parallel Agent tool_use blocks are all delivered to the App.
// (QUM-386)
type AssistantContentMsg struct {
	Msgs []tea.Msg
}

// tea.Msg types for protocol events from the host session.
// These are consumed by the TUI's Update method to drive UI state.

// TurnState represents the current state of a conversation turn.
type TurnState int

const (
	TurnIdle TurnState = iota
	TurnThinking
	TurnStreaming
	TurnComplete
)

// String returns the human-readable name of the turn state.
func (s TurnState) String() string {
	switch s {
	case TurnIdle:
		return "idle"
	case TurnThinking:
		return "thinking"
	case TurnStreaming:
		return "streaming"
	case TurnComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// AssistantTextMsg contains a streaming text chunk from Claude.
type AssistantTextMsg struct {
	Text string
}

// ThinkingMsg carries a model-emitted thinking content block. Routed by
// AppModel to ChatList.AppendThinking, which renders a ThinkingItem in the
// viewport (collapsed by default with a ✻ glyph; expandable via Ctrl+O
// fan-out). Always arrives whole — thinking blocks do not stream chunk by
// chunk in the wire protocol the way assistant text does. (QUM-677 S7)
type ThinkingMsg struct {
	Text string
}

// ToolCallMsg represents a tool call observed in the assistant's response.
type ToolCallMsg struct {
	ToolName string
	ToolID   string
	Approved bool
	Input    string // concise summary of tool input
	// FullInput is the un-truncated, multi-line representation of the raw
	// tool input. Bash returns the verbatim command; everything else is
	// pretty-printed JSON. Surfaced when the user toggles the global
	// tool-input expand state in AppModel (QUM-335).
	FullInput string
	// HeaderArg is the per-tool "main argument" displayed inline on the
	// compact header line (QUM-419) — e.g. the Bash command, the file_path
	// for Read/Edit/Write, the pattern for Grep/Glob. Pre-computed by the
	// protocol mapping layer so the renderer stays JSON-free.
	HeaderArg string
	// HeaderParams is the ordered list of secondary k=v pairs rendered after
	// HeaderArg on the compact header line (QUM-419). Dropped by the renderer
	// when including them would shrink the main arg below MinMainArgCells.
	HeaderParams []KVPair
	// ParentToolUseID is the wire-level parent_tool_use_id from the assistant
	// envelope (protocol.AssistantMessage.ParentToolUseID). Non-empty when the
	// emitting assistant turn ran inside a sidechain. The viewport
	// uses it verbatim to attribute the tool call to the correct outer Agent
	// container, taking precedence over the lastActiveAgent heuristic for
	// parallel-Agent scenarios. Empty for top-level assistant turns. (QUM-386 live-path fix —
	// sibling to replay path's wire-field plumbing in scanTranscriptWithSidechain.)
	ParentToolUseID string
}

// ToolResultMsg carries the result of a previously-emitted tool call. The
// bridge produces one whenever Claude emits a `user` protocol message whose
// content array contains a `tool_result` block. ToolID matches the originating
// ToolCallMsg.ToolID so the AppModel can flip the lifecycle state on the right
// MessageEntry. Content is the raw result text (string or joined text-block
// array). IsError mirrors the `is_error` field of the tool_result block.
// (QUM-336)
type ToolResultMsg struct {
	ToolID  string
	Content string
	IsError bool
}

// TurnStateMsg signals a change in the conversation turn lifecycle.
type TurnStateMsg struct {
	State TurnState
}

// SessionErrorMsg carries an error from the host session or process death.
//
// QUM-479: `Err: io.EOF` is reserved exclusively for the session backend
// (`internal/tuiruntime.TUIAdapter`) — that producer signals end-of-session
// and triggers AppModel's auto-restart path. The ChildStreamAdapter EventBus
// adapter MUST NOT emit SessionErrorMsg{io.EOF} on subscription close; use the
// dedicated ChildStreamClosedMsg sentinel instead, or the AppModel will
// mis-interpret a harmless adapter teardown as the session ending and fire a
// phantom "Session restarting..." cycle.
type SessionErrorMsg struct {
	Err error
}

// ChildStreamClosedMsg signals that a ChildStreamAdapter's EventBus
// subscription has closed (Cancel or runtime stop). Carries the agent name
// (filled in by childStreamWaitCmd) and the adapter epoch at the moment of
// the read so AppModel can ignore stale-generation deliveries. The handler
// silently tears down the adapter — it does NOT trigger a bridge restart.
// (QUM-479)
type ChildStreamClosedMsg struct {
	Agent string
	Epoch uint64
}

// Error implements the error interface for convenience.
func (m SessionErrorMsg) Error() string {
	return m.Err.Error()
}

// SessionResultMsg signals that a turn is complete, with cost/token info.
type SessionResultMsg struct {
	Result       string
	IsError      bool
	DurationMs   int
	NumTurns     int
	TotalCostUsd float64
}

// SessionUsageMsg carries token usage data extracted from an assistant message.
// Each assistant message reports the latest snapshot — values are NOT
// cumulative across turns. True context window consumption is the sum of
// InputTokens + CacheReadInputTokens + CacheCreationInputTokens (QUM-385);
// the Anthropic API splits the prefix across these three fields when prompt
// caching is enabled.
type SessionUsageMsg struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
}

// SessionModelMsg carries the model name from the system/init message,
// used to derive the context window limit.
type SessionModelMsg struct {
	Model string
}

// AutoContinueMsg signals a harness auto-continue (autonomous) turn was
// triggered by a completed background task (QUM-634). Summary is the
// human-readable task_notification summary, rendered as a distinct marker.
type AutoContinueMsg struct {
	Summary string
}

// taskNotification* are the literal wrapping tokens the harness records in the
// JSONL transcript for an autonomous-turn trigger (QUM-634 resume path). The
// trigger is a `type:user` record whose string content is a
// `<task-notification>…</task-notification>` wrapper carrying a `<summary>`.
const (
	taskNotificationOpenTag      = "<task-notification>"
	taskNotificationSummaryOpen  = "<summary>"
	taskNotificationSummaryClose = "</summary>"
)

// parseTaskNotificationSummary extracts the <summary> text from a
// <task-notification>…</task-notification> user-record body (QUM-634 resume
// path). Returns ok=false when the wrapper or summary tag is absent.
func parseTaskNotificationSummary(s string) (summary string, ok bool) {
	if !strings.Contains(s, taskNotificationOpenTag) {
		return "", false
	}
	start := strings.Index(s, taskNotificationSummaryOpen)
	if start < 0 {
		return "", false
	}
	start += len(taskNotificationSummaryOpen)
	end := strings.Index(s[start:], taskNotificationSummaryClose)
	if end < 0 {
		return "", false
	}
	return s[start : start+end], true
}

// UserMessageSentMsg confirms that user input was dispatched to the session.
type UserMessageSentMsg struct{}

// SessionInitializedMsg signals that the Claude session is ready.
type SessionInitializedMsg struct{}

// SubmitMsg is sent by InputModel when the user presses Enter with non-empty text.
type SubmitMsg struct {
	Text string
}

// pasteLookaheadMsg fires pasteLookaheadWindow after a plain Enter to
// resolve the pending submit if no follow-up KeyPressMsg has reclassified
// it as an embedded paste newline. seq matches the InputModel's
// pendingEnterSeq at scheduling time; mismatched seqs are stale Ticks
// from reclassified Enters and must be ignored.
type pasteLookaheadMsg struct{ seq uint64 }

// AgentTreeMsg carries refreshed agent tree data from the supervisor.
// RootUnread is the unread count in the root agent's (weave's) maildir,
// polled alongside child-agent unread counts so the tree can render an
// unread badge on the weave root row (QUM-205 / QUM-311).
type AgentTreeMsg struct {
	Nodes      []TreeNode
	RootUnread int
}

// BackendFaultMsg signals that a child runtime's backend session has fired
// a sticky terminal error (QUM-602). The App responds by appending a
// viewport banner and tagging the agent's tree row with a [FAULT:class]
// indicator. Dispatched by cmd/enter.go's BackendFaultEmitter wrapper
// around the supervisor's per-runtime fault subscriber.
type BackendFaultMsg struct {
	Agent      string
	Class      string
	Reason     string
	NextAction string
}

// BackendFaultClearedMsg signals that a previously-faulted backend session
// has been successfully recovered in-place (QUM-601). The App responds by
// removing the per-agent fault sticker, appending a "backend recovered on X"
// banner to the root viewport, and rebuilding the tree so the FAULT badge
// disappears from the row. Viewport history is intentionally retained — the
// operator forensic trail through the fault and recovery sequence stays
// visible.
type BackendFaultClearedMsg struct {
	Agent string
}

// AgentsResumedMsg signals that the runEnter startup scan finished its
// best-effort restart of suspended child agents (QUM-372). The App renders a
// short viewport banner summarizing the counts. Resumed counts the number of
// agents whose StartResume call succeeded; Failed counts the per-agent
// failures isolated by Real.RecoverAgents (StartResume returned an error AND
// the on-disk status was flipped to resume_failed by the callback). The
// banner is suppressed when both counts are zero.
type AgentsResumedMsg struct {
	Resumed int
	Failed  int
}

// InboxArrivalMsg signals that a message has been delivered to the root
// agent's (weave's) maildir. Dispatched by the TUI-aware notifier installed
// in `cmd/enter.go` before the bubbletea program starts (QUM-311). The App
// responds by appending a short status banner and scheduling an immediate
// agent-tree refresh so the weave row's unread badge updates within ~1s
// instead of waiting for the next 2s tick.
type InboxArrivalMsg struct {
	From    string
	Subject string
}

// AgentSelectedMsg is emitted when the user presses Enter on a tree node.
type AgentSelectedMsg struct {
	Name string
}

// ConfirmResultMsg carries the user's response from the confirmation dialog.
type ConfirmResultMsg struct {
	Confirmed bool
}

// SignalMsg indicates an OS signal (SIGTERM, SIGHUP) was received.
type SignalMsg struct{}

// RestartSessionMsg signals that the user wants to restart the Claude subprocess.
type RestartSessionMsg struct{}

// HandoffRequestedMsg signals that the weave subprocess invoked the
// handoff MCP tool. The App responds by tearing down the current
// bridge and triggering the same restart path EOF takes. Supervisors fire a
// channel event that `cmd/enter.go` converts into this msg via tea.Program.Send.
type HandoffRequestedMsg struct{}

// OpenPaletteMsg requests that the command palette overlay be shown. The
// app gates this on input not being disabled and no other modal being active.
type OpenPaletteMsg struct{}

// InjectPromptMsg carries a command's prompt template to be sent to Claude
// via the bridge, without rendering it as a user message in the viewport.
type InjectPromptMsg struct {
	Template string
}

// InboxDrainMsg carries a pre-rendered flush-queue prompt that the TUI should
// inject into the current bridge as a user turn. Dispatched by the AppModel's
// AgentTreeMsg handler (or cmd/enter.go's notifier) when weave's harness
// queue has pending entries AND the turn is idle. The AppModel treats this
// like InjectPromptMsg but with an inbox-origin banner; after the send
// succeeds, the entries are moved from pending/ to delivered/ so they are
// not re-injected. QUM-323.
type InboxDrainMsg struct {
	Prompt   string
	EntryIDs []string
	// Class is "async" or "interrupt" — drives the banner wording. Empty
	// defaults to "async".
	Class string
}

// ToggleHelpMsg flips the help overlay visibility (same effect as F1).
type ToggleHelpMsg struct{}

// ToggleTreeMsg flips the agent-tree modal visibility. Emitted by the /tree
// palette command (QUM-733 5b). No-op when a higher-priority modal is up.
type ToggleTreeMsg struct{}

// PaletteQuitMsg requests an immediate app quit triggered by the palette's
// /exit command. The app sets `quitting=true` then returns tea.Quit — same
// post-confirm semantics as the Ctrl-C path.
type PaletteQuitMsg struct{}

// SessionRestartingMsg signals that the TUI is transitioning between Claude
// subprocess sessions (e.g. after transport EOF or /handoff). The App renders
// a status banner carrying Reason while the restart work runs.
type SessionRestartingMsg struct {
	Reason string
}

// ChildTranscriptMsg carries a freshly-loaded snapshot of a child agent's
// Claude session transcript. Emitted when the user observes a non-root agent
// (initial hydrate on AgentSelectedMsg, plus periodic re-reads while observed).
// The App applies it only if Agent matches the currently-observed agent.
//
// Empty Entries with no error → "Waiting for <agent>..." placeholder
// (covers: agent has no session_id yet, or the session log has not been
// created on disk yet). QUM-332.
type ChildTranscriptMsg struct {
	Agent     string
	SessionID string
	Entries   []MessageEntry
	Err       error
}

// InterruptResultMsg signals the outcome of a bridge.Interrupt() call triggered
// by the user pressing ESC during a streaming/thinking turn (QUM-380).
// Request-ack only — see InterruptCompletedMsg for terminal. Err is non-nil if
// the interrupt request failed at the transport level.
type InterruptResultMsg struct {
	Err error
}

// InterruptCompletedMsg signals a turn was terminated by an interrupt and the
// runtime has finished draining it. Sibling of SessionResultMsg — its handler
// must drive full turn cleanup (TurnIdle + finalize + re-arm + queue drain).
// Distinct from InterruptResultMsg, which is the request-ack fired when
// bridge.Interrupt() returns (Esc handler feedback only).
// Currently emitted only by the unified-runtime TUIAdapter.
type InterruptCompletedMsg struct {
	Result       string
	DurationMs   int
	NumTurns     int
	TotalCostUsd float64
}

// ConsolidationPhaseMsg signals a phase change in the background consolidation
// pipeline running after a handoff. Dispatched by the event-forwarder goroutine
// in cmd/enter.go's onStart hook. The App appends a viewport status banner and
// updates the status bar label so the user can see what the consolidation is
// doing instead of a generic "restart Ns" counter. (QUM-391)
type ConsolidationPhaseMsg struct {
	Phase string
}

// ConsolidationCompleteMsg signals that the background consolidation pipeline
// finished (successfully or with an error). The App appends a completion or
// failure banner in the viewport. (QUM-391)
type ConsolidationCompleteMsg struct {
	Err      error
	Duration time.Duration
}

// ChildStreamMsg envelopes a live tea.Msg produced by the per-child
// TUIAdapter observing a non-root agent's UnifiedRuntime EventBus (QUM-439).
// Agent identifies which child the inner msg belongs to; Epoch matches the
// AppModel's child-adapter epoch at the time the cmd was issued so stale
// deliveries (after a viewport switch / cancellation) are dropped.
type ChildStreamMsg struct {
	Agent string
	Epoch uint64
	Inner tea.Msg
}

// MCPCallStartedMsg is dispatched by the in-process MCP server when a tool
// call begins. It mirrors the calllog.Begin() event so the TUI can surface a
// live "operation in flight" indicator in the status bar (QUM-497).
type MCPCallStartedMsg struct {
	CallID  string
	Tool    string
	Caller  string
	Started time.Time
	Step    string // optional initial step label
}

// MCPCallProgressMsg is dispatched whenever the in-process MCP server's
// per-call checkpoint fires (calllog.Checkpoint). Carries the latest step
// name and an optional Tail line (e.g. last validate output line). The TUI
// uses this to update the status-bar segment without spamming the viewport.
// (QUM-497)
type MCPCallProgressMsg struct {
	CallID string
	Step   string
	Tail   string
}

// ValidateEventMsg is dispatched by cmd/enter.go's wrapper around the
// supervisor's validateEmitter. It carries every merge.* checkpoint (queued,
// starting, validate-started, validate-line, validate-ended) with the full
// kv payload preserved as a string map so the ValidatePopupModel can read
// `cmd`, `log_path`, `line`, `behind`, `exit`, `error`, etc. by name (QUM-588).
type ValidateEventMsg struct {
	CallID string
	Step   string
	KV     map[string]string
}

// MCPCallEndedMsg is dispatched when a tool call returns (success, error,
// or panic). The TUI removes the op from the status bar and stops tracking
// elapsed time. (QUM-497)
type MCPCallEndedMsg struct {
	CallID   string
	Status   string // "ok" | "error" | "panic"
	Duration time.Duration
}

// mcpOpTickMsg is the 1Hz self-perpetuating tick that re-renders elapsed
// time on active MCP ops. Self-stops when no ops remain. (QUM-497)
type mcpOpTickMsg struct{}

// mcpOpThresholdMsg fires once per Started msg, 60s after start. If the op
// is still active, AppModel raises a viewport banner with SIGUSR1 guidance.
// (QUM-497)
type mcpOpThresholdMsg struct {
	CallID string
}

// TurnWatchdogTickMsg is the periodic check that recovers from a wedged
// turnState — i.e. the TUI is stuck in TurnStreaming/TurnThinking after a
// dropped terminal EventTurnCompleted. The reducer queries the optional
// LivenessProbe capability on the bridge and, if the runtime is idle yet
// the TUI thinks a turn is in flight, forces finalizeTurn(). Self-
// perpetuating: each tick schedules the next. (QUM-775 item 2.)
type TurnWatchdogTickMsg struct{}

// EventDropDetectedMsg signals that the TUIAdapter observed a gap in the
// EventBus sequence number stream (QUM-669). From/To bracket the gap (From
// is the last good seq the adapter saw, To is the seq of the first event
// after the gap). Missing reports the number of skipped seq values
// (To - From - 1). The AppModel reduces this msg into the gap-detection
// state machine described in docs/designs/qum-669-viewport-wedge-recovery.md
// §2.3 — it does not by itself trigger resync.
type EventDropDetectedMsg struct {
	From    uint64
	To      uint64
	Missing uint64
}

// ViewportResyncMsg carries the outcome of an async session-log resync read
// (QUM-669). Entries is the rebuilt MessageEntry slice produced by
// LoadTranscript; MissingCount is the gap size that triggered the resync
// (carried through so the resync banner can say "recovered N events"). Err
// is non-nil if the resync read failed (missing file, parse error, empty
// session ID) — the AppModel's reducer treats Err != nil as the failure
// path and emits a "resync failed" status entry.
type ViewportResyncMsg struct {
	Entries      []MessageEntry
	MissingCount uint64
	Err          error
}

// gapConfirmMsg is the QUM-669 internal debounce-confirmation tick. It fires
// after gapDebounceWindow when the reducer enters the gap-pending state; if
// the carried gapID still matches the AppModel's current pending gap ID,
// the reducer transitions to the "dropped" state and kicks off the resync.
// Stale deliveries (a recovery completed in the meantime, or a newer gap
// supersedes this one) are ignored — same self-cancellation pattern as
// mcpOpThresholdCmd.
type gapConfirmMsg struct {
	gapID uint64
}

// ToastSpawnMsg requests that a toast notification be rendered (QUM-649).
type ToastSpawnMsg struct{ Toast Toast }

// ToastDismissMsg requests removal of a specific toast (by ID) or all toasts
// (All=true). (QUM-649)
type ToastDismissMsg struct {
	ID  string
	All bool
}

// ToastConditionClearedMsg signals that a condition-keyed toast should be
// dismissed. Every toast registered via ConditionDismiss(ID) is removed.
// (QUM-649)
type ToastConditionClearedMsg struct{ ID string }

// toastTimerMsg is the internal tick fired by ToastModel.Spawn when a toast
// is registered with TimerDismiss(d). Idempotent: Dismiss is a no-op on
// unknown IDs. (QUM-649)
type toastTimerMsg struct{ ID string }

// RestartCompleteMsg delivers the outcome of the async restart work
// (QUM-260). Bridge carries the freshly-launched Claude subprocess on
// success; Err is non-nil if restartFunc failed. The App installs the new
// bridge, clears the restarting flag, and either shows an error dialog or
// renders the New-Session banner.
type RestartCompleteMsg struct {
	Bridge SessionBackend
	Err    error
}

// ShowUsageMsg requests that the /usage modal be opened (QUM-721).
type ShowUsageMsg struct{}

// DismissUsageMsg requests that the /usage modal be closed (QUM-721).
type DismissUsageMsg struct{}

// IncidentSnapshotRequestedMsg requests that the configured snapshot helper
// run to produce an incident bundle under .sprawl/incidents/. Emitted by the
// Ctrl+\ key handler (QUM-728). The AppModel reducer surfaces a "capturing"
// transient label and dispatches the configured snapshotCmd; the cmd runs in
// a background goroutine (Bubble Tea Cmd) and returns IncidentSnapshotCompleteMsg.
type IncidentSnapshotRequestedMsg struct{}

// IncidentSnapshotCompleteMsg carries the outcome of a snapshot run. On
// success Path is the absolute incident dir; on failure Err is non-nil and
// the AppModel spawns an error toast plus a "snapshot failed" transient
// label. (QUM-728)
type IncidentSnapshotCompleteMsg struct {
	Path string
	Err  error
}
