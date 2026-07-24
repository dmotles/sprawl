package perfstat

import "testing"

func TestCacheStats_LookupsAndHitRate(t *testing.T) {
	tests := []struct {
		name        string
		stats       CacheStats
		wantLookups uint64
		wantRate    float64
	}{
		{"zero lookups", CacheStats{}, 0, 0},
		{"all hits", CacheStats{Hits: 1}, 1, 1},
		{"all misses", CacheStats{Misses: 1}, 1, 0},
		{"97 of 100", CacheStats{Hits: 97, Misses: 3}, 100, 0.97},
		{"large counters", CacheStats{Hits: 3_000_000, Misses: 1_000_000}, 4_000_000, 0.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.stats.Lookups(); got != tt.wantLookups {
				t.Errorf("Lookups() = %d, want %d", got, tt.wantLookups)
			}
			if got := tt.stats.HitRate(); got != tt.wantRate {
				t.Errorf("HitRate() = %v, want %v", got, tt.wantRate)
			}
		})
	}
}
