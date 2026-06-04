package tui

// QUM-671 — long-history rendering benchmark. The S3 ≤2s regression gate
// (plan §3 S3, §4.1) needs a populated viewport to measure the dominant
// renderMessages/glamour hot path. Today's app_bench_test.go benches run
// against an empty viewport and therefore can't anchor that gate (see
// docs/research/qum-670-baseline.md §Gap).
//
// This bench loads testdata/longhist-1500.jsonl (375 turns × {user, assistant
// markdown, Bash tool_use, tool_result} = 1500 records) into a ViewportModel
// via the production LoadTranscript path and then measures steady-state
// View() cost. It exercises:
//   - the renderMessages walk,
//   - the QUM-667 per-entry render cache (steady-state should hit),
//   - glamour markdown rendering (~375 assistant blocks),
//   - tool-call box rendering (~375 entries),
//   - lipgloss style application.
//
// To measure uncached cost (e.g. for "did the cache regress?" sanity), the
// QUM-667 escape hatch `disableRenderCacheForTest=true` can be toggled.

import (
	"path/filepath"
	"testing"
)

// loadLongHistoryFixture loads the committed 1500-frame fixture and returns
// the corresponding MessageEntry slice. Fails the test on any error — the
// fixture is committed and must always parse.
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

// BenchmarkViewportModel_View_LongHistory_SteadyState measures View() cost
// on a 1500-frame transcript at steady state. The first SetMessages warms
// the per-entry render cache; subsequent View() calls measure the cache-hit
// path. This is the anchor for S3's "no >2s regression" gate.
func BenchmarkViewportModel_View_LongHistory_SteadyState(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	theme := NewTheme("")
	vp := NewViewportModel(&theme)
	vp.SetSize(200, 60)
	vp.SetMessages(entries)
	_ = vp.View()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vp.View()
	}
}

// BenchmarkViewportModel_RenderMessages_LongHistory_Cold measures the
// uncached renderMessages walk so the S3 gate can compare against the
// "renderer is doing real work" baseline rather than just the cache lookup.
// Uses the QUM-667 escape hatch to bypass the per-entry cache.
func BenchmarkViewportModel_RenderMessages_LongHistory_Cold(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	theme := NewTheme("")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		vp := NewViewportModel(&theme)
		vp.SetSize(200, 60)
		vp.disableRenderCacheForTest = true
		vp.SetMessages(entries)
		b.StartTimer()
		_ = vp.renderMessages()
	}
}

// BenchmarkViewportModel_RenderMessages_LongHistory_Cached measures the
// renderMessages walk with the QUM-667 cache enabled — i.e. what S3's
// ChatList per-item cache must beat or match. The first call inside SetMessages
// warms it; the timed call should be near-zero (top-level cache hit).
func BenchmarkViewportModel_RenderMessages_LongHistory_Cached(b *testing.B) {
	entries := loadLongHistoryFixture(b)
	theme := NewTheme("")
	vp := NewViewportModel(&theme)
	vp.SetSize(200, 60)
	vp.SetMessages(entries) // warms both per-entry and top-level cache
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = vp.renderMessages()
	}
}
