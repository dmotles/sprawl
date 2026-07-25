package perfstat

import "time"

// InvariantSample is an out-of-band measurement of the chat model's
// cacheability invariant: no item is stranded in an unfinished state where it
// can never be cached.
//
// It is deliberately NOT a field on FrameSample. Computing Orphans is an O(n)
// walk over every chat item, and if the caller ran that walk inside its timed
// render region the frame percentiles would describe the detector instead of
// the app — a diagnostic that corrupts its own readings. A separate call makes
// the ordering explicit at the call site and hard to get wrong. Call it before
// or after the timed render, never inside it.
type InvariantSample struct {
	// Orphans is the number of items that violate the invariant: unfinished
	// assistant-text items that are not the tail. The tail is excluded because
	// a trailing unfinished item is the legitimately streaming one.
	//
	// It must be computed from real per-item finished state. It must NOT be
	// derived from turn-scoped bookkeeping (a "streaming" flag, a pending-tool
	// counter): those read ~zero in exactly the stranded state this check
	// exists to catch, which would make the check silent under its own bug.
	//
	// In-flight tool calls are NOT orphans. They are uncacheable — see
	// CacheStats.Uncacheable — but being uncacheable is not a violation;
	// tool calls legitimately stay pending for as long as the tool runs, and
	// counting them here would keep the check permanently tripped, which is
	// as useless as permanently silent.
	Orphans int

	// Streaming reports whether a turn is actually in flight, per the runtime's
	// turn state. Same contract as FrameSample.Streaming: never derive it from
	// per-item finished state. Mid-turn the invariant does not hold, so
	// streaming observations are neutral.
	Streaming bool

	// At is the observation's timestamp. Zero means "use the collector's clock".
	At time.Time
}

// InvariantStatus is the invariant check's state for display.
//
// Checked distinguishes "measured, no orphans" from "never measured". Without
// it a diagnostics overlay would render a reassuring zero for a check that
// never ran, which is the specific dishonesty this whole mechanism exists to
// rule out.
type InvariantStatus struct {
	Enabled bool
	Checked bool
	Orphans int

	// Streaming reports that the last observation was taken mid-turn, when the
	// invariant does not hold and a nonzero Orphans is expected. A display that
	// ignores this would alarm on a healthy streaming turn — the mirror of the
	// false-zero problem Checked prevents, and just as dishonest.
	Streaming bool

	At time.Time
}

// ObserveInvariant classifies one invariant observation against the most recent
// frame's cache context. It returns a Report with ok true only on the first
// confirmed violation of an episode.
//
// The check is DIRECTIONAL: the invariant is "orphans == 0", so any positive
// count is a violation. It is deliberately not an equality check against
// CacheStats.Uncacheable, which legitimately over-counts — a settled tail item
// can leave a turn-scoped flag set with nothing actually unfinished — so an
// equality check would fire on healthy frames. A check that cries wolf gets
// disabled, which is worse than not having it.
//
// Classification:
//   - disabled: no-op.
//   - streaming: neutral. Mid-turn an unfinished non-tail item is by design, so
//     this clears any accumulating confirmation run and never credits recovery.
//   - orphans > 0 on InvariantConfirmations consecutive observations: a defect.
//   - orphans == 0: credits re-arming, which needs InvariantRecovery clean
//     observations with no violation in between. Streaming observations are
//     skipped rather than counted or reset, so a run may span turns.
//
// Confirmation and recovery are counted in OBSERVATIONS, not frames. The caller
// may sample only one frame in N, so a threshold counting consecutive frames
// could never be reached and the check would be wired, exercised, and dead.
func (d *Detector) ObserveInvariant(at time.Time, s InvariantSample, c CacheStats) (Report, bool) {
	if !d.cfg.Enabled {
		return Report{}, false
	}
	if s.Streaming {
		d.orphanObservations = 0
		return Report{}, false
	}

	if s.Orphans <= 0 {
		d.orphanObservations = 0
		d.orphanClean++
		if d.orphanTripped && d.orphanClean >= d.cfg.InvariantRecovery {
			d.orphanTripped = false
		}
		return Report{}, false
	}

	d.orphanObservations++
	d.orphanClean = 0 // re-arming needs clean observations with no violation between
	if d.orphanObservations < d.cfg.InvariantConfirmations {
		return Report{}, false
	}

	firstOfEpisode := !d.orphanTripped
	if firstOfEpisode {
		d.orphanTripped = true
		d.episodes++
		d.orphanEpisode = d.episodes
	}
	// Labeled with the episode this path owns, not the shared counter: the frame
	// path may have opened an episode since this one latched.
	report := d.buildOrphanReport(at, s, c)
	d.last = report
	d.hasReport = true
	if !firstOfEpisode {
		// Already reported this episode. The latched report was refreshed above
		// so an overlay shows live counts, but the caller is told once.
		return Report{}, false
	}
	return report, true
}

// buildOrphanReport labels the report with the orphan path's own episode and
// carries whatever frame context exists.
//
// Limit is deliberately NOT set: UncacheableLimit governs nothing in a
// directional check, and carrying it would imply to anyone reading a structured
// dump that a threshold was applied to the orphan count.
//
// HaveFrameContext records whether any frame has been observed yet. Without it
// a report rendered before the first frame would present the zero CacheStats as
// measurements — "22 of 0 items", revision 0, width 0, hit rate 0.0% — which
// is exactly the kind of confidently-wrong readout this detector exists to
// avoid. An observation legitimately lands first after Collector.Reset.
func (d *Detector) buildOrphanReport(at time.Time, s InvariantSample, c CacheStats) Report {
	return Report{
		At:                 at,
		Kind:               DefectOrphans,
		Episode:            d.orphanEpisode,
		Orphans:            s.Orphans,
		OrphanObservations: d.orphanObservations,
		Items:              c.Items,
		Uncacheable:        c.Uncacheable,
		HitRate:            c.HitRate(),
		Revision:           c.Revision,
		Width:              c.Width,
		HaveFrameContext:   d.havePrev,
	}
}
