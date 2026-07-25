package tui

// QUM-933 — intermediate assistant text items must settle (Finalize) as soon
// as a non-assistant item is appended after them, not only at turn end.
//
// Before this fix, FinalizeAssistantMessage only ever touched items[n-1] and
// was only called from finalizeTurn / SessionResultMsg, so every text block
// followed by a tool call stayed Finished()==false forever. Consequences:
//   - renderEnvelope refuses to cache unfinished items, so each orphan
//     re-ran the full goldmark+glamour pipeline on EVERY frame (pegged CPU).
//   - AssistantTextItem.Render appends the ▍ streaming cursor when !finished,
//     so every orphan showed a stray cursor on a long-settled message.
//
// The fix is a single settle chokepoint shared by every append path that
// pushes a non-assistant item. It must NOT settle for pending-zone adds, for
// the thinking marker, or between chunks of one streaming block, and must not
// clear the turn-scoped streamingAssistant flag (app.go gates its
// duplicate-final-bubble guard on it).
//
// The perf anchor is TestChatList_NoGlamourReRenderForSettledAssistants,
// which counts actual glamour invocations via MarkdownRenderer.RenderCalls.

import (
	"fmt"
	"strings"
	"testing"
)

// appendTestToolCall appends a plain top-level pending tool call.
func appendTestToolCall(cl *ChatList, toolID string) {
	cl.AppendToolCallWithHeader("Read", toolID, true, "{}", "{}", "f", nil, "")
}

// assistantAt returns items[i] as an *AssistantTextItem, failing the test
// (rather than panicking) if it is some other item kind.
func assistantAt(t *testing.T, cl *ChatList, i int) *AssistantTextItem {
	t.Helper()
	if i >= len(cl.items) {
		t.Fatalf("items[%d] out of range: Len() = %d", i, cl.Len())
	}
	a, ok := cl.items[i].item.(*AssistantTextItem)
	if !ok {
		t.Fatalf("items[%d] = %T, want *AssistantTextItem", i, cl.items[i].item)
	}
	return a
}

// assertNoOrphans pins the QUM-933 invariant as stated in the issue: no
// assistant text item anywhere in the list is unfinished except possibly the
// trailing one, which may be genuinely in flight.
func assertNoOrphans(t *testing.T, cl *ChatList) {
	t.Helper()
	for i := 0; i < len(cl.items)-1; i++ {
		if a, ok := cl.items[i].item.(*AssistantTextItem); ok && !a.Finished() {
			t.Errorf("orphan: items[%d] assistant %q is unfinished but not the tail", i, a.Text())
		}
	}
}

func cursorCount(cl *ChatList, width int) int {
	return strings.Count(stripANSI(cl.Render(width)), itemsStreamingCursor)
}

func TestChatList_ToolCallSettlesTrailingAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first block")
	appendTestToolCall(cl, "t1")

	if got := cl.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	a := assistantAt(t, cl, 0)
	if !a.Finished() {
		t.Errorf("assistant item Finished() = false, want true (settled by the tool-call append)")
	}
	if got := a.Text(); got != "first block" {
		t.Errorf("settling mutated content: Text() = %q, want %q", got, "first block")
	}
	if _, ok := cl.items[1].item.(*ToolCallItem); !ok {
		t.Errorf("items[1] = %T, want *ToolCallItem", cl.items[1].item)
	}
}

// TestChatList_TextToolTextSequence covers the canonical streaming shape end
// to end: the first block settles, the tool row lands between them, and the
// second block stays live.
func TestChatList_TextToolTextSequence(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("one")
	appendTestToolCall(cl, "t1")
	cl.AppendAssistantChunk("two")

	if got := cl.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3 (assistant, tool, assistant)", got)
	}
	first, second := assistantAt(t, cl, 0), assistantAt(t, cl, 2)
	if _, ok := cl.items[1].item.(*ToolCallItem); !ok {
		t.Errorf("items[1] = %T, want *ToolCallItem", cl.items[1].item)
	}
	if !first.Finished() {
		t.Errorf("first block Finished() = false, want true")
	}
	if got := first.Text(); got != "one" {
		t.Errorf("first block Text() = %q, want %q", got, "one")
	}
	if second.Finished() {
		t.Errorf("second block Finished() = true, want false (still in flight)")
	}
	if got := second.Text(); got != "two" {
		t.Errorf("second block Text() = %q, want %q", got, "two")
	}
	if got := cursorCount(cl, 80); got != 1 {
		t.Errorf("cursor count = %d, want exactly 1 (only the live tail)", got)
	}
	out := stripANSI(cl.Render(80))
	if i, j := strings.Index(out, "one"), strings.Index(out, "two"); i < 0 || j < 0 || i > j {
		t.Errorf("transcript order wrong: %q at %d, %q at %d", "one", i, "two", j)
	}
	assertNoOrphans(t, cl)
}

// TestChatList_SettleIsIdempotent pins that a second settle against an
// already-finished trailing assistant is a harmless no-op — back-to-back tool
// calls must not clobber the settled block's content.
func TestChatList_SettleIsIdempotent(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("one")
	appendTestToolCall(cl, "t1")
	appendTestToolCall(cl, "t2")

	if got := cl.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}
	a := assistantAt(t, cl, 0)
	if !a.Finished() {
		t.Errorf("Finished() = false, want true")
	}
	if got := a.Text(); got != "one" {
		t.Errorf("double settle mutated content: Text() = %q, want %q", got, "one")
	}
}

func TestChatList_SettledAssistantBecomesCacheable(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first block")
	appendTestToolCall(cl, "t1")
	cl.Render(80)

	if cl.items[0].cache == nil {
		t.Fatalf("settled assistant envelope has no render cache; it would re-render every frame")
	}
	if got := cl.items[0].cache.width; got != 80 {
		t.Errorf("cache.width = %d, want 80", got)
	}
	if cl.items[0].cache.out == "" {
		t.Errorf("cache.out is empty; an empty cache entry is not a real hit")
	}
}

func TestChatList_SettledAssistantHasNoStreamingCursor(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first block")
	// Positive control: an in-flight block DOES carry the cursor.
	if got := cursorCount(cl, 80); got != 1 {
		t.Fatalf("in-flight cursor count = %d, want 1", got)
	}

	appendTestToolCall(cl, "t1")
	if got := cursorCount(cl, 80); got != 0 {
		t.Errorf("settled assistant still renders %d stray %q cursors, want 0", got, itemsStreamingCursor)
	}
}

// TestChatList_ConsecutiveChunksStillCoalesce guards the main regression risk:
// chunks of the SAME streaming block must merge into one unfinished item.
func TestChatList_ConsecutiveChunksStillCoalesce(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("hel")
	cl.AppendAssistantChunk("lo")

	if got := cl.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1 (chunks must coalesce, not explode into items)", got)
	}
	a := assistantAt(t, cl, 0)
	if got := a.Text(); got != "hello" {
		t.Errorf("Text() = %q, want %q", got, "hello")
	}
	if a.Finished() {
		t.Errorf("mid-stream block Finished() = true, want false (settled between chunks of one block)")
	}
	cl.Render(80)
	if cl.items[0].cache != nil {
		t.Errorf("in-flight item was cached; streaming chunks would stop repainting")
	}
	if !cl.HasPendingAssistant() {
		t.Errorf("HasPendingAssistant() = false mid-stream, want true")
	}
	if cl.Idle() {
		t.Errorf("Idle() = true mid-stream, want false")
	}
	if got := cursorCount(cl, 80); got != 1 {
		t.Errorf("in-flight cursor count = %d, want exactly 1", got)
	}
}

// TestChatList_NoGlamourReRenderForSettledAssistants is the perf assertion:
// with N settled orphans plus ONE genuinely in-flight block, each frame must
// invoke glamour exactly once — for the live block only.
//
// The live tail is load-bearing: it keeps Idle() false by construction, so the
// outer Render cache is bypassed and the count measures the per-envelope cache
// rather than accidentally passing because streamingAssistant got cleared.
// renderBuilds is expected to grow here and must not be asserted.
func TestChatList_NoGlamourReRenderForSettledAssistants(t *testing.T) {
	const orphans = 5
	cl := newTestChatList()
	cl.SetSize(80)
	for i := 0; i < orphans; i++ {
		cl.AppendAssistantChunk(fmt.Sprintf("## block %d\n\nbody with `code` and a\n\n- bullet\n", i))
		toolID := fmt.Sprintf("t%d", i)
		appendTestToolCall(cl, toolID)
		cl.MarkToolResult(toolID, "ok", false)
	}
	cl.AppendAssistantChunk("still streaming")

	cl.Render(80) // warm the per-envelope caches
	cl.ctx.renderer.ResetRenderCalls()
	const frames = 10
	for j := 0; j < frames; j++ {
		cl.Render(80)
	}
	if got := cl.ctx.renderer.RenderCalls(); got != frames {
		t.Errorf("steady-state glamour calls = %d over %d frames, want %d (one live block only)",
			got, frames, frames)
	}
	if got := cursorCount(cl, 80); got != 1 {
		t.Errorf("cursor count = %d with %d settled orphans, want exactly 1", got, orphans)
	}
	for i := 0; i < len(cl.items)-1; i++ {
		if _, ok := cl.items[i].item.(*AssistantTextItem); ok && cl.items[i].cache == nil {
			t.Errorf("settled assistant items[%d] has no render cache", i)
		}
	}
	assertNoOrphans(t, cl)
}

// TestChatList_MidTurnSettleKeepsTurnBookkeeping pins that settling an
// intermediate item does NOT clear the turn-scoped streamingAssistant flag —
// app.go's SessionResultMsg guard keys on it to avoid a duplicate final bubble.
func TestChatList_MidTurnSettleKeepsTurnBookkeeping(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first block")
	appendTestToolCall(cl, "t1")

	if !cl.HasPendingAssistant() {
		t.Errorf("HasPendingAssistant() = false after mid-turn settle, want true (turn still live)")
	}
	if cl.Idle() {
		t.Errorf("Idle() = true with a pending tool call, want false")
	}
	if !cl.MarkToolResult("t1", "ok", false) {
		t.Fatalf("MarkToolResult found no matching tool call")
	}
	if !cl.HasPendingAssistant() {
		t.Errorf("HasPendingAssistant() = false after tool result, want true (turn not finalized)")
	}
	if cl.HasPendingToolCall() {
		t.Errorf("HasPendingToolCall() = true after MarkToolResult, want false")
	}
	if cl.Idle() {
		t.Errorf("Idle() = true before turn finalize, want false")
	}

	cl.FinalizeAssistantMessage()
	if !cl.Idle() {
		t.Errorf("Idle() = false after finalize, want true (stuck spinner)")
	}
	if cl.HasPendingAssistant() {
		t.Errorf("HasPendingAssistant() = true after finalize, want false")
	}
	if got := assistantAt(t, cl, 0).Text(); got != "first block" {
		t.Errorf("Text() = %q, want %q", got, "first block")
	}
}

// TestChatList_ZoneAddDoesNotSettleTrailingAssistant pins the WRONG-to-settle
// guard: pending-zone adds live in a separate slice (QUM-833) and must never
// disturb the in-flight assistant block — settling there would split the
// model's message at an arbitrary keystroke.
func TestChatList_ZoneAddDoesNotSettleTrailingAssistant(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*ChatList)
	}{
		{"ZoneAddUser", func(cl *ChatList) { cl.ZoneAddUser("u1", "hi") }},
		{"ZoneAddUserWithAttachments", func(cl *ChatList) { cl.ZoneAddUserWithAttachments("u1", "hi", nil) }},
		{"ZoneAddSystem", func(cl *ChatList) {
			cl.ZoneAddSystem("u1", `<system-notification type="message">hi</system-notification>`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := newTestChatList()
			cl.SetSize(80)
			cl.AppendAssistantChunk("answer")
			tc.apply(cl)

			if got := cl.Len(); got != 1 {
				t.Fatalf("Len() = %d, want 1 (zone adds do not touch items)", got)
			}
			if assistantAt(t, cl, 0).Finished() {
				t.Errorf("%s settled the in-flight assistant; it would split the model's message", tc.name)
			}
		})
	}
}

func TestChatList_ZoneSettleSettlesTrailingAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("answer")
	cl.ZoneAddUser("u1", "hi")

	if !cl.ZoneSettle("u1") {
		t.Fatalf("ZoneSettle(u1) = false, want true")
	}
	if got := cl.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if !assistantAt(t, cl, 0).Finished() {
		t.Errorf("ZoneSettle did not settle the trailing assistant")
	}
	u, ok := cl.items[1].item.(*UserItem)
	if !ok {
		t.Fatalf("items[1] = %T, want *UserItem", cl.items[1].item)
	}
	if got := u.Text(); got != "hi" {
		t.Errorf("relocated user Text() = %q, want %q", got, "hi")
	}
}

// TestChatList_ThinkingDoesNotSettleTrailingAssistant pins the other
// WRONG-to-settle site: the thinking marker is transient and is dropped by the
// next content append, so settling under it would kill the live cursor while
// the model is still working on that same block.
func TestChatList_ThinkingDoesNotSettleTrailingAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("one")
	cl.AppendThinking()

	if got := cl.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2 (assistant + thinking marker)", got)
	}
	if assistantAt(t, cl, 0).Finished() {
		t.Errorf("AppendThinking settled the trailing assistant; the live cursor would vanish")
	}
	if got := cursorCount(cl, 80); got != 1 {
		t.Errorf("cursor count = %d under the thinking marker, want 1", got)
	}
}

// TestChatList_ThinkingBetweenTextBlocksLeavesNoOrphan covers the second
// orphan source: the thinking marker is dropped, re-exposing the unfinished
// assistant at the tail, and then a NEW assistant item is appended over it.
func TestChatList_ThinkingBetweenTextBlocksLeavesNoOrphan(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("one")
	cl.AppendThinking()
	cl.AppendAssistantChunk("two")

	if got := cl.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2 (marker dropped, two assistant blocks)", got)
	}
	if !assistantAt(t, cl, 0).Finished() {
		t.Errorf("first block Finished() = false, want true (orphaned by the thinking marker)")
	}
	if assistantAt(t, cl, 1).Finished() {
		t.Errorf("second block Finished() = true, want false (still in flight)")
	}
	if got := cursorCount(cl, 80); got != 1 {
		t.Errorf("cursor count = %d, want exactly 1", got)
	}
	assertNoOrphans(t, cl)
}

// TestChatList_EveryContentAppendSettlesTrailingAssistant is the chokepoint
// guard: a future append path that forgets the settle fails here rather than
// silently reintroducing the orphan leak. It also asserts every path leaves
// streamingAssistant set — a path wired to FinalizeAssistantMessage instead of
// the settle helper would clear it and resurrect the duplicate-bubble bug.
func TestChatList_EveryContentAppendSettlesTrailingAssistant(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*ChatList)
	}{
		{"AppendUser", func(cl *ChatList) { cl.AppendUser("u") }},
		{"AppendUserWithAttachments", func(cl *ChatList) { cl.AppendUserWithAttachments("u", nil) }},
		{"AppendToolCall", func(cl *ChatList) { appendTestToolCall(cl, "t1") }},
		{"AppendSystemNotification", func(cl *ChatList) {
			cl.AppendSystemNotification(`<system-notification type="message">hi</system-notification>`)
		}},
		{"AppendAutoTrigger", func(cl *ChatList) { cl.AppendAutoTrigger() }},
		{"AppendCompactBanner", func(cl *ChatList) { cl.AppendCompactBanner("compacted") }},
		{"ZoneSettle", func(cl *ChatList) {
			cl.ZoneAddUser("u1", "hi")
			cl.ZoneSettle("u1")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := newTestChatList()
			cl.SetSize(80)
			cl.AppendAssistantChunk("body")
			tc.apply(cl)

			if got := cl.Len(); got < 2 {
				t.Fatalf("%s appended nothing: Len() = %d, want >= 2", tc.name, got)
			}
			if !assistantAt(t, cl, 0).Finished() {
				t.Errorf("%s did not settle the trailing assistant item", tc.name)
			}
			if !cl.HasPendingAssistant() {
				t.Errorf("%s cleared streamingAssistant; the turn's final text would double-render", tc.name)
			}
			if got := cursorCount(cl, 80); got != 0 {
				t.Errorf("%s left %d stray %q cursors, want 0", tc.name, got, itemsStreamingCursor)
			}
		})
	}
}

// TestChatList_ResetSystemNotificationSettlesIncompleteAssistant pins the one
// direct c.items append inside Reset, which bypasses the Append* verbs.
func TestChatList_ResetSystemNotificationSettlesIncompleteAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageAssistant, Content: "partial", Complete: false},
		{Type: MessageSystemNotification, Content: "note", NotificationType: NotificationKindMessage},
	})

	if !assistantAt(t, cl, 0).Finished() {
		t.Errorf("replayed incomplete assistant followed by a notification was not settled")
	}
	if !cl.Idle() {
		t.Errorf("Idle() = false after Reset, want true")
	}
}

// TestChatList_ResetAlternatingIncompleteAssistantsHasNoOrphans pins the
// worst-case rehydrate fixture: entries where EVERY assistant is Complete:false.
// Reset finalizes only `if e.Complete`, and its trailing
// FinalizeAssistantMessage clears streamingAssistant GLOBALLY while finalizing
// only items[n-1] — so before the settle chokepoint this stranded one unfinished
// assistant per pair with no flag left recording that they existed. Their cost
// is invisible at rest (Idle() is true, the outer cache serves a memoized
// string) and only materializes on the next rebuild, which is exactly what makes
// this class of leak silent. The per-append settle closes it inductively: at
// most one unfinished assistant can exist, and it is always the tail.
func TestChatList_ResetAlternatingIncompleteAssistantsHasNoOrphans(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	var entries []MessageEntry
	for i := 0; i < 6; i++ {
		entries = append(entries,
			MessageEntry{Type: MessageAssistant, Content: fmt.Sprintf("## block %d\n\nbody", i), Complete: false},
			MessageEntry{Type: MessageUser, Content: fmt.Sprintf("follow-up %d", i)},
		)
	}
	cl.Reset(entries)

	for i := range cl.items {
		if !cl.items[i].item.Finished() {
			t.Errorf("replayed items[%d] (%T) is unfinished — stranded orphan", i, cl.items[i].item)
		}
	}
	if got := cursorCount(cl, 80); got != 0 {
		t.Errorf("replayed transcript renders %d stray %q cursors, want 0", got, itemsStreamingCursor)
	}

	// The discriminating check: force a rebuild (as a spinner tick would) and
	// assert glamour is not re-invoked. A steady-state-only assertion would pass
	// even with the orphans present, because at rest nothing rebuilds.
	cl.Render(80)
	cl.ctx.renderer.ResetRenderCalls()
	const frames = 5
	for j := 0; j < frames; j++ {
		cl.invalidate()
		cl.Render(80)
	}
	if got := cl.ctx.renderer.RenderCalls(); got != 0 {
		t.Errorf("rebuild after Reset invoked glamour %d times over %d frames, want 0", got, frames)
	}
}

// TestChatList_ResetFullTranscriptHasNoOrphans pins the rehydrate AC: a
// replayed transcript is fully settled, fully cacheable, cursor-free and idle.
func TestChatList_ResetFullTranscriptHasNoOrphans(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageUser, Content: "do the thing"},
		{Type: MessageAssistant, Content: "## first\n\nbody", Complete: true},
		{Type: MessageToolCall, Content: "Read", ToolID: "t1", Approved: true, Result: "ok"},
		{Type: MessageAssistant, Content: "## second\n\nbody", Complete: true},
	})

	cl.Render(80)
	for i := range cl.items {
		if !cl.items[i].item.Finished() {
			t.Errorf("replayed items[%d] (%T) Finished() = false, want true", i, cl.items[i].item)
		}
		if cl.items[i].cache == nil {
			t.Errorf("replayed items[%d] (%T) has no render cache", i, cl.items[i].item)
		}
	}
	if got := cursorCount(cl, 80); got != 0 {
		t.Errorf("replayed transcript renders %d stray %q cursors, want 0", got, itemsStreamingCursor)
	}
	if !cl.Idle() {
		t.Errorf("Idle() = false after full-transcript Reset, want true")
	}
}

// TestChatList_FinalizeAfterThinkingLeavesNoOrphan closes the one residual hole
// left by the settle chokepoint (QUM-975).
//
// AppendThinking is the single c.items append that deliberately bypasses
// beginContentAppend, on the argument that the marker is transient: the next
// CONTENT append drops it and then settles, re-exposing the assistant. That
// argument holds for content appends — but FinalizeAssistantMessage is not a
// content append and did not drop the marker, so settleTrailingAssistant saw a
// *ThinkingItem, no-oped, and the flag was cleared anyway:
//
//	AppendAssistantChunk("body")   // [assistant(unfinished)]
//	AppendThinking()               // [assistant(unfinished), thinking]
//	FinalizeAssistantMessage()     // settle sees *ThinkingItem -> no-op
//
// Idle() is then TRUE with a permanently unfinished non-tail item: the turn is
// over, a stray ▍ renders in a settled transcript, and the item stays
// uncacheable until some later content append happens to heal it — unbounded
// across an idle gap, and forever if the session ends there.
//
// Reachable through all three terminal handlers via finalizeTurn, most plainly
// by pressing Esc during a thinking block that follows streamed text. The
// !HasPendingAssistant() gate makes the bad case exactly the one that occurs:
// text streamed means the flag is set, so no result text is appended and
// nothing heals it.
//
// Note UncacheableCount() is 0 here (the tail is the finished marker) — only the
// OrphanCount walk detects this, which is why the two accessors are not
// redundant.
func TestChatList_FinalizeAfterThinkingLeavesNoOrphan(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("body")
	cl.AppendThinking()
	cl.FinalizeAssistantMessage()

	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d after finalize-over-thinking, want 0 (stranded item)", got)
	}
	a := assistantAt(t, cl, 0)
	if !a.Finished() {
		t.Errorf("assistant item Finished() = false; the turn is over, it must be settled")
	}
	if got := a.Text(); got != "body" {
		t.Errorf("Text() = %q, want %q (content must be preserved)", got, "body")
	}
	if got := cursorCount(cl, 80); got != 0 {
		t.Errorf("settled transcript renders %d stray %q cursors, want 0", got, itemsStreamingCursor)
	}
	// The transient marker's turn is over, so it is discarded rather than kept.
	if got := cl.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 (the transient thinking marker is dropped)", got)
	}

	// Bookkeeping: finalize legitimately clears streamingAssistant (unlike the
	// settle helper), and that must still hold on this path.
	if cl.HasPendingAssistant() {
		t.Errorf("HasPendingAssistant() = true after finalize, want false")
	}
	if !cl.Idle() {
		t.Errorf("Idle() = false after finalize, want true")
	}
}

// TestChatList_FinalizeAfterThinkingWithNoAssistant guards the adjacent case: a
// thinking marker with no preceding assistant text must not crash or strand,
// and finalize must still clear the flag.
func TestChatList_FinalizeAfterThinkingWithNoAssistant(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendUser("hi")
	cl.AppendThinking()
	cl.FinalizeAssistantMessage()

	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d, want 0", got)
	}
	if !cl.Idle() {
		t.Errorf("Idle() = false, want true")
	}
	if got := cursorCount(cl, 80); got != 0 {
		t.Errorf("%d stray cursors, want 0", got)
	}
}
