package tui

// QUM-928 — additive ChatList accessors: OrphanCount and UncacheableCount.
//
// Both exist because of QUM-933's render-cache work, and the asymmetry between
// them is the whole point:
//
//   - OrphanCount counts INVARIANT VIOLATIONS. Contract: always 0. Scoped to
//     unfinished *AssistantTextItem other than the tail. Tool calls are
//     EXCLUDED, because a pending non-tail tool call is routine and legitimate
//     (parallel tool calls in one assistant message; async Agent rows pending
//     for minutes), so counting them would build a detector that always fires.
//   - UncacheableCount counts what the render cache cannot serve, which DOES
//     include a pending tool call at the tail.
//
// Uncacheable != invariant-violating. Pending tool calls are the first without
// being the second.

import (
	"fmt"
	"testing"
)

func TestChatList_OrphanCount_ZeroUnderEveryContentAppend(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*ChatList)
	}{
		{"AppendUser", func(cl *ChatList) { cl.AppendUser("u") }},
		{"AppendToolCall", func(cl *ChatList) { appendTestToolCall(cl, "t1") }},
		{"AppendSystemNotification", func(cl *ChatList) {
			cl.AppendSystemNotification(`<system-notification type="message">hi</system-notification>`)
		}},
		{"AppendAutoTrigger", func(cl *ChatList) { cl.AppendAutoTrigger() }},
		{"AppendCompactBanner", func(cl *ChatList) { cl.AppendCompactBanner("compacted") }},
		{"ZoneSettle", func(cl *ChatList) { cl.ZoneAddUser("u1", "hi"); cl.ZoneSettle("u1") }},
		{"AppendThinking then chunk", func(cl *ChatList) { cl.AppendThinking(); cl.AppendAssistantChunk("two") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := newTestChatList()
			cl.SetSize(80)
			cl.AppendAssistantChunk("body")
			tc.apply(cl)
			if got := cl.OrphanCount(); got != 0 {
				t.Errorf("OrphanCount() = %d after %s, want 0 (contract)", got, tc.name)
			}
		})
	}
}

// TestChatList_OrphanCount_ExcludesPendingToolCalls is the load-bearing
// distinction. A pending NON-TAIL tool call is normal — this is the shape that
// a naive "unfinished items excluding the tail" implementation would report as
// a violation, firing continuously on every parallel tool call and every async
// sidechain.
func TestChatList_OrphanCount_ExcludesPendingToolCalls(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("thinking about it")
	// Two parallel tool calls: t1 is pending and NOT the tail.
	appendTestToolCall(cl, "t1")
	appendTestToolCall(cl, "t2")

	if !cl.HasPendingToolCall() {
		t.Fatal("expected pending tool calls in this fixture")
	}
	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d with two pending tool calls, want 0 — a pending "+
			"non-tail tool call is legitimately in flight, not an invariant violation", got)
	}
	// The tail (t2) is unfinished, so it IS uncacheable.
	if got := cl.UncacheableCount(); got != 1 {
		t.Errorf("UncacheableCount() = %d, want 1 (pending tool call at the tail)", got)
	}

	// Async Agent rows pending for minutes, with content appended after them.
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "", "", nil, "")
	cl.AppendAssistantChunk("meanwhile")
	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d with a pending Agent row mid-list, want 0", got)
	}
}

// TestChatList_OrphanCount_DetectsSyntheticOrphan proves the detector is not
// vacuously zero. This is the ONLY test allowed to build the illegal state, and
// it does so directly because no public append path can produce it.
func TestChatList_OrphanCount_DetectsSyntheticOrphan(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("first")
	appendTestToolCall(cl, "t1")
	if got := cl.OrphanCount(); got != 0 {
		t.Fatalf("precondition: OrphanCount() = %d, want 0", got)
	}

	// Forcibly un-settle the non-tail assistant item — the exact state QUM-933
	// eliminated, reconstructed to prove the detector notices.
	cl.items[0].item.(*AssistantTextItem).finished = false
	if got := cl.OrphanCount(); got != 1 {
		t.Errorf("OrphanCount() = %d for a synthetic orphan, want 1 (detector is vacuous)", got)
	}
}

func TestChatList_OrphanCount_ZeroAfterReset(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	var entries []MessageEntry
	for i := 0; i < 6; i++ {
		entries = append(entries,
			MessageEntry{Type: MessageAssistant, Content: fmt.Sprintf("block %d", i), Complete: false},
			MessageEntry{Type: MessageUser, Content: fmt.Sprintf("follow %d", i)})
	}
	cl.Reset(entries)
	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d after an all-incomplete Reset, want 0", got)
	}
}

func TestChatList_UncacheableCount_TailStates(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		cl := newTestChatList()
		cl.SetSize(80)
		if got := cl.UncacheableCount(); got != 0 {
			t.Errorf("UncacheableCount() = %d, want 0", got)
		}
	})

	t.Run("streaming assistant tail", func(t *testing.T) {
		cl := newTestChatList()
		cl.SetSize(80)
		cl.AppendAssistantChunk("streaming")
		if got := cl.UncacheableCount(); got != 1 {
			t.Errorf("UncacheableCount() = %d, want 1", got)
		}
		cl.FinalizeAssistantMessage()
		if got := cl.UncacheableCount(); got != 0 {
			t.Errorf("after finalize UncacheableCount() = %d, want 0", got)
		}
	})

	t.Run("pending tool tail", func(t *testing.T) {
		cl := newTestChatList()
		cl.SetSize(80)
		appendTestToolCall(cl, "t1")
		if got := cl.UncacheableCount(); got != 1 {
			t.Errorf("UncacheableCount() = %d, want 1 (pending tool at tail IS uncacheable)", got)
		}
		cl.MarkToolResult("t1", "ok", false)
		if got := cl.UncacheableCount(); got != 0 {
			t.Errorf("after result UncacheableCount() = %d, want 0", got)
		}
	})

	// The anti-derived assertion. `pendingTools + (streamingAssistant ? 1 : 0)`
	// would return 1 in this state (pendingTools==0, streamingAssistant==true)
	// while the actual number of unfinished items is 0. Cross-reference
	// TestChatList_MidTurnSettleKeepsTurnBookkeeping, which pins that state.
	t.Run("mid-turn after tool result over-counts if derived", func(t *testing.T) {
		cl := newTestChatList()
		cl.SetSize(80)
		cl.AppendAssistantChunk("first block")
		appendTestToolCall(cl, "t1")
		cl.MarkToolResult("t1", "ok", false)

		if !cl.HasPendingAssistant() {
			t.Fatal("precondition: streamingAssistant must still be set mid-turn")
		}
		if cl.HasPendingToolCall() {
			t.Fatal("precondition: no pending tool calls")
		}
		if got := cl.UncacheableCount(); got != 0 {
			t.Errorf("UncacheableCount() = %d, want 0 — every item is finished here; "+
				"a flag-derived count would wrongly say 1", got)
		}
	})

	// Reset force-finalizes, which desyncs the flags from item state in the
	// other direction: a derived count would UNDER-report.
	t.Run("after Reset", func(t *testing.T) {
		cl := newTestChatList()
		cl.SetSize(80)
		cl.Reset([]MessageEntry{
			{Type: MessageAssistant, Content: "done", Complete: true},
		})
		if got := cl.UncacheableCount(); got != 0 {
			t.Errorf("UncacheableCount() = %d, want 0", got)
		}
	})

	t.Run("zone entry does not count", func(t *testing.T) {
		cl := newTestChatList()
		cl.SetSize(80)
		cl.AppendAssistantChunk("answer")
		cl.FinalizeAssistantMessage()
		cl.ZoneAddUser("u1", "queued")
		if got := cl.UncacheableCount(); got != 0 {
			t.Errorf("UncacheableCount() = %d, want 0 (zone items are always finished)", got)
		}
	})
}

// TestChatList_UncacheableCount_IsO1 pins the complexity contract: the value
// depends only on the tail, never on list length. Its O(1)-ness rests on
// settleTrailingAssistant's inductive invariant.
func TestChatList_UncacheableCount_IsO1(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	for i := 0; i < 2000; i++ {
		cl.AppendUser(fmt.Sprintf("msg %d", i))
	}
	if got := cl.UncacheableCount(); got != 0 {
		t.Errorf("UncacheableCount() = %d over 2000 finished items, want 0", got)
	}
	cl.AppendAssistantChunk("now streaming")
	if got := cl.UncacheableCount(); got != 1 {
		t.Errorf("UncacheableCount() = %d, want 1 (bounded by the tail, not the length)", got)
	}
	// Accessors must not render.
	before := cl.renderBuilds
	_, _ = cl.OrphanCount(), cl.UncacheableCount()
	if cl.renderBuilds != before {
		t.Errorf("accessors triggered a render (renderBuilds %d -> %d)", before, cl.renderBuilds)
	}
}
