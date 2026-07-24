package perfstat

import (
	"math"
	"slices"
	"time"
)

// FrameRingCapacity is the fixed sample window. 256 frames is roughly 4s at
// 60fps — long enough for p95 to mean something, short enough that it reflects
// current health rather than a session-long average.
const FrameRingCapacity = 256

// FrameTimer is a fixed-size ring of frame render durations. Observe is O(1)
// and allocation-free, so it is safe to call on every frame. It is a value
// type with no shared backing storage, and is NOT safe for concurrent use —
// Collector owns the synchronization.
type FrameTimer struct {
	buf   [FrameRingCapacity]time.Duration
	idx   int           // next write slot
	n     int           // retained samples, saturates at FrameRingCapacity
	total uint64        // lifetime observation count
	max   time.Duration // lifetime maximum
}

// Observe records one frame duration. Negative durations are dropped.
func (t *FrameTimer) Observe(d time.Duration) {
	if d < 0 {
		return
	}
	t.buf[t.idx] = d
	t.idx = (t.idx + 1) % FrameRingCapacity
	if t.n < FrameRingCapacity {
		t.n++
	}
	t.total++
	if d > t.max {
		t.max = d
	}
}

// Len reports how many samples are currently retained.
func (t *FrameTimer) Len() int { return t.n }

// Count reports the lifetime number of observations, including evicted ones.
func (t *FrameTimer) Count() uint64 { return t.total }

// Max reports the lifetime maximum, which survives ring eviction.
func (t *FrameTimer) Max() time.Duration { return t.max }

// Percentile returns the nearest-rank percentile over the retained samples:
// rank = ceil(p/100 * n), clamped to [1, n]. p is clamped to [0, 100]; NaN is
// treated as 0. An empty timer returns 0.
func (t *FrameTimer) Percentile(p float64) time.Duration {
	if t.n == 0 {
		return 0
	}
	var scratch [FrameRingCapacity]time.Duration
	return percentileOf(t.sortedInto(&scratch), p)
}

// sortedInto copies the retained samples into scratch and sorts them, leaving
// the ring's insertion order intact so eviction stays correct.
func (t *FrameTimer) sortedInto(scratch *[FrameRingCapacity]time.Duration) []time.Duration {
	sorted := scratch[:t.n]
	copy(sorted, t.buf[:t.n])
	slices.Sort(sorted)
	return sorted
}

func percentileOf(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if math.IsNaN(p) {
		p = 0
	}
	p = math.Min(math.Max(p, 0), 100)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// P50 returns the median retained frame duration.
func (t *FrameTimer) P50() time.Duration { return t.Percentile(50) }

// P95 returns the 95th-percentile retained frame duration.
func (t *FrameTimer) P95() time.Duration { return t.Percentile(95) }

// Stats returns every timing query as one immutable value, sorting the
// retained samples once for both percentiles.
func (t *FrameTimer) Stats() FrameStats {
	stats := FrameStats{Samples: t.n, Count: t.total, Max: t.max}
	if t.n > 0 {
		var scratch [FrameRingCapacity]time.Duration
		sorted := t.sortedInto(&scratch)
		stats.P50 = percentileOf(sorted, 50)
		stats.P95 = percentileOf(sorted, 95)
	}
	return stats
}

// Reset drops all samples and lifetime counters.
func (t *FrameTimer) Reset() { *t = FrameTimer{} }

// FrameStats is a point-in-time view of frame timings.
type FrameStats struct {
	Samples int           // retained samples
	Count   uint64        // lifetime observations
	P50     time.Duration // over retained samples
	P95     time.Duration // over retained samples
	Max     time.Duration // lifetime
}
