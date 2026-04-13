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
	Type     MessageType
	Content  string
	Complete bool
	Approved bool // only used for MessageToolCall
}

// ViewportModel wraps a bubbles viewport with theme styling.
type ViewportModel struct {
	vp            viewport.Model
	theme         *Theme
	messages      []MessageEntry
	autoScroll    bool
	hasNewContent bool
}

// NewViewportModel creates a viewport with placeholder content.
func NewViewportModel(theme *Theme) ViewportModel {
	vp := viewport.New()
	vp.SetContent(placeholderContent)
	return ViewportModel{
		vp:         vp,
		theme:      theme,
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
func (m *ViewportModel) AppendToolCall(name string, approved bool) {
	m.messages = append(m.messages, MessageEntry{
		Type:     MessageToolCall,
		Content:  name,
		Complete: true,
		Approved: approved,
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

// IsAutoScroll returns whether auto-scroll is enabled.
func (m *ViewportModel) IsAutoScroll() bool {
	return m.autoScroll
}

// SetAutoScroll sets the auto-scroll state.
func (m *ViewportModel) SetAutoScroll(v bool) {
	m.autoScroll = v
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
	var sb strings.Builder
	for i, msg := range m.messages {
		if i > 0 {
			sb.WriteString("\n")
		}
		switch msg.Type {
		case MessageUser:
			sb.WriteString("You: ")
			sb.WriteString(msg.Content)
		case MessageAssistant:
			sb.WriteString(msg.Content)
			if !msg.Complete {
				sb.WriteString(StreamingCursor)
			}
		case MessageToolCall:
			indicator := "⏳"
			if msg.Approved {
				indicator = "✓"
			}
			sb.WriteString(fmt.Sprintf(" %s Tool: %s ", indicator, msg.Content))
		case MessageStatus:
			sb.WriteString("― ")
			sb.WriteString(msg.Content)
			sb.WriteString(" ―")
		case MessageError:
			sb.WriteString("ERROR: ")
			sb.WriteString(msg.Content)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
