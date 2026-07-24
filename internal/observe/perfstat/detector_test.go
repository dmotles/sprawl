package perfstat

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// enabledCfg is the tuned default config with the debug gate opened, which is
// what these tests exercise.
func enabledCfg() DetectorConfig {
	cfg := DefaultDetectorConfig()
	cfg.Enabled = true
	return cfg
}

// TestDefaultDetectorConfig_IsDisabled pins the debug gate: an agent wiring
// this up with DefaultDetectorConfig() must not silently ship an always-on
// detector.
func TestDefaultDetectorConfig_IsDisabled(t *testing.T) {
	if DefaultDetectorConfig().Enabled {
		t.Error("DefaultDetectorConfig().Enabled = true, want false (opt-in only)")
	}
}

// feeder drives a Detector with synthetic frames, mimicking how the TUI will
// call it: absolute cache counters that only ever move forward.
type feeder struct {
	t       *testing.T
	det     *Detector
	cache   CacheStats
	at      time.Time
	reports []Report
}

func newFeeder(t *testing.T, cfg DetectorConfig) *feeder {
	t.Helper()
	return &feeder{
		t:     t,
		det:   NewDetector(cfg),
		cache: CacheStats{Items: 200, Revision: 1000, Width: 120},
		at:    time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
	}
}

// frame feeds one frame. mutate adjusts the running cache counters first.
func (f *feeder) frame(streaming bool, mutate func(c *CacheStats)) (Report, bool) {
	f.t.Helper()
	if mutate != nil {
		mutate(&f.cache)
	}
	f.at = f.at.Add(16 * time.Millisecond)
	rep, ok := f.det.Observe(f.at, streaming, f.cache)
	if ok {
		f.reports = append(f.reports, rep)
	}
	return rep, ok
}

func (f *feeder) frames(n int, streaming bool, mutate func(c *CacheStats)) {
	f.t.Helper()
	for range n {
		f.frame(streaming, mutate)
	}
}

// defeat simulates the QUM-933 signature: a rebuild every frame with the
// revision pinned (nothing actually changed).
func defeat(c *CacheStats) { c.Misses++ }

// hit simulates a healthy cached frame.
func hit(c *CacheStats) { c.Hits++ }

// legitInvalidation simulates a rebuild caused by real content change — the
// revision moves, so the miss is expected, not a defeat.
func legitInvalidation(c *CacheStats) {
	c.Misses++
	c.Revision++
}

// quiet simulates an idle frame where no render lookup happened at all.
func quiet(*CacheStats) {}

func TestDetector_DisabledNeverTrips(t *testing.T) {
	cfg := enabledCfg()
	cfg.Enabled = false
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = 50
	f.frames(10_000, false, defeat)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports, want 0 while disabled", len(f.reports))
	}
	st := f.det.Status()
	if st.Enabled {
		t.Error("Status().Enabled = true, want false")
	}
	if st.Tripped {
		t.Error("Status().Tripped = true, want false")
	}
}

func TestDetector_TripsAtExactStreakOnIdleMisses(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	// Seed a healthy history so the reported hit rate is a real number.
	f.frames(10, false, hit)
	// This frame only establishes the miss baseline; the streak starts after it.
	f.frame(false, nil)
	for i := 1; i <= cfg.DefectStreak; i++ {
		_, ok := f.frame(false, defeat)
		if i < cfg.DefectStreak && ok {
			t.Fatalf("tripped early at defective frame %d (streak=%d)", i, cfg.DefectStreak)
		}
		if i == cfg.DefectStreak && !ok {
			t.Fatalf("did not trip at defective frame %d (streak=%d)", i, cfg.DefectStreak)
		}
	}
	rep := f.reports[0]
	if rep.Kind&DefectIdleMisses == 0 {
		t.Errorf("Kind = %v, want DefectIdleMisses bit set", rep.Kind)
	}
	if rep.Episode != 1 {
		t.Errorf("Episode = %d, want 1", rep.Episode)
	}
	if rep.IdleFrames != cfg.DefectStreak {
		t.Errorf("IdleFrames = %d, want %d", rep.IdleFrames, cfg.DefectStreak)
	}
	if rep.IdleMisses != uint64(cfg.DefectStreak) {
		t.Errorf("IdleMisses = %d, want %d", rep.IdleMisses, cfg.DefectStreak)
	}
	if rep.Zero() {
		t.Error("Zero() = true on a trip report, want false")
	}
	if !rep.At.Equal(f.at) {
		t.Errorf("At = %v, want %v", rep.At, f.at)
	}
	if want := f.cache.HitRate(); rep.HitRate != want {
		t.Errorf("HitRate = %v, want %v", rep.HitRate, want)
	}
	if !f.det.Status().Tripped {
		t.Error("Status().Tripped = false after trip, want true")
	}
}

func TestDetector_TripsOnUncacheableAboveLimit(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = cfg.UncacheableLimit + 5
	// No miss delta at all — the uncacheable count alone must trip.
	f.frames(cfg.DefectStreak+1, false, hit)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want exactly 1", len(f.reports))
	}
	rep := f.reports[0]
	if rep.Kind != DefectUncacheable {
		t.Errorf("Kind = %v, want DefectUncacheable only", rep.Kind)
	}
	if rep.Uncacheable != cfg.UncacheableLimit+5 {
		t.Errorf("Uncacheable = %d, want %d", rep.Uncacheable, cfg.UncacheableLimit+5)
	}
	if rep.Limit != cfg.UncacheableLimit {
		t.Errorf("Limit = %d, want %d", rep.Limit, cfg.UncacheableLimit)
	}
	if rep.IdleMisses != 0 {
		t.Errorf("IdleMisses = %d, want 0 (no rebuilds occurred)", rep.IdleMisses)
	}
}

// TestDetector_NewDetectorNormalizesConfig is a false-positive guard: a
// directly-constructed config with zero thresholds must not mean "trip on the
// first defective frame".
func TestDetector_NewDetectorNormalizesConfig(t *testing.T) {
	f := newFeeder(t, DetectorConfig{Enabled: true})
	def := enabledCfg()
	f.cache.Uncacheable = def.UncacheableLimit + 3
	f.frames(def.DefectStreak-1, false, defeat)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports before the default streak elapsed, want 0", len(f.reports))
	}
	f.frame(false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports at the default streak, want 1", len(f.reports))
	}
	if got := f.reports[0].Limit; got != def.UncacheableLimit {
		t.Errorf("Limit = %d, want the default %d", got, def.UncacheableLimit)
	}
}

func TestDetector_TripKindBitsCombine(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = cfg.UncacheableLimit + 1
	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	want := DefectIdleMisses | DefectUncacheable
	if got := f.reports[0].Kind; got != want {
		t.Errorf("Kind = %v (%d), want %v (%d)", got, got, want, want)
	}
	if got := want.String(); !strings.Contains(got, "idle-misses") || !strings.Contains(got, "uncacheable") {
		t.Errorf("Kind.String() = %q, want both defect names", got)
	}
}

func TestDetector_DiagnosisNamesCounts(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Items = 214
	f.cache.Uncacheable = 7
	f.cache.Revision = 1188
	f.cache.Width = 132
	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	rep := f.reports[0]
	if rep.Items != 214 || rep.Revision != 1188 || rep.Width != 132 {
		t.Errorf("report cache fields = items %d rev %d width %d, want 214/1188/132",
			rep.Items, rep.Revision, rep.Width)
	}
	diag := rep.Diagnosis()
	for _, want := range []string{
		"episode 1",
		fmt.Sprintf("limit %d", cfg.UncacheableLimit),
		fmt.Sprintf("%d of %d", 7, 214), // uncacheable of total items
		"revision 1188",                 // the pinned fingerprint
		"width 132",
		"idle-misses",
		"uncacheable",
	} {
		if !strings.Contains(diag, want) {
			t.Errorf("Diagnosis() = %q, missing %q", diag, want)
		}
	}
	if strings.Contains(diag, "\n") {
		t.Errorf("Diagnosis() = %q, want a single line", diag)
	}
	if got := (Report{}).Diagnosis(); got != "" {
		t.Errorf("zero Report Diagnosis() = %q, want empty", got)
	}
}

func TestDetector_ZeroReportForNoTrip(t *testing.T) {
	f := newFeeder(t, enabledCfg())
	rep, ok := f.frame(false, hit)
	if ok {
		t.Fatal("clean frame tripped")
	}
	if !rep.Zero() {
		t.Errorf("no-trip report = %+v, want zero value", rep)
	}
	if f.det.Status().HasReport {
		t.Error("Status().HasReport = true before any trip, want false")
	}
}

// --- No-trip (false positive) cases: the highest-value coverage here. ---

func TestDetector_NoTripWhileStreaming(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	// Everything that would look pathological at idle, but a turn is in flight:
	// per-frame rebuilds with a pinned revision AND many unfinished items.
	f.cache.Uncacheable = cfg.UncacheableLimit + 6
	f.frames(10_000, true, defeat)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports while streaming, want 0: %s", len(f.reports), f.reports[0].Diagnosis())
	}
	if f.det.Status().Tripped {
		t.Error("Status().Tripped = true while streaming, want false")
	}
}

func TestDetector_NoTripIdlePureHits(t *testing.T) {
	f := newFeeder(t, enabledCfg())
	f.frames(10_000, false, hit)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports on a healthy idle session, want 0", len(f.reports))
	}
}

func TestDetector_NoTripOneLegitimateInFlightItem(t *testing.T) {
	f := newFeeder(t, enabledCfg())
	f.cache.Uncacheable = 1
	f.frames(10_000, false, hit)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports with 1 unfinished item, want 0", len(f.reports))
	}
}

func TestDetector_UncacheableLimitBoundary(t *testing.T) {
	cfg := enabledCfg()

	atLimit := newFeeder(t, cfg)
	atLimit.cache.Uncacheable = cfg.UncacheableLimit
	atLimit.frames(10_000, false, hit)
	if len(atLimit.reports) != 0 {
		t.Errorf("Uncacheable == limit (%d) tripped; want silent", cfg.UncacheableLimit)
	}

	aboveLimit := newFeeder(t, cfg)
	aboveLimit.cache.Uncacheable = cfg.UncacheableLimit + 1
	aboveLimit.frames(cfg.DefectStreak+1, false, hit)
	if len(aboveLimit.reports) != 1 {
		t.Errorf("Uncacheable == limit+1 (%d) produced %d reports; want 1",
			cfg.UncacheableLimit+1, len(aboveLimit.reports))
	}
}

func TestDetector_NoTripIdleMissesWithRevisionBumps(t *testing.T) {
	// The highest-value false-positive case: idle rebuilds are legitimate when
	// content actually changed (appends, expand/collapse, keystroke redraws),
	// which the revision fingerprint reflects.
	f := newFeeder(t, enabledCfg())
	f.frames(10_000, false, legitInvalidation)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports on legitimate invalidations, want 0: %s",
			len(f.reports), f.reports[0].Diagnosis())
	}
}

// TestDetector_NoTripWidthChangeStorm covers a resize storm with the revision
// pinned: the width is part of the render-cache key, so a rebuild after a width
// change is legitimate even though nothing about the content changed.
func TestDetector_NoTripWidthChangeStorm(t *testing.T) {
	f := newFeeder(t, enabledCfg())
	for i := range 10_000 {
		f.frame(false, func(c *CacheStats) {
			c.Width = 60 + i%80
			c.Misses++ // revision stays pinned; only the width moved
		})
	}
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports during a resize storm, want 0: %s",
			len(f.reports), f.reports[0].Diagnosis())
	}
}

// TestDetector_NoTripOnQuietFrames covers idle frames where no render lookup
// happened at all — neither counter moves, so there is no evidence either way.
func TestDetector_NoTripOnQuietFrames(t *testing.T) {
	f := newFeeder(t, enabledCfg())
	f.frames(10_000, false, quiet)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports on quiet frames, want 0", len(f.reports))
	}
}

// TestDetector_QuietFramesDoNotResetTheStreak pins the other half of the
// quiet-frame rule: a no-lookup frame is neutral, so it neither credits
// recovery nor clears an accumulating suspicion streak.
func TestDetector_QuietFramesDoNotResetTheStreak(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	for i := range cfg.DefectStreak + 1 {
		if i%3 == 1 {
			f.frame(false, quiet) // interleaved no-lookup frames
		}
		f.frame(false, defeat)
	}
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 (quiet frames must not reset the streak)", len(f.reports))
	}
}

// TestDetector_WidthChangeCreditsRecovery pins the recovery side of the
// width-change excuse: it is a real lookup that is simply not a defect, so it
// counts toward recovery (unlike quiet or streaming frames, which are neutral).
func TestDetector_WidthChangeCreditsRecovery(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	for i := range cfg.RecoveryStreak {
		f.frame(false, func(c *CacheStats) {
			c.Width = 60 + i%40
			c.Misses++
		})
	}
	if f.det.Status().Tripped {
		t.Error("Status().Tripped = true after a recovery streak of width-change frames, want false")
	}
}

// TestDetector_StillDetectsAfterLongStreaming is the other side of "streaming
// frames are neutral": re-baselining on every streaming frame must not leave
// the detector blind once the turn ends.
func TestDetector_StillDetectsAfterLongStreaming(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(5_000, true, defeat)
	f.frames(cfg.DefectStreak, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports after streaming then a real defect, want 1", len(f.reports))
	}
}

// TestDetector_UncacheableTripsOnQuietFrames pins that the uncacheable signal
// is a state, not a delta: a pile of never-finishing items is pathological even
// on frames where no render happened.
func TestDetector_UncacheableTripsOnQuietFrames(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = cfg.UncacheableLimit + 4
	f.frames(cfg.DefectStreak, false, quiet)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	if got := f.reports[0].Kind; got != DefectUncacheable {
		t.Errorf("Kind = %v, want DefectUncacheable", got)
	}
}

// TestDetector_QuietFramesDoNotCreditRecovery is the mirror of the streaming
// case: an absence of rebuilds is not evidence the cache recovered, so quiet
// frames must not silently un-latch a live episode.
func TestDetector_QuietFramesDoNotCreditRecovery(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	f.frames(10_000, false, quiet)
	if !f.det.Status().Tripped {
		t.Fatal("Status().Tripped = false after quiet frames, want still latched")
	}
	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 (quiet frames must not re-arm)", len(f.reports))
	}
}

func TestDetector_NoTripIntermittentDefect(t *testing.T) {
	f := newFeeder(t, enabledCfg())
	for i := range 10_000 {
		if i%2 == 0 {
			f.frame(false, defeat)
		} else {
			f.frame(false, hit)
		}
	}
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports on an alternating defect/clean pattern, want 0", len(f.reports))
	}
}

func TestDetector_NoTripStreamingInterleavedBeforeThreshold(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	for range 100 {
		f.frames(cfg.DefectStreak-1, false, defeat)
		f.frame(true, defeat) // a streaming frame resets the suspicion streak
	}
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports, want 0 (streaming frames reset the streak)", len(f.reports))
	}
}

func TestDetector_NoTripOnCounterReset(t *testing.T) {
	cfg := enabledCfg()
	cfg.DefectStreak = 1 // any single bogus delta would trip
	f := newFeeder(t, cfg)
	f.frames(20, false, legitInvalidation) // accumulate real misses
	// Session restart: the absolute counters go back to zero while the revision
	// happens to land back where it was. An unguarded uint64 delta would
	// underflow to an enormous "miss burst" and trip instantly.
	f.frame(false, func(c *CacheStats) {
		c.Hits, c.Misses = 0, 0
	})
	f.frames(20, false, hit)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports after a counter reset, want 0: %s",
			len(f.reports), f.reports[0].Diagnosis())
	}
}

func TestDetector_NoTripOnRevisionRegression(t *testing.T) {
	cfg := enabledCfg()
	cfg.DefectStreak = 1
	f := newFeeder(t, cfg)
	f.frames(20, false, legitInvalidation)
	f.frame(false, func(c *CacheStats) {
		c.Revision = 1 // rehydrate: the fingerprint restarts
		c.Misses++
	})
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports after a revision regression, want 0", len(f.reports))
	}
}

// TestDetector_StillDetectsAfterCounterReset guards the other side of the
// re-baselining guard: it must not leave the detector permanently blind.
func TestDetector_StillDetectsAfterCounterReset(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(20, false, legitInvalidation)
	f.frame(false, func(c *CacheStats) { c.Hits, c.Misses = 0, 0 })
	f.frames(cfg.DefectStreak, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports after a reset followed by real cache defeat, want 1",
			len(f.reports))
	}
}

func TestDetector_FirstFrameEstablishesBaseline(t *testing.T) {
	cfg := enabledCfg()
	cfg.DefectStreak = 1
	f := newFeeder(t, cfg)
	f.cache.Misses = 5_000_000 // a long, healthy session precedes us
	if _, ok := f.frame(false, nil); ok {
		t.Fatal("first observed frame tripped; it must only establish a baseline")
	}
}

// TestDetector_SuspicionDoesNotLeakIntoTheNextEpisode: a defect that resolves
// before the streak threshold must leave nothing behind, or the eventual report
// names a defect that is no longer present.
func TestDetector_SuspicionDoesNotLeakIntoTheNextEpisode(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = cfg.UncacheableLimit + 5
	f.frames(cfg.DefectStreak-1, false, hit) // sub-threshold uncacheable suspicion
	f.frame(false, func(c *CacheStats) {     // condition resolves
		c.Uncacheable = 0
		hit(c)
	})
	f.frames(cfg.DefectStreak, false, defeat) // a genuine, different defect
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	rep := f.reports[0]
	if rep.Kind != DefectIdleMisses {
		t.Errorf("Kind = %v, want DefectIdleMisses only (the uncacheable condition resolved)", rep.Kind)
	}
	if rep.Uncacheable != 0 {
		t.Errorf("Uncacheable = %d, want 0", rep.Uncacheable)
	}
	if rep.IdleMisses != uint64(cfg.DefectStreak) {
		t.Errorf("IdleMisses = %d, want %d (no carry-over from the resolved suspicion)",
			rep.IdleMisses, cfg.DefectStreak)
	}
}

func TestDetector_StreamingClearsSuspicionAccumulators(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak-1, false, defeat)
	f.frame(true, defeat) // a turn starts: suspicion is abandoned
	f.frames(cfg.DefectStreak, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	if got := f.reports[0].IdleMisses; got != uint64(cfg.DefectStreak) {
		t.Errorf("IdleMisses = %d, want %d (pre-streaming rebuilds must not carry over)",
			got, cfg.DefectStreak)
	}
}

// TestDetector_UncacheableEpisodeDoesNotFabricateRebuilds: on an
// uncacheable-only episode every rebuild was legitimate invalidation, so the
// report must not claim rebuilds happened with the revision pinned.
func TestDetector_UncacheableEpisodeDoesNotFabricateRebuilds(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = cfg.UncacheableLimit + 1
	f.frames(cfg.DefectStreak+1, false, legitInvalidation)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	rep := f.reports[0]
	if rep.Kind != DefectUncacheable {
		t.Errorf("Kind = %v, want DefectUncacheable only", rep.Kind)
	}
	if rep.IdleMisses != 0 {
		t.Errorf("IdleMisses = %d, want 0 (every rebuild followed a revision bump)", rep.IdleMisses)
	}
}

// --- Rate limiting / episode hysteresis ---

func TestDetector_ReportsOncePerEpisode(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports at trip, want 1", len(f.reports))
	}
	f.frames(5_000, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports over a sustained episode, want exactly 1", len(f.reports))
	}
	// The latched report keeps tracking live counts for /perf to display.
	st := f.det.Status()
	if !st.HasReport || !st.Tripped {
		t.Fatalf("Status() = %+v, want HasReport && Tripped", st)
	}
	if st.LastReport.IdleFrames <= cfg.DefectStreak {
		t.Errorf("LastReport.IdleFrames = %d, want it to keep growing past %d",
			st.LastReport.IdleFrames, cfg.DefectStreak)
	}
	if st.Episodes != 1 {
		t.Errorf("Episodes = %d, want 1", st.Episodes)
	}
}

func TestDetector_ReArmsAfterRecovery(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	f.frames(cfg.RecoveryStreak, false, hit) // recovered
	if f.det.Status().Tripped {
		t.Fatal("Status().Tripped = true after a full recovery streak, want false")
	}
	f.frames(cfg.DefectStreak, false, defeat) // relapse
	if len(f.reports) != 2 {
		t.Fatalf("got %d reports across two episodes, want 2", len(f.reports))
	}
	if got := f.reports[1].Episode; got != 2 {
		t.Errorf("second report Episode = %d, want 2", got)
	}
	if got := f.det.Status().Episodes; got != 2 {
		t.Errorf("Status().Episodes = %d, want 2", got)
	}
}

func TestDetector_RecoveryRequiresFullStreak(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	f.frames(cfg.RecoveryStreak-1, false, hit)
	if !f.det.Status().Tripped {
		t.Fatal("Status().Tripped = false after a partial recovery streak, want still latched")
	}
	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 (episode never ended)", len(f.reports))
	}
}

// TestDetector_FlickeringDefectDoesNotUnlatch: recovery needs *consecutive*
// healthy frames. A live defect that flickers must not accumulate its way out
// of a latched episode, or /perf shows Tripped=false mid-defect and the next
// relapse logs a bogus second episode.
func TestDetector_FlickeringDefectDoesNotUnlatch(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	for range 5 {
		f.frames(cfg.RecoveryStreak-10, false, hit)
		f.frames(3, false, defeat)
	}
	if !f.det.Status().Tripped {
		t.Error("Status().Tripped = false, want still latched while the defect flickers")
	}
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
}

func TestDetector_StreamingDoesNotCreditRecovery(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	f.frames(10_000, true, hit) // busy for a long while; that is not evidence of recovery
	if !f.det.Status().Tripped {
		t.Fatal("Status().Tripped = false after streaming frames, want still latched")
	}
	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 (streaming must not re-arm the detector)", len(f.reports))
	}
}

func TestDetector_Reset(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.frames(cfg.DefectStreak+1, false, defeat)
	f.det.Reset()
	st := f.det.Status()
	if st.Tripped || st.HasReport || st.Episodes != 0 {
		t.Fatalf("Status() after Reset = %+v, want cleared", st)
	}
	if !st.Enabled {
		t.Error("Status().Enabled = false after Reset, want config preserved")
	}
	// Re-baselines, then trips again as episode 1.
	f.frames(cfg.DefectStreak+1, false, defeat)
	if got := f.det.Status().LastReport.Episode; got != 1 {
		t.Errorf("Episode after Reset = %d, want 1", got)
	}
}

func TestDetectorConfigFromEnv(t *testing.T) {
	def := enabledCfg()
	tests := []struct {
		name string
		env  map[string]string
		want DetectorConfig
	}{
		{"unset is disabled", nil, DetectorConfig{
			Enabled: false, UncacheableLimit: def.UncacheableLimit,
			DefectStreak: def.DefectStreak, RecoveryStreak: def.RecoveryStreak,
		}},
		{"1 enables", map[string]string{envInvariant: "1"}, DetectorConfig{
			Enabled: true, UncacheableLimit: def.UncacheableLimit,
			DefectStreak: def.DefectStreak, RecoveryStreak: def.RecoveryStreak,
		}},
		{"true enables", map[string]string{envInvariant: "true"}, DetectorConfig{
			Enabled: true, UncacheableLimit: def.UncacheableLimit,
			DefectStreak: def.DefectStreak, RecoveryStreak: def.RecoveryStreak,
		}},
		{"0 stays disabled", map[string]string{envInvariant: "0"}, DetectorConfig{
			Enabled: false, UncacheableLimit: def.UncacheableLimit,
			DefectStreak: def.DefectStreak, RecoveryStreak: def.RecoveryStreak,
		}},
		{"overrides applied", map[string]string{
			envInvariant: "1", envDefectStreak: "5", envUncacheableLimit: "9",
		}, DetectorConfig{
			Enabled: true, UncacheableLimit: 9,
			DefectStreak: 5, RecoveryStreak: def.RecoveryStreak,
		}},
		{"malformed overrides fall back", map[string]string{
			envInvariant: "1", envDefectStreak: "abc", envUncacheableLimit: "-3",
		}, DetectorConfig{
			Enabled: true, UncacheableLimit: def.UncacheableLimit,
			DefectStreak: def.DefectStreak, RecoveryStreak: def.RecoveryStreak,
		}},
		{"zero streak falls back", map[string]string{
			envInvariant: "1", envDefectStreak: "0",
		}, DetectorConfig{
			Enabled: true, UncacheableLimit: def.UncacheableLimit,
			DefectStreak: def.DefectStreak, RecoveryStreak: def.RecoveryStreak,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(k string) (string, bool) {
				v, ok := tt.env[k]
				return v, ok
			}
			if got := DetectorConfigFromEnv(lookup); got != tt.want {
				t.Errorf("DetectorConfigFromEnv() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
