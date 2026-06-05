package tui

// QUM-676 S6 — RED-phase tests for selection-mode's port from
// ViewportModel.GetMessages() / selection.AssembleRawMarkdown(msgs) onto
// the new ChatList.Items() + Item.RawMarkdown() surfaces.
//
// The legacy AssembleRawMarkdown([]MessageEntry, lo, hi) function will be
// replaced (or wrapped) by a ChatList-aware equivalent. The exact name is up
// to the implementer; this test asserts the behavior of a new helper called
// AssembleRawMarkdownFromItems that takes []Item, lo, hi. Rename freely.

import (
	"strings"
	"testing"
)

// TestAssembleRawMarkdownFromItems_AssistantOnly mirrors
// TestAssembleRawMarkdown_AssistantOnly but consumes ChatList items.
func TestAssembleRawMarkdownFromItems_AssistantOnly(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("# Heading\n\nBody text.")
	cl.FinalizeAssistantMessage()
	got := AssembleRawMarkdownFromItems(cl.Items(), 0, 0)
	want := "# Heading\n\nBody text."
	if got != want {
		t.Errorf("AssembleRawMarkdownFromItems() =\n%q\nwant\n%q", got, want)
	}
}

// TestAssembleRawMarkdownFromItems_UserIsBlockquoted mirrors the same
// behavior for the user-item branch.
func TestAssembleRawMarkdownFromItems_UserIsBlockquoted(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("please summarize\nthe design")
	got := AssembleRawMarkdownFromItems(cl.Items(), 0, 0)
	want := "> please summarize\n> the design"
	if got != want {
		t.Errorf("AssembleRawMarkdownFromItems() =\n%q\nwant\n%q", got, want)
	}
}

// TestAssembleRawMarkdownFromItems_ToolCallRenderedAsHTMLComment mirrors
// the legacy assertion that tool calls yank as HTML comments.
func TestAssembleRawMarkdownFromItems_ToolCallRenderedAsHTMLComment(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "tu_1", true, "ls -la", "ls -la", "ls -la", nil, "")
	got := AssembleRawMarkdownFromItems(cl.Items(), 0, 0)
	if !strings.Contains(got, "<!--") || !strings.Contains(got, "Bash") || !strings.Contains(got, "ls -la") {
		t.Errorf("AssembleRawMarkdownFromItems() tool call should be HTML comment with name+input, got %q", got)
	}
}

// TestAssembleRawMarkdownFromItems_MixedTypesBlankLineSeparated mirrors the
// legacy two-newline separator between distinct items.
func TestAssembleRawMarkdownFromItems_MixedTypesBlankLineSeparated(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	cl.AppendAssistantChunk("hello back")
	cl.FinalizeAssistantMessage()
	got := AssembleRawMarkdownFromItems(cl.Items(), 0, 1)
	want := "> hi\n\nhello back"
	if got != want {
		t.Errorf("AssembleRawMarkdownFromItems() =\n%q\nwant\n%q", got, want)
	}
}

// TestAssembleRawMarkdownFromItems_RangeClampedToBuffer pins the bounds-
// clamping behavior so callers can pass loose ranges without panicking.
func TestAssembleRawMarkdownFromItems_RangeClampedToBuffer(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("a")
	cl.FinalizeAssistantMessage()
	cl.AppendAssistantChunk("b")
	cl.FinalizeAssistantMessage()
	got := AssembleRawMarkdownFromItems(cl.Items(), -3, 99)
	want := "a\n\nb"
	if got != want {
		t.Errorf("clamped range = %q, want %q", got, want)
	}
}
