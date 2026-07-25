package tui

import (
	"testing"
	"time"
)

// QUM-934 — ChatRegion frame timing.
//
// The /perf overlay reports frame-time percentiles for the CHAT REGION, which
// is the surface the render cache actually protects. These tests pin what gets
// timed and, just as importantly, what does not.
//
// The clock is injected so the assertions are exact rather than "> 0". A
// duration test that only asserts positivity passes against a stopwatch that
// measures the wrong span.

// fakeClock advances by a fixed step on every reading, so one timed span reads
// as exactly step.
type fakeClock struct {
	now  time.Time
	step time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.now = c.now.Add(c.step)
	return c.now
}

func newTimedTestChatRegion(t *testing.T, step time.Duration) (*ChatRegion, *fakeClock) {
	t.Helper()
	theme := NewTheme("colour212")
	r := NewChatRegion(&theme)
	clk := &fakeClock{now: time.Date(2026, 7, 25, 6, 0, 0, 0, time.UTC), step: step}
	r.nowFn = clk.Now
	r.SetSize(80, 24)
	r.ChatList().AppendUser("hello")
	return r, clk
}

func TestChatRegion_LastRenderDurIsUnsetBeforeAnyView(t *testing.T) {
	r, _ := newTimedTestChatRegion(t, 5*time.Millisecond)
	if got := r.LastRenderDur(); got != 0 {
		t.Errorf("LastRenderDur() = %v before any View(), want 0", got)
	}
}

func TestChatRegion_ViewRecordsItsDuration(t *testing.T) {
	r, _ := newTimedTestChatRegion(t, 5*time.Millisecond)
	r.View()
	if got, want := r.LastRenderDur(), 5*time.Millisecond; got != want {
		t.Errorf("LastRenderDur() = %v after a build, want %v", got, want)
	}
}

// TestChatRegion_CacheHitIsTimedToo: the cheap path must be sampled as well.
// Timing only rebuilds would report the percentiles of the slow path as though
// they described the app — a distribution that omits every fast frame is not a
// distribution of frames.
func TestChatRegion_CacheHitIsTimedToo(t *testing.T) {
	r, _ := newTimedTestChatRegion(t, 3*time.Millisecond)
	r.View() // build
	buildsAfterFirst := r.viewBuilds
	r.View() // cache hit: no rebuild
	if r.viewBuilds != buildsAfterFirst {
		t.Fatalf("second View() rebuilt (%d -> %d); this test is no longer exercising the cache-hit path",
			buildsAfterFirst, r.viewBuilds)
	}
	if got, want := r.LastRenderDur(), 3*time.Millisecond; got != want {
		t.Errorf("LastRenderDur() = %v after a cache hit, want %v — the fast path must be sampled",
			got, want)
	}
}

// TestChatRegion_UnsizedViewIsNotTimed: a width-0 View() paints nothing and
// returns immediately. Recording it would inject a fabricated ~0 sample into
// the percentiles for a frame that never rendered — the same fabricated-zero
// problem the overlay guards against, here on the collection side.
func TestChatRegion_UnsizedViewIsNotTimed(t *testing.T) {
	r, _ := newTimedTestChatRegion(t, 7*time.Millisecond)
	r.View()
	before := r.LastRenderDur()

	r.SetSize(0, 24)
	if got := r.View(); got != "" {
		t.Fatalf("View() = %q at width 0, want empty; this test is not exercising the guard", got)
	}
	if got := r.LastRenderDur(); got != before {
		t.Errorf("LastRenderDur() = %v after an unsized View(), want it unchanged at %v", got, before)
	}
}
