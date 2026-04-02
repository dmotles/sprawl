package memory

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultBudgetConfig(t *testing.T) {
	cfg := DefaultBudgetConfig()
	if cfg.MaxTotalChars != 10000 {
		t.Errorf("MaxTotalChars = %d, want 10000", cfg.MaxTotalChars)
	}
	if cfg.MaxSessionChars != 2000 {
		t.Errorf("MaxSessionChars = %d, want 2000", cfg.MaxSessionChars)
	}
}

func TestDefaultTimelineCompressionConfig(t *testing.T) {
	cfg := DefaultTimelineCompressionConfig()
	if cfg.WeeklySummaryAge != 30*24*time.Hour {
		t.Errorf("WeeklySummaryAge = %v, want %v", cfg.WeeklySummaryAge, 30*24*time.Hour)
	}
	if cfg.MonthlySummaryAge != 90*24*time.Hour {
		t.Errorf("MonthlySummaryAge = %v, want %v", cfg.MonthlySummaryAge, 90*24*time.Hour)
	}
	if cfg.MaxEntries != 200 {
		t.Errorf("MaxEntries = %d, want 200", cfg.MaxEntries)
	}
	if cfg.MaxSizeChars != 50000 {
		t.Errorf("MaxSizeChars = %d, want 50000", cfg.MaxSizeChars)
	}
}

func TestMeasureBytes_ASCII(t *testing.T) {
	if got := MeasureBytes("hello"); got != 5 {
		t.Errorf("MeasureBytes(%q) = %d, want 5", "hello", got)
	}
}

func TestMeasureBytes_Empty(t *testing.T) {
	if got := MeasureBytes(""); got != 0 {
		t.Errorf("MeasureBytes(%q) = %d, want 0", "", got)
	}
}

func TestMeasureBytes_MultiByte(t *testing.T) {
	// "café" has 5 bytes in UTF-8 (é is 2 bytes)
	if got := MeasureBytes("café"); got != 5 {
		t.Errorf("MeasureBytes(%q) = %d, want 5", "café", got)
	}
}

func TestMeasureBytes_Emoji(t *testing.T) {
	// 👍 is 4 bytes in UTF-8
	if got := MeasureBytes("👍"); got != 4 {
		t.Errorf("MeasureBytes(%q) = %d, want 4", "👍", got)
	}
}

func TestTruncateWithNote_UnderLimit(t *testing.T) {
	s := "short"
	got := TruncateWithNote(s, 100)
	if got != s {
		t.Errorf("TruncateWithNote(%q, 100) = %q, want %q", s, got, s)
	}
}

func TestTruncateWithNote_ExactlyAtLimit(t *testing.T) {
	s := "exact"
	got := TruncateWithNote(s, len(s))
	if got != s {
		t.Errorf("TruncateWithNote(%q, %d) = %q, want %q", s, len(s), got, s)
	}
}

func TestTruncateWithNote_OneOverLimit(t *testing.T) {
	s := strings.Repeat("a", 30)
	maxChars := 29
	got := TruncateWithNote(s, maxChars)
	if len(got) != maxChars {
		t.Errorf("len(result) = %d, want %d", len(got), maxChars)
	}
	if !strings.HasSuffix(got, "\n[...truncated]") {
		t.Errorf("result should end with truncation note, got %q", got)
	}
}

func TestTruncateWithNote_LargeOverage(t *testing.T) {
	s := strings.Repeat("x", 10000)
	maxChars := 100
	got := TruncateWithNote(s, maxChars)
	if len(got) != maxChars {
		t.Errorf("len(result) = %d, want %d", len(got), maxChars)
	}
	if !strings.HasSuffix(got, "\n[...truncated]") {
		t.Errorf("result should end with truncation note, got %q", got)
	}
}

func TestTruncateWithNote_EmptyString(t *testing.T) {
	got := TruncateWithNote("", 100)
	if got != "" {
		t.Errorf("TruncateWithNote(%q, 100) = %q, want %q", "", got, "")
	}
}

func TestTruncateWithNote_MaxCharsLessThanNote(t *testing.T) {
	s := strings.Repeat("a", 100)
	// The note "\n[...truncated]" is 15 bytes. Use maxChars < 15.
	maxChars := 5
	got := TruncateWithNote(s, maxChars)
	if len(got) != maxChars {
		t.Errorf("len(result) = %d, want %d", len(got), maxChars)
	}
	// Should be best-effort truncation without the note
	if got != "aaaaa" {
		t.Errorf("TruncateWithNote result = %q, want %q", got, "aaaaa")
	}
}

func TestTruncateWithNote_MaxCharsEqualsNote(t *testing.T) {
	note := "\n[...truncated]"
	s := strings.Repeat("b", 100)
	maxChars := len(note)
	got := TruncateWithNote(s, maxChars)
	// maxChars equals note length: content would be 0 bytes + note
	if len(got) != maxChars {
		t.Errorf("len(result) = %d, want %d", len(got), maxChars)
	}
	if got != note {
		t.Errorf("TruncateWithNote result = %q, want %q", got, note)
	}
}

func TestTruncateWithNote_MaxCharsZero(t *testing.T) {
	got := TruncateWithNote("some text", 0)
	if got != "" {
		t.Errorf("TruncateWithNote with maxChars=0 = %q, want %q", got, "")
	}
}
