package tui

// QUM-769 — long-history bench for the ChatRegion.View() path. Attributes
// per-keystroke typing-latency cost between ChatList.Render (string concat
// over all envelopes) and vp.SetContent + vp.View() (soft-wrap layout over
// the rendered content). Mirrors chatlist_longhist_bench_test.go shape so
// before/after numbers are apples-to-apples with the chatlist trio.

import "testing"

// chatRegionFromEntries builds a ChatRegion seeded from the long-history
// fixture via the inner ChatList's Reset path, sized to the bench width.
func chatRegionFromEntries(b *testing.B, entries []MessageEntry, width, height int) *ChatRegion {
	b.Helper()
	theme := NewTheme("")
	region := NewChatRegion(&theme)
	region.SetSize(width, height)
	region.ChatList().Reset(entries)
	return region
}

// BenchmarkChatRegion_View_LongHistory_SteadyState measures the per-call
// cost of ChatRegion.View() with no underlying ChatList mutation between
// calls. This is the per-keystroke hot path while typing into the input box
// over a long chat: ChatList state is unchanged, only the input is dirty,
// but the legacy code still re-walks ChatList and re-soft-wraps the entire
// content for every paint.
func BenchmarkChatRegion_View_LongHistory_SteadyState(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	region := chatRegionFromEntries(b, entries, 200, 50)
	_ = region.View() // warm caches
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = region.View()
	}
}

// BenchmarkChatRegion_View_LongHistory_AfterAppend appends a trivial user
// item between paints so the ChatList outer render cache (QUM-769) must
// rebuild every iteration. Validates that the cache invalidation path is
// still bounded.
func BenchmarkChatRegion_View_LongHistory_AfterAppend(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	region := chatRegionFromEntries(b, entries, 200, 50)
	_ = region.View()
	cl := region.ChatList()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cl.AppendUser("x")
		_ = region.View()
	}
}

// BenchmarkChatRegion_View_LongHistory_AfterScroll toggles scroll position
// between paints so any ChatRegion-level cache must invalidate on scroll.
func BenchmarkChatRegion_View_LongHistory_AfterScroll(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	region := chatRegionFromEntries(b, entries, 200, 50)
	_ = region.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			region.PageUp()
		} else {
			region.GotoBottom()
		}
		_ = region.View()
	}
}
