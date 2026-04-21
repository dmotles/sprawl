package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

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
}

// SelectionGutter is the visual prefix placed on selected message blocks when
// the viewport is in select mode.
const SelectionGutter = "▌ "

// NewViewportModel creates a viewport with placeholder content.
func NewViewportModel(theme *Theme) ViewportModel {
	vp := viewport.New()
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
	// Tool name line with accent color
	toolHeader := fmt.Sprintf("┌ %s %s", indicator, msg.Content)
	sb.WriteString(m.theme.AccentText.Render(toolHeader))
	// Input summary on next line if present
	if msg.ToolInput != "" {
		sb.WriteString("\n")
		inputLine := fmt.Sprintf("│ %s", msg.ToolInput)
		sb.WriteString(m.theme.NormalText.Render(inputLine))
	}
	sb.WriteString("\n")
	sb.WriteString(m.theme.AccentText.Render("└"))
}
