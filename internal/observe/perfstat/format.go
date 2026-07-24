package perfstat

import (
	"fmt"
	"math"
	"time"
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
