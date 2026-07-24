// Package perfstat collects render-health statistics for the TUI: frame
// timings, render-cache accounting, and an invariant detector for the silent
// cache-defeat failure mode described in QUM-934 (deliverables C and D).
//
// The package is deliberately standalone and stdlib-only. It never reads TUI
// state: callers hand it plain scalars (see CacheStats) plus an explicit
// streaming flag, so the accounting surface can evolve independently of
// whatever accessors ChatList ends up exposing.
package perfstat

import (
	"sync"
	"time"
)

// FrameSample is what the caller reports for each rendered frame.
type FrameSample struct {
	// Dur is the measured render duration.
	Dur time.Duration

	// Streaming reports whether a turn is actually in flight, per the runtime's
	// turn state. It must NOT be derived from per-item finished/unfinished
	// state: that is what the cache-defeat bug corrupts, so deriving it there
	// would make the detector permanently silent under the very bug it exists
	// to catch.
	Streaming bool

	// Cache carries the caller's render-cache accounting for this frame.
	Cache CacheStats

	// At is the frame's timestamp. Zero means "use the collector's clock".
	At time.Time
}

// Config configures a Collector.
type Config struct {
	Detector DetectorConfig
	Now      func() time.Time // nil means time.Now
}

// Collector owns all render-health state. The per-frame path takes an
// uncontended mutex so an off-goroutine reader — the incident-bundle capture —
// can call Snapshot without racing the render loop.
type Collector struct {
	mu        sync.Mutex
	timer     FrameTimer
	cache     CacheStats
	streaming bool
	at        time.Time
	det       *Detector
	now       func() time.Time
}

// New returns a Collector. The detector is disabled unless cfg says otherwise.
func New(cfg Config) *Collector {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Collector{det: NewDetector(cfg.Detector), now: now}
}

// ObserveFrame records one frame and feeds the invariant detector. It returns a
// Report with ok true only on the first defective frame of an episode. It is
// allocation-free on the healthy path.
func (c *Collector) ObserveFrame(f FrameSample) (Report, bool) {
	at := f.At
	if at.IsZero() {
		at = c.now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer.Observe(f.Dur)
	c.cache = f.Cache
	c.streaming = f.Streaming
	c.at = at
	return c.det.Observe(at, f.Streaming, f.Cache)
}

// Snapshot returns everything a diagnostics view displays. The result is a pure
// value with no aliasing back into the collector.
func (c *Collector) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	at := c.at
	if at.IsZero() {
		at = c.now()
	}
	return Snapshot{
		At:        at,
		Frame:     c.timer.Stats(),
		Cache:     c.cache,
		Streaming: c.streaming,
		Detector:  c.det.Status(),
	}
}

// Reset clears all accumulated statistics, preserving configuration. Call it
// when the session restarts and the counters no longer apply.
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timer.Reset()
	c.cache = CacheStats{}
	c.streaming = false
	c.at = time.Time{}
	c.det.Reset()
}

// Snapshot is a point-in-time view of render health. At is the timestamp of the
// most recent observed frame, or the collector's clock if no frame has been
// observed yet.
type Snapshot struct {
	At        time.Time
	Frame     FrameStats
	Cache     CacheStats
	Streaming bool
	Detector  DetectorStatus
}
