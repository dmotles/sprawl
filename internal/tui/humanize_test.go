package tui

import (
	"testing"
	"time"
)

func TestHumanizeSince_Buckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},
		{30 * time.Second, "30s"},
		{59 * time.Second, "59s"},
		{time.Minute, "1m"},
		{90 * time.Second, "2m"},
		{59 * time.Minute, "59m"},
		{time.Hour, "1h"},
		{90 * time.Minute, "2h"},
		{15 * time.Hour, "15h"},
		// Deliberate: the hour bucket rounds, so the last renderable value
		// below a day is "24h". The jump 24h -> "1d" is a real, reachable
		// discontinuity in the existing humanizer, pinned here rather than
		// silently inherited.
		{23*time.Hour + 59*time.Minute, "24h"},
		{24 * time.Hour, "1d"},
		// 36h discriminates the day rule: truncation gives 1d, rounding 2d.
		{36 * time.Hour, "1d"},
		{50 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := HumanizeSince(c.d); got != c.want {
			t.Errorf("HumanizeSince(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	if got := Ago(time.Time{}, now); got != "" {
		t.Errorf("Ago(zero) = %q, want empty (never-observed is not an age)", got)
	}
	if got := Ago(now.Add(-15*time.Hour), now); got != "15h ago" {
		t.Errorf("Ago(now-15h) = %q, want %q", got, "15h ago")
	}
	if got := Ago(now.Add(-3*time.Minute), now); got != "3m ago" {
		t.Errorf("Ago(now-3m) = %q, want %q", got, "3m ago")
	}
	// Clock skew must not render a negative or nonsense age.
	if got := Ago(now.Add(5*time.Minute), now); got != "0s ago" {
		t.Errorf("Ago(future) = %q, want %q", got, "0s ago")
	}
}
