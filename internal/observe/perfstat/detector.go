package perfstat

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Environment keys that gate and tune the detector. They are read once, at
// construction, via DetectorConfigFromEnv — never on the per-frame path.
const (
	envInvariant        = "SPRAWL_PERF_INVARIANT"
	envDefectStreak     = "SPRAWL_PERF_INVARIANT_STREAK"
	envUncacheableLimit = "SPRAWL_PERF_UNCACHEABLE_LIMIT"
)

// DefectKind is a bitmask of the signals that make up a defect.
type DefectKind uint8

const (
	// DefectIdleMisses means the render cache was rebuilt on an idle frame even
	// though nothing had changed (same revision, same width) — definitionally a
	// defeated cache.
	DefectIdleMisses DefectKind = 1 << iota
	// DefectUncacheable means too many items never reached a finished state, so
	// they can never be cached. This is the QUM-933 tell.
	DefectUncacheable
)

func (k DefectKind) String() string {
	var names []string
	if k&DefectIdleMisses != 0 {
		names = append(names, "idle-misses")
	}
	if k&DefectUncacheable != 0 {
		names = append(names, "uncacheable")
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "+")
}

// DetectorConfig tunes the invariant detector. Non-positive values are
// normalized to the defaults by NewDetector, so a spuriously zeroed threshold
// can never mean "trip on the first suspicious frame".
type DetectorConfig struct {
	Enabled          bool // when false, every hook is a no-op
	UncacheableLimit int  // trip when the unfinished-item count exceeds this
	DefectStreak     int  // consecutive defective idle frames before tripping
	RecoveryStreak   int  // consecutive healthy idle frames before re-arming
}

// DefaultDetectorConfig returns the tuned thresholds, with the detector left
// disabled: it is opt-in, and DetectorConfigFromEnv is what turns it on.
//
// At most one item should legitimately be in flight, so a limit of 2 leaves
// slack. Tripping is cheap to enter (30 frames, ~0.5s at 60fps) and expensive to
// leave (60 consecutive clean frames), which is the hysteresis that keeps a
// flapping condition down to one report. The recovery length is deliberately not
// tunable: shortening it only makes the detector noisier, which is the failure
// mode worth avoiding.
func DefaultDetectorConfig() DetectorConfig {
	return DetectorConfig{
		UncacheableLimit: 2,
		DefectStreak:     30,
		RecoveryStreak:   60,
	}
}

// DetectorConfigFromEnv builds a config from the debug env gate. lookup is
// injected (pass os.LookupEnv) so this is testable and so the process
// environment is read exactly once, at construction. Unset or malformed values
// fall back to the defaults; an unset gate leaves the detector disabled.
func DetectorConfigFromEnv(lookup func(string) (string, bool)) DetectorConfig {
	cfg := DefaultDetectorConfig()
	if raw, ok := lookup(envInvariant); ok {
		if enabled, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			cfg.Enabled = enabled
		}
	}
	if raw, ok := lookup(envDefectStreak); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			cfg.DefectStreak = n
		}
	}
	if raw, ok := lookup(envUncacheableLimit); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && n > 0 {
			cfg.UncacheableLimit = n
		}
	}
	return cfg
}

// Report is a structured diagnosis of one detected episode.
type Report struct {
	At          time.Time
	Kind        DefectKind
	Episode     int    // 1-based, incremented per episode
	IdleFrames  int    // consecutive defective idle frames so far
	IdleMisses  uint64 // rebuilds accumulated across those frames
	Items       int
	Uncacheable int
	Limit       int
	HitRate     float64
	Revision    uint64
	Width       int
}

// Zero reports whether r carries no diagnosis.
func (r Report) Zero() bool { return r.Kind == 0 && r.IdleFrames == 0 }

// Diagnosis renders a single-line, human-readable summary naming every count.
func (r Report) Diagnosis() string {
	if r.Zero() {
		return ""
	}
	return fmt.Sprintf(
		"render-cache defeat suspected (episode %d, %s): %d consecutive idle frames rebuilt the chat render with revision %d pinned at width %d (%d rebuilds, hit rate %s); %d of %d items are unfinished/uncacheable (limit %d)",
		r.Episode, r.Kind, r.IdleFrames, r.Revision, r.Width, r.IdleMisses,
		FormatPercent(r.HitRate), r.Uncacheable, r.Items, r.Limit,
	)
}

// DetectorStatus is the detector's state for display.
type DetectorStatus struct {
	Enabled    bool
	Tripped    bool // an episode is currently latched
	Episodes   int
	HasReport  bool
	LastReport Report
}

// Detector watches per-frame cache accounting for the pathological steady
// state: rebuilds that accumulate while nothing is happening, or a pile of
// items that can never be cached.
//
// Frames are classified by the caller-supplied streaming flag, which must come
// from the runtime's turn state — never from per-item finished/unfinished
// state, since that is exactly what the defect being detected corrupts.
//
// Not safe for concurrent use; Collector owns the synchronization.
type Detector struct {
	cfg DetectorConfig

	havePrev     bool
	prevHits     uint64
	prevMisses   uint64
	prevRevision uint64
	prevWidth    int

	defectStreak  int
	cleanStreak   int
	kind          DefectKind
	episodeMisses uint64

	tripped   bool
	episodes  int
	hasReport bool
	last      Report
}

// NewDetector returns a detector with cfg normalized.
func NewDetector(cfg DetectorConfig) *Detector {
	def := DefaultDetectorConfig()
	if cfg.UncacheableLimit <= 0 {
		cfg.UncacheableLimit = def.UncacheableLimit
	}
	if cfg.DefectStreak <= 0 {
		cfg.DefectStreak = def.DefectStreak
	}
	if cfg.RecoveryStreak <= 0 {
		cfg.RecoveryStreak = def.RecoveryStreak
	}
	return &Detector{cfg: cfg}
}

// Observe classifies one frame. It returns a Report with ok true only on the
// first defective frame of an episode, so a sustained defect reports once
// rather than once per frame.
//
// Frame classification:
//   - streaming: neutral, and clears any accumulating suspicion. Mid-turn
//     rebuilds and several unfinished items are both by design.
//   - idle with no cache lookup at all: neutral. An absence of rebuilds is not
//     evidence of health, so it neither clears suspicion nor credits recovery.
//   - idle with a rebuild while the revision and width are unchanged: a defect.
//     A rebuild after a revision or width change is legitimate invalidation.
//   - unfinished items above the limit: a defect regardless of lookups, since
//     it is a state rather than an event.
func (d *Detector) Observe(at time.Time, streaming bool, c CacheStats) (Report, bool) {
	if !d.cfg.Enabled {
		return Report{}, false
	}
	if streaming {
		d.rebaseline(c)
		d.abandonSuspicion()
		return Report{}, false
	}

	var missDelta, hitDelta uint64
	if d.havePrev {
		// Counters can restart (session restart, rehydrate). Treat any backwards
		// movement as a re-baseline rather than an enormous delta.
		if c.Misses >= d.prevMisses {
			missDelta = c.Misses - d.prevMisses
		}
		if c.Hits >= d.prevHits {
			hitDelta = c.Hits - d.prevHits
		}
	}
	pinned := d.havePrev && c.Revision == d.prevRevision && c.Width == d.prevWidth
	d.rebaseline(c)

	var kind DefectKind
	if missDelta > 0 && pinned {
		kind |= DefectIdleMisses
	}
	if c.Uncacheable > d.cfg.UncacheableLimit {
		kind |= DefectUncacheable
	}

	if kind != 0 {
		d.defectStreak++
		d.cleanStreak = 0 // recovery needs consecutive clean frames
		d.kind |= kind
		if kind&DefectIdleMisses != 0 {
			// Only rebuilds that happened with the revision and width pinned are
			// evidence; a rebuild after real invalidation is not.
			d.episodeMisses += missDelta
		}
		if d.defectStreak < d.cfg.DefectStreak {
			return Report{}, false
		}
		report := d.buildReport(at, c)
		if d.tripped {
			// Already reported this episode; keep the latched report current so
			// a diagnostics overlay shows live counts.
			d.last = report
			return Report{}, false
		}
		d.tripped = true
		d.episodes++
		report.Episode = d.episodes
		d.last = report
		d.hasReport = true
		return report, true
	}

	if missDelta+hitDelta == 0 {
		return Report{}, false // quiet frame: no evidence either way
	}
	d.abandonSuspicion()
	d.cleanStreak++
	if d.tripped && d.cleanStreak >= d.cfg.RecoveryStreak {
		d.tripped = false
		d.cleanStreak = 0
		d.kind = 0
		d.episodeMisses = 0
	}
	return Report{}, false
}

// abandonSuspicion drops an in-progress suspicion streak. While an episode is
// latched the accumulators belong to that episode, so they survive until
// recovery clears them.
func (d *Detector) abandonSuspicion() {
	d.defectStreak = 0
	if !d.tripped {
		d.kind = 0
		d.episodeMisses = 0
	}
}

// Status returns the detector's current state.
func (d *Detector) Status() DetectorStatus {
	return DetectorStatus{
		Enabled:    d.cfg.Enabled,
		Tripped:    d.tripped,
		Episodes:   d.episodes,
		HasReport:  d.hasReport,
		LastReport: d.last,
	}
}

// Reset clears all episode state, preserving the configuration. Call it when
// the session restarts and the counters it was tracking no longer apply.
func (d *Detector) Reset() { *d = Detector{cfg: d.cfg} }

func (d *Detector) rebaseline(c CacheStats) {
	d.prevHits = c.Hits
	d.prevMisses = c.Misses
	d.prevRevision = c.Revision
	d.prevWidth = c.Width
	d.havePrev = true
}

func (d *Detector) buildReport(at time.Time, c CacheStats) Report {
	return Report{
		At:          at,
		Kind:        d.kind,
		Episode:     d.episodes,
		IdleFrames:  d.defectStreak,
		IdleMisses:  d.episodeMisses,
		Items:       c.Items,
		Uncacheable: c.Uncacheable,
		Limit:       d.cfg.UncacheableLimit,
		HitRate:     c.HitRate(),
		Revision:    c.Revision,
		Width:       c.Width,
	}
}
