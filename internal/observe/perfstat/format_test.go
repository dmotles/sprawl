package perfstat

import (
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// FormatDuration renders 3 significant digits in the largest unit that keeps
// the value >= 1. A negative duration is the only "no value" case — a real
// 0-duration frame is a legitimate sample, so 0 formats as a number.
func TestFormatDuration(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{-1 * time.Millisecond, "—"},
		{0, "0ns"},
		{500 * time.Nanosecond, "500ns"},
		{999 * time.Nanosecond, "999ns"},
		{1 * time.Microsecond, "1.00µs"},
		{820 * time.Microsecond, "820µs"},
		{999 * time.Microsecond, "999µs"},
		{1000 * time.Microsecond, "1.00ms"},
		{12_400 * time.Microsecond, "12.4ms"},
		{999 * time.Millisecond, "999ms"},
		{1 * time.Second, "1.00s"},
		{999_600 * time.Nanosecond, "1.00ms"}, // rounding must not print "1000µs"
		{1240 * time.Millisecond, "1.24s"},
		{90 * time.Second, "90.0s"},
		{3600 * time.Second, "3600s"}, // seconds is the largest unit
	}
	for _, tt := range tests {
		if got := FormatDuration(tt.in); got != tt.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatPercent(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0.0%"},
		{0.0421, "4.2%"},
		{0.97345, "97.3%"},
		{0.9756, "97.6%"},
		{1, "100.0%"},
		{-0.5, "0.0%"},
		{1.5, "100.0%"},
		{math.NaN(), "—"},
	}
	for _, tt := range tests {
		if got := FormatPercent(tt.in); got != tt.want {
			t.Errorf("FormatPercent(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestFormatDurationCol_UnitFlipStaysAligned pins the property the /perf
// overlay's timing column depends on: a µs→ms flip must read as a magnitude
// change, not as jitter. That only works if every cell occupies the same
// number of DISPLAY COLUMNS, so the padding is counted in runes.
//
// The byte-vs-rune leg is the one with teeth: "µ" is U+00B5, two bytes and one
// column, so padding computed with len() silently under-pads every microsecond
// row by one and mis-aligns exactly the rows the operator is comparing.
func TestFormatDurationCol_UnitFlipStaysAligned(t *testing.T) {
	const width = 8
	cases := []time.Duration{
		490 * time.Microsecond,   // the healthy reading: µs, 2-byte unit rune
		51300 * time.Microsecond, // the pathological reading: ms
		1200 * time.Microsecond,  // just over the promotion boundary
		999 * time.Nanosecond,    // sub-µs: ns
		0,                        // a real sample of zero
		-1,                       // no measurement at all
		5 * time.Second,          // widest natural rendering
	}
	for _, d := range cases {
		got := FormatDurationCol(d, width)
		if n := utf8.RuneCountInString(got); n != width {
			t.Errorf("FormatDurationCol(%v, %d) = %q, %d display columns, want %d",
				d, width, got, n, width)
		}
		if strings.TrimSpace(got) != FormatDuration(d) {
			t.Errorf("FormatDurationCol(%v, %d) = %q, trims to %q, want %q — padding only",
				d, width, got, strings.TrimSpace(got), FormatDuration(d))
		}
		if strings.HasSuffix(got, " ") {
			t.Errorf("FormatDurationCol(%v, %d) = %q, want right-aligned (no trailing pad)",
				d, width, got)
		}
	}

	// The regression leg. A µs cell and an ms cell must agree on width; under a
	// len()-based implementation the µs cell is one byte longer and this fails.
	us := FormatDurationCol(490*time.Microsecond, width)
	ms := FormatDurationCol(51300*time.Microsecond, width)
	if utf8.RuneCountInString(us) != utf8.RuneCountInString(ms) {
		t.Errorf("µs cell %q (%d cols) and ms cell %q (%d cols) disagree — the unit flip reads as jitter",
			us, utf8.RuneCountInString(us), ms, utf8.RuneCountInString(ms))
	}
	if len(us) == len(ms) {
		t.Errorf("byte lengths of %q and %q are equal (%d); this test's µs case no longer "+
			"exercises the multi-byte rune, so it cannot catch a len()-based regression", us, ms, len(us))
	}

	// A width narrower than the content must not truncate a measurement: a
	// clipped number is a wrong number, and this column exists to be read.
	if got := FormatDurationCol(51300*time.Microsecond, 2); got != FormatDuration(51300*time.Microsecond) {
		t.Errorf("FormatDurationCol(51.3ms, 2) = %q, want the unpadded %q — never truncate a reading",
			got, FormatDuration(51300*time.Microsecond))
	}
}
