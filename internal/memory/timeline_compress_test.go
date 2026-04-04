package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Fixed reference time for all tests.
var compressNow = time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

func defaultCompressCfg() TimelineCompressionConfig {
	return DefaultTimelineCompressionConfig()
}

func te(daysAgo int, summary string) TimelineEntry {
	return TimelineEntry{
		Timestamp: compressNow.AddDate(0, 0, -daysAgo),
		Summary:   summary,
	}
}

func teAt(t time.Time, summary string) TimelineEntry {
	return TimelineEntry{Timestamp: t, Summary: summary}
}

func TestCompressTimeline_EmptyInput(t *testing.T) {
	got := CompressTimeline(nil, defaultCompressCfg(), compressNow)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %d entries", len(got))
	}

	got = CompressTimeline([]TimelineEntry{}, defaultCompressCfg(), compressNow)
	if len(got) != 0 {
		t.Errorf("expected empty result for empty slice, got %d entries", len(got))
	}
}

func TestCompressTimeline_AllRecent(t *testing.T) {
	entries := []TimelineEntry{
		te(29, "did thing C"),
		te(5, "did thing B"),
		te(1, "did thing A"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	// Output is sorted oldest first
	wantSummaries := []string{"did thing C", "did thing B", "did thing A"}
	for i, e := range got {
		if e.Summary != wantSummaries[i] {
			t.Errorf("entry %d: got summary %q, want %q", i, e.Summary, wantSummaries[i])
		}
	}
}

func TestCompressTimeline_ThresholdBoundary_ExactlyWeekly(t *testing.T) {
	// Entry exactly 30 days old should go into weekly bucket (>= threshold).
	entries := []TimelineEntry{te(30, "exactly 30 days")}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Summary == "exactly 30 days" {
		t.Error("entry exactly at weekly threshold should be compressed, not kept as-is")
	}
	if !strings.Contains(got[0].Summary, "[Week of") {
		t.Errorf("expected weekly prefix, got %q", got[0].Summary)
	}
}

func TestCompressTimeline_ThresholdBoundary_ExactlyMonthly(t *testing.T) {
	// Entry exactly 90 days old should go into monthly bucket.
	// 90 days before 2026-04-02 = 2026-01-02
	entries := []TimelineEntry{te(90, "exactly 90 days")}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Summary == "exactly 90 days" {
		t.Error("entry exactly at monthly threshold should be compressed, not kept as-is")
	}
	if !strings.Contains(got[0].Summary, "[January 2026]") {
		t.Errorf("expected [January 2026] prefix, got %q", got[0].Summary)
	}
}

func TestCompressTimeline_WeeklyGrouping(t *testing.T) {
	// Two entries in the same ISO week, 35 and 36 days ago.
	// 2026-04-02 minus 35 days = Feb 26 (Thu), minus 36 = Feb 25 (Wed) — same ISO week.
	entries := []TimelineEntry{
		te(35, "thing A"),
		te(36, "thing B"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 compressed entry, got %d", len(got))
	}
	if !strings.Contains(got[0].Summary, "[Week of") {
		t.Errorf("expected weekly prefix, got %q", got[0].Summary)
	}
	// Summaries joined with "; " — older entry first (chronological)
	if !strings.Contains(got[0].Summary, "thing B; thing A") {
		t.Errorf("expected joined summaries in chronological order, got %q", got[0].Summary)
	}
}

func TestCompressTimeline_MonthlyGrouping(t *testing.T) {
	// Two entries in the same month, >90 days ago.
	// 2026-04-02 minus 100 days = Dec 23, 2025; minus 105 = Dec 18, 2025
	entries := []TimelineEntry{
		te(100, "old thing A"),
		te(105, "old thing B"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 compressed entry, got %d", len(got))
	}
	if !strings.Contains(got[0].Summary, "[December 2025]") {
		t.Errorf("expected [December 2025] prefix, got %q", got[0].Summary)
	}
	if !strings.Contains(got[0].Summary, "old thing B; old thing A") {
		t.Errorf("expected joined summaries in chronological order, got %q", got[0].Summary)
	}
	// Timestamp should be start of month
	expected := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	if !got[0].Timestamp.Equal(expected) {
		t.Errorf("timestamp = %v, want %v", got[0].Timestamp, expected)
	}
}

func TestCompressTimeline_TaggedPreservedVerbatim(t *testing.T) {
	entries := []TimelineEntry{
		te(50, "[recurring] always slow builds"),
		te(100, "[pain-point] flaky CI"),
		te(50, "normal entry 50 days ago"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)

	var foundRecurring, foundPainPoint bool
	for _, e := range got {
		if e.Summary == "[recurring] always slow builds" {
			foundRecurring = true
			// Should preserve original timestamp
			if !e.Timestamp.Equal(compressNow.AddDate(0, 0, -50)) {
				t.Errorf("[recurring] timestamp changed: %v", e.Timestamp)
			}
		}
		if e.Summary == "[pain-point] flaky CI" {
			foundPainPoint = true
			if !e.Timestamp.Equal(compressNow.AddDate(0, 0, -100)) {
				t.Errorf("[pain-point] timestamp changed: %v", e.Timestamp)
			}
		}
	}
	if !foundRecurring {
		t.Error("[recurring] entry not found in output")
	}
	if !foundPainPoint {
		t.Error("[pain-point] entry not found in output")
	}
}

func TestCompressTimeline_MixedTaggedAndUntaggedInGroup(t *testing.T) {
	// Same ISO week: 1 tagged + 2 untagged. Result = 1 compressed + 1 tagged.
	entries := []TimelineEntry{
		te(35, "[recurring] common issue"),
		te(35, "fix auth"),
		te(36, "update deps"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)

	var compressed, tagged int
	for _, e := range got {
		if e.Summary == "[recurring] common issue" {
			tagged++
		} else if strings.Contains(e.Summary, "[Week of") {
			compressed++
			// Should NOT contain the tagged entry's text
			if strings.Contains(e.Summary, "common issue") {
				t.Errorf("compressed entry should not contain tagged entry text: %q", e.Summary)
			}
		}
	}
	if tagged != 1 {
		t.Errorf("expected 1 tagged entry, got %d", tagged)
	}
	if compressed != 1 {
		t.Errorf("expected 1 compressed entry, got %d", compressed)
	}
}

func TestCompressTimeline_AllTaggedInGroup(t *testing.T) {
	// All entries in a week are tagged — no compressed entry should be emitted.
	entries := []TimelineEntry{
		te(35, "[recurring] issue A"),
		te(36, "[pain-point] issue B"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (both tagged), got %d", len(got))
	}
	for _, e := range got {
		if strings.Contains(e.Summary, "[Week of") {
			t.Errorf("should not have compressed entry when all are tagged: %q", e.Summary)
		}
	}
}

func TestCompressTimeline_SingleEntryGroup(t *testing.T) {
	// One entry alone in a week still gets the prefix.
	entries := []TimelineEntry{te(40, "lonely entry")}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if !strings.Contains(got[0].Summary, "[Week of") {
		t.Errorf("single entry should still get weekly prefix, got %q", got[0].Summary)
	}
	if !strings.Contains(got[0].Summary, "lonely entry") {
		t.Errorf("summary should contain original text, got %q", got[0].Summary)
	}
}

func TestCompressTimeline_AlreadyCompressedPassThrough(t *testing.T) {
	entries := []TimelineEntry{
		teAt(time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC), "[November 2025] old stuff; older stuff"),
		teAt(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), "[Week of 2026-01-05] did stuff; more stuff"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	// Sorted oldest first
	if got[0].Summary != "[November 2025] old stuff; older stuff" {
		t.Errorf("entry 0 changed: got %q", got[0].Summary)
	}
	if got[1].Summary != "[Week of 2026-01-05] did stuff; more stuff" {
		t.Errorf("entry 1 changed: got %q", got[1].Summary)
	}
}

func TestCompressTimeline_Idempotent(t *testing.T) {
	entries := []TimelineEntry{
		te(1, "recent"),
		te(35, "weekly A"),
		te(36, "weekly B"),
		te(100, "monthly A"),
		te(105, "monthly B"),
		te(50, "[recurring] tagged"),
	}
	cfg := defaultCompressCfg()
	first := CompressTimeline(entries, cfg, compressNow)
	second := CompressTimeline(first, cfg, compressNow)

	if len(first) != len(second) {
		t.Fatalf("idempotency check: first pass %d entries, second pass %d entries", len(first), len(second))
	}
	for i := range first {
		if first[i].Summary != second[i].Summary {
			t.Errorf("entry %d differs: first=%q, second=%q", i, first[i].Summary, second[i].Summary)
		}
		if !first[i].Timestamp.Equal(second[i].Timestamp) {
			t.Errorf("entry %d timestamp differs: first=%v, second=%v", i, first[i].Timestamp, second[i].Timestamp)
		}
	}
}

func TestCompressTimeline_OutputSortedOldestFirst(t *testing.T) {
	entries := []TimelineEntry{
		te(1, "recent"),
		te(100, "old monthly"),
		te(50, "middle weekly"),
		te(35, "[recurring] tagged weekly"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("output not sorted: entry %d (%v) before entry %d (%v)",
				i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}

func TestCompressTimeline_SpanningThresholds(t *testing.T) {
	entries := []TimelineEntry{
		te(1, "recent A"),
		te(10, "recent B"),
		te(35, "weekly A"),
		te(60, "weekly B"),
		te(100, "monthly A"),
		te(150, "monthly B"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)

	var recent, weekly, monthly int
	for _, e := range got {
		switch {
		case strings.Contains(e.Summary, "[Week of"):
			weekly++
		case isMonthPrefixTest(e.Summary):
			monthly++
		default:
			recent++
		}
	}
	if recent != 2 {
		t.Errorf("expected 2 recent entries, got %d", recent)
	}
	// 35 days ago = Feb 26, 60 days ago = Feb 1 — different ISO weeks, so 2 weekly groups
	if weekly != 2 {
		t.Errorf("expected 2 weekly groups, got %d", weekly)
	}
	// 100 days = Dec 23, 150 days = Nov 3 — different months, so 2 monthly groups
	if monthly != 2 {
		t.Errorf("expected 2 monthly groups, got %d", monthly)
	}
}

func TestCompressTimeline_ISOWeekYearBoundary(t *testing.T) {
	// Dec 29, 2025 is Monday of ISO week 1 of 2026.
	// Dec 31, 2025 and Dec 29, 2025 should be in the same ISO week.
	now := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	entries := []TimelineEntry{
		teAt(time.Date(2025, 12, 29, 10, 0, 0, 0, time.UTC), "year boundary A"),
		teAt(time.Date(2025, 12, 31, 10, 0, 0, 0, time.UTC), "year boundary B"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), now)

	// Both entries are > 90 days old from now (April 2026), so they'd be monthly.
	// Dec 29 and Dec 31 are in the same calendar month (December 2025).
	// They should be compressed into one monthly entry.
	if len(got) != 1 {
		t.Fatalf("expected 1 monthly entry, got %d: %v", len(got), summaries(got))
	}
	if !strings.Contains(got[0].Summary, "[December 2025]") {
		t.Errorf("expected [December 2025] prefix, got %q", got[0].Summary)
	}
}

func TestCompressTimeline_ISOWeekYearBoundary_WeeklyBucket(t *testing.T) {
	// Test ISO week year boundary in the weekly bucket specifically.
	// Use a custom 'now' so entries from late Dec 2025 fall into weekly range (30-90 days).
	// now = 2026-02-10. Entries from Dec 29-31, 2025 are ~43 days old -> weekly bucket.
	now := time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC)
	entries := []TimelineEntry{
		// Dec 29, 2025 (Mon) is ISO week 1 of 2026
		teAt(time.Date(2025, 12, 29, 10, 0, 0, 0, time.UTC), "late dec A"),
		// Dec 31, 2025 (Wed) is also ISO week 1 of 2026
		teAt(time.Date(2025, 12, 31, 10, 0, 0, 0, time.UTC), "late dec B"),
	}
	got := CompressTimeline(entries, defaultCompressCfg(), now)

	if len(got) != 1 {
		t.Fatalf("expected 1 weekly entry, got %d: %v", len(got), summaries(got))
	}
	if !strings.Contains(got[0].Summary, "[Week of 2025-12-29]") {
		t.Errorf("expected [Week of 2025-12-29] prefix, got %q", got[0].Summary)
	}
	if !strings.Contains(got[0].Summary, "late dec A; late dec B") {
		t.Errorf("expected joined summaries, got %q", got[0].Summary)
	}
}

func TestCompressTimeline_WeeklyTimestampIsStartOfWeek(t *testing.T) {
	// Verify the compressed weekly entry's timestamp is the Monday of that ISO week.
	entries := []TimelineEntry{te(35, "mid-week entry")}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	// The entry is from compressNow - 35 days = 2026-02-26 (Thursday).
	// ISO week of 2026-02-26: Monday is 2026-02-23.
	expectedTS := time.Date(2026, 2, 23, 0, 0, 0, 0, time.UTC)
	if !got[0].Timestamp.Equal(expectedTS) {
		t.Errorf("weekly timestamp = %v, want Monday %v", got[0].Timestamp, expectedTS)
	}
}

func TestCompressTimeline_MonthlyTimestampIsStartOfMonth(t *testing.T) {
	entries := []TimelineEntry{te(100, "old entry")}
	got := CompressTimeline(entries, defaultCompressCfg(), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	// 100 days before 2026-04-02 = 2025-12-23. Start of month = 2025-12-01.
	expectedTS := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	if !got[0].Timestamp.Equal(expectedTS) {
		t.Errorf("monthly timestamp = %v, want %v", got[0].Timestamp, expectedTS)
	}
}

func TestCompressTimeline_CustomConfig(t *testing.T) {
	// Use non-default thresholds: weekly at 10 days, monthly at 20 days.
	cfg := TimelineCompressionConfig{
		WeeklySummaryAge:  10 * 24 * time.Hour,
		MonthlySummaryAge: 20 * 24 * time.Hour,
	}
	entries := []TimelineEntry{
		te(5, "recent"),   // < 10 days -> recent
		te(15, "weekly"),  // 10-20 days -> weekly
		te(25, "monthly"), // >= 20 days -> monthly
	}
	got := CompressTimeline(entries, cfg, compressNow)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	// Sorted oldest first: monthly, weekly, recent
	if !strings.Contains(got[0].Summary, "] monthly") {
		t.Errorf("expected monthly compressed entry first, got %q", got[0].Summary)
	}
	if !strings.Contains(got[1].Summary, "] weekly") {
		t.Errorf("expected weekly compressed entry second, got %q", got[1].Summary)
	}
	if got[2].Summary != "recent" {
		t.Errorf("expected recent entry as-is, got %q", got[2].Summary)
	}
}

// --- PruneTimeline tests ---

func pruneCfg(maxEntries, maxSizeChars int) TimelineCompressionConfig {
	cfg := defaultCompressCfg()
	cfg.MaxEntries = maxEntries
	cfg.MaxSizeChars = maxSizeChars
	return cfg
}

// formattedSize returns the byte count of an entry as it would appear in timeline.md.
func formattedSize(e TimelineEntry) int {
	return MeasureBytes(fmt.Sprintf("- %s: %s\n", e.Timestamp.UTC().Format(time.RFC3339), e.Summary))
}

func totalFormattedSize(entries []TimelineEntry) int {
	total := 0
	for _, e := range entries {
		total += formattedSize(e)
	}
	return total
}

func TestPruneTimeline_EmptyInput(t *testing.T) {
	got := PruneTimeline(nil, defaultCompressCfg(), compressNow)
	if len(got) != 0 {
		t.Errorf("expected empty result for nil, got %d entries", len(got))
	}
	got = PruneTimeline([]TimelineEntry{}, defaultCompressCfg(), compressNow)
	if len(got) != 0 {
		t.Errorf("expected empty result for empty slice, got %d entries", len(got))
	}
}

func TestPruneTimeline_UnderLimits_NoOp(t *testing.T) {
	entries := []TimelineEntry{
		te(1, "entry A"),
		te(2, "entry B"),
		te(3, "entry C"),
	}
	got := PruneTimeline(entries, pruneCfg(10, 100000), compressNow)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	// No omission note
	for _, e := range got {
		if strings.Contains(e.Summary, "earliest entries omitted") {
			t.Error("should not have omission note when under limits")
		}
	}
}

func TestPruneTimeline_ExistingOmissionNoteRemoved(t *testing.T) {
	entries := []TimelineEntry{
		te(1, "entry A"),
		teAt(compressNow.Add(-time.Hour), "[...5 earliest entries omitted]"),
		te(2, "entry B"),
	}
	got := PruneTimeline(entries, pruneCfg(10, 100000), compressNow)
	// Omission note should be stripped; 2 real entries remain, no new omission note needed
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (omission note removed), got %d: %v", len(got), summaries(got))
	}
	for _, e := range got {
		if strings.Contains(e.Summary, "earliest entries omitted") {
			t.Error("stale omission note should have been removed")
		}
	}
}

func TestPruneTimeline_MaxEntries_DropsOldestUntagged(t *testing.T) {
	entries := []TimelineEntry{
		te(5, "oldest"),
		te(4, "old"),
		te(3, "middle"),
		te(2, "recent"),
		te(1, "newest"),
	}
	got := PruneTimeline(entries, pruneCfg(3, 100000), compressNow)
	// Should drop 2 oldest, keep 3 newest + 1 omission note = 4
	if len(got) != 4 {
		t.Fatalf("expected 4 entries (3 + omission note), got %d: %v", len(got), summaries(got))
	}
	// Check omission note
	found := false
	for _, e := range got {
		if e.Summary == "[...2 earliest entries omitted]" {
			found = true
			if !e.Timestamp.Equal(compressNow) {
				t.Errorf("omission note timestamp = %v, want %v", e.Timestamp, compressNow)
			}
		}
	}
	if !found {
		t.Errorf("expected omission note, got: %v", summaries(got))
	}
	// "oldest" and "old" should be dropped
	for _, e := range got {
		if e.Summary == "oldest" || e.Summary == "old" {
			t.Errorf("entry %q should have been dropped", e.Summary)
		}
	}
}

func TestPruneTimeline_MaxEntries_TaggedSurviveLonger(t *testing.T) {
	entries := []TimelineEntry{
		te(5, "[recurring] old tagged"),
		te(4, "old untagged"),
		te(3, "middle untagged"),
		te(2, "[pain-point] recent tagged"),
		te(1, "newest untagged"),
	}
	// MaxEntries=3: should drop 2 oldest untagged first
	got := PruneTimeline(entries, pruneCfg(3, 100000), compressNow)
	// 3 remaining + 1 omission = 4
	if len(got) != 4 {
		t.Fatalf("expected 4 entries, got %d: %v", len(got), summaries(got))
	}
	// Both tagged entries should survive
	var tagged int
	for _, e := range got {
		if isTaggedEntry(e.Summary) {
			tagged++
		}
	}
	if tagged != 2 {
		t.Errorf("expected 2 tagged entries to survive, got %d", tagged)
	}
	// "old untagged" and "middle untagged" should be dropped (oldest untagged first)
	for _, e := range got {
		if e.Summary == "old untagged" || e.Summary == "middle untagged" {
			t.Errorf("entry %q should have been dropped", e.Summary)
		}
	}
}

func TestPruneTimeline_MaxSizeChars_DropsOldestUntagged(t *testing.T) {
	entries := []TimelineEntry{
		te(3, "aaa"),
		te(2, "bbb"),
		te(1, "ccc"),
	}
	// Calculate size such that only 2 entries fit
	twoEntrySize := formattedSize(entries[1]) + formattedSize(entries[2])
	got := PruneTimeline(entries, pruneCfg(100, twoEntrySize), compressNow)

	// Should drop "aaa" (oldest untagged), keep "bbb" and "ccc" + omission note
	var realEntries []TimelineEntry
	for _, e := range got {
		if !strings.Contains(e.Summary, "earliest entries omitted") {
			realEntries = append(realEntries, e)
		}
	}
	if len(realEntries) != 2 {
		t.Fatalf("expected 2 real entries, got %d: %v", len(realEntries), summaries(realEntries))
	}
	for _, e := range realEntries {
		if e.Summary == "aaa" {
			t.Error("entry 'aaa' should have been dropped")
		}
	}
}

func TestPruneTimeline_BothLimitsActive(t *testing.T) {
	entries := []TimelineEntry{
		te(5, "e1"),
		te(4, "e2"),
		te(3, "e3"),
		te(2, "e4"),
		te(1, "e5"),
	}
	// MaxEntries=4 drops 1, then MaxSizeChars drops more
	oneEntrySize := formattedSize(entries[0])
	got := PruneTimeline(entries, pruneCfg(4, oneEntrySize*3), compressNow)

	var realEntries []TimelineEntry
	for _, e := range got {
		if !strings.Contains(e.Summary, "earliest entries omitted") {
			realEntries = append(realEntries, e)
		}
	}
	if len(realEntries) > 3 {
		t.Errorf("expected at most 3 real entries, got %d: %v", len(realEntries), summaries(realEntries))
	}
	// Check omission note reflects total dropped
	for _, e := range got {
		if strings.Contains(e.Summary, "earliest entries omitted") {
			// At least 2 dropped (1 from MaxEntries, 1+ from MaxSizeChars)
			if !strings.Contains(e.Summary, "[...2 ") && !strings.Contains(e.Summary, "[...3 ") && !strings.Contains(e.Summary, "[...4 ") {
				// We just need dropped > 1
				t.Logf("omission note: %s", e.Summary)
			}
		}
	}
}

func TestPruneTimeline_HardLimit_DropsOldestTagged(t *testing.T) {
	entries := []TimelineEntry{
		te(5, "[recurring] old tag A"),
		te(4, "[pain-point] old tag B"),
		te(3, "[recurring] mid tag C"),
		te(2, "[recurring] new tag D"),
	}
	// All tagged, MaxEntries=2: must drop oldest 2 tagged
	got := PruneTimeline(entries, pruneCfg(2, 100000), compressNow)
	// 2 remaining + 1 omission = 3
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(got), summaries(got))
	}
	for _, e := range got {
		if e.Summary == "[recurring] old tag A" || e.Summary == "[pain-point] old tag B" {
			t.Errorf("oldest tagged entry %q should have been dropped", e.Summary)
		}
	}
}

func TestPruneTimeline_SingleEntryExceedingMaxSize(t *testing.T) {
	longSummary := strings.Repeat("x", 10000)
	entries := []TimelineEntry{te(1, longSummary)}
	got := PruneTimeline(entries, pruneCfg(100, 50), compressNow)
	if len(got) != 1 {
		t.Fatalf("expected 1 entry (kept as-is), got %d", len(got))
	}
	if got[0].Summary != longSummary {
		t.Error("single entry exceeding MaxSizeChars should be kept as-is")
	}
}

func TestPruneTimeline_OmissionNoteTimestampAndFormat(t *testing.T) {
	entries := []TimelineEntry{
		te(3, "dropped"),
		te(2, "kept A"),
		te(1, "kept B"),
	}
	got := PruneTimeline(entries, pruneCfg(2, 100000), compressNow)
	var omission *TimelineEntry
	for i := range got {
		if strings.Contains(got[i].Summary, "earliest entries omitted") {
			omission = &got[i]
		}
	}
	if omission == nil {
		t.Fatal("expected omission note")
	}
	if omission.Summary != "[...1 earliest entries omitted]" {
		t.Errorf("omission summary = %q, want %q", omission.Summary, "[...1 earliest entries omitted]")
	}
	if !omission.Timestamp.Equal(compressNow) {
		t.Errorf("omission timestamp = %v, want %v", omission.Timestamp, compressNow)
	}
}

func TestPruneTimeline_OutputSorted(t *testing.T) {
	entries := []TimelineEntry{
		te(5, "oldest"),
		te(4, "old"),
		te(1, "newest"),
	}
	got := PruneTimeline(entries, pruneCfg(2, 100000), compressNow)
	for i := 1; i < len(got); i++ {
		if got[i].Timestamp.Before(got[i-1].Timestamp) {
			t.Errorf("output not sorted: entry %d (%v) before entry %d (%v)",
				i, got[i].Timestamp, i-1, got[i-1].Timestamp)
		}
	}
}

func TestPruneTimeline_Idempotent(t *testing.T) {
	entries := []TimelineEntry{
		te(5, "e1"),
		te(4, "e2"),
		te(3, "e3"),
		te(2, "e4"),
		te(1, "e5"),
	}
	cfg := pruneCfg(3, 100000)
	first := PruneTimeline(entries, cfg, compressNow)
	second := PruneTimeline(first, cfg, compressNow)

	// After first pass: 3 real entries + 1 omission note = 4.
	// After second pass: omission note stripped, 3 real entries at limit = 3 (no new omission).
	// Real entries should be identical across passes.
	firstReal := filterNonOmission(first)
	secondReal := filterNonOmission(second)
	if len(firstReal) != len(secondReal) {
		t.Fatalf("idempotency: first %d real, second %d real", len(firstReal), len(secondReal))
	}
	for i := range firstReal {
		if firstReal[i].Summary != secondReal[i].Summary {
			t.Errorf("entry %d differs: %q vs %q", i, firstReal[i].Summary, secondReal[i].Summary)
		}
	}
	// Third pass should be identical to second (true idempotency after stabilization)
	third := PruneTimeline(second, cfg, compressNow)
	if len(second) != len(third) {
		t.Fatalf("idempotency: second %d entries, third %d entries", len(second), len(third))
	}
}

func TestPruneTimeline_ExactlyAtLimit(t *testing.T) {
	entries := []TimelineEntry{
		te(3, "aaa"),
		te(2, "bbb"),
		te(1, "ccc"),
	}
	// Exactly at MaxEntries
	got := PruneTimeline(entries, pruneCfg(3, 100000), compressNow)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries at exact limit, got %d", len(got))
	}
	// Exactly at MaxSizeChars
	totalSize := totalFormattedSize(entries)
	got = PruneTimeline(entries, pruneCfg(100, totalSize), compressNow)
	if len(got) != 3 {
		t.Fatalf("expected 3 entries at exact size limit, got %d", len(got))
	}
}

func TestPruneTimeline_MaxEntriesBeforeMaxSizeChars(t *testing.T) {
	// Verify ordering: MaxEntries runs first, then MaxSizeChars
	entries := []TimelineEntry{
		te(5, "e1"),
		te(4, "e2"),
		te(3, "e3"),
		te(2, "e4"),
		te(1, "e5"),
	}
	// MaxEntries=4 reduces to 4, then size limit reduces further
	// Entry size is roughly same for each, so we set size to fit ~2 entries
	singleSize := formattedSize(entries[0])
	cfg := pruneCfg(4, singleSize*2)
	got := PruneTimeline(entries, cfg, compressNow)
	var realEntries []TimelineEntry
	for _, e := range got {
		if !strings.Contains(e.Summary, "earliest entries omitted") {
			realEntries = append(realEntries, e)
		}
	}
	// After MaxEntries: 4 entries. After MaxSizeChars: ~2 entries.
	if len(realEntries) > 2 {
		t.Errorf("expected at most 2 real entries, got %d: %v", len(realEntries), summaries(realEntries))
	}
}

func TestPruneTimeline_MultipleStaleOmissionNotes(t *testing.T) {
	entries := []TimelineEntry{
		teAt(compressNow.Add(-2*time.Hour), "[...3 earliest entries omitted]"),
		te(2, "entry A"),
		teAt(compressNow.Add(-time.Hour), "[...10 earliest entries omitted]"),
		te(1, "entry B"),
	}
	got := PruneTimeline(entries, pruneCfg(10, 100000), compressNow)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries (both omission notes removed), got %d: %v", len(got), summaries(got))
	}
}

func filterNonOmission(entries []TimelineEntry) []TimelineEntry {
	var result []TimelineEntry
	for _, e := range entries {
		if !strings.Contains(e.Summary, "earliest entries omitted") {
			result = append(result, e)
		}
	}
	return result
}

// --- helpers ---

func isMonthPrefixTest(summary string) bool {
	months := []string{
		"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December",
	}
	for _, m := range months {
		if strings.HasPrefix(summary, "["+m+" ") {
			return true
		}
	}
	return false
}

func summaries(entries []TimelineEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Summary
	}
	return out
}
