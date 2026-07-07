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

// TestChatList_Reset_ForceFinalizesStreamingAssistant codifies the QUM-669
// wedge-exit invariant (chatlist-invariants.md §8): Reset of a transcript
// whose trailing entry is a Complete=false assistant must force-finalize it
// so the resync path leaves cl Idle. Without this, a resync from a wedged
// session would inherit a half-open assistant and the next user input would
// stall behind pendingTools accounting. Direct unit assertion preserved
// after S6 deleted the chatlist_s4_test.go suite that previously held it.
func TestChatList_Reset_ForceFinalizesStreamingAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageUser, Content: "hi", Complete: true},
		{Type: MessageAssistant, Content: "partial reply, no close fence", Complete: false},
	})
	if !cl.Idle() {
		t.Errorf("Reset with trailing Complete=false assistant must force-finalize and leave Idle (QUM-669 wedge-exit)")
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

// QUM-854: Empty() must reflect BOTH committed items and the pending zone —
// Len() (committed only) was the buggy emptiness gate that hid a fresh-session
// pending prompt until its CLI echo settled it into the transcript.
func TestChatList_Empty_TrueWhenNoItemsNoZone(t *testing.T) {
	cl := newTestChatList()
	if !cl.Empty() {
		t.Errorf("Empty() = false on a fresh ChatList; want true")
	}
}

func TestChatList_Empty_FalseWithPendingUserZone(t *testing.T) {
	cl := newTestChatList()
	cl.ZoneAddUser("u1", "pending hello")
	if cl.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 (zone entry is not a committed item)", cl.Len())
	}
	if cl.Empty() {
		t.Errorf("Empty() = true with a pending user zone entry; want false")
	}
}

func TestChatList_Empty_FalseWithPendingSystemZone(t *testing.T) {
	cl := newTestChatList()
	cl.ZoneAddSystem("n1", notifFrameA)
	if cl.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 (zone entry is not a committed item)", cl.Len())
	}
	if cl.Empty() {
		t.Errorf("Empty() = true with a pending system zone entry; want false")
	}
}

func TestChatList_Empty_FalseWithCommittedItem(t *testing.T) {
	cl := newTestChatList()
	cl.AppendUser("x")
	if cl.Empty() {
		t.Errorf("Empty() = true with a committed item; want false")
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
	// returns the cached string verbatim. QUM-769 added an outer Render
	// cache on top; drop it here so this test still pins the per-envelope
	// cache behavior in isolation.
	env := cl.items[0]
	env.cache.out = "POISONED"
	cl.renderCache = nil
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
	// QUM-677 S7 pivot: ThinkingItem no longer implements Expandable, and
	// would in any case be dropped by AppendToolCall/MarkToolResult. The
	// fan-out only fans across the actual Expandable items (ToolCallItem
	// here).
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCall(ToolCallSpec{Name: "Bash", ToolID: "t", Input: "ls", InputFull: "ls -la\n"})
	cl.MarkToolResult("t", "ok", false)
	collapsedAll := cl.Render(80)
	// Trigger global fan-out.
	cl.SetToolInputsExpanded(true)
	for _, env := range cl.items {
		if _, ok := env.item.(Expandable); ok && env.cache != nil {
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
	// Second call with identical state is a no-op.
	cl.SetToolInputsExpanded(true)
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

// TestChatList_AppendSystemNotificationDropsRawText pins the QUM-674 L3
// alignment: no envelope means no cl item. The legacy vp side still
// surfaces the text via AppendSystemMessage (a contract violator) which
// trips HasContractViolators and routes the chat region via vp.View(), so
// the user never loses the text — cl just doesn't duplicate-with-divergence.
func TestChatList_AppendSystemNotificationDropsRawText(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendSystemNotification("just a plain banner with no envelope")
	if cl.Len() != 0 {
		t.Errorf("L3: cl must drop raw-text notifications; got Len=%d", cl.Len())
	}
}

func TestChatList_AppendAutoTriggerAndThinking(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAutoTrigger()
	cl.AppendThinking()
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

// QUM-691: Inter-item separator behavior. The outer Render loop owns blank
// lines between items based on type transitions. Per-item Render returns
// content WITHOUT leading/trailing blanks; the loop inserts one blank line
// between items when the previous item's type differs from the current.
// No leading blank on the first item. Streaming assistant chunks remain
// contiguous (they mutate one in-flight item rather than appending).

// chatLinesNoTrailingEmpty splits Render output into logical lines and trims
// the single trailing empty element produced by the final "\n" terminator.
// That trailing element is structural (every item is followed by "\n") and
// not a blank visual line.
func chatLinesNoTrailingEmpty(rendered string) []string {
	lines := strings.Split(rendered, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// isBlankLine reports whether l contains no visible content after stripping
// ANSI escapes and surrounding whitespace.
func isBlankLine(l string) bool {
	return strings.TrimSpace(stripANSI(l)) == ""
}

func TestChatList_Separator_UserAfterAssistantHasBlank(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hello there")
	cl.FinalizeAssistantMessage()
	cl.AppendUser("follow up")
	lines := chatLinesNoTrailingEmpty(cl.Render(80))
	// Find the line containing the user chevron; the line immediately above
	// it must be blank.
	idx := -1
	for i, ln := range lines {
		if strings.Contains(stripANSI(ln), "follow up") {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("did not find user message in render:\n%s", cl.Render(80))
	}
	if idx == 0 {
		t.Fatalf("user message at index 0, expected an assistant block above")
	}
	if !isBlankLine(lines[idx-1]) {
		t.Errorf("expected blank line between assistant and user; got line %q above user line %q",
			lines[idx-1], lines[idx])
	}
}

func TestChatList_Separator_FirstItemNoLeadingBlank(t *testing.T) {
	cases := []struct {
		name string
		seed func(*ChatList)
	}{
		{"user-first", func(cl *ChatList) { cl.AppendUser("hi") }},
		{"assistant-first", func(cl *ChatList) {
			cl.AppendAssistantChunk("hello")
			cl.FinalizeAssistantMessage()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := newTestChatList()
			cl.SetSize(80)
			tc.seed(cl)
			out := cl.Render(80)
			lines := chatLinesNoTrailingEmpty(out)
			if len(lines) == 0 {
				t.Fatalf("empty render")
			}
			if isBlankLine(lines[0]) {
				t.Errorf("first item has unexpected leading blank line:\n%s", out)
			}
		})
	}
}

func TestChatList_Separator_LastAssistantNoTrailingBlank(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	cl.AppendAssistantChunk("hello there")
	cl.FinalizeAssistantMessage()
	out := cl.Render(80)
	lines := chatLinesNoTrailingEmpty(out)
	if len(lines) == 0 {
		t.Fatalf("empty render")
	}
	if isBlankLine(lines[len(lines)-1]) {
		t.Errorf("trailing blank line after last item:\n%s", out)
	}
}

func TestChatList_Separator_ConsecutiveSameTypeNoExtraBlank(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("first")
	cl.AppendUser("second")
	out := cl.Render(80)
	lines := chatLinesNoTrailingEmpty(out)
	blanks := 0
	for _, ln := range lines {
		if isBlankLine(ln) {
			blanks++
		}
	}
	if blanks != 0 {
		t.Errorf("consecutive same-type items produced %d blank lines, want 0:\n%s",
			blanks, out)
	}
}

func TestChatList_Separator_FullSequenceBlankPattern(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("u1")
	cl.AppendAssistantChunk("a1")
	cl.FinalizeAssistantMessage()
	cl.AppendUser("u2")
	cl.AppendAssistantChunk("a2")
	cl.FinalizeAssistantMessage()
	cl.AppendUser("u3")
	out := cl.Render(80)
	lines := chatLinesNoTrailingEmpty(out)

	// Collect (index, marker) of recognizable boundaries. Markers we look
	// for: "u1","u2","u3","a1","a2".
	type marker struct {
		idx   int
		label string
	}
	want := []string{"u1", "a1", "u2", "a2", "u3"}
	var found []marker
	for i, ln := range lines {
		clean := stripANSI(ln)
		for _, w := range want {
			if strings.Contains(clean, w) {
				found = append(found, marker{i, w})
			}
		}
	}
	if len(found) < len(want) {
		t.Fatalf("did not find all markers; found=%v\nrender:\n%s", found, out)
	}
	// Between every consecutive pair of distinct items (all 4 transitions
	// here are between different types), expect at least one blank line.
	for k := 1; k < len(found); k++ {
		prev := found[k-1]
		cur := found[k]
		blanks := 0
		for i := prev.idx + 1; i < cur.idx; i++ {
			if isBlankLine(lines[i]) {
				blanks++
			}
		}
		if blanks != 1 {
			t.Errorf("between %s and %s: want 1 blank line, got %d\nlines between:\n%q",
				prev.label, cur.label, blanks, lines[prev.idx+1:cur.idx])
		}
	}
}

func TestChatList_Separator_StreamingChunksContiguous(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first chunk ")
	cl.AppendAssistantChunk("second chunk ")
	cl.AppendAssistantChunk("third chunk")
	out := cl.Render(80)
	// Streaming chunks mutate a single in-flight item, so no inter-item
	// separator can apply. The rendered text must contain all three chunks
	// without an intervening blank line.
	clean := stripANSI(out)
	// Locate all three substrings — they must occur in order with no blank
	// line (two consecutive newlines) between any pair.
	idx1 := strings.Index(clean, "first chunk")
	idx2 := strings.Index(clean, "second chunk")
	idx3 := strings.Index(clean, "third chunk")
	if idx1 < 0 || idx2 < 0 || idx3 < 0 {
		t.Fatalf("missing streamed chunk in render:\n%s", out)
	}
	if strings.Contains(clean[idx1:idx3], "\n\n") {
		t.Errorf("streaming chunks introduced a mid-message blank line:\n%s", out)
	}
}

// QUM-677 S7 pivot tests for the count-marker behavior.

func TestChatList_AppendThinking_Coalesces(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendThinking()
	cl.AppendThinking()
	cl.AppendThinking()
	if cl.Len() != 1 {
		t.Fatalf("3 consecutive AppendThinking → Len=%d, want 1 (should coalesce)", cl.Len())
	}
	ti, ok := cl.items[0].item.(*ThinkingItem)
	if !ok {
		t.Fatalf("items[0] = %T, want *ThinkingItem", cl.items[0].item)
	}
	if ti.Count() != 3 {
		t.Errorf("Count() = %d, want 3", ti.Count())
	}
	out := cl.Render(80)
	if !strings.Contains(out, "(3 blocks)") {
		t.Errorf("render missing '(3 blocks)': %q", out)
	}
}

func TestChatList_AppendThinking_RemovedOnAssistantText(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendThinking()
	cl.AppendThinking()
	cl.AppendAssistantChunk("hi")
	if cl.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (thinking marker should be dropped)", cl.Len())
	}
	if _, ok := cl.items[0].item.(*AssistantTextItem); !ok {
		t.Errorf("items[0] = %T, want *AssistantTextItem", cl.items[0].item)
	}
}

func TestChatList_AppendThinking_RemovedOnToolCall(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendThinking()
	cl.AppendToolCall(ToolCallSpec{Name: "Bash", ToolID: "t1", Input: "ls"})
	if cl.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (thinking marker dropped on tool call)", cl.Len())
	}
	if _, ok := cl.items[0].item.(*ToolCallItem); !ok {
		t.Errorf("items[0] = %T, want *ToolCallItem", cl.items[0].item)
	}
}

func TestChatList_AppendThinking_Mixed_NoOrphan(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendThinking()
	cl.AppendToolCall(ToolCallSpec{Name: "Bash", ToolID: "t1", Input: "ls"})
	cl.AppendThinking()
	cl.AppendAssistantChunk("done")
	if cl.Len() != 2 {
		t.Fatalf("Len() = %d, want 2 (no orphan markers)", cl.Len())
	}
	if _, ok := cl.items[0].item.(*ToolCallItem); !ok {
		t.Errorf("items[0] = %T, want *ToolCallItem", cl.items[0].item)
	}
	if _, ok := cl.items[1].item.(*AssistantTextItem); !ok {
		t.Errorf("items[1] = %T, want *AssistantTextItem", cl.items[1].item)
	}
}

func TestChatList_NoThinking_NoMarker(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hello")
	for _, env := range cl.items {
		if _, ok := env.item.(*ThinkingItem); ok {
			t.Errorf("unexpected ThinkingItem in list with no thinking arrivals")
		}
	}
}

func TestChatList_PendingToolTickCmds_NoPendingReturnsNil(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	if cmd := cl.PendingToolTickCmds(); cmd != nil {
		t.Errorf("no pending tools should yield nil cmd, got non-nil")
	}
}

func TestChatList_PendingToolTickCmds_ArmsOncePerItem(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "t1", true, "ls", "ls", "ls", nil, "")
	cl.AppendToolCallWithHeader("Bash", "t2", true, "pwd", "pwd", "pwd", nil, "")
	first := cl.PendingToolTickCmds()
	if first == nil {
		t.Fatalf("expected non-nil tick cmd for 2 pending items")
	}
	// Second call returns nil — both items already ticking.
	if again := cl.PendingToolTickCmds(); again != nil {
		t.Errorf("PendingToolTickCmds must be idempotent for already-ticking items")
	}
}

func TestChatList_Update_RoutesToMatchingItem(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "t1", true, "ls", "ls", "ls", nil, "")
	cl.AppendToolCallWithHeader("Bash", "t2", true, "pwd", "pwd", "pwd", nil, "")
	before := cl.Render(80)
	cmd := cl.Update(toolTickMsg{ToolID: "t2"})
	if cmd == nil {
		t.Errorf("Update with matching ToolID should return follow-up cmd")
	}
	after := cl.Render(80)
	if before == after {
		t.Errorf("matching tick should change render")
	}
}

func TestChatList_Update_NoLeakAfterCompletion(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "t1", true, "ls", "ls", "ls", nil, "")
	cl.MarkToolResult("t1", "out", false)
	if cmd := cl.Update(toolTickMsg{ToolID: "t1"}); cmd != nil {
		t.Errorf("Update after MarkToolResult must return nil cmd (tick lifecycle dies)")
	}
}

func TestChatList_Update_UnknownIDReturnsNil(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	if cmd := cl.Update(toolTickMsg{ToolID: "ghost"}); cmd != nil {
		t.Errorf("Update for missing item should return nil")
	}
}

func TestChatList_ResetPendingToolTicking_AllowsReArm(t *testing.T) {
	// Models the observed-agent switch-back scenario: an item was previously
	// armed for ticking, its tick chain was orphaned (delivered elsewhere,
	// dead-ended), and PendingToolTickCmds on switch-back must be able to
	// arm a fresh tick. Without ResetPendingToolTicking the item would stay
	// frozen for the rest of its lifetime (QUM-732).
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Bash", "t1", true, "ls", "ls", "ls", nil, "")
	if cmd := cl.PendingToolTickCmds(); cmd == nil {
		t.Fatalf("initial arm should be non-nil")
	}
	// Simulate orphaned tick chain: items now have ticking=true.
	if cmd := cl.PendingToolTickCmds(); cmd != nil {
		t.Fatalf("second arm without reset should be nil (item already ticking)")
	}
	cl.ResetPendingToolTicking()
	if cmd := cl.PendingToolTickCmds(); cmd == nil {
		t.Errorf("after ResetPendingToolTicking, PendingToolTickCmds must re-arm pending items")
	}
}

func TestChatList_AppendThinking_DroppedOnNextUser(t *testing.T) {
	// Turn-boundary cleanup: any orphan trailing marker at the next user
	// turn must be dropped (symmetry with assistant/tool-call paths).
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendThinking()
	cl.AppendUser("next prompt")
	if cl.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (orphan marker dropped on AppendUser)", cl.Len())
	}
	if _, ok := cl.items[0].item.(*UserItem); !ok {
		t.Errorf("items[0] = %T, want *UserItem", cl.items[0].item)
	}
}
