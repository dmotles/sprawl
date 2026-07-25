package perfstat

import (
	"math"
	"testing"
	"time"
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
