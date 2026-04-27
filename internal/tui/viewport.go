package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
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

// SetContent replaces the viewport content.
func (m *ViewportModel) SetContent(s string) {
	m.vp.SetContent(s)
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
	m.messages = append(m.messages, MessageEntry{
		Type:          MessageToolCall,
		Content:       name,
		Complete:      true,
		Approved:      approved,
		ToolInput:     input,
		ToolInputFull: fullInput,
		ToolID:        toolID,
		Pending:       true,
	})
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
	// Replacing the buffer invalidates any active selection.
	m.selection = SelectionState{}
	m.renderAndUpdate()
}

// IsSelecting reports whether the viewport is in select mode.
func (m *ViewportModel) IsSelecting() bool { return m.selection.Active }

// EnterSelect puts the viewport into select mode with the anchor and cursor
// both positioned on the most recent message. No-op when the message buffer
// is empty. Disables auto-scroll so highlight updates don't yank the view.
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
	var selLo, selHi int
	selecting := m.selection.Active
	if selecting {
		selLo, selHi = m.selection.Range()
	}
	var sb strings.Builder
	for i, msg := range m.messages {
		if i > 0 {
			sb.WriteString("\n")
		}
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
			m.renderToolCall(&block, msg)
		case MessageStatus:
			block.WriteString(m.theme.NormalText.Render("― " + msg.Content + " ―"))
		case MessageError:
			block.WriteString(m.theme.AccentText.Render("ERROR: "))
			block.WriteString(msg.Content)
		case MessageSystem:
			// QUM-338: system-injected content (inbox drains today). Mail
			// glyph + distinct SystemText style so the human watching the TUI
			// can tell at a glance the system spoke, not them.
			block.WriteString(m.theme.SystemText.Render("✉ " + msg.Content))
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
	// Tool name line with accent color. Truncated to the viewport width so a
	// long tool name (or ANSI garbage in msg.Content) cannot bleed past the
	// right border (QUM-324). The indicator is styled separately so the
	// failure ✗ keeps its distinct color while the rest of the header is
	// accent-styled.
	name := msg.Content
	if m.width > 0 {
		// "┌ " (2 cells) + indicator (1 cell) + " " (1 cell) + name. Budget
		// the trailing name to whatever is left.
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
	// Input summary on following line(s) if present. Multi-line tool input
	// is preserved but wrapped at the viewport inner width so each wrapped
	// segment stays inside the `│ …` gutter (QUM-324). When the global
	// expand-tool-inputs flag is on (QUM-335) and the entry carries a full
	// representation, render that instead so the user sees the un-truncated
	// command / pretty JSON. Falls back to the truncated summary otherwise.
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

// previewResultLines splits result on newlines, drops empty/whitespace-only
// entries, returns up to maxLines truncated to width cells, and the count of
// remaining (non-empty) source lines that did not fit. width <= 0 disables
// truncation. maxLines < 0 means "no cap" — every non-empty line is returned
// (used by the QUM-343 expand-tool-calls path).
func previewResultLines(result string, maxLines, width int) ([]string, int) {
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
