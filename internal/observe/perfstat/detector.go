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
//
// The two report paths produce DISJOINT Kinds by construction: the frame path
// sets only DefectIdleMisses and/or DefectUncacheable, the invariant path sets
// only DefectOrphans. A Kind mixing them is unreachable — the bitmask exists so
// the frame path's two bits can combine, and so String() can name any
// combination, not because a mixed report is produced anywhere.
type DefectKind uint8

const (
	// DefectIdleMisses means the render cache was rebuilt on an idle frame even
	// though nothing had changed (same revision, same width) — definitionally a
	// defeated cache.
	DefectIdleMisses DefectKind = 1 << iota
	// DefectUncacheable means too many items never reached a finished state, so
	// they can never be cached. This is the QUM-933 tell.
	DefectUncacheable
	// DefectOrphans means items are stranded unfinished somewhere other than the
	// tail, so they re-render through the full markdown pipeline on every rebuild
	// forever. Unlike the other two this is not a threshold heuristic but a
	// direct violation of the chat model's cacheability invariant, so it reports
	// on a short confirmation run of observations rather than a long
	// consecutive-frame streak. See ObserveInvariant.
	DefectOrphans
)

func (k DefectKind) String() string {
	var names []string
	if k&DefectIdleMisses != 0 {
		names = append(names, "idle-misses")
	}
	if k&DefectUncacheable != 0 {
		names = append(names, "uncacheable")
	}
	if k&DefectOrphans != 0 {
		names = append(names, "orphans")
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

	// InvariantConfirmations and InvariantRecovery are counted in invariant
	// OBSERVATIONS, not frames — the caller may sample only one frame in N, and
	// a frame-counted threshold could never be reached by such a signal.
	//
	// "Consecutive" means consecutive among observations that carry evidence:
	// streaming observations are skipped rather than counted or reset, so a
	// recovery run spans turns. This mirrors the frame path, where a streaming
	// frame clears suspicion without touching the clean streak.
	//
	// Neither is env-tunable, for the same reason RecoveryStreak is not:
	// shortening either only makes the detector noisier, which is the failure
	// mode worth avoiding.
	InvariantConfirmations int // consecutive violating observations before reporting
	InvariantRecovery      int // consecutive clean observations before re-arming
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
//
// The invariant thresholds are far shorter because the signal is categorically
// different: a violation is not a heuristic that needs corroborating, it is a
// broken invariant. Two confirmations only guard against a caller sampling a
// transient mid-mutation state; re-arming takes ten so a flickering condition
// reports once rather than once per flicker.
func DefaultDetectorConfig() DetectorConfig {
	return DetectorConfig{
		UncacheableLimit:       2,
		DefectStreak:           30,
		RecoveryStreak:         60,
		InvariantConfirmations: 2,
		InvariantRecovery:      10,
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

	Orphans            int // invariant-violating items at observation time
	OrphanObservations int // consecutive violating observations so far

	// HaveFrameContext reports whether the cache fields above carry a real
	// measurement. An orphan observation can arrive before any frame — nothing
	// orders the two calls, and Collector.Reset clears the frame context — in
	// which case Items/Revision/Width/HitRate are zero values, not readings.
	HaveFrameContext bool
}

// Zero reports whether r carries no diagnosis. Every constructed report sets at
// least one Kind bit, so an unset Kind is the whole test — the previous
// IdleFrames conjunct was unreachable and invited readers to hunt for a
// Kind-less-but-populated report that cannot exist.
func (r Report) Zero() bool { return r.Kind == 0 }

// Diagnosis renders a single-line, human-readable summary naming every count.
//
// The two paths report different evidence, so the body is chosen rather than
// concatenated — an orphan report must not claim that rebuilds were observed
// when none were. The paths' Kinds are disjoint (see DefectKind), so this is an
// either/or, not an accumulation.
func (r Report) Diagnosis() string {
	if r.Zero() {
		return ""
	}
	head := fmt.Sprintf("render-cache defeat suspected (episode %d, %s)", r.Episode, r.Kind)
	if r.Kind&DefectOrphans == 0 {
		return head + fmt.Sprintf(
			": %d consecutive idle frames rebuilt the chat render with revision %d pinned at width %d (%d rebuilds, hit rate %s); %d of %d items are unfinished/uncacheable (limit %d)",
			r.IdleFrames, r.Revision, r.Width, r.IdleMisses,
			FormatPercent(r.HitRate), r.Uncacheable, r.Items, r.Limit,
		)
	}
	// An invariant violation is not a suspicion, so it says "violated".
	head += fmt.Sprintf(": cacheability invariant violated on %d consecutive observations; ",
		r.OrphanObservations)
	if !r.HaveFrameContext {
		// No frame observed yet: the cache fields are zero values, not
		// measurements, so naming them would fabricate a reading — including
		// the item total, which would render as "22 of 0".
		return head + fmt.Sprintf(
			"%d items are stranded unfinished away from the tail, so every rebuild re-renders them (no frame observed yet, so no cache context)",
			r.Orphans,
		)
	}
	return head + fmt.Sprintf(
		"%d of %d items are stranded unfinished away from the tail, so every rebuild re-renders them (revision %d, width %d, hit rate %s)",
		r.Orphans, r.Items, r.Revision, r.Width, FormatPercent(r.HitRate),
	)
}

// DetectorStatus is the detector's state for display.
type DetectorStatus struct {
	Enabled bool
	Tripped bool // a frame-path episode is currently latched
	// OrphanTripped is a separate latch from Tripped. The two signals are
	// independent evidence, so neither may suppress the other's report: on a
	// machine with the bug, forced rebuilds and stranded items co-occur, and
	// sharing one latch would silently swallow whichever arrived second.
	OrphanTripped bool
	Episodes      int
	HasReport     bool
	LastReport    Report
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

	tripped bool
	// frameEpisode is the episode number the frame path's latched episode owns.
	// It must NOT be re-read from the shared episodes counter when refreshing a
	// latched report: the other path may have opened an episode since, and the
	// refresh would relabel this defect with that episode's number.
	frameEpisode int

	// Orphan-path state. Counted in observations, not frames — see
	// DetectorConfig.InvariantConfirmations.
	orphanObservations int // consecutive violating observations
	orphanClean        int // consecutive clean observations since the trip
	orphanTripped      bool
	orphanEpisode      int // as frameEpisode, for the orphan path

	// episodes, hasReport and last are shared by both paths: an overlay wants
	// one "most recent diagnosis" and one monotonic episode number, regardless
	// of which signal produced it. Only the latches and the per-path episode
	// labels are per-path.
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
	if cfg.InvariantConfirmations <= 0 {
		cfg.InvariantConfirmations = def.InvariantConfirmations
	}
	if cfg.InvariantRecovery <= 0 {
		cfg.InvariantRecovery = def.InvariantRecovery
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
		firstOfEpisode := !d.tripped
		if firstOfEpisode {
			d.tripped = true
			d.episodes++
			d.frameEpisode = d.episodes
		}
		// Labeled with the episode this path owns, not the shared counter: the
		// other path may have opened an episode since this one latched.
		report := d.buildReport(at, c)
		d.last = report
		d.hasReport = true
		if !firstOfEpisode {
			// Already reported this episode. The latched report was refreshed
			// above so an overlay shows live counts, but the caller is told once.
			return Report{}, false
		}
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
		Enabled:       d.cfg.Enabled,
		Tripped:       d.tripped,
		OrphanTripped: d.orphanTripped,
		Episodes:      d.episodes,
		HasReport:     d.hasReport,
		LastReport:    d.last,
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
		Episode:     d.frameEpisode,
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
