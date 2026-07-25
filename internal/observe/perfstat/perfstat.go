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
// uncontended mutex.
//
// PROVENANCE OF THE LOCK, so nobody has to guess whether it is doing anything:
// as of this commit the only wired caller is the TUI, and internal/tui has no
// goroutines in non-test code — Update and View both run on the single
// bubbletea loop, so there is exactly one writer and one reader and the lock
// guards nothing yet. It is here for the reader that is planned but NOT built:
// an off-goroutine Snapshot from the incident-bundle capture. Keep it, because
// adding that caller must not require also noticing that this type was never
// safe; but do not read this comment as evidence that a second goroutine exists
// today. If you are here because you added one, this is the sentence that
// becomes true.
type Collector struct {
	mu        sync.Mutex
	timer     FrameTimer
	cache     CacheStats
	streaming bool
	at        time.Time
	det       *Detector
	now       func() time.Time

	invariant InvariantStatus
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

// InvariantEnabled reports whether the invariant check is on. The caller needs
// this to decide whether to perform the O(n) orphan walk at all: when the check
// is off the walk is pure waste, so a disabled collector must tell the caller
// not to bother rather than silently discard the result.
//
// The mutex here is not redundant despite cfg being conceptually immutable:
// Detector.Reset does `*d = Detector{cfg: d.cfg}`, which writes cfg's memory,
// so an unlocked read would race a concurrent Reset. That race needs a second
// goroutine to exist, which today it does not (see the Collector doc) — so the
// honest statement is conditional: this lock is what makes adding one safe, and
// removing it as "obvious overhead on the render path" would make the first
// off-goroutine Snapshot racy in a way that is invisible from this read site.
func (c *Collector) InvariantEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.det.cfg.Enabled
}

// ObserveInvariant records one out-of-band invariant observation and feeds the
// detector. It returns a Report with ok true only on the first confirmed
// violation of an episode.
//
// Call it BEFORE or AFTER the caller's timed render region, never inside it:
// the orphan count comes from an O(n) walk, and timing that walk as part of a
// frame would make the reported percentiles describe this diagnostic rather
// than the app. That is why this is a separate call from ObserveFrame and not a
// field on FrameSample — do not "tidy" it back into the frame path.
func (c *Collector) ObserveInvariant(s InvariantSample) (Report, bool) {
	at := s.At
	if at.IsZero() {
		at = c.now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.det.cfg.Enabled {
		// Leave Checked false: a disabled check has measured nothing, and
		// recording it as a measurement would let an overlay display a
		// reassuring zero.
		return Report{}, false
	}
	c.invariant = InvariantStatus{
		Enabled:   true,
		Checked:   true,
		Orphans:   s.Orphans,
		Streaming: s.Streaming,
		At:        at,
	}
	return c.det.ObserveInvariant(at, s, c.cache)
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
	inv := c.invariant
	inv.Enabled = c.det.cfg.Enabled
	return Snapshot{
		At:        at,
		Frame:     c.timer.Stats(),
		Cache:     c.cache,
		Streaming: c.streaming,
		Detector:  c.det.Status(),
		Invariant: inv,
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
	c.invariant = InvariantStatus{}
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
	Invariant InvariantStatus
}
