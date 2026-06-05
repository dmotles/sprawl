package tui

// QUM-674 S4 — long-history bench for the ChatList render path. M1 from S3's
// deferred residue: today only the legacy-viewport-side bench in
// app_longhist_bench_test.go exists; without a ChatList-direct counterpart
// future slices have no anchor to gate against. These benches mirror the
// viewport-side trio (SteadyState / Cold / Cached) so the +30% gates can be
// applied at the same shape S5+ adopts.
//
// Initial baselines are reported in the QUM-674 completion comment.

import (
	"path/filepath"
	"testing"
)

// loadLongHistoryFixture loads the committed 1500-frame fixture and returns
// the corresponding MessageEntry slice. Moved here when QUM-676 deleted the
// legacy app_longhist_bench_test.go (the viewport-side benches retired with
// the ViewportModel rendering surface).
func loadLongHistoryFixture(tb testing.TB) []MessageEntry {
	tb.Helper()
	path := filepath.Join("testdata", "longhist-1500.jsonl")
	entries, err := LoadTranscript(path, 0)
	if err != nil {
		tb.Fatalf("LoadTranscript(%s): %v", path, err)
	}
	if len(entries) < 1000 {
		tb.Fatalf("fixture parsed too few entries: %d", len(entries))
	}
	return entries
}

// chatListFromEntries builds a ChatList seeded from the long-history fixture
// via the Reset path. Width matches the existing viewport bench for
// apples-to-apples comparison.
func chatListFromEntries(b *testing.B, entries []MessageEntry, width int) *ChatList {
	b.Helper()
	theme := NewTheme("")
	cl := NewChatList(&theme)
	cl.SetSize(width)
	cl.Reset(entries)
	return cl
}

// BenchmarkChatList_Render_LongHistory_SteadyState measures Render cost at
// steady state. The first Render call warms each envelope's cache;
// subsequent calls measure the per-item cache-hit path.
func BenchmarkChatList_Render_LongHistory_SteadyState(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	cl := chatListFromEntries(b, entries, 200)
	_ = cl.Render(200)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cl.Render(200)
	}
}

// BenchmarkChatList_Render_LongHistory_Cold rebuilds the ChatList per
// iteration so every Render walks the full item slice without any cached
// envelopes. This is the apples-to-apples counterpart to the viewport's
// disableRenderCacheForTest=true bench.
func BenchmarkChatList_Render_LongHistory_Cold(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	theme := NewTheme("")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		cl := NewChatList(&theme)
		cl.SetSize(200)
		cl.Reset(entries)
		b.StartTimer()
		_ = cl.Render(200)
	}
}

// BenchmarkChatList_Render_LongHistory_Cached warms both the Reset path and
// the first Render before the timer starts; the timed Render is the pure
// cache-hit case and should be near-zero.
func BenchmarkChatList_Render_LongHistory_Cached(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	cl := chatListFromEntries(b, entries, 200)
	_ = cl.Render(200) // warm envelope caches
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cl.Render(200)
	}
}
