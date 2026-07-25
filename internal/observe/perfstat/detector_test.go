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

// TestDetector_NewDetectorNormalizesConfig is a false-positive guard: a
// directly-constructed config with zero thresholds must not mean "trip on the
// first defective frame".
func TestDetector_NewDetectorNormalizesConfig(t *testing.T) {
	f := newFeeder(t, DetectorConfig{Enabled: true})
	def := enabledCfg()
	// The first frame only establishes the miss baseline, so the streak's last
	// frame is the one after DefectStreak-1 defective ones.
	f.frames(def.DefectStreak, false, defeat)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports before the default streak elapsed, want 0", len(f.reports))
	}
	f.frame(false, defeat)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports at the default streak, want 1", len(f.reports))
	}
	if got := f.reports[0].IdleFrames; got != def.DefectStreak {
		t.Errorf("IdleFrames = %d, want the normalized default streak %d", got, def.DefectStreak)
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
		fmt.Sprintf("%d of %d", 7, 214), // uncacheable of total items, kept as context
		"revision 1188",                 // the pinned fingerprint
		"width 132",
		"idle-misses",
	} {
		if !strings.Contains(diag, want) {
			t.Errorf("Diagnosis() = %q, missing %q", diag, want)
		}
	}
	// The retired threshold must not survive in the prose either: a named limit
	// implies the count was compared against one.
	if strings.Contains(diag, "limit") {
		t.Errorf("Diagnosis() = %q, must not name a limit", diag)
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
	// What would be the frame path's trip at idle — a rebuild every frame with
	// the revision pinned — is by-design mid-turn and must stay silent.
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

// TestDetector_QuietFramesAreAlwaysNeutral pins what survives the retirement of
// the state-based trip: with DefectIdleMisses the only trip, and a miss delta
// the only evidence for it, a frame where no cache lookup happened can never
// produce a report — no matter what the unfinished-item count says.
//
// This is the frame path's "cannot see" boundary made executable. A stranded
// item drives the render into a bypass that consults no cache, which lands here
// as a quiet frame. It must stay silent (see Observe), and OrphanCount is what
// actually catches that class.
//
// READ THIS BEFORE "FIXING" THIS TEST. Demanding silence in the presence of 400
// unfinished items looks obviously wrong, and the obvious repair — make a
// stranded render trip — seems like a strict improvement. It is not, and here
// is why: the frame path cannot distinguish a strand from a legitimate
// streaming bypass. Both arrive as the same quiet frame carrying the same
// counters. Tripping on one therefore trips on the other, which fires on the
// most common state in the system and produces a detector that gets switched
// off. The silence is a deliberate trade, not an oversight; the class is caught
// by ObserveInvariant instead, where the two states ARE distinguishable.
func TestDetector_QuietFramesAreAlwaysNeutral(t *testing.T) {
	cfg := enabledCfg()
	f := newFeeder(t, cfg)
	f.cache.Uncacheable = 400 // a pile of unfinished items, and still no evidence
	f.frames(10_000, false, quiet)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports from quiet frames, want 0: %s",
			len(f.reports), f.reports[0].Diagnosis())
	}
	if f.det.Status().Tripped {
		t.Error("Status().Tripped = true after quiet frames, want false")
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
	f.frames(cfg.DefectStreak-1, false, defeat) // sub-threshold idle-miss suspicion
	f.frame(false, hit)                         // condition resolves: a clean frame
	f.frames(cfg.DefectStreak, false, defeat)   // a genuine, fresh defect
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

// TestDetectorConfigFromEnv pins the env surface. Each case states its expected
// config as a delta from DefaultDetectorConfig() rather than as a full literal,
// so a newly added config field does not silently fail all seven cases with a
// zero-vs-default mismatch that reads like an implementation bug.
//
// Stating them as deltas also pins the negative: any field NOT named by a case
// must come back at its default, which is what makes the thresholds that are
// deliberately not env-tunable (RecoveryStreak, InvariantConfirmations,
// InvariantRecovery) actually tested as such.
func TestDetectorConfigFromEnv(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		want  func(*DetectorConfig)
		about string
	}{
		{name: "unset is disabled", env: nil},
		{
			name: "1 enables", env: map[string]string{envInvariant: "1"},
			want: func(c *DetectorConfig) { c.Enabled = true },
		},
		{
			name: "true enables", env: map[string]string{envInvariant: "true"},
			want: func(c *DetectorConfig) { c.Enabled = true },
		},
		{name: "0 stays disabled", env: map[string]string{envInvariant: "0"}},
		{
			name: "overrides applied",
			env: map[string]string{
				envInvariant: "1", envDefectStreak: "5",
			},
			want: func(c *DetectorConfig) {
				c.Enabled, c.DefectStreak = true, 5
			},
		},
		{
			name: "malformed overrides fall back",
			env: map[string]string{
				envInvariant: "1", envDefectStreak: "abc",
			},
			want: func(c *DetectorConfig) { c.Enabled = true },
		},
		{
			// The retired knob must be inert rather than merely unread: an
			// unknown key that silently changed a threshold would be worse than
			// one that errors.
			name: "retired uncacheable-limit knob is ignored",
			env: map[string]string{
				envInvariant: "1", "SPRAWL_PERF_UNCACHEABLE_LIMIT": "9",
			},
			want: func(c *DetectorConfig) { c.Enabled = true },
		},
		{
			name: "zero streak falls back",
			env:  map[string]string{envInvariant: "1", envDefectStreak: "0"},
			want: func(c *DetectorConfig) { c.Enabled = true },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := DefaultDetectorConfig()
			if tt.want != nil {
				tt.want(&want)
			}
			lookup := func(k string) (string, bool) {
				v, ok := tt.env[k]
				return v, ok
			}
			if got := DetectorConfigFromEnv(lookup); got != want {
				t.Errorf("DetectorConfigFromEnv() = %+v, want %+v", got, want)
			}
		})
	}
}

// TestDetector_UncacheableIsContextNotATrip pins the retirement of the
// uncacheable trip condition.
//
// An unfinished-item count is NOT a defect signal, for three reasons, any one
// of which is sufficient:
//
//  1. It adds no detection power over DefectOrphans. Every stranded assistant
//     block — the class this count was built to catch — is by definition a
//     non-tail unfinished assistant, which the invariant path counts directly.
//  2. No threshold value is defensible. Measured against
//     ChatList.UncacheableCount's honest O(n) walk, a transcript backfill
//     carrying three pending tool rows lands at 3 with the runtime genuinely
//     idle, which trips a limit of 2 the moment a reload happens. Raising the
//     limit only moves where it lies; there is no principled ceiling on how
//     many items are legitimately in flight.
//  3. Its report was incoherent as well as wrong: on a hit-only frame run it
//     read "30 consecutive idle frames rebuilt the chat render ... (0 rebuilds,
//     hit rate 100.0%)". A signal that fires with no rebuilds is not describing
//     a defeated cache.
//
// The count survives as REPORTED CONTEXT on a report raised by another signal:
// it explains a defeated cache, it does not diagnose one. DefectOrphans is the
// authority for the invariant it once stood in for.
func TestDetector_UncacheableIsContextNotATrip(t *testing.T) {
	t.Run("never trips on its own", func(t *testing.T) {
		f := newFeeder(t, enabledCfg())
		f.cache.Uncacheable = 500
		f.cache.Items = 600
		// Hit-only: no miss delta anywhere, so the only thing that could
		// possibly trip is the unfinished-item count.
		f.frames(10_000, false, hit)
		if len(f.reports) != 0 {
			t.Fatalf("got %d reports from an unfinished-item count alone, want 0: %s",
				len(f.reports), f.reports[0].Diagnosis())
		}
		if f.det.Status().Tripped {
			t.Error("Status().Tripped = true, want false")
		}
	})

	t.Run("still reported as context on an idle-miss trip", func(t *testing.T) {
		cfg := enabledCfg()
		f := newFeeder(t, cfg)
		f.cache.Uncacheable = 500
		f.cache.Items = 600
		f.frames(cfg.DefectStreak+1, false, defeat)
		if len(f.reports) != 1 {
			t.Fatalf("got %d reports, want exactly 1", len(f.reports))
		}
		rep := f.reports[0]
		if rep.Kind != DefectIdleMisses {
			t.Errorf("Kind = %v, want DefectIdleMisses alone (the uncacheable bit is retired)", rep.Kind)
		}
		if rep.Uncacheable != 500 {
			t.Errorf("Uncacheable = %d, want 500 carried through as context", rep.Uncacheable)
		}
		diag := rep.Diagnosis()
		if !strings.Contains(diag, "500 of 600") {
			t.Errorf("Diagnosis() = %q, want it to still name the %q context", diag, "500 of 600")
		}
		if strings.Contains(diag, "limit") {
			t.Errorf("Diagnosis() = %q, must not name a limit — the threshold is retired", diag)
		}
	})

	// The retirement must be a DELETION, not a dormant trip behind a raised
	// threshold. A neutered-but-present trip passes the legs above and is
	// resurrected the moment someone sets the env knob — so assert the knob
	// itself is inert. The key is spelled literally rather than via a const so
	// this still compiles, and still means something, once the const is gone.
	t.Run("the retired env knob cannot resurrect the trip", func(t *testing.T) {
		cfg := DetectorConfigFromEnv(func(k string) (string, bool) {
			switch k {
			case "SPRAWL_PERF_INVARIANT":
				return "1", true
			case "SPRAWL_PERF_UNCACHEABLE_LIMIT":
				return "1", true
			}
			return "", false
		})
		f := newFeeder(t, cfg)
		f.cache.Uncacheable = 500
		f.cache.Items = 600
		f.frames(10_000, false, hit)
		if len(f.reports) != 0 {
			t.Fatalf("got %d reports with the retired knob set to 1, want 0: %s",
				len(f.reports), f.reports[0].Diagnosis())
		}
	})
}

// TestReport_DiagnosisDoesNotContradictItsOwnNumbers applies the check the
// retired uncacheable trip failed: does the message text quantify anything the
// report's own numbers can contradict?
//
// That trip's output was the proof — "30 consecutive idle frames rebuilt the
// chat render ... (0 rebuilds, hit rate 100.0%)" — a sentence refuting itself
// inside one clause. Self-inconsistency is the strongest available evidence
// that a diagnostic is wrong, because unlike a miscalibrated threshold it
// cannot be defended by anyone at any setting. So it is worth asserting against
// rather than discovering years later in a bug report.
//
// The surviving frame trip names consecutive idle frames, rebuild counts,
// revision, width and hit rate. These are the pairs that can disagree.
func TestReport_DiagnosisDoesNotContradictItsOwnNumbers(t *testing.T) {
	cfg := enabledCfg()

	t.Run("a frame trip always reports the rebuilds it claims", func(t *testing.T) {
		f := newFeeder(t, cfg)
		f.frames(cfg.DefectStreak+1, false, defeat)
		if len(f.reports) != 1 {
			t.Fatalf("got %d reports, want 1", len(f.reports))
		}
		rep := f.reports[0]
		diag := rep.Diagnosis()
		if rep.IdleMisses == 0 {
			t.Errorf("report claims %d idle frames rebuilt the render but counts 0 rebuilds: %q",
				rep.IdleFrames, diag)
		}
		if rep.IdleFrames == 0 {
			t.Errorf("report counts %d rebuilds across 0 idle frames: %q", rep.IdleMisses, diag)
		}
		// A cache that was rebuilt on every frame of the streak cannot also have
		// served every lookup.
		if rep.HitRate == 1 {
			t.Errorf("report claims a 100%% hit rate alongside %d rebuilds: %q", rep.IdleMisses, diag)
		}
	})

	t.Run("an orphan report never claims frame evidence it lacks", func(t *testing.T) {
		ocfg := orphanCfg()
		f := newFeeder(t, ocfg)
		// No warmCache: the invariant observation lands before any frame, so
		// there is no cache context to name.
		f.invariants(ocfg.InvariantConfirmations, false, 3)
		if len(f.reports) != 1 {
			t.Fatalf("got %d reports, want 1", len(f.reports))
		}
		rep := f.reports[0]
		diag := rep.Diagnosis()
		if rep.HaveFrameContext {
			t.Fatal("HaveFrameContext = true with no frame observed")
		}
		// With no frame context the prose must not quantify frame-derived
		// values, because their zero values are not readings.
		for _, forbidden := range []string{"rebuilt", "hit rate", "of 0 items", "revision 0", "width 0"} {
			if strings.Contains(diag, forbidden) {
				t.Errorf("orphan report with no frame context names %q: %q", forbidden, diag)
			}
		}
		// It must still name the thing it did measure.
		if !strings.Contains(diag, "3 items are stranded") {
			t.Errorf("orphan report omits its own measured count: %q", diag)
		}
	})

	t.Run("an orphan count of zero can never reach a report", func(t *testing.T) {
		// The directional check's core claim: a report that says "invariant
		// violated" must carry a positive count, or the sentence and the number
		// disagree.
		ocfg := orphanCfg()
		f := newFeeder(t, ocfg)
		f.warmCache(200)
		f.invariants(50, false, 0)
		if len(f.reports) != 0 {
			t.Fatalf("a zero orphan count produced %d reports: %q",
				len(f.reports), f.reports[0].Diagnosis())
		}
	})
}
