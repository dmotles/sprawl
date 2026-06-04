package tui

// QUM-671 S1 — unit coverage for ChatList. Focus areas:
//   - width-0 guard (no render until SetSize)
//   - cache hit/miss per (width, expanded)
//   - Finished gating: streaming items bypass cache
//   - expand toggle invalidates Expandable caches and rebuilds
//   - peel-loop append for system notifications
//   - assistant-chunk mutate-or-append parity with viewport

import (
	"strings"
	"testing"
)

// QUM-673 S3 additions ------------------------------------------------------
// Idle / Reset / additional cache-invariant coverage for the View() switch.

func TestChatList_Idle_EmptyIsIdle(t *testing.T) {
	cl := newTestChatList()
	if !cl.Idle() {
		t.Fatalf("empty ChatList must be Idle")
	}
}

func TestChatList_Idle_StreamingAssistantNotIdle(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first chunk")
	if cl.Idle() {
		t.Fatalf("streaming assistant must not be Idle")
	}
	cl.FinalizeAssistantMessage()
	if !cl.Idle() {
		t.Errorf("finalized assistant must restore Idle")
	}
}

func TestChatList_Idle_PendingToolNotIdle(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "tu_1", true, "ls", "ls", "ls", nil, "")
	if cl.Idle() {
		t.Fatalf("pending tool call must not be Idle")
	}
	cl.MarkToolResult("tu_1", "out", false)
	if !cl.Idle() {
		t.Errorf("resolved tool result must restore Idle")
	}
}

func TestChatList_Reset_TranscriptRoundtrip(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageUser, Content: "hello", Complete: true},
		{Type: MessageAssistant, Content: "world", Complete: true},
		{
			Type: MessageToolCall, Content: "Bash", Complete: true,
			ToolID: "tu_42", ToolInput: "ls", Pending: false, Result: "done",
		},
		{Type: MessageAutoTrigger, Content: "auto", Complete: true},
	})
	if !cl.Idle() {
		t.Errorf("Reset of fully-finished transcript must leave Idle")
	}
	if got := cl.Len(); got != 4 {
		t.Errorf("Reset Len() = %d, want 4", got)
	}
	out := cl.Render(80)
	if !strings.Contains(out, "hello") {
		t.Errorf("Reset output missing user text:\n%s", out)
	}
	if !strings.Contains(out, "world") {
		t.Errorf("Reset output missing assistant text:\n%s", out)
	}
	if !strings.Contains(out, "Bash") {
		t.Errorf("Reset output missing tool name:\n%s", out)
	}
}

func TestChatList_Reset_PendingToolKeepsBufferBusy(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageToolCall, Content: "Bash", ToolID: "t1", Pending: true, Complete: true},
	})
	if cl.Idle() {
		t.Errorf("Reset with pending tool must not be Idle")
	}
}

func TestChatList_Reset_SkipsContractViolators(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageStatus, Content: "transient", Complete: true},
		{Type: MessageBanner, Content: "BANNER", Complete: true},
		{Type: MessageError, Content: "oops", Complete: true},
		{Type: MessageUser, Content: "real", Complete: true},
	})
	if got := cl.Len(); got != 1 {
		t.Errorf("Reset Len() = %d, want 1 (status/banner/error skipped)", got)
	}
}

func newTestChatList() *ChatList {
	theme := NewTheme("")
	return NewChatList(&theme)
}

func TestChatList_RenderNoOpUntilSetSize(t *testing.T) {
	cl := newTestChatList()
	cl.AppendUser("hello")
	if got := cl.Render(80); got != "" {
		t.Fatalf("Render before SetSize returned %q, want empty (width-0 guard)", got)
	}
	cl.SetSize(80)
	if got := cl.Render(80); got == "" {
		t.Errorf("Render after SetSize(80) was empty; expected user item output")
	}
}

func TestChatList_SetSizeZeroResetsGuard(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	if cl.Render(80) == "" {
		t.Fatalf("baseline render unexpectedly empty")
	}
	cl.SetSize(0)
	if got := cl.Render(80); got != "" {
		t.Errorf("SetSize(0) did not re-engage width-0 guard; got %q", got)
	}
}

func TestChatList_LenTracksAppends(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	if cl.Len() != 0 {
		t.Fatalf("Len() = %d at startup, want 0", cl.Len())
	}
	cl.AppendUser("a")
	cl.AppendUser("b")
	if cl.Len() != 2 {
		t.Errorf("Len() = %d after two appends, want 2", cl.Len())
	}
}

func TestChatList_CacheHitOnSecondRenderSameWidth(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hello world")
	// Cold render populates the cache.
	first := cl.Render(80)
	if first == "" {
		t.Fatalf("first render empty")
	}
	// Sentinel: forcibly poison the underlying item so a cache miss would
	// produce different output. If the cache is honored, the second render
	// returns the cached string verbatim.
	env := cl.items[0]
	env.cache.out = "POISONED"
	second := cl.Render(80)
	// Render writes a trailing "\n" after each item to match the legacy
	// viewport.renderMessages convention; the cached envelope output is
	// "POISONED" and the walk appends "\n".
	if second != "POISONED\n" {
		t.Errorf("second render did not honor cache; got %q want %q", second, "POISONED\n")
	}
}

func TestChatList_CacheMissOnWidthChange(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hello")
	cl.Render(80)
	env := cl.items[0]
	env.cache.out = "STALE-80"
	cl.SetSize(40)
	got := cl.Render(40)
	if got == "STALE-80" {
		t.Errorf("width change did not invalidate cache; still got %q", got)
	}
}

func TestChatList_StreamingItemBypassesCache(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hel")
	first := cl.Render(80)
	if cl.items[0].cache != nil {
		t.Fatalf("in-flight assistant item should not be cached; cache=%v", cl.items[0].cache)
	}
	cl.AppendAssistantChunk("lo")
	second := cl.Render(80)
	if first == second {
		t.Errorf("streaming render did not update after chunk append")
	}
	cl.FinalizeAssistantMessage()
	cl.Render(80)
	if cl.items[0].cache == nil {
		t.Errorf("finalized assistant item should cache on next render")
	}
}

func TestChatList_AppendAssistantChunkMutatesTrailing(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hel")
	cl.AppendAssistantChunk("lo")
	if cl.Len() != 1 {
		t.Errorf("expected one assistant item after two chunks, got %d", cl.Len())
	}
	a := cl.items[0].item.(*AssistantTextItem)
	if a.Text() != "hello" {
		t.Errorf("Text() = %q, want %q", a.Text(), "hello")
	}
	cl.FinalizeAssistantMessage()
	// A second chunk after finalize must start a new item, not mutate the
	// finished one (matches viewport.AppendAssistantChunk semantics).
	cl.AppendAssistantChunk("next")
	if cl.Len() != 2 {
		t.Errorf("expected new assistant item after finalize+chunk, got Len=%d", cl.Len())
	}
}

func TestChatList_ToolCallMarkResult(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCall(ToolCallSpec{Name: "Bash", ToolID: "t1", HeaderArg: "ls"})
	cl.Render(80)
	if cl.items[0].cache != nil {
		t.Fatalf("pending tool call should not be cached")
	}
	if !cl.MarkToolResult("t1", "ok", false) {
		t.Fatalf("MarkToolResult returned false; expected match for t1")
	}
	cl.Render(80)
	if cl.items[0].cache == nil {
		t.Errorf("finished tool call should cache on next render")
	}
	if cl.MarkToolResult("unknown", "x", false) {
		t.Errorf("MarkToolResult should return false for unknown toolID")
	}
}

func TestChatList_SetToolInputsExpandedFanout(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCall(ToolCallSpec{Name: "Bash", ToolID: "t", Input: "ls", InputFull: "ls -la\n"})
	cl.AppendThinking("private reasoning here")
	cl.MarkToolResult("t", "ok", false)
	collapsedAll := cl.Render(80)
	// Trigger global fan-out.
	cl.SetToolInputsExpanded(true)
	for _, env := range cl.items {
		if env.cache != nil {
			t.Errorf("SetToolInputsExpanded did not invalidate cache for %T", env.item)
		}
	}
	expandedAll := cl.Render(80)
	if expandedAll == collapsedAll {
		t.Errorf("global expand toggle did not change render output")
	}
	if !strings.Contains(expandedAll, "ls -la") {
		t.Errorf("expanded render missing tool InputFull content: %q", expandedAll)
	}
	if !strings.Contains(expandedAll, "private reasoning here") {
		t.Errorf("expanded render missing thinking body: %q", expandedAll)
	}
	// Second call with identical state is a no-op.
	cl.SetToolInputsExpanded(true)
	// And re-render must serve from cache (Finished items only).
	again := cl.Render(80)
	if again != expandedAll {
		t.Errorf("re-render after no-op SetToolInputsExpanded changed output")
	}
}

func TestChatList_AppendSystemNotificationPeelsMultiple(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	body := `<system-notification type="status_change">a → working</system-notification>` +
		`<system-notification type="message" interrupt="true">heads up</system-notification>`
	cl.AppendSystemNotification(body)
	if cl.Len() != 2 {
		t.Fatalf("expected 2 peeled notifications, got Len=%d", cl.Len())
	}
	first, ok := cl.items[0].item.(*SystemNotificationItem)
	if !ok {
		t.Fatalf("first item not a SystemNotificationItem: %T", cl.items[0].item)
	}
	if first.notificationType != NotificationKindStatusChange {
		t.Errorf("first notif type = %q, want status_change", first.notificationType)
	}
	second := cl.items[1].item.(*SystemNotificationItem)
	if !second.interrupt {
		t.Errorf("second notification should be flagged as interrupt")
	}
}

func TestChatList_AppendSystemNotificationFallbackForRawText(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendSystemNotification("just a plain banner with no envelope")
	if cl.Len() != 1 {
		t.Fatalf("expected fallback to surface raw text as 1 item, got Len=%d", cl.Len())
	}
	item := cl.items[0].item.(*SystemNotificationItem)
	if item.notificationType != NotificationKindMessage {
		t.Errorf("fallback should default to message kind, got %q", item.notificationType)
	}
	if item.content == "" {
		t.Errorf("fallback should preserve the input text")
	}
}

func TestChatList_AppendAutoTriggerAndThinking(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAutoTrigger("inbox: 1 new")
	cl.AppendThinking("ruminating")
	if cl.Len() != 2 {
		t.Fatalf("expected 2 items, got %d", cl.Len())
	}
	out := cl.Render(80)
	if !strings.Contains(out, "auto-continued") {
		t.Errorf("auto-trigger marker missing from render: %q", out)
	}
	if !strings.Contains(out, "thinking") {
		t.Errorf("thinking marker missing from render: %q", out)
	}
}

func TestChatList_ThinkingInheritsGlobalExpandedAtAppend(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.SetToolInputsExpanded(true)
	cl.AppendThinking("global expand was active")
	t1 := cl.items[0].item.(*ThinkingItem)
	if !t1.IsExpanded() {
		t.Errorf("appended ThinkingItem did not inherit global expanded state")
	}
}
