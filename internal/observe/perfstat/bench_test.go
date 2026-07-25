package perfstat

import (
	"testing"
	"time"
)

func BenchmarkFrameTimer_Observe(b *testing.B) {
	var tm FrameTimer
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		tm.Observe(time.Duration(i%1000) * time.Microsecond)
	}
}

func BenchmarkCollector_ObserveFrame(b *testing.B) {
	for _, tt := range []struct {
		name string
		cfg  DetectorConfig
	}{
		{"detector-off", DetectorConfig{}},
		{"detector-on", enabledCfg()},
	} {
		b.Run(tt.name, func(b *testing.B) {
			c := New(Config{Detector: tt.cfg})
			at := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
			cache := CacheStats{Items: 200, Revision: 1000, Width: 120}
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				cache.Hits++
				c.ObserveFrame(FrameSample{
					Dur:   time.Duration(i%1000) * time.Microsecond,
					Cache: cache,
					At:    at,
				})
			}
		})
	}
}

func BenchmarkCollector_ObserveInvariant(b *testing.B) {
	for _, tt := range []struct {
		name string
		cfg  DetectorConfig
	}{
		{"detector-off", DetectorConfig{}},
		{"detector-on", enabledCfg()},
	} {
		b.Run(tt.name, func(b *testing.B) {
			c := New(Config{Detector: tt.cfg})
			at := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
			b.ReportAllocs()
			for b.Loop() {
				c.ObserveInvariant(InvariantSample{At: at})
			}
		})
	}
}

func BenchmarkFrameTimer_Stats(b *testing.B) {
	var tm FrameTimer
	for i := range FrameRingCapacity {
		tm.Observe(time.Duration(i) * time.Microsecond)
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = tm.Stats()
	}
}
