package perfstat

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// invariant feeds one out-of-band invariant observation, mirroring how the TUI
// will call it: the caller walks the item list outside its timed render region
// and hands the resulting orphan count in. mutate adjusts the running cache
// counters first, matching feeder.frame's shape.
func (f *feeder) invariant(streaming bool, orphans int, mutate func(c *CacheStats)) (Report, bool) {
	f.t.Helper()
	if mutate != nil {
		mutate(&f.cache)
	}
	f.at = f.at.Add(16 * time.Millisecond)
	rep, ok := f.det.ObserveInvariant(f.at, InvariantSample{
		Orphans:   orphans,
		Streaming: streaming,
	}, f.cache)
	if ok {
		f.reports = append(f.reports, rep)
	}
	return rep, ok
}

func (f *feeder) invariants(n int, streaming bool, orphans int) {
	f.t.Helper()
	for range n {
		f.invariant(streaming, orphans, nil)
	}
}

// warmCache puts the feeder in the state the QUM-933 leak hides in: a long
// transcript whose outer (width, revision) render cache is warm, so every
// subsequent idle frame is a cache HIT and the stranded items cost nothing at
// rest. Deliberately named for what it does rather than "reload" — it does not
// reset the detector, so it does not model a session restart.
//
// Mutates only the fields it names, so it is safe to call after other cache
// fields have been set and cannot look like a mid-stream counter reset.
func (f *feeder) warmCache(items int) {
	f.t.Helper()
	f.cache.Items = items
	f.cache.Revision = 5000
	f.cache.Width = 120
}

// orphanCfg is the enabled config with the confirmation and re-arm thresholds
// pinned to literals that deliberately DIFFER from the tuned defaults. That
// serves two purposes: these tests keep meaning what they say if the defaults
// are retuned, and an implementation that hardcodes the thresholds instead of
// reading them from the config cannot pass.
func orphanCfg() DetectorConfig {
	cfg := enabledCfg()
	cfg.InvariantConfirmations = 3
	cfg.InvariantRecovery = 7
	return cfg
}

// TestOrphanCfg_DiffersFromDefaults guards the property the rest of this file
// leans on: if orphanCfg's thresholds ever coincide with the defaults, every
// threshold assertion here would pass against an implementation that ignores
// the config entirely.
func TestOrphanCfg_DiffersFromDefaults(t *testing.T) {
	def, cfg := DefaultDetectorConfig(), orphanCfg()
	if cfg.InvariantConfirmations == def.InvariantConfirmations {
		t.Errorf("orphanCfg().InvariantConfirmations == the default (%d); pick a "+
			"different value or the config plumbing goes unproven", def.InvariantConfirmations)
	}
	if cfg.InvariantRecovery == def.InvariantRecovery {
		t.Errorf("orphanCfg().InvariantRecovery == the default (%d); pick a "+
			"different value or the config plumbing goes unproven", def.InvariantRecovery)
	}
}

// TestDetector_StrandedOrphansTrip is the core case. Stranded unfinished items
// report a defect even though the outer render cache is hitting on every frame.
func TestDetector_StrandedOrphansTrip(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	// Idle frames that all HIT: the pathology costs nothing at rest.
	f.frames(50, false, hit)
	f.invariants(cfg.InvariantConfirmations, false, 22)

	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want exactly 1", len(f.reports))
	}
	rep := f.reports[0]
	// Exact equality, not a bitmask test: if this passed via DefectIdleMisses
	// the test would be proving the wrong signal fired.
	if rep.Kind != DefectOrphans {
		t.Errorf("Kind = %v, want exactly %v", rep.Kind, DefectOrphans)
	}
	if rep.Orphans != 22 {
		t.Errorf("Orphans = %d, want 22", rep.Orphans)
	}
	if rep.OrphanObservations != cfg.InvariantConfirmations {
		t.Errorf("OrphanObservations = %d, want %d", rep.OrphanObservations, cfg.InvariantConfirmations)
	}
	if rep.Episode != 1 {
		t.Errorf("Episode = %d, want 1", rep.Episode)
	}
	// An orphan-only report carries no idle-miss evidence, and must not
	// manufacture any.
	if rep.IdleFrames != 0 || rep.IdleMisses != 0 {
		t.Errorf("IdleFrames/IdleMisses = %d/%d, want 0/0 for an orphan-only report",
			rep.IdleFrames, rep.IdleMisses)
	}
	// Diagnosis() early-returns on Zero(), so an orphan report that read as zero
	// would render a blank diagnosis rather than a missing one.
	if rep.Zero() {
		t.Error("orphan Report.Zero() = true, want false (Diagnosis() would blank)")
	}
	// The overlay and the incident bundle both render this timestamp.
	if !rep.At.Equal(f.at) {
		t.Errorf("At = %v, want the observation's timestamp %v", rep.At, f.at)
	}

	st := f.det.Status()
	if !st.OrphanTripped {
		t.Error("Status().OrphanTripped = false, want true")
	}
	if !st.HasReport {
		t.Error("Status().HasReport = false, want true")
	}
	if st.LastReport.Kind != DefectOrphans || st.LastReport.Orphans != 22 {
		t.Errorf("Status().LastReport = %+v, want the orphan report latched", st.LastReport)
	}
}

// TestDetector_SingleOrphanTrips pins that the orphan check is DIRECTIONAL and
// exact — the invariant is orphans == 0, so one orphan is a defect.
//
// This is the guard against implementing the orphan signal as a threshold
// comparison rather than a zero check. Every other test in this file uses
// counts well above any plausible threshold, so they would all pass against a
// thresholded implementation; only this one fails. (The retired
// UncacheableLimit was the specific tempting operand — same struct, defaulted
// to 2 — but the trap outlives the field it named.)
func TestDetector_SingleOrphanTrips(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(cfg.InvariantConfirmations, false, 1)

	if len(f.reports) != 1 {
		t.Fatalf("got %d reports for a single orphan, want 1 — the invariant is "+
			"orphans == 0, not orphans > a threshold", len(f.reports))
	}
	if got := f.reports[0].Orphans; got != 1 {
		t.Errorf("Orphans = %d, want 1", got)
	}
}

// TestDetector_ZeroOrphansNeverTrips is the other half of the boundary.
func TestDetector_ZeroOrphansNeverTrips(t *testing.T) {
	f := newFeeder(t, orphanCfg())
	f.warmCache(200)
	f.invariants(10_000, false, 0)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports with zero orphans, want 0", len(f.reports))
	}
}

// TestDetector_FramePathAloneNeverReportsOrphans pins the in-package half of the
// blindness argument: no sequence of frame observations can produce a
// DefectOrphans report, so the signal cannot arrive by accident from the
// existing two.
//
// It deliberately does NOT claim to prove the blindness premise itself. That
// premise is about the CALLER's derivation — that with items stranded the outer
// cache still hits (zero miss delta) and a flag-derived Uncacheable reads 0 —
// and it can only be proven where the derivation lives, in the TUI wiring
// slice. Asserting it here would just be restating the stipulation.
func TestDetector_FramePathAloneNeverReportsOrphans(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.cache.Uncacheable = 0 // what turn-scoped flags report in the stranded state

	f.frames(5_000, false, hit)
	f.frames(5_000, false, quiet)
	f.frames(5_000, false, legitInvalidation)
	// Pin the premise rather than assume it: if any of those batches did report,
	// the scan below would be vacuously satisfied.
	if len(f.reports) != 0 {
		t.Fatalf("got %d frame-path reports, want 0 — the scan below would be vacuous",
			len(f.reports))
	}
	for _, rep := range f.reports {
		if rep.Kind&DefectOrphans != 0 {
			t.Fatalf("frame path produced a DefectOrphans report: %+v", rep)
		}
	}

	f.invariants(cfg.InvariantConfirmations, false, 22)
	orphanReports := 0
	for _, rep := range f.reports {
		if rep.Kind == DefectOrphans {
			orphanReports++
		}
	}
	if orphanReports != 1 {
		t.Fatalf("got %d orphan reports after supplying observations, want 1", orphanReports)
	}
}

// TestDetector_RebuildForcingFramesStillSilentWhenOrphanFree covers the other
// side of the steady-state trap: a spinner tick or keystroke forces real
// rebuilds, which must not be mistaken for the defect when nothing is stranded.
func TestDetector_RebuildForcingFramesStillSilentWhenOrphanFree(t *testing.T) {
	f := newFeeder(t, orphanCfg())
	f.warmCache(200)
	for range 200 {
		f.frame(false, legitInvalidation)
		f.invariant(false, 0, nil)
	}
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports with orphan-free rebuild-forcing frames, want 0", len(f.reports))
	}
}

// TestDetector_OvercountedUncacheableWithZeroOrphansStaysSilent is why the
// check is directional rather than an equality against the derived count.
//
// ChatList settles an intermediate assistant item mid-turn but deliberately
// leaves the turn-scoped streamingAssistant flag set, so a derived uncacheable
// count over-reports by one with nothing actually unfinished. An equality check
// (derived == actual) would fire on every healthy post-settle frame, and a
// check that cries wolf gets disabled — which is worse than not having it.
func TestDetector_OvercountedUncacheableWithZeroOrphansStaysSilent(t *testing.T) {
	f := newFeeder(t, orphanCfg())
	f.warmCache(200)
	f.cache.Uncacheable = 1 // stale flag; actual unfinished count is 0
	f.frames(1_000, false, hit)
	f.invariants(1_000, false, 0)

	if len(f.reports) != 0 {
		t.Fatalf("got %d reports with an over-counted Uncacheable and no orphans, want 0",
			len(f.reports))
	}
}

// TestDetector_OrphansNeutralWhileStreaming pins that mid-turn is neutral: an
// unfinished non-tail item is by design while a turn is in flight.
func TestDetector_OrphansNeutralWhileStreaming(t *testing.T) {
	f := newFeeder(t, orphanCfg())
	f.warmCache(200)
	f.invariants(10_000, true, 5)
	if len(f.reports) != 0 {
		t.Fatalf("got %d reports while streaming, want 0", len(f.reports))
	}
	if f.det.Status().OrphanTripped {
		t.Error("Status().OrphanTripped = true from streaming observations, want false")
	}
}

// TestDetector_StreamingObservationClearsOrphanSuspicion pins that a streaming
// observation resets the confirmation run, mirroring the frame path's
// streaming-clears-suspicion rule. Without this, suspicion accumulated across
// unrelated turns could add up to a trip.
func TestDetector_StreamingObservationClearsOrphanSuspicion(t *testing.T) {
	cfg := orphanCfg()
	if cfg.InvariantConfirmations < 2 {
		t.Fatalf("InvariantConfirmations = %d, want >= 2 for this test to bite",
			cfg.InvariantConfirmations)
	}
	f := newFeeder(t, cfg)
	f.warmCache(200)

	f.invariants(cfg.InvariantConfirmations-1, false, 7) // one short of confirming
	f.invariant(true, 7, nil)                            // a turn starts: neutral, clears the run
	f.invariants(cfg.InvariantConfirmations-1, false, 7) // one short again

	if len(f.reports) != 0 {
		t.Fatalf("got %d reports, want 0 — a streaming observation must clear the "+
			"confirmation run rather than let two partial runs add up", len(f.reports))
	}
}

// TestDetector_OrphanConfirmationsRequired pins that confirmation requires
// consecutive violating observations.
func TestDetector_OrphanConfirmationsRequired(t *testing.T) {
	cfg := orphanCfg()
	cfg.InvariantConfirmations = 2
	tests := []struct {
		name    string
		orphans []int
		want    int
	}{
		{"single violation is not enough", []int{7}, 0},
		{"consecutive violations confirm", []int{7, 7}, 1},
		{"a clean observation resets the run", []int{7, 0, 7}, 0},
		{"sustained violation reports once", []int{7, 7, 7, 7, 7, 7}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFeeder(t, cfg)
			f.warmCache(200)
			for _, n := range tc.orphans {
				f.invariant(false, n, nil)
			}
			if len(f.reports) != tc.want {
				t.Errorf("got %d reports, want %d", len(f.reports), tc.want)
			}
		})
	}
}

// TestDetector_ConfirmationsCountObservationsNotFrames exercises the mechanism
// at a realistic sampling rate rather than 1-in-1.
//
// The caller's orphan walk is O(n) and need not run every frame, so most frames
// carry no invariant observation at all. The confirmation counter is therefore
// counted in OBSERVATIONS, never in frames: a counter keyed on consecutive
// frames could never be reached by a signal sampled once every N frames, so the
// check would be wired, exercised, and dead.
//
// The interleaved frames deliberately span every frame branch that behaves
// differently — quiet (early return), hit (credits the clean streak),
// legitInvalidation (can un-latch the frame path), and streaming (clears frame
// suspicion) — because each is a different way a shared counter could be
// clobbered.
func TestDetector_ConfirmationsCountObservationsNotFrames(t *testing.T) {
	const every = 30 // a plausible caller cadence
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)

	interleave := []func(*CacheStats){quiet, hit, legitInvalidation}
	for i := range cfg.InvariantConfirmations {
		for j := range every - 1 {
			f.frame(j%7 == 0, interleave[j%len(interleave)])
		}
		f.invariant(false, 22, nil)
		if i == 0 && len(f.reports) != 0 {
			t.Fatalf("tripped after 1 observation, want %d", cfg.InvariantConfirmations)
		}
	}

	orphanReports := 0
	for _, rep := range f.reports {
		if rep.Kind == DefectOrphans {
			orphanReports++
			if rep.OrphanObservations != cfg.InvariantConfirmations {
				t.Errorf("OrphanObservations = %d, want %d (frames must not inflate it)",
					rep.OrphanObservations, cfg.InvariantConfirmations)
			}
		}
	}
	if orphanReports != 1 {
		t.Fatalf("got %d orphan reports at a 1-in-%d sampling rate, want 1 — "+
			"confirmations must count observations, not frames", orphanReports, every)
	}
}

// TestDetector_UnsampledFramesDoNotUnlatchOrphanEpisode pins that a latched
// orphan episode is not cleared by frame activity. Only clean invariant
// observations count toward re-arming, because only they carry evidence about
// the invariant.
func TestDetector_UnsampledFramesDoNotUnlatchOrphanEpisode(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(cfg.InvariantConfirmations, false, 9)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}

	f.frames(10_000, false, hit) // plenty of healthy frames, no observations
	if !f.det.Status().OrphanTripped {
		t.Error("Status().OrphanTripped = false after frame-only activity, want true — " +
			"frames carry no evidence about the invariant")
	}
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 (no re-report while latched)", len(f.reports))
	}
}

// TestDetector_OrphanRecoveryRequiresFullStreak pins asymmetric hysteresis:
// cheap to enter (InvariantConfirmations), expensive to leave
// (InvariantRecovery). One clean observation must not un-latch, or a flickering
// condition reports once per flicker.
func TestDetector_OrphanRecoveryRequiresFullStreak(t *testing.T) {
	cfg := orphanCfg()
	if cfg.InvariantRecovery <= cfg.InvariantConfirmations {
		t.Fatalf("InvariantRecovery = %d, want > InvariantConfirmations = %d "+
			"(cheap to enter, expensive to leave)", cfg.InvariantRecovery, cfg.InvariantConfirmations)
	}
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(cfg.InvariantConfirmations, false, 9)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}

	f.invariants(cfg.InvariantRecovery-1, false, 0) // one short of re-arming
	if !f.det.Status().OrphanTripped {
		t.Error("Status().OrphanTripped = false one observation short of recovery, want true")
	}
	f.invariant(false, 0, nil)
	if f.det.Status().OrphanTripped {
		t.Error("Status().OrphanTripped = true after a full recovery streak, want false")
	}
}

// TestDetector_FlickeringOrphansReportOnce pins that a condition that keeps
// oscillating stays one episode rather than re-reporting on every relapse.
func TestDetector_FlickeringOrphansReportOnce(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	for range 20 {
		f.invariants(cfg.InvariantConfirmations, false, 9)
		f.invariants(cfg.InvariantRecovery-1, false, 0) // never a full recovery
	}
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports from a flickering condition, want 1", len(f.reports))
	}
}

// TestDetector_OrphanReArmAfterRecovery pins that a genuine recovery re-arms, so
// a later relapse is reported as a new episode.
func TestDetector_OrphanReArmAfterRecovery(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)

	f.invariants(cfg.InvariantConfirmations, false, 9)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 after the first episode", len(f.reports))
	}
	// Sustained: still one report, but the latched report stays current so an
	// overlay shows live counts.
	f.invariants(100, false, 9)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 while latched", len(f.reports))
	}
	if got := f.det.Status().LastReport.OrphanObservations; got <= cfg.InvariantConfirmations {
		t.Errorf("LastReport.OrphanObservations = %d, want it to advance past %d while latched",
			got, cfg.InvariantConfirmations)
	}

	f.invariants(cfg.InvariantRecovery, false, 0)
	if f.det.Status().OrphanTripped {
		t.Fatal("Status().OrphanTripped = true after recovery, want false")
	}
	f.invariants(cfg.InvariantConfirmations, false, 9)
	if len(f.reports) != 2 {
		t.Fatalf("got %d reports, want 2 after relapse", len(f.reports))
	}
	if got := f.reports[1].Episode; got != 2 {
		t.Errorf("second report Episode = %d, want 2", got)
	}
}

// --- Interaction between the two report paths ---

// TestDetector_LatchedFrameEpisodeDoesNotSwallowOrphanReport is the interaction
// case that would most plausibly ship dead. The two paths share Report /
// episode state, and the frame path suppresses re-reports while latched. On a
// real machine a keystroke storm (forced rebuilds) and stranded orphans
// co-occur — so if the orphan path reused the frame latch, the orphan report
// would be silently swallowed exactly when it matters most.
func TestDetector_LatchedFrameEpisodeDoesNotSwallowOrphanReport(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)

	f.frames(cfg.DefectStreak+1, false, defeat) // trip the frame path
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 from the frame path", len(f.reports))
	}
	if !f.det.Status().Tripped {
		t.Fatal("Status().Tripped = false, want true")
	}

	f.invariants(cfg.InvariantConfirmations, false, 22)
	if len(f.reports) != 2 {
		t.Fatalf("got %d reports, want 2 — a latched frame episode must not "+
			"suppress the orphan report", len(f.reports))
	}
	orphan := f.reports[1]
	if orphan.Kind != DefectOrphans {
		t.Errorf("Kind = %v, want exactly %v", orphan.Kind, DefectOrphans)
	}
	if orphan.Episode != 2 {
		t.Errorf("Episode = %d, want 2 (episodes are numbered across both paths)", orphan.Episode)
	}
}

// TestDetector_LatchedOrphanEpisodeDoesNotSwallowFrameReport is the mirror.
func TestDetector_LatchedOrphanEpisodeDoesNotSwallowFrameReport(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)

	f.invariants(cfg.InvariantConfirmations, false, 22)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 from the orphan path", len(f.reports))
	}
	if f.det.Status().Tripped {
		t.Error("Status().Tripped = true from an orphan trip, want false " +
			"(the frame latch is separate)")
	}

	f.frames(cfg.DefectStreak+1, false, defeat)
	if len(f.reports) != 2 {
		t.Fatalf("got %d reports, want 2 — a latched orphan episode must not "+
			"suppress the frame report", len(f.reports))
	}
	frame := f.reports[1]
	// Exact equality, symmetric with the test above: a leftover DefectOrphans
	// bit on a frame report is the same cross-contamination in the other
	// direction.
	if frame.Kind != DefectIdleMisses {
		t.Errorf("second report Kind = %v, want exactly %v", frame.Kind, DefectIdleMisses)
	}
	if frame.Orphans != 0 || frame.OrphanObservations != 0 {
		t.Errorf("frame report Orphans/OrphanObservations = %d/%d, want 0/0 (no fabrication "+
			"from the latched orphan episode)", frame.Orphans, frame.OrphanObservations)
	}
}

// TestDetector_StreamingObservationDoesNotCreditOrphanRecovery is the mirror of
// the frame path's streaming-does-not-credit-recovery rule. A latched orphan
// episode must not be un-latched by a busy session: a streaming observation
// carries no evidence that the invariant was restored, so treating it as clean
// would silently clear the latch mid-defect.
func TestDetector_StreamingObservationDoesNotCreditOrphanRecovery(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(cfg.InvariantConfirmations, false, 22)
	if !f.det.Status().OrphanTripped {
		t.Fatal("Status().OrphanTripped = false, want true")
	}

	f.invariants(10_000, true, 5)
	if !f.det.Status().OrphanTripped {
		t.Fatal("Status().OrphanTripped = false after streaming observations, want true")
	}
	// Still latched, so a continuing violation must not re-report.
	f.invariants(cfg.InvariantConfirmations, false, 22)
	if len(f.reports) != 1 {
		t.Errorf("got %d reports, want 1 — streaming observations must not have "+
			"credited recovery", len(f.reports))
	}
}

// TestDetector_FrameRecoveryDoesNotClearOrphanLatch pins that the two latches
// recover independently — frame health says nothing about the invariant.
func TestDetector_FrameRecoveryDoesNotClearOrphanLatch(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(cfg.InvariantConfirmations, false, 22)
	if !f.det.Status().OrphanTripped {
		t.Fatal("Status().OrphanTripped = false, want true")
	}

	f.frames(cfg.RecoveryStreak*2, false, hit) // a full frame-path recovery
	if !f.det.Status().OrphanTripped {
		t.Error("Status().OrphanTripped = false after frame-path recovery, want true")
	}
}

// --- Gating, zero values, reset ---

// TestDetector_DisabledSkipsInvariantWork pins the debug gate on the invariant
// path specifically.
func TestDetector_DisabledSkipsInvariantWork(t *testing.T) {
	cfg := orphanCfg()
	cfg.Enabled = false
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(10_000, false, 99)

	if len(f.reports) != 0 {
		t.Fatalf("got %d reports while disabled, want 0", len(f.reports))
	}
	if st := f.det.Status(); st.OrphanTripped || st.HasReport {
		t.Errorf("Status() = %+v, want no orphan trip and no report while disabled", st)
	}
}

// TestDetector_ZeroReportForNoOrphanTrip pins that a non-reporting observation
// returns the zero Report, so a caller keying on the value rather than ok
// cannot read a half-populated diagnosis.
func TestDetector_ZeroReportForNoOrphanTrip(t *testing.T) {
	f := newFeeder(t, orphanCfg())
	f.warmCache(200)

	// Compared against the zero struct rather than via Zero(), which only looks
	// at Kind and IdleFrames — a report carrying Orphans and a timestamp with
	// Kind unset would satisfy Zero() while still being half-populated.
	rep, ok := f.invariant(false, 0, nil)
	if ok {
		t.Fatal("clean observation reported")
	}
	if rep != (Report{}) {
		t.Errorf("clean observation report = %+v, want the zero value", rep)
	}
	// One short of confirming: still no report, still zero.
	rep, ok = f.invariant(false, 22, nil)
	if ok {
		t.Fatal("first violating observation reported before confirmation")
	}
	if rep != (Report{}) {
		t.Errorf("unconfirmed report = %+v, want the zero value", rep)
	}
}

// TestDetector_NewDetectorNormalizesInvariantConfig pins that a spuriously
// zeroed threshold cannot mean "trip on the first observation" or "re-arm
// instantly", matching the existing normalization contract.
func TestDetector_NewDetectorNormalizesInvariantConfig(t *testing.T) {
	def := DefaultDetectorConfig()
	for _, tc := range []struct {
		name string
		cfg  DetectorConfig
	}{
		{"zero values", DetectorConfig{Enabled: true}},
		{"negative values", DetectorConfig{Enabled: true, InvariantConfirmations: -5, InvariantRecovery: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Asserted behaviorally rather than by reading d.cfg: the point is
			// that the normalized value is USED, not merely stored.
			f := newFeeder(t, tc.cfg)
			f.warmCache(200)
			f.invariants(def.InvariantConfirmations-1, false, 9)
			if len(f.reports) != 0 {
				t.Fatalf("got %d reports one observation short of the default "+
					"confirmation count, want 0", len(f.reports))
			}
			f.invariant(false, 9, nil)
			if len(f.reports) != 1 {
				t.Fatalf("got %d reports at the default confirmation count, want 1",
					len(f.reports))
			}

			// And the re-arm threshold: one short must stay latched.
			f.invariants(def.InvariantRecovery-1, false, 0)
			if !f.det.Status().OrphanTripped {
				t.Error("Status().OrphanTripped = false one observation short of the " +
					"default recovery streak, want true")
			}
			f.invariant(false, 0, nil)
			if f.det.Status().OrphanTripped {
				t.Error("Status().OrphanTripped = true after the default recovery streak, want false")
			}
		})
	}
}

// TestDefaultDetectorConfig_InvariantHysteresisIsAsymmetric pins the tuning
// intent: confirming is cheap, un-latching is expensive.
func TestDefaultDetectorConfig_InvariantHysteresisIsAsymmetric(t *testing.T) {
	def := DefaultDetectorConfig()
	if def.InvariantConfirmations < 2 {
		t.Errorf("InvariantConfirmations = %d, want >= 2 (one observation is not confirmation)",
			def.InvariantConfirmations)
	}
	if def.InvariantRecovery <= def.InvariantConfirmations {
		t.Errorf("InvariantRecovery = %d, want > InvariantConfirmations = %d",
			def.InvariantRecovery, def.InvariantConfirmations)
	}
}

// TestDetector_ResetClearsOrphanState pins that a session restart drops the
// latched orphan episode along with everything else.
func TestDetector_ResetClearsOrphanState(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(200)
	f.invariants(cfg.InvariantConfirmations, false, 9)
	if !f.det.Status().OrphanTripped {
		t.Fatal("Status().OrphanTripped = false, want true before Reset")
	}

	f.det.Reset()
	st := f.det.Status()
	if st.OrphanTripped {
		t.Error("Status().OrphanTripped = true after Reset, want false")
	}
	if st.HasReport {
		t.Error("Status().HasReport = true after Reset, want false")
	}
	if !st.Enabled {
		t.Error("Status().Enabled = false after Reset, want config preserved")
	}

	// A fresh violation must be a first episode again.
	f.invariants(cfg.InvariantConfirmations, false, 9)
	if len(f.reports) != 2 {
		t.Fatalf("got %d reports, want 2 (the post-Reset violation must report)", len(f.reports))
	}
	if got := f.reports[1].Episode; got != 1 {
		t.Errorf("Episode after Reset = %d, want 1", got)
	}
}

// --- Diagnosis ---

func TestDetector_OrphanKindString(t *testing.T) {
	if got := DefectOrphans.String(); got != "orphans" {
		t.Errorf("DefectOrphans.String() = %q, want %q", got, "orphans")
	}
	combined := DefectIdleMisses | DefectOrphans
	got := combined.String()
	if !strings.Contains(got, "idle-misses") || !strings.Contains(got, "orphans") {
		t.Errorf("combined String() = %q, want both names", got)
	}
}

// TestDetector_OrphanDiagnosis pins that an orphan-only report reads as an
// invariant violation, naming the counts in relation to each other rather than
// as bare numbers.
func TestDetector_OrphanDiagnosis(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	f.warmCache(214)
	f.cache.Uncacheable = 30
	f.cache.Width = 132
	// A frame must be observed for the cache context to be a measurement rather
	// than a zero value — see TestDetector_OrphanReportWithoutFrameContext.
	f.frames(1, false, hit)
	f.invariants(cfg.InvariantConfirmations, false, 22)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(f.reports))
	}
	diag := f.reports[0].Diagnosis()
	for _, want := range []string{
		"episode 1",
		"orphans",
		fmt.Sprintf("%d of %d", 22, 214), // orphans of total items
		"width 132",
	} {
		if !strings.Contains(diag, want) {
			t.Errorf("Diagnosis() = %q, missing %q", diag, want)
		}
	}
	if strings.Contains(diag, "\n") {
		t.Errorf("Diagnosis() = %q, want a single line", diag)
	}
}

// TestDetector_OrphanReportWithoutFrameContext pins that an observation arriving
// before any frame does not present the zero CacheStats as measurements.
//
// Nothing orders the two calls, and Collector.Reset clears the frame context, so
// an observation legitimately lands first. Rendering "22 of 0 items, revision 0,
// width 0, hit rate 0.0%" would be a confidently-wrong readout — the same class
// of dishonesty as a reassuring zero for a check that never ran.
func TestDetector_OrphanReportWithoutFrameContext(t *testing.T) {
	cfg := orphanCfg()
	f := newFeeder(t, cfg)
	// Deliberately no warmCache and no frames: the detector has never seen one.
	f.invariants(cfg.InvariantConfirmations, false, 22)
	if len(f.reports) != 1 {
		t.Fatalf("got %d reports, want 1 — a violation is real regardless of "+
			"whether a frame has been observed", len(f.reports))
	}
	rep := f.reports[0]
	if rep.HaveFrameContext {
		t.Error("HaveFrameContext = true with no frame observed, want false")
	}
	diag := rep.Diagnosis()
	if strings.Contains(diag, "of 0 items") {
		t.Errorf("Diagnosis() = %q, must not render a zero item total as a measurement", diag)
	}
	for _, unwanted := range []string{"revision 0", "width 0"} {
		if strings.Contains(diag, unwanted) {
			t.Errorf("Diagnosis() = %q, must not render %q as a measurement", diag, unwanted)
		}
	}
	if !strings.Contains(diag, "no frame observed") {
		t.Errorf("Diagnosis() = %q, want it to say the cache context is missing", diag)
	}

	// And once a frame lands, the context appears.
	f.frames(1, false, hit)
	f.invariants(cfg.InvariantConfirmations, false, 22)
	last := f.det.Status().LastReport
	if !last.HaveFrameContext {
		t.Error("HaveFrameContext = false after a frame was observed, want true")
	}
}

// TestDetector_EpisodeLabelsSurviveTheOtherPathTripping pins that a latched
// report refreshed after the OTHER path opened an episode keeps its own episode
// number.
//
// The episode counter is shared so an overlay sees one monotonic sequence, but
// re-reading it when refreshing a latched report relabels the defect with
// whichever episode opened most recently. On an affected machine both paths are
// latched at once, so that is the normal state, not a corner: every episode
// number a user sees for a sustained defect would be wrong.
func TestDetector_EpisodeLabelsSurviveTheOtherPathTripping(t *testing.T) {
	t.Run("frame episode survives an orphan trip", func(t *testing.T) {
		cfg := orphanCfg()
		f := newFeeder(t, cfg)
		f.warmCache(200)
		f.frames(cfg.DefectStreak+1, false, defeat) // frame path owns episode 1
		f.invariants(cfg.InvariantConfirmations, false, 22)
		f.frames(5, false, defeat) // refresh the latched frame report

		got := f.det.Status().LastReport
		if got.Kind&DefectOrphans != 0 {
			t.Fatalf("LastReport.Kind = %v, want the refreshed frame report", got.Kind)
		}
		if got.Episode != 1 {
			t.Errorf("frame report Episode = %d, want 1 — it must not be relabeled "+
				"with the orphan episode's number", got.Episode)
		}
	})

	t.Run("orphan episode survives a frame trip", func(t *testing.T) {
		cfg := orphanCfg()
		f := newFeeder(t, cfg)
		f.warmCache(200)
		f.invariants(cfg.InvariantConfirmations, false, 22) // orphan path owns episode 1
		f.frames(cfg.DefectStreak+1, false, defeat)         // frame path opens episode 2
		f.invariants(5, false, 22)                          // refresh the latched orphan report

		got := f.det.Status().LastReport
		if got.Kind != DefectOrphans {
			t.Fatalf("LastReport.Kind = %v, want the refreshed orphan report", got.Kind)
		}
		if got.Episode != 1 {
			t.Errorf("orphan report Episode = %d, want 1 — it must not be relabeled "+
				"with the frame episode's number", got.Episode)
		}
	})
}

// --- Collector surface ---

// TestCollector_InvariantEnabledGate pins the accessor the caller needs to
// decide whether to do the O(n) walk at all: when the detector is off, the
// caller must be told not to bother.
func TestCollector_InvariantEnabledGate(t *testing.T) {
	off := New(Config{Now: fixedClock()})
	if off.InvariantEnabled() {
		t.Error("InvariantEnabled() = true with the detector disabled, want false")
	}
	on := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	if !on.InvariantEnabled() {
		t.Error("InvariantEnabled() = false with the detector enabled, want true")
	}
}

// TestCollector_ObserveInvariantDoesNotAffectFrameTimings is the placement
// guarantee as a test. The caller's orphan walk is O(n) and lives outside the
// timed render region; feeding it must not contribute a sample to the frame
// timer, or the reported p50/p95 would describe the detector rather than the
// app.
func TestCollector_ObserveInvariantDoesNotAffectFrameTimings(t *testing.T) {
	c := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	const frames = 20
	for range frames {
		c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: CacheStats{Items: 10}})
		c.ObserveInvariant(InvariantSample{Orphans: 0})
	}
	snap := c.Snapshot()
	if snap.Frame.Count != frames {
		t.Errorf("Frame.Count = %d, want %d (invariant observations must not be timed)",
			snap.Frame.Count, frames)
	}
	if snap.Frame.P50 != time.Millisecond {
		t.Errorf("Frame.P50 = %v, want %v", snap.Frame.P50, time.Millisecond)
	}
}

// TestCollector_ObserveInvariantLeavesCacheStatsAlone pins that the invariant
// path does not overwrite the frame path's cache snapshot.
func TestCollector_ObserveInvariantLeavesCacheStatsAlone(t *testing.T) {
	c := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	want := CacheStats{Hits: 9, Misses: 3, Items: 42, Uncacheable: 1, Revision: 7, Width: 100}
	c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: want})
	c.ObserveInvariant(InvariantSample{Orphans: 5})
	if got := c.Snapshot().Cache; got != want {
		t.Errorf("Snapshot().Cache = %+v, want %+v unchanged", got, want)
	}
}

// TestCollector_SnapshotReportsInvariantFreshness pins that an overlay can tell
// "measured, zero orphans" apart from "never measured". Rendering a reassuring
// 0 for a check that never ran is the vacuity failure this mechanism exists to
// avoid.
func TestCollector_SnapshotReportsInvariantFreshness(t *testing.T) {
	c := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	if got := c.Snapshot().Invariant; got.Checked {
		t.Errorf("Invariant.Checked = true before any observation, want false (%+v)", got)
	}
	c.ObserveInvariant(InvariantSample{Orphans: 4})
	got := c.Snapshot().Invariant
	if !got.Checked {
		t.Error("Invariant.Checked = false after an observation, want true")
	}
	if got.Orphans != 4 {
		t.Errorf("Invariant.Orphans = %d, want 4", got.Orphans)
	}
	if !got.Enabled {
		t.Error("Invariant.Enabled = false, want true")
	}
	if got.At.IsZero() {
		t.Error("Invariant.At is zero, want the observation timestamp")
	}

	// A subsequent clean observation must clear the count, not keep the last
	// nonzero one. An overlay showing "4 orphans" after recovery is the same
	// display dishonesty as showing "0 orphans" for a check that never ran.
	c.ObserveInvariant(InvariantSample{Orphans: 0})
	if got := c.Snapshot().Invariant; got.Orphans != 0 || !got.Checked {
		t.Errorf("Invariant = %+v, want Orphans 0 and Checked true after a clean "+
			"observation", got)
	}

	c.Reset()
	got = c.Snapshot().Invariant
	if got.Checked {
		t.Errorf("Invariant.Checked = true after Reset, want false (%+v)", got)
	}
	if !got.Enabled {
		t.Error("Invariant.Enabled = false after Reset, want config preserved")
	}
}

// TestCollector_DisabledInvariantNeverReadsAsChecked pins the honest-display
// rule for a caller that samples anyway despite InvariantEnabled() being false:
// the overlay must say "not checked", never "0 orphans".
func TestCollector_DisabledInvariantNeverReadsAsChecked(t *testing.T) {
	c := New(Config{Now: fixedClock()}) // detector disabled
	c.ObserveInvariant(InvariantSample{Orphans: 22})
	got := c.Snapshot().Invariant
	if got.Enabled {
		t.Error("Invariant.Enabled = true with the detector disabled, want false")
	}
	if got.Checked {
		t.Errorf("Invariant.Checked = true while disabled, want false (%+v) — a "+
			"disabled check must not read as a measurement", got)
	}
}

// TestCollector_StreamingObservationIsMarkedStreaming pins the other half of
// display honesty. A mid-turn observation legitimately sees orphans, so an
// overlay that rendered the count without knowing it was streaming would alarm
// on a healthy turn — the mirror of the false zero Checked prevents.
func TestCollector_StreamingObservationIsMarkedStreaming(t *testing.T) {
	c := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	c.ObserveInvariant(InvariantSample{Orphans: 5, Streaming: true})
	got := c.Snapshot().Invariant
	if !got.Streaming {
		t.Error("Invariant.Streaming = false for a streaming observation, want true")
	}
	if got.Orphans != 5 {
		t.Errorf("Invariant.Orphans = %d, want 5 (recorded, but marked streaming)", got.Orphans)
	}

	c.ObserveInvariant(InvariantSample{Orphans: 0})
	if got := c.Snapshot().Invariant; got.Streaming {
		t.Error("Invariant.Streaming = true after an idle observation, want false")
	}
}

// TestCollector_ObserveInvariantDoesNotAdvanceSnapshotAt pins that Snapshot.At
// keeps meaning what it is documented to mean — the most recent observed FRAME.
// The invariant path has its own timestamp in Invariant.At.
func TestCollector_ObserveInvariantDoesNotAdvanceSnapshotAt(t *testing.T) {
	c := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: CacheStats{Items: 10}})
	frameAt := c.Snapshot().At

	c.ObserveInvariant(InvariantSample{Orphans: 3})
	snap := c.Snapshot()
	if !snap.At.Equal(frameAt) {
		t.Errorf("Snapshot().At = %v, want the frame's %v unchanged", snap.At, frameAt)
	}
	if snap.Invariant.At.Equal(frameAt) {
		t.Error("Invariant.At == the frame timestamp, want the observation's own")
	}
}

// TestCollector_InvariantSampleAtOverridesClock mirrors the frame path's
// timestamp contract: an explicit At wins, a zero At uses the collector clock.
func TestCollector_InvariantSampleAtOverridesClock(t *testing.T) {
	c := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	explicit := time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)
	c.ObserveInvariant(InvariantSample{Orphans: 1, At: explicit})
	if got := c.Snapshot().Invariant.At; !got.Equal(explicit) {
		t.Errorf("Invariant.At = %v, want the explicit %v", got, explicit)
	}

	// Derive the expected value from the same clock rather than hardcoding its
	// cadence, so retuning fixedClock cannot silently break this.
	clock := fixedClock()
	want := clock()
	c2 := New(Config{Detector: orphanCfg(), Now: fixedClock()})
	c2.ObserveInvariant(InvariantSample{Orphans: 1})
	if got := c2.Snapshot().Invariant.At; !got.Equal(want) {
		t.Errorf("Invariant.At = %v, want the configured clock's first tick %v", got, want)
	}
}

// TestCollector_ForwardsOrphanTripReport pins that a confirmed orphan violation
// reaches the caller and the snapshot, not just the detector's internals.
func TestCollector_ForwardsOrphanTripReport(t *testing.T) {
	cfg := orphanCfg()
	c := New(Config{Detector: cfg, Now: fixedClock()})
	c.ObserveFrame(FrameSample{Dur: time.Millisecond, Cache: CacheStats{Items: 200}})

	var got Report
	var reports int
	for range cfg.InvariantConfirmations {
		if rep, ok := c.ObserveInvariant(InvariantSample{Orphans: 22}); ok {
			got = rep
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("got %d reports from the collector, want 1", reports)
	}
	if got.Kind != DefectOrphans || got.Orphans != 22 {
		t.Errorf("report = %+v, want an orphan report with 22 orphans", got)
	}
	det := c.Snapshot().Detector
	if !det.OrphanTripped {
		t.Error("Snapshot().Detector.OrphanTripped = false, want true")
	}
	if det.LastReport.Kind != DefectOrphans {
		t.Errorf("Snapshot().Detector.LastReport.Kind = %v, want %v",
			det.LastReport.Kind, DefectOrphans)
	}
}

// TestCollector_ObserveInvariantIsAllocationFree keeps the diagnostic off the
// allocator on its healthy path, matching the frame path's guarantee.
func TestCollector_ObserveInvariantIsAllocationFree(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  DetectorConfig
	}{
		{"disabled", DetectorConfig{}},
		{"enabled healthy", orphanCfg()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(Config{Detector: tc.cfg, Now: fixedClock()})
			s := InvariantSample{Orphans: 0}
			allocs := testing.AllocsPerRun(200, func() { c.ObserveInvariant(s) })
			if allocs != 0 {
				t.Errorf("ObserveInvariant allocated %.1f times per run, want 0", allocs)
			}
		})
	}
}
