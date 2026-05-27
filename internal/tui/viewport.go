package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// toolCallInputPrefix is the cell width of the `"│ "` gutter rendered before
// each wrapped line of a tool-call input block. Subtracted from the viewport
// inner width when deciding the wrap column.
const toolCallInputPrefix = 2

const placeholderContent = `Welcome to Sprawl TUI

This is the output viewport. Agent output will appear here.

Use PgUp/PgDn to scroll through content.
Use Tab/Shift+Tab to switch between panels.
Press Ctrl+C to quit.

---

Waiting for agent activity...`

// StreamingCursor is the character shown at the end of an in-progress assistant message.
const StreamingCursor = "▍"

// NewContentIndicator is shown when auto-scroll is off and new content exists below.
const NewContentIndicator = "↓ New content below ↓"

// MessageType identifies the kind of conversation entry.
type MessageType int

const (
	MessageUser MessageType = iota
	MessageAssistant
	MessageToolCall
	MessageStatus
	MessageError
	// MessageSystem is system-injected content (e.g. the inbox-drain body
	// surfaced into the conversation buffer by InboxDrainMsg). Rendered with
	// a mail glyph and the theme's SystemText style so it's visually
	// unmistakable that the system spoke, not the user. (QUM-338)
	MessageSystem
	// MessageSystemNotification is a supervisor-injected `<system-notification>`
	// user-role message (QUM-557). Rendered with a left-bar accent + glyph
	// (✉ async / ⚡ interrupt) using Theme.NotificationText / InterruptText.
	// Distinct from MessageSystem so live drain and JSONL replay produce the
	// same visual treatment on restart — see QUM-557 motivation for the
	// live/replay color-flip bug this resolves.
	MessageSystemNotification
	// MessageBanner is a session banner (ASCII art + tagline) that lives in
	// the messages slice so it survives renderAndUpdate() cycles. Rendered
	// verbatim without markdown processing.
	MessageBanner
	// MessageAutoTrigger is a synthetic header rendered before an autonomous
	// (harness-initiated) turn's assistant response so the user sees WHY weave
	// responded. Content is the task_notification summary. (QUM-634)
	MessageAutoTrigger
)

// MessageEntry is a single item in the conversation buffer.
type MessageEntry struct {
	Type      MessageType
	Content   string
	Complete  bool
	Approved  bool   // only used for MessageToolCall
	ToolInput string // concise tool input summary (MessageToolCall only)
	// ToolInputFull is the un-truncated, multi-line representation of the
	// raw tool input — surfaced by renderToolCall when the viewport's
	// expand-tool-inputs flag is on (QUM-335). May be empty for legacy
	// messages or when the bridge couldn't parse the input.
	ToolInputFull string
	// ToolID is the tool_use_id from Claude's protocol — used by
	// MarkToolResult to find the matching entry when a tool_result event
	// arrives. MessageToolCall only. (QUM-336)
	ToolID string
	// Pending is true while a tool call is in flight (no tool_result yet).
	// MessageToolCall only. AppendToolCall sets this; MarkToolResult clears
	// it. The renderer uses Pending to decide whether to show a spinner
	// frame or the success/failure glyph. (QUM-336)
	Pending bool
	// Failed is true when the corresponding tool_result arrived with
	// is_error=true. Drives the ✗ failure indicator. MessageToolCall only.
	// (QUM-336)
	Failed bool
	// Result is the raw tool result text (concatenated when the protocol
	// content was an array of text blocks). The renderer derives a 3-line
	// preview from this; the full text is retained for future expand
	// integration. MessageToolCall only. (QUM-336)
	Result string
	// Depth is the nesting level of a sub-agent tool call. Top-level calls
	// have Depth 0. A tool call made inside an "Agent" tool call has Depth 1,
	// nested two levels deep has Depth 2, etc. Used by the renderer to indent
	// nested entries as compact single lines under their parent Agent call.
	// (QUM-379)
	Depth int
	// ParentToolID is the ToolID of the enclosing Agent tool call, if any.
	// Empty when Depth == 0. Used to attribute nested tool calls to the
	// correct parallel Agent container. (QUM-386)
	ParentToolID string
	// Interrupt is set on MessageSystemNotification entries whose wrapper
	// carries `interrupt="true"` OR whose body starts with the literal
	// `[interrupt]` marker (back-compat). Drives the renderer's
	// color/glyph choice within the message-class branch (⚡ + InterruptText
	// vs ✉ + NotificationText) so live and replay both render identically.
	// (QUM-557 / QUM-562)
	Interrupt bool
	// NotificationType is the parsed `type` attribute on
	// MessageSystemNotification entries (QUM-562). One of
	// NotificationKindMessage or NotificationKindStatusChange. Drives the
	// renderer's top-level branch selection (message-class vs status_change
	// glyph + color). Defaults to NotificationKindMessage for untyped legacy
	// wrappers so pre-QUM-562 transcripts replay identically.
	NotificationType string
	// HeaderArg is the per-tool main argument inlined on the compact header
	// line (QUM-419). Empty falls back to the legacy "tool name only" header.
	// MessageToolCall only.
	HeaderArg string
	// HeaderParams is the ordered list of secondary k=v pairs displayed
	// after HeaderArg (QUM-419). Dropped at render time when including them
	// would shrink the main arg below MinMainArgCells. MessageToolCall only.
	HeaderParams []KVPair
}

// ViewportModel wraps a bubbles viewport with theme styling.
type ViewportModel struct {
	vp            viewport.Model
	theme         *Theme
	renderer      *MarkdownRenderer
	messages      []MessageEntry
	autoScroll    bool
	hasNewContent bool
	selection     SelectionState
	// width tracks the viewport's inner cell width so row-rendering helpers
	// (e.g. renderToolCall) can clip/wrap against the visible column count.
	// Mirrors the value passed to SetSize; 0 means not-yet-sized.
	width int
	// toolInputsExpanded mirrors AppModel.toolInputsExpanded (QUM-335). When
	// true, renderToolCall renders MessageEntry.ToolInputFull (multi-line)
	// instead of the truncated ToolInput summary. AppModel propagates the
	// flag to every per-agent viewport via SetToolInputsExpanded.
	toolInputsExpanded bool
	// spinnerFrame is the latest single-glyph frame string injected by
	// AppModel on every spinner.TickMsg (QUM-336). renderToolCall uses it
	// as the indicator for any Pending tool call. Empty until first push.
	spinnerFrame string
	// activeAgents tracks the tool IDs of in-flight "Agent" tool calls.
	// Non-Agent tool calls inside any active agent get depth 1 and are
	// attributed to lastActiveAgent. Agent tool calls are always top-level
	// (depth 0). MarkToolResult removes completed agents. SetMessages
	// clears the map. (QUM-386)
	activeAgents    map[string]bool
	lastActiveAgent string
}

// SelectionGutter is the visual prefix placed on selected message blocks when
// the viewport is in select mode.
const SelectionGutter = "▌ "

// NewViewportModel creates a viewport with placeholder content.
//
// SoftWrap is enabled so the viewport never horizontally scrolls: content is
// already width-constrained by the glamour markdown renderer, and the default
// bubbles/v2 Left/Right bindings (h/l, ←/→) would otherwise bump `xOffset` and
// render each line as `ansi.Cut(line, xOffset, xOffset+width)` — i.e. the
// tail half of every line, with leading characters eaten. That surfaced as
// the "unreadable viewport" seen on 2026-04-22 after a stray key press.
func NewViewportModel(theme *Theme) ViewportModel {
	vp := viewport.New()
	vp.SoftWrap = true
	vp.SetContent(placeholderContent)
	return ViewportModel{
		vp:         vp,
		theme:      theme,
		renderer:   NewMarkdownRenderer(80),
		autoScroll: true,
	}
}

// Update delegates to the inner viewport for scroll handling.
func (m ViewportModel) Update(msg tea.Msg) (ViewportModel, tea.Cmd) {
	wasAtBottom := m.vp.AtBottom()
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)

	switch msg.(type) {
	case tea.KeyPressMsg, tea.MouseWheelMsg:
		if m.vp.AtBottom() {
			m.autoScroll = true
			m.hasNewContent = false
		} else if wasAtBottom {
			m.autoScroll = false
		}
	}

	return m, cmd
}

// View renders the viewport content.
func (m ViewportModel) View() string {
	view := m.vp.View()
	// QUM-602: when the inner bubble viewport has zero dimensions
	// (size not yet applied — common in unit tests that don't dispatch
	// WindowSizeMsg), bubbles renders an empty string. Fall back to the
	// already-rendered messages buffer so observers can still inspect
	// appended status/banners independent of layout.
	if view == "" && len(m.messages) > 0 {
		mm := m
		view = mm.renderMessages()
	}
	if m.hasNewContent && !m.autoScroll {
		indicator := m.theme.AccentText.Render("  " + NewContentIndicator + "  ")
		lines := strings.Split(view, "\n")
		if len(lines) > 0 {
			lines[len(lines)-1] = indicator
		}
		view = strings.Join(lines, "\n")
	}
	return view
}

// Len returns the number of message entries currently in the buffer.
func (m *ViewportModel) Len() int { return len(m.messages) }

// Width returns the inner cell width last applied via SetSize. Zero means
// not-yet-sized.
func (m *ViewportModel) Width() int { return m.width }

// Height returns the viewport's row count last applied via SetSize. Used by
// tests to assert layout reflows correctly when the input bar is hidden
// while observing a non-root agent (QUM-340).
func (m *ViewportModel) Height() int { return m.vp.Height() }

// SetSize updates the viewport dimensions.
func (m *ViewportModel) SetSize(w, h int) {
	m.width = w
	m.vp.SetWidth(w)
	m.vp.SetHeight(h)
	if m.renderer != nil {
		m.renderer.SetWidth(w)
	}
	if len(m.messages) > 0 {
		m.renderAndUpdate()
	}
}

// AppendBanner adds a session banner to the conversation buffer. The banner
// is stored as a MessageEntry so it survives renderAndUpdate() cycles —
// unlike the old SetContent approach which was silently overwritten by the
// first streaming message.
func (m *ViewportModel) AppendBanner(text string) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageBanner,
		Content:  text,
		Complete: true,
	})
	m.renderAndUpdate()
}

// AppendAutoTrigger adds an auto-continue trigger marker (QUM-634) — a
// synthetic header rendered before an autonomous turn's assistant response so
// the user sees WHY weave responded. Content is the task_notification summary.
func (m *ViewportModel) AppendAutoTrigger(summary string) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageAutoTrigger,
		Content:  summary,
		Complete: true,
	})
	m.renderAndUpdate()
}

// AppendUserMessage adds a user message to the conversation buffer.
func (m *ViewportModel) AppendUserMessage(text string) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageUser,
		Content:  text,
		Complete: true,
	})
	m.renderAndUpdate()
}

// AppendAssistantChunk appends text to the current assistant message (streaming).
func (m *ViewportModel) AppendAssistantChunk(text string) {
	if n := len(m.messages); n > 0 && m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete {
		m.messages[n-1].Content += text
	} else {
		m.messages = append(m.messages, MessageEntry{
			Type:    MessageAssistant,
			Content: text,
		})
	}
	m.renderAndUpdate()
}

// FinalizeAssistantMessage marks the current assistant message as complete.
func (m *ViewportModel) FinalizeAssistantMessage() {
	if n := len(m.messages); n > 0 && m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete {
		m.messages[n-1].Complete = true
		m.renderAndUpdate()
	}
}

// AppendToolCall adds a tool call notification line. toolID is the
// tool_use_id from Claude's protocol; MarkToolResult uses it to find this
// entry when the result arrives. fullInput is the un-truncated, multi-line
// representation surfaced by the global expand-tool-inputs toggle (QUM-335);
// pass "" if not available. The new entry starts in the Pending state
// (QUM-336) — its indicator animates until MarkToolResult flips it.
func (m *ViewportModel) AppendToolCall(name, toolID string, approved bool, input, fullInput string) {
	m.AppendToolCallWithHeader(name, toolID, approved, input, fullInput, "", nil, "")
}

// AppendToolCallWithHeader is the QUM-419 entry point that carries the
// pre-computed per-tool header fields (HeaderArg + HeaderParams) alongside
// the legacy summary + full-input strings. AppendToolCall is preserved as a
// thin wrapper so existing call sites (tests, replay paths that haven't been
// migrated) keep compiling; new production paths should set the header
// fields so the compact header line reads correctly.
func (m *ViewportModel) AppendToolCallWithHeader(name, toolID string, approved bool, input, fullInput, headerArg string, headerParams []KVPair, parentToolUseID string) {
	depth := 0
	parentID := ""
	// Non-Agent tool calls inside any active agent get depth 1.
	// Agent tool calls are always top-level (depth 0). (QUM-386)
	switch {
	case parentToolUseID != "":
		// QUM-386 live-path: wire field is authoritative for sidechain attribution.
		// Sister of replay path's parent_tool_use_id read in scanTranscriptWithSidechain.
		parentID = parentToolUseID
		depth = 1
	case len(m.activeAgents) > 0 && name != "Agent":
		// Heuristic fallback retained for callers/tests that don't carry the wire field.
		depth = 1
		parentID = m.lastActiveAgent
	}

	m.messages = append(m.messages, MessageEntry{
		Type:          MessageToolCall,
		Content:       name,
		Complete:      true,
		Approved:      approved,
		ToolInput:     input,
		ToolInputFull: fullInput,
		ToolID:        toolID,
		Pending:       true,
		Depth:         depth,
		ParentToolID:  parentID,
		HeaderArg:     headerArg,
		HeaderParams:  headerParams,
	})

	if name == "Agent" && toolID != "" {
		if m.activeAgents == nil {
			m.activeAgents = make(map[string]bool)
		}
		m.activeAgents[toolID] = true
		m.lastActiveAgent = toolID
	}
	m.renderAndUpdate()
}

// MarkToolResult finds the most recent pending MessageToolCall entry whose
// ToolID matches and updates its Pending/Failed/Result fields. Returns true
// if a matching entry was found and updated; false if the toolID does not
// match any entry (orphan tool_result). Triggers a re-render on success
// so the indicator and preview update immediately. (QUM-336)
func (m *ViewportModel) MarkToolResult(toolID, content string, isError bool) bool {
	if toolID == "" {
		return false
	}
	// Walk newest → oldest so a duplicate ID (shouldn't happen with Claude's
	// UUIDs, but be defensive) targets the most recent in-flight call.
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Type != MessageToolCall {
			continue
		}
		if m.messages[i].ToolID != toolID {
			continue
		}
		m.messages[i].Pending = false
		m.messages[i].Failed = isError
		m.messages[i].Result = content
		// QUM-386: remove completed Agent from active set.
		if m.activeAgents[toolID] {
			delete(m.activeAgents, toolID)
			if m.lastActiveAgent == toolID {
				m.lastActiveAgent = ""
				for id := range m.activeAgents {
					m.lastActiveAgent = id
					break
				}
			}
		}
		m.renderAndUpdate()
		return true
	}
	return false
}

// SetSpinnerFrame stores the spinner frame string injected by AppModel on
// every spinner.TickMsg. Pending tool calls read this frame as their
// indicator glyph. AppModel re-renders the viewport after pushing each
// frame so the animation visibly advances. (QUM-336)
func (m *ViewportModel) SetSpinnerFrame(frame string) {
	if m.spinnerFrame == frame {
		return
	}
	m.spinnerFrame = frame
	if m.hasPendingToolCall() {
		m.renderAndUpdate()
	}
}

// HasPendingToolCall reports whether at least one tool call entry is still
// in flight (Pending=true). Used by AppModel to decide whether to push the
// spinner frame to a particular agent's viewport.
func (m *ViewportModel) HasPendingToolCall() bool { return m.hasPendingToolCall() }

func (m *ViewportModel) hasPendingToolCall() bool {
	for _, msg := range m.messages {
		if msg.Type == MessageToolCall && msg.Pending {
			return true
		}
	}
	return false
}

// SetToolInputsExpanded toggles the per-viewport flag that controls whether
// renderToolCall draws the truncated summary or the full multi-line input
// (QUM-335). AppModel calls this on every per-agent viewport when the user
// presses the global toggle binding; the call re-renders so the new state
// is visible immediately.
func (m *ViewportModel) SetToolInputsExpanded(expanded bool) {
	if m.toolInputsExpanded == expanded {
		return
	}
	m.toolInputsExpanded = expanded
	if len(m.messages) > 0 {
		m.renderAndUpdate()
	}
}

// ToolInputsExpanded reports whether the viewport is currently rendering
// tool calls in their expanded multi-line form (QUM-335). Exposed for
// tests; production code drives the flag through AppModel.
func (m *ViewportModel) ToolInputsExpanded() bool {
	return m.toolInputsExpanded
}

// AppendStatus adds a status/system message.
func (m *ViewportModel) AppendStatus(text string) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageStatus,
		Content:  text,
		Complete: true,
	})
	m.renderAndUpdate()
}

// AppendSystemMessage adds a system-injected message (e.g. an inbox-drain
// body) to the conversation buffer. Rendered with a mail glyph and the
// theme's SystemText style so it's visibly distinct from a user-typed turn.
// The underlying Claude session still receives the body as a user-role
// message — this entry is viewport-only display. (QUM-338)
func (m *ViewportModel) AppendSystemMessage(text string) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageSystem,
		Content:  text,
		Complete: true,
	})
	m.renderAndUpdate()
}

// AppendSystemNotification adds supervisor-injected `<system-notification>`
// message(s) to the conversation buffer (QUM-557 / QUM-562 / QUM-574). The
// input may contain ONE OR MORE back-to-back envelopes (e.g. a status_change
// immediately followed by a message in the same flush window) — each peels
// to a distinct MessageSystemNotification entry so the viewport renders them
// with their respective glyphs/colors and no raw tags leak through.
//
// When the input does NOT begin with the tag (legacy inbox banners
// pre-QUM-555), this falls back to AppendSystemMessage so the long-standing
// MessageSystem rendering keeps working. Any trailing non-tag residue after
// a successful peel-loop is surfaced via AppendSystemMessage rather than
// dropped, so unexpected content stays visible to the user.
func (m *ViewportModel) AppendSystemNotification(text string) {
	rest := text
	appended := false
	for {
		stripped, notifType, isInterrupt, remaining, ok := stripSystemNotificationTag(rest)
		if !ok {
			break
		}
		m.messages = append(m.messages, MessageEntry{
			Type:             MessageSystemNotification,
			Content:          stripped,
			Complete:         true,
			Interrupt:        isInterrupt,
			NotificationType: notifType,
		})
		appended = true
		rest = remaining
	}
	if !appended {
		// No envelope at all — preserve the legacy fallback path.
		m.AppendSystemMessage(text)
		return
	}
	// Tail residue (e.g. trailing whitespace, or unexpected non-tag text
	// after the final envelope) — surface it so nothing is silently dropped.
	if strings.TrimSpace(rest) != "" {
		m.AppendSystemMessage(rest)
		return
	}
	m.renderAndUpdate()
}

// AppendError adds an error message with visual distinction.
func (m *ViewportModel) AppendError(text string) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageError,
		Content:  text,
		Complete: true,
	})
	m.renderAndUpdate()
}

// HasPendingAssistant returns true if there is an incomplete assistant message
// (i.e., assistant text was streamed but not yet finalized).
func (m *ViewportModel) HasPendingAssistant() bool {
	if n := len(m.messages); n > 0 {
		return m.messages[n-1].Type == MessageAssistant && !m.messages[n-1].Complete
	}
	return false
}

// IsAutoScroll returns whether auto-scroll is enabled.
func (m *ViewportModel) IsAutoScroll() bool {
	return m.autoScroll
}

// SetAutoScroll sets the auto-scroll state.
func (m *ViewportModel) SetAutoScroll(v bool) {
	m.autoScroll = v
}

// GetMessages returns a copy of the current message buffer.
func (m *ViewportModel) GetMessages() []MessageEntry {
	result := make([]MessageEntry, len(m.messages))
	copy(result, m.messages)
	return result
}

// SetMessages replaces the message buffer and re-renders.
func (m *ViewportModel) SetMessages(msgs []MessageEntry) {
	m.messages = make([]MessageEntry, len(msgs))
	copy(m.messages, msgs)
	m.selection = SelectionState{}
	// QUM-476: rebuild Agent nesting state from in-flight Agent entries so
	// re-seeding a child viewport (Ctrl+N round-trip) preserves depth=1
	// nesting for subsequent tool calls.
	m.activeAgents = nil
	m.lastActiveAgent = ""
	for _, e := range m.messages {
		if e.Type == MessageToolCall && e.Content == "Agent" && e.ToolID != "" && e.Result == "" && !e.Failed {
			if m.activeAgents == nil {
				m.activeAgents = make(map[string]bool)
			}
			m.activeAgents[e.ToolID] = true
			m.lastActiveAgent = e.ToolID
		}
	}
	m.renderAndUpdate()
}

// IsSelecting reports whether the viewport is in select mode.
func (m *ViewportModel) IsSelecting() bool { return m.selection.Active }

// EnterSelect puts the viewport into select mode with the anchor and cursor
// both positioned on the most recent message. No-op when the message buffer
// is empty. Disables auto-scroll so highlight updates don't yank the view.
// NOTE: selection operates on raw buffer indices — child entries rendered
// inside Agent containers (QUM-386) are included in SelectedRaw() yank
// output but their visual gutter highlight is not visible (they're drawn
// inside the container, not as standalone rows).
func (m *ViewportModel) EnterSelect() {
	if len(m.messages) == 0 {
		return
	}
	last := len(m.messages) - 1
	m.selection = SelectionState{Active: true, Anchor: last, Cursor: last}
	m.autoScroll = false
	m.renderAndUpdate()
}

// ExitSelect leaves select mode without yanking. Auto-scroll state is left as
// the user set it.
func (m *ViewportModel) ExitSelect() {
	m.selection = SelectionState{}
	m.renderAndUpdate()
}

// MoveCursor shifts the selection cursor by delta (positive = down toward
// newer messages). Clamps to the buffer bounds. No-op when not selecting.
func (m *ViewportModel) MoveCursor(delta int) {
	if !m.selection.Active {
		return
	}
	m.selection = m.selection.MoveCursor(delta, len(m.messages))
	m.renderAndUpdate()
}

// SelectedRaw returns the raw-markdown payload for the current selection, or
// an empty string when not selecting.
func (m *ViewportModel) SelectedRaw() string {
	if !m.selection.Active {
		return ""
	}
	lo, hi := m.selection.Range()
	return AssembleRawMarkdown(m.messages, lo, hi)
}

func (m *ViewportModel) renderAndUpdate() {
	rendered := m.renderMessages()
	m.vp.SetContent(rendered)
	if m.autoScroll {
		m.vp.GotoBottom()
	} else {
		m.hasNewContent = true
	}
}

func (m *ViewportModel) renderMessages() string {
	// QUM-386 Pass 1: build parent→children index for Agent containers.
	childrenOf := make(map[string][]int)
	childRendered := make(map[int]bool)
	for i, msg := range m.messages {
		if msg.ParentToolID != "" {
			childrenOf[msg.ParentToolID] = append(childrenOf[msg.ParentToolID], i)
			childRendered[i] = true
		}
	}

	var selLo, selHi int
	selecting := m.selection.Active
	if selecting {
		selLo, selHi = m.selection.Range()
	}
	var sb strings.Builder
	first := true
	for i, msg := range m.messages {
		if childRendered[i] {
			continue
		}
		if !first {
			sb.WriteString("\n")
		}
		first = false
		var block strings.Builder
		switch msg.Type {
		case MessageUser:
			block.WriteString(m.theme.AccentText.Render("You: "))
			block.WriteString(msg.Content)
		case MessageAssistant:
			block.WriteString(m.renderer.Render(msg.Content))
			if !msg.Complete {
				block.WriteString(StreamingCursor)
			}
		case MessageToolCall:
			switch {
			case msg.Content == "Agent":
				m.renderAgentContainer(&block, msg, childrenOf[msg.ToolID])
			case msg.Depth > 0:
				m.renderNestedToolCall(&block, msg)
			default:
				m.renderToolCall(&block, msg)
			}
		case MessageStatus:
			block.WriteString(m.theme.NormalText.Render("― " + msg.Content + " ―"))
		case MessageError:
			block.WriteString(m.theme.AccentText.Render("ERROR: "))
			block.WriteString(msg.Content)
		case MessageSystem:
			formatted := formatSystemMessage(msg.Content, m.width)
			block.WriteString(m.theme.SystemText.Render("✉ " + formatted))
		case MessageSystemNotification:
			// QUM-557 / QUM-562: left-bar accent + glyph + body, all under
			// the same style so the row reads as a unified accent block.
			// Tags are stripped at append/replay time; msg.Content is the
			// raw body. Branch on NotificationType first, then on Interrupt
			// within the message class.
			glyph, style := notificationGlyphAndStyle(m.theme, msg)
			formatted := formatSystemMessage(msg.Content, m.width)
			block.WriteString(style.Render("│ " + glyph + " " + formatted))
		case MessageBanner:
			block.WriteString(msg.Content)
		case MessageAutoTrigger:
			// QUM-634: a "why this turn happened" header for autonomous turns,
			// visually distinct from assistant text and the user bubble.
			block.WriteString(m.theme.SystemText.Render("↻ auto-continued — " + msg.Content))
		}
		if selecting && i >= selLo && i <= selHi {
			sb.WriteString(addSelectionGutter(block.String(), m.theme.AccentText.Render(SelectionGutter)))
		} else {
			sb.WriteString(block.String())
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// addSelectionGutter prefixes each line of block with the gutter marker so
// the whole selected message block is visually distinguished.
func addSelectionGutter(block, gutter string) string {
	lines := strings.Split(block, "\n")
	for i, ln := range lines {
		lines[i] = gutter + ln
	}
	return strings.Join(lines, "\n")
}

func (m *ViewportModel) renderToolCall(sb *strings.Builder, msg MessageEntry) {
	// QUM-379: nested tool calls (inside an Agent) render as compact single
	// lines under the parent's │ gutter, indented by depth.
	if msg.Depth > 0 {
		m.renderNestedToolCall(sb, msg)
		return
	}
	// Indicator: spinner while pending, ✗ on failure, ✓ on success.
	// Pre-render with the right style so the failure glyph stands out
	// (QUM-336).
	var renderedIndicator string
	switch {
	case msg.Pending:
		frame := m.spinnerFrame
		if frame == "" {
			// First render before any spinner.TickMsg has propagated; show a
			// static glyph so the entry isn't blank.
			frame = "⠋"
		}
		renderedIndicator = m.theme.AccentText.Render(frame)
	case msg.Failed:
		renderedIndicator = m.theme.ErrorText.Render("✗")
	default:
		renderedIndicator = m.theme.AccentText.Render("✓")
	}
	// QUM-419: compact per-tool header. The display tool name uses the
	// `FormatToolDisplayName` mapping so MCP tools render as
	// `<server>/<action>` (e.g. `mcp__linear__save_issue` → `linear/save_issue`,
	// QUM-589) — preserves the server context users need to tell `linear`
	// from `sprawl` at a glance. The tool name
	// renders bold-accent; the main arg + k=v params render in NormalText so
	// the eye can pick out the call shape at a glance. Width budgeting
	// mirrors crush/internal/ui/chat/tools.go toolParamList: kv params are
	// dropped when including them would shrink mainArg below MinMainArgCells.
	displayName := FormatToolDisplayName(msg.Content)
	mainArg := msg.HeaderArg
	// Legacy fallback: pre-QUM-419 entries (tests, older replay records)
	// don't carry HeaderArg, but they often carry a usable ToolInput
	// summary. Surfacing it here keeps backward compatibility — a test that
	// passes input="ls -la /tmp" still sees that string on the header.
	if mainArg == "" && msg.HeaderParams == nil {
		mainArg = msg.ToolInput
	}
	params := msg.HeaderParams
	paramsStr := RenderKVPairs(params)
	if m.width > 0 {
		// "┌ " (2 cells) + indicator (1 cell) + " " (1 cell) + name.
		fixed := 4
		nameCells := ansi.StringWidth(displayName)
		budget := m.width - fixed - nameCells
		if budget < 1 {
			budget = 1
		}
		if mainArg != "" {
			// Account for the single space between name and mainArg.
			budget--
		}
		if paramsStr != "" {
			// Reserve one extra space between mainArg and the (k=v...) suffix.
			remaining := budget - ansi.StringWidth(paramsStr) - 1
			if remaining < MinMainArgCells {
				paramsStr = ""
			} else {
				mainArg = ansi.Truncate(mainArg, remaining, "…")
			}
		}
		if paramsStr == "" && mainArg != "" {
			mainArg = ansi.Truncate(mainArg, budget, "…")
		}
		// If the tool name itself is over budget (extreme narrow widths),
		// truncate it so the row never wraps.
		if nameCells > m.width-3 {
			displayName = ansi.Truncate(displayName, m.width-3, "…")
		}
	}
	sb.WriteString(m.theme.AccentText.Render("┌ "))
	sb.WriteString(renderedIndicator)
	sb.WriteString(m.theme.AccentText.Bold(true).Render(" " + displayName))
	if mainArg != "" {
		sb.WriteString(m.theme.NormalText.Render(" " + mainArg))
	}
	if paramsStr != "" {
		sb.WriteString(m.theme.NormalText.Render(" " + paramsStr))
	}
	// QUM-419: in compact mode the header line carries the full at-a-glance
	// summary; the multi-line body block is reserved for the Ctrl+O expanded
	// view. When expanded, prefer ToolInputFull (verbatim Bash / pretty JSON)
	// and fall back to the truncated summary for legacy entries that never
	// carried a full representation.
	if m.toolInputsExpanded {
		body := msg.ToolInputFull
		if body == "" {
			body = msg.ToolInput
		}
		if body != "" {
			sb.WriteString("\n")
			for i, ln := range wrapToolInput(body, m.width-toolCallInputPrefix) {
				if i > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(m.theme.NormalText.Render("│ " + ln))
			}
		}
	}
	// Result preview block: shown only after the tool has completed (not
	// pending) AND the bridge captured a non-empty result. Up to 3 non-empty
	// lines, each truncated to the inner-gutter width, with a `+ N more
	// lines` trailer when the source has more. Failures render in the
	// error style so they stand out at a glance (QUM-336). When the global
	// expand-tool-calls flag is on (QUM-343) we render every non-empty
	// result line and skip the trailer.
	if !msg.Pending && msg.Result != "" {
		previewStyle := m.theme.NormalText
		if msg.Failed {
			previewStyle = m.theme.ErrorText
		}
		maxLines := 3
		if m.toolInputsExpanded {
			maxLines = -1
		}
		previewLines, more := previewResultLines(msg.Result, maxLines, m.width-toolCallInputPrefix)
		for _, ln := range previewLines {
			sb.WriteString("\n")
			sb.WriteString(previewStyle.Render("│ " + ln))
		}
		if more > 0 {
			trailer := fmt.Sprintf("+ %d more lines", more)
			if m.width > toolCallInputPrefix {
				trailer = ansi.Truncate(trailer, m.width-toolCallInputPrefix, "…")
			}
			sb.WriteString("\n")
			sb.WriteString(m.theme.NormalText.Render("│ " + trailer))
		}
	}
	sb.WriteString("\n")
	sb.WriteString(m.theme.AccentText.Render("└"))
}

// nestedToolCallIndent is the number of extra spaces per nesting depth level
// when rendering compact nested tool calls (QUM-379).
const nestedToolCallIndent = 2

// renderNestedToolCall renders a compact single-line representation of a tool
// call inside an Agent invocation. Format: `│ {indent}{indicator} {name}  {input}`
// No box-drawing (┌/└), no result preview, no multi-line body. (QUM-379)
func (m *ViewportModel) renderNestedToolCall(sb *strings.Builder, msg MessageEntry) {
	// Indicator: same logic as the full-box path.
	var renderedIndicator string
	switch {
	case msg.Pending:
		frame := m.spinnerFrame
		if frame == "" {
			frame = "⠋"
		}
		renderedIndicator = m.theme.AccentText.Render(frame)
	case msg.Failed:
		renderedIndicator = m.theme.ErrorText.Render("✗")
	default:
		renderedIndicator = m.theme.AccentText.Render("✓")
	}

	// Build the plain-text content: "{name}  {input}" — truncated to fit.
	indent := strings.Repeat(" ", msg.Depth*nestedToolCallIndent)
	// "│ " (2 cells) + indent + indicator (1 cell) + " " (1 cell) + name…
	const fixedCells = 4 // "│ " + indicator + " "
	budget := m.width - fixedCells - len(indent)
	if budget < 1 {
		budget = 1
	}

	body := msg.Content
	if msg.ToolInput != "" {
		body += "  " + msg.ToolInput
	}
	body = ansi.Truncate(body, budget, "…")

	sb.WriteString(m.theme.AccentText.Render("│ " + indent))
	sb.WriteString(renderedIndicator)
	sb.WriteString(m.theme.NormalText.Render(" " + body))
}

// renderAgentContainer renders an Agent tool call as a visual container.
// When pending: header + input + nested child tool calls (live activity).
// When complete: header + collapsed result preview (children hidden). (QUM-386)
func (m *ViewportModel) renderAgentContainer(sb *strings.Builder, msg MessageEntry, childIndices []int) {
	// Indicator
	var renderedIndicator string
	switch {
	case msg.Pending:
		frame := m.spinnerFrame
		if frame == "" {
			frame = "⠋"
		}
		renderedIndicator = m.theme.AccentText.Render(frame)
	case msg.Failed:
		renderedIndicator = m.theme.ErrorText.Render("✗")
	default:
		renderedIndicator = m.theme.AccentText.Render("✓")
	}

	// Header: ┌ {indicator} Agent
	name := msg.Content
	if m.width > 0 {
		const fixedHeaderCells = 4
		budget := m.width - fixedHeaderCells
		if budget < 1 {
			budget = 1
		}
		name = ansi.Truncate(msg.Content, budget, "…")
	}
	sb.WriteString(m.theme.AccentText.Render("┌ "))
	sb.WriteString(renderedIndicator)
	sb.WriteString(m.theme.AccentText.Render(" " + name))

	// Input summary
	body := msg.ToolInput
	if m.toolInputsExpanded && msg.ToolInputFull != "" {
		body = msg.ToolInputFull
	}
	if body != "" {
		sb.WriteString("\n")
		for i, ln := range wrapToolInput(body, m.width-toolCallInputPrefix) {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(m.theme.NormalText.Render("│ " + ln))
		}
	}

	if msg.Pending {
		// Show nested children while in-flight.
		for _, ci := range childIndices {
			child := m.messages[ci]
			sb.WriteString("\n")
			m.renderNestedToolCall(sb, child)
		}
	} else if msg.Result != "" {
		// Collapsed: show result preview only (children hidden).
		previewStyle := m.theme.NormalText
		if msg.Failed {
			previewStyle = m.theme.ErrorText
		}
		maxLines := 3
		if m.toolInputsExpanded {
			maxLines = -1
		}
		previewLines, more := previewResultLines(msg.Result, maxLines, m.width-toolCallInputPrefix)
		for _, ln := range previewLines {
			sb.WriteString("\n")
			sb.WriteString(previewStyle.Render("│ " + ln))
		}
		if more > 0 {
			trailer := fmt.Sprintf("+ %d more lines", more)
			if m.width > toolCallInputPrefix {
				trailer = ansi.Truncate(trailer, m.width-toolCallInputPrefix, "…")
			}
			sb.WriteString("\n")
			sb.WriteString(m.theme.NormalText.Render("│ " + trailer))
		}
	}

	sb.WriteString("\n")
	sb.WriteString(m.theme.AccentText.Render("└"))
}

// previewResultLines splits result on newlines, drops empty/whitespace-only
// entries, returns up to maxLines truncated to width cells, and the count of
// remaining (non-empty) source lines that did not fit. width <= 0 disables
// truncation. maxLines < 0 means "no cap" — every non-empty line is returned
// (used by the QUM-343 expand-tool-calls path).
func previewResultLines(result string, maxLines, width int) ([]string, int) {
	// Normalize CR / CRLF so progress output (e.g. `git rebase`, `npm install`)
	// doesn't emit bare \r into the rendered viewport. A bare \r would reset
	// the host terminal cursor to column 0 and bleed text outside the activity
	// panel border (QUM-433). Mirrors the normalization in formatSystemMessage.
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

// formatSystemMessage prepares a system-message body for rendering by:
//  1. normalizing CRLF / lone CR into LF,
//  2. collapsing runs of >=2 consecutive blank (whitespace-only) lines down to
//     exactly one blank line (QUM-401 — drains and other system injections
//     produced by upstream agent prompts often arrive with multi-blank gaps
//     that bloat the viewport),
//  3. dropping leading and trailing blank lines, and
//  4. soft-wrapping each non-blank line at word boundaries using
//     ansi.Wordwrap so long messages don't escape the viewport.
//
// Word-wrap is skipped when the wrap budget would be <1 (caller hasn't been
// sized yet, or the budget is too small to be useful) — the collapse logic
// still applies. We reserve 4 cells from `width` for the wrap budget: 2 for
// the leading `"✉ "` glyph + space rendered before the first wrapped line
// and another 2 of headroom so that the lipgloss SystemText.Render call
// (which background-fills shorter lines to the longest line's width) leaves
// every line at most `width-2` cells wide. This matches the QUM-401
// soft-wrap budget asserted in TestViewportModel_RenderSystemMessage_*.
// notificationGlyphAndStyle selects the (glyph, style) pair for a
// MessageSystemNotification entry, branching first on NotificationType
// (QUM-562 status_change vs message-class) and then on Interrupt within the
// message class. Defaults to message-class async for empty/unknown types so
// pre-QUM-562 legacy entries render identically. Lookup-table shape kept
// small and explicit — additional notification kinds (none planned per
// QUM-562 YAGNI guard) would extend the switch.
func notificationGlyphAndStyle(theme *Theme, msg MessageEntry) (glyph string, style lipgloss.Style) {
	switch msg.NotificationType {
	case NotificationKindStatusChange:
		return "◉", theme.StatusChangeText
	default: // NotificationKindMessage and any unknown/legacy value
		if msg.Interrupt {
			return "⚡", theme.InterruptText
		}
		return "✉", theme.NotificationText
	}
}

func formatSystemMessage(content string, width int) string {
	// Normalize line endings: CRLF -> LF, then any remaining CR -> LF.
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
	// Drop trailing blank lines (defensive — current logic already prevents
	// them, but keep this so future edits to the loop don't regress the
	// invariant).
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// wrapToolInput prepares a tool-input string for rendering inside the tool
// block. Carriage returns are dropped; each logical line is wrapped to at
// most width cells (hard-breaking within words when needed) so the `│ `
// gutter always lines up and nothing escapes the viewport. When width<=0 the
// input is returned as-is (caller hasn't been sized yet).
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
