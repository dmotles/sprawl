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
//     include pending tool calls. It is an O(n) count over items + zone with no
//     positional invariant, so every orphan OrphanCount reports is necessarily
//     counted here too: UncacheableCount >= OrphanCount, always.
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
	// Both t1 and t2 are unfinished, so BOTH are uncacheable — the asymmetry is
	// about which states are legal, not about how many get counted.
	if got := cl.UncacheableCount(); got != 2 {
		t.Errorf("UncacheableCount() = %d, want 2 — both pending tool calls are "+
			"uncacheable; a tail-only probe counts just t2", got)
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

// TestChatList_UncacheableCount_SettleTransitions covers what the table test
// cannot: the TRANSITIONS that flip an envelope from uncacheable to cacheable.
func TestChatList_UncacheableCount_SettleTransitions(t *testing.T) {
	t.Run("finalize settles the streaming assistant", func(t *testing.T) {
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
}

// groundTruthUncacheable counts, from the render cache itself, how many
// envelopes renderEnvelope refused to cache. renderEnvelope nils the cache of
// every unfinished envelope and re-caches every finished one, so a nil cache
// after a real buildRender is exactly "will re-render next frame".
//
// This reaches the same predicate as UncacheableCount by a different route —
// the real render path rather than the accessor's walk — so it catches SCOPE and
// POSITIONAL errors (items-only, tail-only, off-by-one) but structurally cannot
// catch a wrong predicate. That is why every call site pairs it with a hardcoded
// literal want.
//
// Requires width > 0 and c.width > 0: otherwise Render short-circuits to "" and
// the renderBuilds Fatal below fires. The width need not match any prior render —
// a mismatch misses the cache hit but a finished envelope is still re-cached
// non-nil, so nil-ness is width-independent.
func groundTruthUncacheable(t *testing.T, cl *ChatList, width int) int {
	t.Helper()
	before := cl.renderBuilds
	cl.Render(width)
	if cl.renderBuilds == before {
		t.Fatalf("Render(%d) served the outer cache; envelope caches were not rebuilt "+
			"and this oracle would be reading stale state", width)
	}
	n := 0
	for _, env := range cl.items {
		if env.cache == nil {
			n++
		}
	}
	for _, e := range cl.zone.order {
		for _, env := range e.items {
			if env.cache == nil {
				n++
			}
		}
	}
	return n
}

// TestChatList_UncacheableCount_UnfinishedBehindThinkingMarker is the defect
// this accessor shipped with. AppendThinking is the one c.items append that
// deliberately bypasses the beginContentAppend chokepoint, so `chunk ->
// AppendThinking` parks an in-flight assistant at n-2 beneath a marker whose
// Finished() is hardcoded true. A tail-only probe reads the marker, says 0, and
// misses an item that re-runs the whole markdown pipeline every frame.
func TestChatList_UncacheableCount_UnfinishedBehindThinkingMarker(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("body")
	cl.AppendThinking()

	if got := cl.Len(); got != 2 {
		t.Fatalf("precondition: Len() = %d, want 2", got)
	}
	if assistantAt(t, cl, 0).Finished() {
		t.Fatal("precondition: items[0] must be the in-flight assistant")
	}
	if !cl.items[1].item.Finished() {
		t.Fatal("precondition: items[1] (the thinking marker) must report Finished()")
	}

	if got := cl.UncacheableCount(); got != 1 {
		t.Errorf("UncacheableCount() = %d, want 1 — a tail-only probe reads the "+
			"finished thinking marker and says 0 while items[0] re-renders every frame", got)
	}
	// Anchor the claim in observable rendering, not just in item flags.
	if got := groundTruthUncacheable(t, cl, 80); got != 1 {
		t.Errorf("render cache says %d envelopes are uncacheable, want 1", got)
	}
	if got := cursorCount(cl, 80); got != 1 {
		t.Errorf("%d streaming cursors rendered, want 1 (the in-flight assistant)", got)
	}
}

// TestChatList_UncacheableCount_CountsEveryPendingToolCall is the second red
// case, and it sits on the happy path: parallel tool calls in one assistant
// message are routine, and a tail probe under-reports them silently. The
// MarkToolResult leg is the sharpest single constraint — settling only the tail
// leaves t1 pending, which the probe reports as 0.
func TestChatList_UncacheableCount_CountsEveryPendingToolCall(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	appendTestToolCall(cl, "t1")
	appendTestToolCall(cl, "t2")

	if got := cl.UncacheableCount(); got != 2 {
		t.Errorf("UncacheableCount() = %d with two pending tool calls, want 2", got)
	}
	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d, want 0 — pending tool calls are uncacheable "+
			"but never invariant violations", got)
	}
	if got := groundTruthUncacheable(t, cl, 80); got != 2 {
		t.Errorf("render cache says %d envelopes are uncacheable, want 2", got)
	}

	cl.MarkToolResult("t2", "ok", false)
	if got := cl.UncacheableCount(); got != 1 {
		t.Errorf("UncacheableCount() = %d after settling only the TAIL, want 1 — "+
			"t1 is still pending behind a finished tail", got)
	}
}

// TestChatList_UncacheableCount_AgreesWithRenderCache is the breadth sweep.
// Every row cross-checks the accessor against the render cache and pins
// UncacheableCount >= OrphanCount.
func TestChatList_UncacheableCount_AgreesWithRenderCache(t *testing.T) {
	cases := []struct {
		name  string
		build func(*ChatList)
		want  int
	}{
		{"empty", func(cl *ChatList) {}, 0},
		{"streaming tail", func(cl *ChatList) { cl.AppendAssistantChunk("x") }, 1},
		{"chunk then thinking", func(cl *ChatList) {
			cl.AppendAssistantChunk("body")
			cl.AppendThinking()
		}, 1},
		{"two parallel tool calls", func(cl *ChatList) {
			appendTestToolCall(cl, "t1")
			appendTestToolCall(cl, "t2")
		}, 2},
		{"pending tool plus streaming text", func(cl *ChatList) {
			appendTestToolCall(cl, "t1")
			cl.AppendAssistantChunk("streaming")
		}, 2},
		{"mid-turn after tool result", func(cl *ChatList) {
			cl.AppendAssistantChunk("first")
			appendTestToolCall(cl, "t1")
			cl.MarkToolResult("t1", "ok", false)
		}, 0},
		{"after Reset", func(cl *ChatList) {
			cl.Reset([]MessageEntry{{Type: MessageAssistant, Content: "done", Complete: true}})
		}, 0},
		{"synthetic orphan", func(cl *ChatList) {
			cl.AppendAssistantChunk("first")
			appendTestToolCall(cl, "t1")
			cl.MarkToolResult("t1", "ok", false)
			cl.items[0].item.(*AssistantTextItem).finished = false
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := newTestChatList()
			cl.SetSize(80)
			tc.build(cl)

			got := cl.UncacheableCount()
			if got != tc.want {
				t.Errorf("UncacheableCount() = %d, want %d", got, tc.want)
			}
			if truth := groundTruthUncacheable(t, cl, 80); got != truth {
				t.Errorf("UncacheableCount() = %d but the render cache says %d", got, truth)
			}
			if orphans := cl.OrphanCount(); got < orphans {
				t.Errorf("UncacheableCount() = %d < OrphanCount() = %d; every orphan is "+
					"by definition uncacheable", got, orphans)
			}
		})
	}
}

// TestChatList_UncacheableCount_WalksThePendingZone pins the zone decision.
// buildRender runs zone envelopes through the same renderEnvelope as committed
// items, so the zone is part of the surface this count measures. Today every
// zone item kind reports Finished()==true, so walking it is numerically a no-op
// — the point is that the count must not ASSUME that.
func TestChatList_UncacheableCount_WalksThePendingZone(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.ZoneAddUser("u1", "queued")
	cl.ZoneAddSystem("s1", `<system-notification type="message">hi</system-notification>`)
	if got := cl.UncacheableCount(); got != 0 {
		t.Errorf("UncacheableCount() = %d, want 0 (all zone item kinds are finished)", got)
	}

	cl.zone.add(&pendingEntry{
		uuid:  "x",
		kind:  pendingUser,
		items: []*itemEnvelope{{item: NewAssistantTextItem(&cl.ctx, "in flight")}},
	})
	// LOAD-BEARING: the assertion below is the ONLY one in the package that
	// distinguishes an items+zone walk from an items-only walk — every other zone
	// expectation is 0 under both, because all zone item kinds report Finished().
	// Do not delete it as an unreachable synthetic: doing so makes the zone loop
	// dead code and lets the accessor silently regress with a green suite.
	if cl.zone.byUUID["x"].items[0].item.Finished() {
		t.Fatal("precondition: the synthetic zone envelope must be unfinished")
	}
	if got := cl.UncacheableCount(); got != 1 {
		t.Errorf("UncacheableCount() = %d, want 1 — an unfinished zone envelope "+
			"re-renders every frame just like a committed one", got)
	}
	if got := groundTruthUncacheable(t, cl, 80); got != 1 {
		t.Errorf("render cache says %d envelopes are uncacheable, want 1", got)
	}

	// Walking two regions introduces a failure mode the tail probe could not
	// have: counting one logical envelope twice. ZoneSettle relocates the entry
	// from the zone into items, so the regions must stay disjoint.
	if !cl.ZoneSettle("x") {
		t.Fatal("precondition: ZoneSettle must relocate the synthetic entry")
	}
	if got := cl.UncacheableCount(); got != 1 {
		t.Errorf("UncacheableCount() = %d after settling the unfinished entry, want 1 — "+
			"relocation must not count the same envelope in both items and zone", got)
	}
}

// TestChatList_UncacheableCount_TracksUnfinishedNotLength pins that the VALUE
// tracks unfinished items and not list length: 2000 settled items count 0, and
// one streaming tail counts 1. This is not a complexity guarantee — the walk is
// deliberately O(n).
func TestChatList_UncacheableCount_TracksUnfinishedNotLength(t *testing.T) {
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
		t.Errorf("UncacheableCount() = %d, want 1", got)
	}
}

// TestChatList_Accessors_DoNotRender keeps both debug accessors off the render
// path. Cheap to state, and the reason the O(n) walk costs nothing that matters.
func TestChatList_Accessors_DoNotRender(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendAssistantChunk("streaming")
	appendTestToolCall(cl, "t1")
	cl.ZoneAddUser("u1", "queued")

	before := cl.renderBuilds
	cl.OrphanCount()
	cl.UncacheableCount()
	if cl.renderBuilds != before {
		t.Errorf("accessors triggered a render (renderBuilds %d -> %d)", before, cl.renderBuilds)
	}
}
