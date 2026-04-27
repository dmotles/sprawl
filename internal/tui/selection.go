package tui

import (
	"fmt"
	"strings"
)

// SelectionState tracks a contiguous, message-level selection in the viewport.
// Kept as a plain value (no bubbletea dependency) so it can be unit-tested in
// isolation. See docs/designs/viewport-yank.md.
type SelectionState struct {
	Active bool
	Anchor int // message index where selection started
	Cursor int // message index the user is moving
}

// Range returns the inclusive [lo, hi] message-index range, normalized so that
// lo <= hi regardless of which side of the anchor the cursor sits on.
func (s SelectionState) Range() (lo, hi int) {
	lo, hi = s.Anchor, s.Cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi
}

// MoveCursor returns a copy of s with Cursor shifted by delta and clamped to
// [0, n-1]. When n == 0 the cursor is held at 0.
func (s SelectionState) MoveCursor(delta, n int) SelectionState {
	if n <= 0 {
		s.Cursor = 0
		return s
	}
	c := s.Cursor + delta
	if c < 0 {
		c = 0
	}
	if c > n-1 {
		c = n - 1
	}
	s.Cursor = c
	return s
}

// AssembleRawMarkdown returns the raw-markdown concatenation of messages whose
// indices lie in [lo, hi] (inclusive, clamped to the buffer). Conversation
// chrome (status, error) is skipped; user messages are rendered as markdown
// blockquotes; tool calls are rendered as HTML comments. Assistant messages
// are emitted verbatim so the payload reflects the source the renderer
// consumed, not the reflowed terminal display.
func AssembleRawMarkdown(msgs []MessageEntry, lo, hi int) string {
	n := len(msgs)
	if n == 0 {
		return ""
	}
	if lo < 0 {
		lo = 0
	}
	if hi > n-1 {
		hi = n - 1
	}
	if lo > hi {
		return ""
	}
	var parts []string
	for i := lo; i <= hi; i++ {
		m := msgs[i]
		switch m.Type {
		case MessageAssistant:
			parts = append(parts, m.Content)
		case MessageUser:
			parts = append(parts, quoteLines(m.Content))
		case MessageToolCall:
			if m.ToolInput != "" {
				parts = append(parts, fmt.Sprintf("<!-- tool: %s (%s) -->", m.Content, m.ToolInput))
			} else {
				parts = append(parts, fmt.Sprintf("<!-- tool: %s -->", m.Content))
			}
		case MessageStatus, MessageError, MessageSystem:
			// Skip: TUI chrome, not conversation content. MessageSystem
			// (QUM-338) bodies are already part of the user-role turn the
			// bridge delivered to Claude — re-emitting here would double-count.
		}
	}
	return strings.Join(parts, "\n\n")
}

func quoteLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = "> " + ln
	}
	return strings.Join(lines, "\n")
}
