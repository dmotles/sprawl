package perfstat

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// noValue is rendered for inputs that carry no measurement.
const noValue = "—"

// FormatDuration renders d with three significant digits in the largest unit
// that keeps the value at or above 1. A negative duration has no measurement;
// zero is a legitimate sample and renders as a number.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		return noValue
	}
	if d < time.Microsecond {
		return fmt.Sprintf("%dns", int64(d))
	}
	units := []struct {
		scale  float64
		suffix string
	}{
		{float64(time.Microsecond), "µs"},
		{float64(time.Millisecond), "ms"},
		{float64(time.Second), "s"},
	}
	for i, u := range units {
		v := float64(d) / u.scale
		// Promote when rounding would overflow the unit (999.6µs is 1.00ms).
		if v >= 999.5 && i < len(units)-1 {
			continue
		}
		return significant3(v) + u.suffix
	}
	return noValue // unreachable: the loop always returns on its last unit
}

// FormatPercent renders a fraction in [0,1] as a percentage with one decimal.
// Out-of-range values are clamped; NaN has no measurement.
func FormatPercent(f float64) string {
	if math.IsNaN(f) {
		return noValue
	}
	f = math.Min(math.Max(f, 0), 1)
	return fmt.Sprintf("%.1f%%", f*100)
}

func significant3(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

// FormatDurationCol renders d right-aligned in a fixed-width column.
//
// The alignment is the whole point: across the boundary this package exists to
// detect, a healthy frame reads "490µs" and a pathological one "51.3ms". Those
// differ by two orders of magnitude, but in a ragged column the eye compares
// the digits — 490 against 51.3 — and reads the pathology as smaller. Fixed
// width puts the unit in the same place every row, so the flip is what moves.
//
// Padded in RUNES, never bytes. "µ" (U+00B5) is two bytes and one display
// column, and the no-measurement "—" is three bytes and one column, so a
// len()-based pad silently under-pads exactly the µs and no-value rows —
// mis-aligning the two readings the operator is comparing. Guarded by the
// byte-vs-rune leg of TestFormatDurationCol_UnitFlipStaysAligned.
//
// A width narrower than the content returns the unpadded rendering rather than
// truncating: a clipped measurement is a wrong measurement, and a column too
// narrow to hold its data should look wrong rather than read plausibly.
func FormatDurationCol(d time.Duration, width int) string {
	s := FormatDuration(d)
	if pad := width - utf8.RuneCountInString(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}
