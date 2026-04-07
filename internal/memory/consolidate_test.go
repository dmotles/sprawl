package memory

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// consolidateTestNow returns a now func that keeps Jan 2026 entries within the "recent" window
// so compression doesn't affect them.
func consolidateTestNow() func() time.Time {
	return func() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) }
}

// helper: create N sessions in a temp sprawl root using WriteSessionSummary.
// Sessions are timestamped sequentially starting 2026-01-01 with +1 day increments.
func createTestSessions(t *testing.T, root string, n int) ([]Session, []string) {
	t.Helper()
	sessions := make([]Session, n)
	bodies := make([]string, n)
	for i := range n {
		s := Session{
			SessionID:    fmt.Sprintf("sess-%d", i),
			Timestamp:    time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC),
			Handoff:      false,
			AgentsActive: []string{"agent-a"},
		}
		body := fmt.Sprintf("Summary body for session %d", i)
		if err := WriteSessionSummary(root, s, body); err != nil {
			t.Fatalf("WriteSessionSummary[%d]: %v", i, err)
		}
		sessions[i] = s
		bodies[i] = body
	}
	return sessions, bodies
}

func TestConsolidate_NoopFewerThan3Sessions(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 2)

	mock := &mockClaudeInvoker{response: "should not be called"}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if mock.lastPrompt != "" {
		t.Error("invoker should not have been called with fewer than 3 sessions")
	}
}

func TestConsolidate_Exactly3Sessions_Noop(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 3)

	mock := &mockClaudeInvoker{response: "should not be called"}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if mock.lastPrompt != "" {
		t.Error("invoker should not have been called with exactly 3 sessions (0 candidates)")
	}
}

func TestConsolidate_FirstConsolidation(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// Sessions 0,1 are candidates (oldest); 2,3,4 are the 3 most recent.
	// Mock returns valid timeline output for the two candidates.
	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: Distilled session 0",
		"- 2026-01-02T00:00:00Z: Distilled session 1",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	// Verify invoker was called
	if mock.lastPrompt == "" {
		t.Fatal("invoker should have been called")
	}

	// Verify prompt contains candidate session bodies (sessions 0 and 1)
	if !strings.Contains(mock.lastPrompt, "Summary body for session 0") {
		t.Error("prompt should contain body of session 0 (candidate)")
	}
	if !strings.Contains(mock.lastPrompt, "Summary body for session 1") {
		t.Error("prompt should contain body of session 1 (candidate)")
	}

	// Verify prompt does NOT contain the 3 most recent sessions' bodies
	for _, i := range []int{2, 3, 4} {
		needle := fmt.Sprintf("Summary body for session %d", i)
		if strings.Contains(mock.lastPrompt, needle) {
			t.Errorf("prompt should NOT contain body of session %d (recent, not candidate)", i)
		}
	}

	// Verify timeline was written
	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d timeline entries, want 2", len(entries))
	}
	if entries[0].Summary != "Distilled session 0" {
		t.Errorf("entry[0].Summary = %q, want %q", entries[0].Summary, "Distilled session 0")
	}
	if entries[1].Summary != "Distilled session 1" {
		t.Errorf("entry[1].Summary = %q, want %q", entries[1].Summary, "Distilled session 1")
	}
}

func TestConsolidate_WithExistingTimeline(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// Write an existing timeline
	existingEntries := []TimelineEntry{
		{Timestamp: time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC), Summary: "Pre-existing event"},
	}
	if err := WriteTimeline(root, existingEntries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// Mock returns merged output including the pre-existing entry
	mockOutput := strings.Join([]string{
		"- 2025-12-15T00:00:00Z: Pre-existing event",
		"- 2026-01-01T00:00:00Z: Distilled session 0",
		"- 2026-01-02T00:00:00Z: Distilled session 1",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	// Verify prompt includes existing timeline text
	if !strings.Contains(mock.lastPrompt, "Pre-existing event") {
		t.Error("prompt should include existing timeline entries")
	}

	// Verify updated timeline
	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d timeline entries, want 3", len(entries))
	}
	if entries[0].Summary != "Pre-existing event" {
		t.Errorf("entry[0].Summary = %q, want %q", entries[0].Summary, "Pre-existing event")
	}
}

func TestConsolidate_PromptOnlyIncludesCandidates(t *testing.T) {
	root := t.TempDir()
	_, bodies := createTestSessions(t, root, 6)

	// Sessions 0,1,2 are candidates; 3,4,5 are the 3 most recent.
	mockOutput := "- 2026-01-01T00:00:00Z: Consolidated"

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	// Candidates (sessions 0-2) should be in the prompt
	for i := range 3 {
		if !strings.Contains(mock.lastPrompt, bodies[i]) {
			t.Errorf("prompt should contain body of candidate session %d", i)
		}
	}

	// Recent sessions (3-5) should NOT be in the prompt
	for i := 3; i < 6; i++ {
		if strings.Contains(mock.lastPrompt, bodies[i]) {
			t.Errorf("prompt should NOT contain body of recent session %d", i)
		}
	}
}

func TestConsolidate_ClaudeError(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// Write an existing timeline to verify it's unchanged after error
	existingEntries := []TimelineEntry{
		{Timestamp: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), Summary: "Should survive"},
	}
	if err := WriteTimeline(root, existingEntries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	mock := &mockClaudeInvoker{err: fmt.Errorf("api unavailable")}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err == nil {
		t.Fatal("expected error when invoker fails")
	}
	if !strings.Contains(err.Error(), "api unavailable") {
		t.Errorf("error should contain invoker error, got: %v", err)
	}

	// Timeline should be unchanged
	entries, readErr := ReadTimeline(root)
	if readErr != nil {
		t.Fatalf("ReadTimeline: %v", readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d timeline entries, want 1 (unchanged)", len(entries))
	}
	if entries[0].Summary != "Should survive" {
		t.Errorf("timeline entry should be unchanged, got %q", entries[0].Summary)
	}
}

func TestConsolidate_MalformedOutputSkipped(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// Mix of valid and invalid lines
	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: Valid entry one",
		"This is garbage",
		"- not-a-timestamp: Also garbage",
		"- 2026-01-02T00:00:00Z: Valid entry two",
		"",
		"random noise",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d timeline entries, want 2 (only valid lines)", len(entries))
	}
	if entries[0].Summary != "Valid entry one" {
		t.Errorf("entry[0].Summary = %q, want %q", entries[0].Summary, "Valid entry one")
	}
	if entries[1].Summary != "Valid entry two" {
		t.Errorf("entry[1].Summary = %q, want %q", entries[1].Summary, "Valid entry two")
	}
}

func TestConsolidate_AllMalformedWithExistingTimeline(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// Write an existing timeline
	existingEntries := []TimelineEntry{
		{Timestamp: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), Summary: "Must not be lost"},
	}
	if err := WriteTimeline(root, existingEntries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// All garbage output
	mockOutput := strings.Join([]string{
		"Here is some text that doesn't match",
		"Another garbage line",
		"No timeline entries at all",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err == nil {
		t.Fatal("expected error when all output is malformed and existing timeline is non-empty")
	}

	// Timeline should be unchanged (safety measure)
	entries, readErr := ReadTimeline(root)
	if readErr != nil {
		t.Fatalf("ReadTimeline: %v", readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d timeline entries, want 1 (unchanged)", len(entries))
	}
	if entries[0].Summary != "Must not be lost" {
		t.Errorf("timeline should be unchanged, got %q", entries[0].Summary)
	}
}

func TestConsolidate_RecurringAndPainPointMarkers(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: [recurring] Build failures on CI",
		"- 2026-01-02T00:00:00Z: [pain-point] Slow test suite taking 10min+",
		"- 2026-01-03T00:00:00Z: Normal entry without markers",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d timeline entries, want 3", len(entries))
	}
	if !strings.Contains(entries[0].Summary, "[recurring]") {
		t.Errorf("entry[0] should preserve [recurring] marker, got %q", entries[0].Summary)
	}
	if !strings.Contains(entries[1].Summary, "[pain-point]") {
		t.Errorf("entry[1] should preserve [pain-point] marker, got %q", entries[1].Summary)
	}
	if entries[2].Summary != "Normal entry without markers" {
		t.Errorf("entry[2].Summary = %q, want %q", entries[2].Summary, "Normal entry without markers")
	}
}

func TestConsolidate_NoSessionsDir(t *testing.T) {
	root := t.TempDir()
	// Do NOT create any sessions directory

	mock := &mockClaudeInvoker{response: "should not be called"}
	err := Consolidate(context.Background(), root, mock, nil, consolidateTestNow())
	if err != nil {
		t.Fatalf("expected nil error for missing sessions dir, got: %v", err)
	}
	if mock.lastPrompt != "" {
		t.Error("invoker should not have been called when sessions dir doesn't exist")
	}
}

func TestParseTimelineOutput(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantEntries   int
		wantSkipped   int
		wantSummaries []string
	}{
		{
			name:          "all valid",
			raw:           "- 2026-01-01T00:00:00Z: First\n- 2026-01-02T00:00:00Z: Second",
			wantEntries:   2,
			wantSkipped:   0,
			wantSummaries: []string{"First", "Second"},
		},
		{
			name:          "mixed valid and invalid",
			raw:           "- 2026-01-01T00:00:00Z: Valid\ngarbage\n- bad-ts: Also bad\n- 2026-01-03T00:00:00Z: Also valid",
			wantEntries:   2,
			wantSkipped:   2,
			wantSummaries: []string{"Valid", "Also valid"},
		},
		{
			name:        "all invalid",
			raw:         "no entries here\njust text\nnothing useful",
			wantEntries: 0,
			wantSkipped: 3,
		},
		{
			name:        "empty string",
			raw:         "",
			wantEntries: 0,
			wantSkipped: 0,
		},
		{
			name:          "blank lines skipped without counting",
			raw:           "- 2026-01-01T00:00:00Z: Entry\n\n\n- 2026-01-02T00:00:00Z: Another",
			wantEntries:   2,
			wantSkipped:   0,
			wantSummaries: []string{"Entry", "Another"},
		},
		{
			name:          "with markers",
			raw:           "- 2026-01-01T00:00:00Z: [recurring] Build flakes\n- 2026-01-02T00:00:00Z: [pain-point] Slow deploys",
			wantEntries:   2,
			wantSkipped:   0,
			wantSummaries: []string{"[recurring] Build flakes", "[pain-point] Slow deploys"},
		},
		{
			name:          "summary with colons",
			raw:           "- 2026-01-01T00:00:00Z: Error: something went wrong: details",
			wantEntries:   1,
			wantSkipped:   0,
			wantSummaries: []string{"Error: something went wrong: details"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entries, skipped := parseTimelineOutput(tt.raw)
			if len(entries) != tt.wantEntries {
				t.Errorf("got %d entries, want %d", len(entries), tt.wantEntries)
			}
			if skipped != tt.wantSkipped {
				t.Errorf("got %d skipped, want %d", skipped, tt.wantSkipped)
			}
			for i, want := range tt.wantSummaries {
				if i >= len(entries) {
					break
				}
				if entries[i].Summary != want {
					t.Errorf("entry[%d].Summary = %q, want %q", i, entries[i].Summary, want)
				}
			}
		})
	}
}

func TestBuildConsolidationPrompt(t *testing.T) {
	existingTimeline := []TimelineEntry{
		{Timestamp: time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC), Summary: "Old event"},
	}

	sessions := []Session{
		{
			SessionID: "sess-0",
			Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Handoff:   false,
		},
		{
			SessionID: "sess-1",
			Timestamp: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			Handoff:   true,
		},
	}
	bodies := []string{
		"Body of session zero",
		"Body of session one",
	}

	prompt := buildConsolidationPrompt(existingTimeline, sessions, bodies)

	// Verify the prompt contains existing timeline info
	if !strings.Contains(prompt, "Old event") {
		t.Error("prompt should contain existing timeline entry")
	}
	if !strings.Contains(prompt, "2025-12-15T00:00:00Z") {
		t.Error("prompt should contain existing timeline timestamp")
	}

	// Verify the prompt contains candidate session bodies
	if !strings.Contains(prompt, "Body of session zero") {
		t.Error("prompt should contain session 0 body")
	}
	if !strings.Contains(prompt, "Body of session one") {
		t.Error("prompt should contain session 1 body")
	}

	// Verify the prompt contains session IDs
	if !strings.Contains(prompt, "sess-0") {
		t.Error("prompt should contain session 0 ID")
	}
	if !strings.Contains(prompt, "sess-1") {
		t.Error("prompt should contain session 1 ID")
	}

	// Verify the prompt mentions the expected output format
	if !strings.Contains(prompt, "RFC3339") || !strings.Contains(prompt, "- ") {
		t.Error("prompt should describe expected output format with RFC3339 timestamps and '- ' prefix")
	}
}

func TestBuildConsolidationPrompt_NoExistingTimeline(t *testing.T) {
	sessions := []Session{
		{
			SessionID: "sess-0",
			Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	bodies := []string{"Session zero body"}

	prompt := buildConsolidationPrompt(nil, sessions, bodies)

	// Should still work without existing timeline
	if !strings.Contains(prompt, "Session zero body") {
		t.Error("prompt should contain session body even without existing timeline")
	}

	// Should not crash or contain "nil" for empty timeline
	if strings.Contains(prompt, "<nil>") {
		t.Error("prompt should not contain <nil> for empty timeline")
	}
}

// --- Compression/Pruning Integration Tests (QUM-100) ---

func TestConsolidate_CompressAndPrune_Integration(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// now is April 1, 2026
	now := func() time.Time { return time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) }

	// Config: entries older than 14 days compressed weekly, older than 60 days monthly
	cfg := &TimelineCompressionConfig{
		WeeklySummaryAge:  14 * 24 * time.Hour,
		MonthlySummaryAge: 60 * 24 * time.Hour,
		MaxEntries:        200,
		MaxSizeChars:      50000,
	}

	// Write existing timeline with an old entry (6 months ago)
	existingEntries := []TimelineEntry{
		{Timestamp: time.Date(2025, 10, 15, 0, 0, 0, 0, time.UTC), Summary: "Ancient event from October"},
	}
	if err := WriteTimeline(root, existingEntries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// Mock Claude output with entries spanning different time ranges:
	// - Recent (within 14 days): kept individually
	// - Medium age (14-60 days): compressed weekly
	// - Old (60+ days): compressed monthly (merged with existing)
	mockOutput := strings.Join([]string{
		"- 2025-10-15T00:00:00Z: Ancient event from October", // existing, 6mo old -> monthly
		"- 2026-02-10T00:00:00Z: February event A",           // ~49 days old -> weekly
		"- 2026-02-12T00:00:00Z: February event B",           // ~47 days old -> same week
		"- 2026-03-25T00:00:00Z: Recent event A",             // 7 days old -> kept
		"- 2026-03-28T00:00:00Z: Recent event B",             // 4 days old -> kept
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, cfg, now)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	// Recent entries should be preserved individually
	var recentCount int
	for _, e := range entries {
		if strings.Contains(e.Summary, "Recent event") {
			recentCount++
		}
	}
	if recentCount != 2 {
		t.Errorf("expected 2 recent entries preserved individually, got %d", recentCount)
	}

	// The old October entry should be compressed into a monthly summary
	var hasMonthly bool
	for _, e := range entries {
		if strings.Contains(e.Summary, "[October 2025]") || strings.Contains(e.Summary, "Ancient event") {
			hasMonthly = true
			break
		}
	}
	if !hasMonthly {
		t.Error("expected old October entry to be compressed into monthly summary or preserved")
	}

	// The February entries should be compressed into a weekly summary
	var hasWeekly bool
	for _, e := range entries {
		if strings.Contains(e.Summary, "[Week of") && strings.Contains(e.Summary, "February") {
			hasWeekly = true
			break
		}
	}
	if !hasWeekly {
		t.Error("expected February entries to be compressed into weekly summary")
	}
}

func TestConsolidate_CustomConfig_AggressivePruning(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	now := func() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) }

	// Very tight config: only 3 max entries
	cfg := &TimelineCompressionConfig{
		WeeklySummaryAge:  30 * 24 * time.Hour,
		MonthlySummaryAge: 90 * 24 * time.Hour,
		MaxEntries:        3,
		MaxSizeChars:      50000,
	}

	// Mock Claude output with 6 entries — all recent so compression is no-op,
	// but pruning should enforce MaxEntries=3.
	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: Entry A",
		"- 2026-01-02T00:00:00Z: Entry B",
		"- 2026-01-03T00:00:00Z: [recurring] Tagged entry C",
		"- 2026-01-04T00:00:00Z: Entry D",
		"- 2026-01-05T00:00:00Z: Entry E",
		"- 2026-01-06T00:00:00Z: Entry F",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, cfg, now)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	// MaxEntries=3, so we should get at most 3 real entries + possible omission note
	realEntries := 0
	hasOmission := false
	for _, e := range entries {
		if strings.HasPrefix(e.Summary, "[...") && strings.HasSuffix(e.Summary, "entries omitted]") {
			hasOmission = true
		} else {
			realEntries++
		}
	}

	if realEntries > 3 {
		t.Errorf("expected at most 3 real entries after pruning, got %d", realEntries)
	}
	if !hasOmission {
		t.Error("expected omission note since entries were dropped")
	}

	// Tagged entry should be preserved preferentially
	var taggedPreserved bool
	for _, e := range entries {
		if strings.Contains(e.Summary, "[recurring]") {
			taggedPreserved = true
			break
		}
	}
	if !taggedPreserved {
		t.Error("expected tagged [recurring] entry to be preserved preferentially")
	}
}

func TestConsolidate_NoUnnecessaryWrite(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	now := func() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) }

	// Write existing timeline
	existingEntries := []TimelineEntry{
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Summary: "Entry A"},
		{Timestamp: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Summary: "Entry B"},
	}
	if err := WriteTimeline(root, existingEntries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// Get the file's mod time before consolidation
	tlPath := timelinePath(root)
	infoBefore, err := os.Stat(tlPath)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	// Mock Claude returns exactly the same entries
	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: Entry A",
		"- 2026-01-02T00:00:00Z: Entry B",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err = Consolidate(context.Background(), root, mock, nil, now)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	// Verify timeline content is unchanged
	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Check file wasn't rewritten (mod time check — may be flaky on fast systems)
	infoAfter, err := os.Stat(tlPath)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Error("timeline file was rewritten even though content didn't change")
	}
}

func TestConsolidate_MissingTimeline_CompressionStillRuns(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	// now = April 2026, entries from Jan 2026 are ~90 days old
	now := func() time.Time { return time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) }

	cfg := &TimelineCompressionConfig{
		WeeklySummaryAge:  14 * 24 * time.Hour,
		MonthlySummaryAge: 60 * 24 * time.Hour,
		MaxEntries:        200,
		MaxSizeChars:      50000,
	}

	// No existing timeline.md — it doesn't exist
	// Mock Claude output with old entries that should be compressed
	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: Old event A",
		"- 2026-01-03T00:00:00Z: Old event B",
		"- 2026-01-04T00:00:00Z: Old event C",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, cfg, now)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	// All entries are >60 days old, so they should be compressed into monthly summaries
	// 3 individual entries should become 1 monthly summary for January 2026
	if len(entries) != 1 {
		t.Fatalf("expected 1 compressed entry (monthly summary), got %d entries: %v", len(entries), entries)
	}
	if !strings.Contains(entries[0].Summary, "[January 2026]") {
		t.Errorf("expected monthly summary prefix, got %q", entries[0].Summary)
	}
}

func TestConsolidate_MergeDeduplicatesOverlap(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	now := consolidateTestNow()

	// Write existing timeline with one entry
	existingEntries := []TimelineEntry{
		{Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Summary: "Shared entry"},
	}
	if err := WriteTimeline(root, existingEntries); err != nil {
		t.Fatalf("WriteTimeline: %v", err)
	}

	// Mock Claude returns the same entry (overlap) plus a new one
	mockOutput := strings.Join([]string{
		"- 2026-01-01T00:00:00Z: Shared entry",
		"- 2026-01-05T00:00:00Z: New entry from Claude",
	}, "\n")

	mock := &mockClaudeInvoker{response: mockOutput}
	err := Consolidate(context.Background(), root, mock, nil, now)
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}

	// Should have 2 entries (deduplicated), not 3
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (deduplicated), got %d: %v", len(entries), entries)
	}
	if entries[0].Summary != "Shared entry" {
		t.Errorf("entry[0] = %q, want %q", entries[0].Summary, "Shared entry")
	}
	if entries[1].Summary != "New entry from Claude" {
		t.Errorf("entry[1] = %q, want %q", entries[1].Summary, "New entry from Claude")
	}
}

func TestConsolidate_NilConfig_UsesDefaults(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	now := consolidateTestNow()

	mockOutput := "- 2026-01-01T00:00:00Z: Test entry"
	mock := &mockClaudeInvoker{response: mockOutput}

	// cfg=nil should use DefaultTimelineCompressionConfig and not panic
	err := Consolidate(context.Background(), root, mock, nil, now)
	if err != nil {
		t.Fatalf("Consolidate with nil config: %v", err)
	}

	entries, err := ReadTimeline(root)
	if err != nil {
		t.Fatalf("ReadTimeline: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected at least one entry in timeline")
	}
}

func TestConsolidate_NilNow_UsesTimeNow(t *testing.T) {
	root := t.TempDir()
	createTestSessions(t, root, 5)

	mockOutput := "- 2026-01-01T00:00:00Z: Test entry"
	mock := &mockClaudeInvoker{response: mockOutput}

	// now=nil should use time.Now and not panic
	err := Consolidate(context.Background(), root, mock, nil, nil)
	if err != nil {
		t.Fatalf("Consolidate with nil now: %v", err)
	}
}
