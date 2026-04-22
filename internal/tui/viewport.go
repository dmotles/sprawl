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
)

// MessageEntry is a single item in the conversation buffer.
type MessageEntry struct {
	Type      MessageType
	Content   string
	Complete  bool
	Approved  bool   // only used for MessageToolCall
	ToolInput string // concise tool input summary (MessageToolCall only)
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

// AppendToolCall adds a tool call notification line.
func (m *ViewportModel) AppendToolCall(name string, approved bool, input string) {
	m.messages = append(m.messages, MessageEntry{
		Type:      MessageToolCall,
		Content:   name,
		Complete:  true,
		Approved:  approved,
		ToolInput: input,
	})
	m.renderAndUpdate()
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
	indicator := "⏳"
	if msg.Approved {
		indicator = "✓"
	}
	// Tool name line with accent color. Truncated to the viewport width so a
	// long tool name (or ANSI garbage in msg.Content) cannot bleed past the
	// right border (QUM-324).
	toolHeader := fmt.Sprintf("┌ %s %s", indicator, msg.Content)
	if m.width > 0 {
		toolHeader = ansi.Truncate(toolHeader, m.width, "…")
	}
	sb.WriteString(m.theme.AccentText.Render(toolHeader))
	// Input summary on following line(s) if present. Multi-line tool input
	// is preserved but wrapped at the viewport inner width so each wrapped
	// segment stays inside the `│ …` gutter (QUM-324).
	if msg.ToolInput != "" {
		sb.WriteString("\n")
		for i, ln := range wrapToolInput(msg.ToolInput, m.width-toolCallInputPrefix) {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(m.theme.NormalText.Render("│ " + ln))
		}
	}
	sb.WriteString("\n")
	sb.WriteString(m.theme.AccentText.Render("└"))
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
