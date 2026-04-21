package tui

import (
	"strings"
	"testing"
)

func TestSelectionState_RangeNormalizedWhenCursorBeforeAnchor(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 3, Cursor: 1}
	lo, hi := s.Range()
	if lo != 1 || hi != 3 {
		t.Errorf("Range() = (%d, %d), want (1, 3)", lo, hi)
	}
}

func TestSelectionState_RangeSingleMessage(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 2, Cursor: 2}
	lo, hi := s.Range()
	if lo != 2 || hi != 2 {
		t.Errorf("Range() = (%d, %d), want (2, 2)", lo, hi)
	}
}

func TestSelectionState_MoveCursorClampsLow(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 2, Cursor: 1}
	s = s.MoveCursor(-5, 4)
	if s.Cursor != 0 {
		t.Errorf("MoveCursor(-5, 4).Cursor = %d, want 0", s.Cursor)
	}
}

func TestSelectionState_MoveCursorClampsHigh(t *testing.T) {
	s := SelectionState{Active: true, Anchor: 2, Cursor: 2}
	s = s.MoveCursor(10, 4)
	if s.Cursor != 3 {
		t.Errorf("MoveCursor(10, 4).Cursor = %d, want 3", s.Cursor)
	}
}

func TestSelectionState_MoveCursorNoMessages(t *testing.T) {
	s := SelectionState{Active: true}
	s = s.MoveCursor(1, 0)
	if s.Cursor != 0 {
		t.Errorf("MoveCursor on empty buffer should stay at 0, got %d", s.Cursor)
	}
}

func TestAssembleRawMarkdown_AssistantOnly(t *testing.T) {
	msgs := []MessageEntry{
		{Type: MessageAssistant, Content: "# Heading\n\nBody text.", Complete: true},
	}
	got := AssembleRawMarkdown(msgs, 0, 0)
	want := "# Heading\n\nBody text."
	if got != want {
		t.Errorf("AssembleRawMarkdown() =\n%q\nwant\n%q", got, want)
	}
}

func TestAssembleRawMarkdown_UserIsBlockquoted(t *testing.T) {
	msgs := []MessageEntry{
		{Type: MessageUser, Content: "please summarize\nthe design", Complete: true},
	}
	got := AssembleRawMarkdown(msgs, 0, 0)
	want := "> please summarize\n> the design"
	if got != want {
		t.Errorf("AssembleRawMarkdown() =\n%q\nwant\n%q", got, want)
	}
}

func TestAssembleRawMarkdown_ToolCallRenderedAsHTMLComment(t *testing.T) {
	msgs := []MessageEntry{
		{Type: MessageToolCall, Content: "Bash", ToolInput: "ls -la", Complete: true},
	}
	got := AssembleRawMarkdown(msgs, 0, 0)
	if !strings.Contains(got, "<!--") || !strings.Contains(got, "Bash") || !strings.Contains(got, "ls -la") {
		t.Errorf("AssembleRawMarkdown() tool call should be HTML comment with name+input, got %q", got)
	}
}

func TestAssembleRawMarkdown_SkipsStatusAndError(t *testing.T) {
	msgs := []MessageEntry{
		{Type: MessageStatus, Content: "session started"},
		{Type: MessageAssistant, Content: "hello", Complete: true},
		{Type: MessageError, Content: "transient failure"},
	}
	got := AssembleRawMarkdown(msgs, 0, 2)
	if strings.Contains(got, "session started") {
		t.Errorf("status should be skipped, got %q", got)
	}
	if strings.Contains(got, "transient failure") {
		t.Errorf("error should be skipped, got %q", got)
	}
	if !strings.Contains(got, "hello") {
		t.Errorf("assistant content missing, got %q", got)
	}
}

func TestAssembleRawMarkdown_MixedTypesBlankLineSeparated(t *testing.T) {
	msgs := []MessageEntry{
		{Type: MessageUser, Content: "hi", Complete: true},
		{Type: MessageAssistant, Content: "hello back", Complete: true},
	}
	got := AssembleRawMarkdown(msgs, 0, 1)
	want := "> hi\n\nhello back"
	if got != want {
		t.Errorf("AssembleRawMarkdown() =\n%q\nwant\n%q", got, want)
	}
}

func TestAssembleRawMarkdown_EmptyRangeReturnsEmpty(t *testing.T) {
	msgs := []MessageEntry{{Type: MessageAssistant, Content: "x"}}
	got := AssembleRawMarkdown(msgs, 5, 7)
	if got != "" {
		t.Errorf("out-of-range should return empty, got %q", got)
	}
}

func TestAssembleRawMarkdown_RangeClampedToBuffer(t *testing.T) {
	msgs := []MessageEntry{
		{Type: MessageAssistant, Content: "a", Complete: true},
		{Type: MessageAssistant, Content: "b", Complete: true},
	}
	got := AssembleRawMarkdown(msgs, -3, 99)
	want := "a\n\nb"
	if got != want {
		t.Errorf("clamped range = %q, want %q", got, want)
	}
}
