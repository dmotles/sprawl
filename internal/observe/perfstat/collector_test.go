package perfstat

import (
	"sync"
	"testing"
	"time"
)

func fixedClock() func() time.Time {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		now = now.Add(16 * time.Millisecond)
		return now
	}
}

func TestCollector_ZeroConfigSnapshotIsEmpty(t *testing.T) {
	c := New(Config{})
	snap := c.Snapshot()
	if snap.Frame != (FrameStats{}) {
		t.Errorf("Frame = %+v, want zero", snap.Frame)
	}
	if snap.Cache != (CacheStats{}) {
		t.Errorf("Cache = %+v, want zero", snap.Cache)
	}
	if snap.Detector.Enabled {
		t.Error("Detector.Enabled = true, want false by default")
	}
	if snap.At.IsZero() {
		t.Error("At is zero; want the collector's clock to stamp it")
	}
}

func TestCollector_ObserveFrameUpdatesTimerAndCache(t *testing.T) {
	c := New(Config{Now: fixedClock()})
	cache := CacheStats{Hits: 90, Misses: 10, Items: 12, Uncacheable: 1, Revision: 7, Width: 120}
	for i := 1; i <= 3; i++ {
		c.ObserveFrame(FrameSample{Dur: time.Duration(i) * time.Millisecond, Cache: cache, Streaming: true})
	}
	snap := c.Snapshot()
	if snap.Frame.Samples != 3 || snap.Frame.Count != 3 {
		t.Errorf("Frame = %+v, want 3 samples", snap.Frame)
	}
	if snap.Frame.Max != 3*time.Millisecond {
		t.Errorf("Frame.Max = %v, want 3ms", snap.Frame.Max)
	}
	if snap.Cache != cache {
		t.Errorf("Cache = %+v, want %+v", snap.Cache, cache)
	}
	if !snap.Streaming {
		t.Error("Streaming = false, want the last frame's value")
	}
	if snap.Cache.HitRate() != 0.9 {
		t.Errorf("HitRate() = %v, want 0.9", snap.Cache.HitRate())
	}
}

// Snapshot().At is the timestamp of the most recent observed frame, falling
// back to the collector's clock when no frame has been observed yet.
func TestCollector_FrameSampleAtOverridesClock(t *testing.T) {
	c := New(Config{Now: fixedClock()})
	at := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	c.ObserveFrame(FrameSample{Dur: time.Millisecond, At: at})
	if got := c.Snapshot().At; !got.Equal(at) {
		t.Errorf("Snapshot().At = %v, want %v", got, at)
	}
}

func TestCollector_ZeroFrameAtUsesConfiguredClock(t *testing.T) {
	start := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	c := New(Config{Detector: enabledCfg(), Now: fixedClock()})
	c.ObserveFrame(FrameSample{Dur: time.Millisecond})
	want := start.Add(16 * time.Millisecond)
	if got := c.Snapshot().At; !got.Equal(want) {
		t.Errorf("Snapshot().At = %v, want the injected clock's %v", got, want)
	}
}

func TestCollector_ClockReachesReport(t *testing.T) {
	cfg := enabledCfg()
	c := New(Config{Detector: cfg, Now: fixedClock()})
	cache := CacheStats{Items: 10, Uncacheable: 1, Revision: 4}
	var rep Report
	// Idle misses with the revision pinned: the frame path's only trip. The
	// first frame just establishes the baseline, so drive one extra.
	for range cfg.DefectStreak + 1 {
		cache.Misses++
		rep, _ = c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: cache})
	}
	if rep.Zero() {
		t.Fatal("expected a trip report")
	}
	want := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC).
		Add(time.Duration(cfg.DefectStreak+1) * 16 * time.Millisecond)
	if !rep.At.Equal(want) {
		t.Errorf("Report.At = %v, want the injected clock's %v", rep.At, want)
	}
}

func TestCollector_SnapshotIsIndependentValue(t *testing.T) {
	c := New(Config{Now: fixedClock()})
	c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: CacheStats{Hits: 1}})
	snap := c.Snapshot()
	for range 100 {
		c.ObserveFrame(FrameSample{Dur: 50 * time.Millisecond, Cache: CacheStats{Hits: 500}})
	}
	if snap.Frame.Samples != 1 || snap.Cache.Hits != 1 {
		t.Errorf("earlier snapshot mutated: %+v", snap)
	}
}

func TestCollector_ForwardsTripReport(t *testing.T) {
	cfg := enabledCfg()
	c := New(Config{Detector: cfg, Now: fixedClock()})
	cache := CacheStats{Items: 40, Uncacheable: 9, Revision: 5, Width: 100}
	var reports int
	// Idle misses with the revision pinned. Uncacheable rides along as report
	// context — it is asserted below, but it is not what trips.
	for range cfg.DefectStreak + 1 {
		cache.Misses++
		if _, ok := c.ObserveFrame(FrameSample{Dur: 40 * time.Millisecond, Cache: cache}); ok {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("got %d reports through the collector, want 1", reports)
	}
	snap := c.Snapshot()
	if !snap.Detector.Enabled || !snap.Detector.Tripped || !snap.Detector.HasReport {
		t.Fatalf("Detector status = %+v, want enabled+tripped+report", snap.Detector)
	}
	if snap.Detector.LastReport.Uncacheable != 9 {
		t.Errorf("LastReport.Uncacheable = %d, want 9", snap.Detector.LastReport.Uncacheable)
	}
}

func TestCollector_DisabledDetectorNeverReports(t *testing.T) {
	c := New(Config{Now: fixedClock()})
	cache := CacheStats{Items: 40, Uncacheable: 99, Revision: 5}
	for range 5_000 {
		if _, ok := c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: cache}); ok {
			t.Fatal("collector reported with the detector disabled")
		}
	}
	if c.Snapshot().Detector.Tripped {
		t.Error("Detector.Tripped = true with the detector disabled")
	}
}

func TestCollector_Reset(t *testing.T) {
	cfg := enabledCfg()
	c := New(Config{Detector: cfg, Now: fixedClock()})
	cache := CacheStats{Items: 40, Uncacheable: 9, Revision: 5}
	for range cfg.DefectStreak + 1 {
		c.ObserveFrame(FrameSample{Dur: 40 * time.Millisecond, Cache: cache})
	}
	c.Reset()
	snap := c.Snapshot()
	if snap.Frame != (FrameStats{}) || snap.Cache != (CacheStats{}) {
		t.Errorf("after Reset frame/cache = %+v / %+v, want zero", snap.Frame, snap.Cache)
	}
	if snap.Detector.Tripped || snap.Detector.Episodes != 0 {
		t.Errorf("after Reset detector = %+v, want cleared", snap.Detector)
	}
	if !snap.Detector.Enabled {
		t.Error("Reset disabled the detector; want config preserved")
	}
}

// TestCollector_ConcurrentObserveAndSnapshot justifies the Collector's mutex:
// the incident bundle snapshots from a background goroutine while the TUI
// render loop is still observing frames. Run under -race.
func TestCollector_ConcurrentObserveAndSnapshot(t *testing.T) {
	c := New(Config{Detector: enabledCfg()})
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				c.ObserveFrame(FrameSample{
					Dur:   time.Duration(i) * time.Microsecond,
					Cache: CacheStats{Hits: uint64(i), Items: i, Revision: uint64(i)},
				})
			}
		}()
	}
	// The invariant path mutates collector state from the render goroutine too,
	// so it belongs under the same race gate.
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 500 {
				c.ObserveInvariant(InvariantSample{Orphans: i % 3})
			}
		}()
	}
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				_ = c.Snapshot()
			}
		}()
	}
	wg.Wait()
	if got := c.Snapshot().Frame.Count; got != 2000 {
		t.Errorf("Frame.Count = %d, want 2000", got)
	}
}

// TestCollector_ObserveFrameZeroAllocs gates the per-frame path. Uncacheable
// and Revision are load-bearing: they keep the detector from tripping, so this
// measures the steady-state path a healthy session actually takes. The trip
// path allocates (it builds a Report string) but runs once per episode.
func TestCollector_ObserveFrameZeroAllocs(t *testing.T) {
	cache := CacheStats{Hits: 1, Items: 10, Uncacheable: 1, Revision: 3, Width: 120}
	at := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)

	for _, tt := range []struct {
		name string
		cfg  DetectorConfig
	}{
		{"detector off", DetectorConfig{}},
		{"detector on", enabledCfg()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := New(Config{Detector: tt.cfg})
			sample := FrameSample{Dur: 2 * time.Millisecond, Cache: cache, At: at}
			n := testing.AllocsPerRun(1000, func() {
				cache.Hits++
				sample.Cache = cache
				c.ObserveFrame(sample)
			})
			if n != 0 {
				t.Fatalf("Collector.ObserveFrame allocated %v times per run, want 0", n)
			}
		})
	}
}
