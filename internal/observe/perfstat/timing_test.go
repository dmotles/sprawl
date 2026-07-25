package perfstat

import (
	"math"
	"testing"
	"time"
)

func TestFrameTimer_Empty(t *testing.T) {
	var tm FrameTimer
	if got := tm.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if got := tm.Count(); got != 0 {
		t.Errorf("Count() = %d, want 0", got)
	}
	if got := tm.Max(); got != 0 {
		t.Errorf("Max() = %v, want 0", got)
	}
	if got := tm.P50(); got != 0 {
		t.Errorf("P50() = %v, want 0", got)
	}
	if got := tm.P95(); got != 0 {
		t.Errorf("P95() = %v, want 0", got)
	}
	if got := tm.Percentile(50); got != 0 {
		t.Errorf("Percentile(50) = %v, want 0", got)
	}
}

func TestFrameTimer_SingleSample(t *testing.T) {
	var tm FrameTimer
	tm.Observe(7 * time.Millisecond)
	if got := tm.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1", got)
	}
	if got := tm.Count(); got != 1 {
		t.Errorf("Count() = %d, want 1", got)
	}
	for _, tc := range []struct {
		name string
		got  time.Duration
	}{
		{"P50", tm.P50()},
		{"P95", tm.P95()},
		{"Max", tm.Max()},
		{"Percentile(0)", tm.Percentile(0)},
		{"Percentile(100)", tm.Percentile(100)},
	} {
		if tc.got != 7*time.Millisecond {
			t.Errorf("%s = %v, want 7ms", tc.name, tc.got)
		}
	}
}

func TestFrameTimer_NearestRankKnownDistribution(t *testing.T) {
	var tm FrameTimer
	for i := 1; i <= 100; i++ {
		tm.Observe(time.Duration(i) * time.Millisecond)
	}
	tests := []struct {
		p    float64
		want time.Duration
	}{
		{0, 1 * time.Millisecond},
		{1, 1 * time.Millisecond},
		{50, 50 * time.Millisecond},
		{95, 95 * time.Millisecond},
		{99, 99 * time.Millisecond},
		{100, 100 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := tm.Percentile(tt.p); got != tt.want {
			t.Errorf("Percentile(%v) = %v, want %v", tt.p, got, tt.want)
		}
	}
	if got := tm.P50(); got != 50*time.Millisecond {
		t.Errorf("P50() = %v, want 50ms", got)
	}
	if got := tm.P95(); got != 95*time.Millisecond {
		t.Errorf("P95() = %v, want 95ms", got)
	}
}

func TestFrameTimer_AllEqual(t *testing.T) {
	var tm FrameTimer
	const v = 3 * time.Millisecond
	for range 50 {
		tm.Observe(v)
	}
	for _, p := range []float64{0, 25, 50, 95, 100} {
		if got := tm.Percentile(p); got != v {
			t.Errorf("Percentile(%v) = %v, want %v", p, got, v)
		}
	}
	if got := tm.Max(); got != v {
		t.Errorf("Max() = %v, want %v", got, v)
	}
}

func TestFrameTimer_InsertionOrderIrrelevant(t *testing.T) {
	var asc, desc FrameTimer
	for i := 1; i <= 100; i++ {
		asc.Observe(time.Duration(i) * time.Millisecond)
	}
	for i := 100; i >= 1; i-- {
		desc.Observe(time.Duration(i) * time.Millisecond)
	}
	if asc.Stats() != desc.Stats() {
		t.Errorf("ascending stats %+v != descending stats %+v", asc.Stats(), desc.Stats())
	}
}

func TestFrameTimer_RingWraparound(t *testing.T) {
	var tm FrameTimer
	// First fill the ring with small samples, then overwrite half of it with
	// large ones. The evicted small samples must not influence percentiles.
	for range FrameRingCapacity {
		tm.Observe(1 * time.Millisecond)
	}
	half := FrameRingCapacity / 2
	for range half {
		tm.Observe(100 * time.Millisecond)
	}
	if got := tm.Len(); got != FrameRingCapacity {
		t.Errorf("Len() = %d, want %d", got, FrameRingCapacity)
	}
	if got, want := tm.Count(), uint64(FrameRingCapacity+half); got != want {
		t.Errorf("Count() = %d, want %d", got, want)
	}
	// Retained window: half 1ms + half 100ms. p50 lands on the 1ms half's top.
	if got := tm.P50(); got != 1*time.Millisecond {
		t.Errorf("P50() = %v, want 1ms", got)
	}
	if got := tm.P95(); got != 100*time.Millisecond {
		t.Errorf("P95() = %v, want 100ms", got)
	}

	// Now evict every small sample: only 100ms samples remain retained.
	for range FrameRingCapacity {
		tm.Observe(100 * time.Millisecond)
	}
	if got := tm.P50(); got != 100*time.Millisecond {
		t.Errorf("after full eviction P50() = %v, want 100ms", got)
	}
	if got := tm.Percentile(0); got != 100*time.Millisecond {
		t.Errorf("after full eviction Percentile(0) = %v, want 100ms", got)
	}
}

func TestFrameTimer_MaxIsLifetimeAcrossWraparound(t *testing.T) {
	var tm FrameTimer
	tm.Observe(5 * time.Second)
	for range FrameRingCapacity * 2 {
		tm.Observe(1 * time.Millisecond)
	}
	if got := tm.Max(); got != 5*time.Second {
		t.Errorf("Max() = %v, want 5s (lifetime max survives eviction)", got)
	}
	if got := tm.P95(); got != 1*time.Millisecond {
		t.Errorf("P95() = %v, want 1ms (evicted outlier must not appear)", got)
	}
}

func TestFrameTimer_PercentileClamped(t *testing.T) {
	var tm FrameTimer
	for i := 1; i <= 10; i++ {
		tm.Observe(time.Duration(i) * time.Millisecond)
	}
	lo, hi := tm.Percentile(0), tm.Percentile(100)
	if lo != 1*time.Millisecond || hi != 10*time.Millisecond {
		t.Fatalf("Percentile(0)/Percentile(100) = %v/%v, want 1ms/10ms", lo, hi)
	}
	tests := []struct {
		p    float64
		want time.Duration
	}{
		{-5, lo},
		{-0.0001, lo},
		{math.NaN(), lo}, // NaN is treated as the low clamp, never a panic
		{math.Inf(-1), lo},
		{100.0001, hi},
		{150, hi},
		{math.Inf(1), hi},
	}
	for _, tt := range tests {
		if got := tm.Percentile(tt.p); got != tt.want {
			t.Errorf("Percentile(%v) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

func TestFrameTimer_TinyNPercentiles(t *testing.T) {
	tests := []struct {
		samples []time.Duration
		p       float64
		want    time.Duration
	}{
		{[]time.Duration{1, 2}, 50, 1},
		{[]time.Duration{1, 2}, 95, 2},
		{[]time.Duration{1, 2, 3}, 50, 2},
		{[]time.Duration{1, 2, 3}, 95, 3},
		{[]time.Duration{1, 2, 3, 4}, 50, 2},
		{[]time.Duration{1, 2, 3, 4}, 25, 1},
	}
	for _, tt := range tests {
		var tm FrameTimer
		for _, s := range tt.samples {
			tm.Observe(s)
		}
		if got := tm.Percentile(tt.p); got != tt.want {
			t.Errorf("samples %v Percentile(%v) = %v, want %v", tt.samples, tt.p, got, tt.want)
		}
	}
}

func TestFrameTimer_CapacityIsAMeaningfulWindow(t *testing.T) {
	// p95 over a handful of frames is noise. 240 frames is ~4s at 60fps — long
	// enough to be meaningful, short enough to reflect current health.
	if FrameRingCapacity < 240 {
		t.Errorf("FrameRingCapacity = %d, want >= 240", FrameRingCapacity)
	}
	if FrameRingCapacity%2 != 0 {
		t.Errorf("FrameRingCapacity = %d, want even (the wraparound tests split it in half)", FrameRingCapacity)
	}
}

// TestFrameTimer_QueryDoesNotDisturbTheRing guards against a query path that
// sorts the ring in place: that would scramble insertion order, so subsequent
// Observe calls would evict the wrong samples.
func TestFrameTimer_QueryDoesNotDisturbTheRing(t *testing.T) {
	var tm FrameTimer
	for i := 1; i <= FrameRingCapacity; i++ {
		tm.Observe(time.Duration(i) * time.Millisecond)
	}
	if got := tm.P50(); got != time.Duration(FrameRingCapacity/2)*time.Millisecond {
		t.Fatalf("P50() = %v, want %v", got, time.Duration(FrameRingCapacity/2)*time.Millisecond)
	}
	half := FrameRingCapacity / 2
	for range half {
		tm.Observe(500 * time.Millisecond)
	}
	// The oldest surviving original sample must be the (half+1)-th one.
	wantMin := time.Duration(half+1) * time.Millisecond
	if got := tm.Percentile(0); got != wantMin {
		t.Errorf("Percentile(0) = %v, want %v (eviction order corrupted)", got, wantMin)
	}
}

// TestFrameTimer_CopyIsIndependent pins FrameTimer as a true value type — the
// Collector embeds one by value, so a shared backing array would alias.
func TestFrameTimer_CopyIsIndependent(t *testing.T) {
	var tm FrameTimer
	// Fill to capacity so the copy's writes wrap onto slots the original reads —
	// a shared backing array would corrupt the original's percentiles.
	for i := 1; i <= FrameRingCapacity; i++ {
		tm.Observe(time.Duration(i) * time.Millisecond)
	}
	before := tm.Stats()
	cp := tm
	for range FrameRingCapacity {
		cp.Observe(9 * time.Second)
	}
	if got := tm.Stats(); got != before {
		t.Errorf("original Stats() = %+v after mutating a copy, want %+v", got, before)
	}
}

func TestFrameTimer_IgnoresNegativeDuration(t *testing.T) {
	var tm FrameTimer
	tm.Observe(-1 * time.Millisecond)
	if got := tm.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0 (negative sample must be dropped)", got)
	}
	if got := tm.Count(); got != 0 {
		t.Errorf("Count() = %d, want 0", got)
	}
	if got := tm.Max(); got != 0 {
		t.Errorf("Max() = %v, want 0", got)
	}
}

func TestFrameTimer_ObserveZeroIsRecorded(t *testing.T) {
	var tm FrameTimer
	tm.Observe(0)
	if got := tm.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 (a zero-duration frame is a real sample)", got)
	}
}

func TestFrameTimer_Reset(t *testing.T) {
	var tm FrameTimer
	for i := range 300 {
		tm.Observe(time.Duration(i) * time.Millisecond)
	}
	tm.Reset()
	if got := (tm.Stats()); got != (FrameStats{}) {
		t.Errorf("Stats() after Reset = %+v, want zero value", got)
	}
	// And it must still be usable.
	tm.Observe(2 * time.Millisecond)
	if got := tm.P50(); got != 2*time.Millisecond {
		t.Errorf("P50() after Reset+Observe = %v, want 2ms", got)
	}
}

func TestFrameTimer_StatsMatchesIndividualQueries(t *testing.T) {
	var tm FrameTimer
	for i := 1; i <= 40; i++ {
		tm.Observe(time.Duration(i) * time.Millisecond)
	}
	want := FrameStats{
		Samples: tm.Len(),
		Count:   tm.Count(),
		P50:     tm.P50(),
		P95:     tm.P95(),
		Max:     tm.Max(),
	}
	if got := tm.Stats(); got != want {
		t.Errorf("Stats() = %+v, want %+v", got, want)
	}
}

// TestFrameTimer_ObserveZeroAllocs is the hard gate behind QUM-934's "no
// always-on measurable overhead in the render hot path" constraint.
func TestFrameTimer_ObserveZeroAllocs(t *testing.T) {
	var tm FrameTimer
	if n := testing.AllocsPerRun(1000, func() { tm.Observe(1_234_567) }); n != 0 {
		t.Fatalf("FrameTimer.Observe allocated %v times per run, want 0", n)
	}
}
