package tui

// QUM-676 S6 — RED-phase tests for the new ChatList.Items() accessor and the
// per-Item RawMarkdown() method.
//
// Design choice (test-writer): the new surface is
//
//	type Item interface {
//	    Render(width int) string
//	    Finished() bool
//	    RawMarkdown() string // NEW: copy-for-selection payload
//	}
//
//	func (c *ChatList) Items() []Item
//
// If the implementer prefers EachItem(func(Item)) or returning the existing
// envelope wrappers, rename accordingly — the assertions below only need
// ordered, typed access to each concrete item plus its raw-markdown form.
//
// The intent: selection.go's AssembleRawMarkdown — which today consumes
// []MessageEntry — gets ported to consume the per-item RawMarkdown() output
// once viewport.go is deleted. The output for each concrete Item type must
// match what AssembleRawMarkdown produces today for the equivalent
// MessageEntry so the user-visible yank payload is preserved.

import (
	"strings"
	"testing"
)

// CONTRACT: ChatList.Items() returns the **unwrapped** Item slice — internal
// render-cache envelopes (*itemEnvelope) must not leak through. The concrete
// type asserts in the tests below (e.g. *UserItem, *AssistantTextItem,
// *SystemNotificationItem) rely on this contract. An implementation that
// exposes []*itemEnvelope would break every test in this file at the type
// assertion site, which is the intended signal.

// NOTE on ThinkingItem: ThinkingItem is a real Item type (see items.go:161),
// but ChatList.Reset has no MessageEntry type that produces one — Reset's
// switch in chatlist.go (~L306) handles User / Assistant / ToolCall /
// SystemNotification / AutoTrigger only. Thinking items are produced via the
// live AppendThinking path, not from MessageEntry replay. The tests below
// drive Items() via Reset and therefore intentionally do not exercise
// ThinkingItem; the broader Items() contract (Thinking surfacing through the
// unwrapped slice) is covered transitively wherever AppendThinking is
// followed by Items() — out of scope for S6 backfill behavior.

// TestChatList_Items_ReturnsOrderedItems asserts that Items() exposes the
// underlying item slice in append-order after a Reset with a mixed transcript.
// Status / Banner / Error / System entries should be dropped (they're S5
// contract violators that the new model does not represent as items).
func TestChatList_Items_ReturnsOrderedItems(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageUser, Content: "hello", Complete: true},
		{Type: MessageAssistant, Content: "world", Complete: true},
		{
			Type: MessageToolCall, Content: "Bash", Complete: true,
			ToolID: "tu_1", ToolInput: "ls", Pending: false, Result: "ok",
		},
		// These are contract-violators — they should NOT yield items.
		{Type: MessageStatus, Content: "Resumed from prior session", Complete: true},
		{Type: MessageError, Content: "boom", Complete: true},
		{Type: MessageBanner, Content: "banner", Complete: true},
		{Type: MessageSystem, Content: "sysmsg", Complete: true},
		// System notification is a real item, not a contract-violator.
		{Type: MessageSystemNotification, Content: "ping", NotificationType: NotificationKindMessage, Complete: true},
		{Type: MessageAutoTrigger, Content: "auto-run", Complete: true},
	})

	items := cl.Items()
	if len(items) != 5 {
		t.Fatalf("Items() returned %d items, want 5 (status/error/banner/system are skipped); items=%v", len(items), items)
	}
	if _, ok := items[0].(*UserItem); !ok {
		t.Errorf("items[0] = %T, want *UserItem", items[0])
	}
	if _, ok := items[1].(*AssistantTextItem); !ok {
		t.Errorf("items[1] = %T, want *AssistantTextItem", items[1])
	}
	if _, ok := items[2].(*ToolCallItem); !ok {
		t.Errorf("items[2] = %T, want *ToolCallItem", items[2])
	}
	if _, ok := items[3].(*SystemNotificationItem); !ok {
		t.Errorf("items[3] = %T, want *SystemNotificationItem", items[3])
	}
	if _, ok := items[4].(*AutoTriggerItem); !ok {
		t.Errorf("items[4] = %T, want *AutoTriggerItem", items[4])
	}
}

// TestUserItem_RawMarkdown asserts that a user item's raw-markdown form is the
// blockquoted text (matches the legacy AssembleRawMarkdown user-branch).
func TestUserItem_RawMarkdown(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("please summarize\nthe design")
	got := cl.Items()[0].RawMarkdown()
	want := "> please summarize\n> the design"
	if got != want {
		t.Errorf("UserItem.RawMarkdown() =\n%q\nwant\n%q", got, want)
	}
}

// TestAssistantItem_RawMarkdown_PreservesMarkdown asserts that the assistant
// item's raw-markdown form is the verbatim glamour input (no rendering).
// Critical: fenced code blocks must be preserved so yanked content can be
// re-pasted as markdown.
func TestAssistantItem_RawMarkdown_PreservesMarkdown(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	body := "# Heading\n\n```go\nfmt.Println(\"hi\")\n```"
	cl.AppendAssistantChunk(body)
	cl.FinalizeAssistantMessage()
	got := cl.Items()[0].RawMarkdown()
	if got != body {
		t.Errorf("AssistantTextItem.RawMarkdown() should be verbatim source markdown;\ngot %q\nwant %q", got, body)
	}
}

// TestToolCallItem_RawMarkdown_HtmlCommentForm asserts that the tool-call
// item's raw-markdown form is the legacy "<!-- tool: name (input) -->"
// shape so selection yanking remains source-compatible.
func TestToolCallItem_RawMarkdown_HtmlCommentForm(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "tu_1", true, "ls -la /tmp", "ls -la /tmp", "ls -la /tmp", nil, "")
	got := cl.Items()[0].RawMarkdown()
	if !strings.HasPrefix(got, "<!--") || !strings.HasSuffix(got, "-->") {
		t.Errorf("ToolCallItem.RawMarkdown() should be wrapped in an HTML comment; got %q", got)
	}
	if !strings.Contains(got, "Bash") {
		t.Errorf("ToolCallItem.RawMarkdown() missing tool name; got %q", got)
	}
	if !strings.Contains(got, "ls -la /tmp") {
		t.Errorf("ToolCallItem.RawMarkdown() missing input summary; got %q", got)
	}
}

// TestSystemNotificationItem_RawMarkdown_NonEmpty asserts that
// SystemNotificationItem yields non-empty raw-markdown so peer agent envelopes
// (which today are part of the user-role turn the bridge delivered) appear in
// yanked output. The exact shape can evolve; what matters is the content
// surfaces in some form.
func TestSystemNotificationItem_RawMarkdown_NonEmpty(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{
			Type:             MessageSystemNotification,
			Content:          "from alice: hi",
			NotificationType: NotificationKindMessage,
			Complete:         true,
		},
	})
	items := cl.Items()
	if len(items) != 1 {
		t.Fatalf("expected 1 item after system-notification Reset, got %d", len(items))
	}
	got := items[0].RawMarkdown()
	if !strings.Contains(got, "from alice: hi") {
		t.Errorf("SystemNotificationItem.RawMarkdown() should surface the body; got %q", got)
	}
}

// TestChatList_Items_EmptyChatListReturnsNilOrEmpty asserts that calling
// Items() on a fresh ChatList does not panic and returns a zero-length slice.
func TestChatList_Items_EmptyChatListReturnsNilOrEmpty(t *testing.T) {
	cl := newTestChatList()
	items := cl.Items()
	if len(items) != 0 {
		t.Errorf("Items() on empty ChatList = %d items, want 0", len(items))
	}
}
