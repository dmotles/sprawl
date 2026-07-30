package rootinit

// QUM-972: runConsolidationPipeline runs its two phases under an errgroup, and
// each phase writes to the caller-supplied stdout io.Writer directly AND starts
// a spinner goroutine that writes to the same io.Writer. That is up to four
// concurrent unsynchronised writers to one Writer.
//
// io.Writer carries no concurrency guarantee. In production stdout is an
// *os.File, so the damage is confined to garbled output — two spinners animating
// the same terminal line. Anywhere the caller passes an in-memory writer (which
// is what this package's own tests do) it is a genuine data race, and
// `go test -race ./internal/rootinit/` reports it.
//
// These tests deliberately do NOT depend on -race, so they constrain the
// production defect on its own merits and stay meaningful under a plain
// `go test`. The -race gate catches the same thing from the other direction.

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/memory"
)

// concurrencyDetectingWriter reports whether any two Write calls ever overlap.
// The sleep widens the window so an overlap is observed rather than merely
// possible; it discards the bytes since only the concurrency is under test.
type concurrencyDetectingWriter struct {
	inUse      atomic.Bool
	writes     atomic.Int64
	violations atomic.Int64
	// warnings counts writes carrying a phase warning, which is the only
	// evidence the direct fmt.Fprintf calls inside the errgroup bodies were
	// reached at all. Without it, the error subtest's write floor is satisfied
	// by the spinners alone, so if those branches ever stop printing the subtest
	// silently degenerates into a duplicate of the spinners-only one.
	warnings atomic.Int64
}

func (w *concurrencyDetectingWriter) Write(p []byte) (int, error) {
	w.writes.Add(1)
	if bytes.Contains(p, []byte("warning:")) {
		w.warnings.Add(1)
	}
	if !w.inUse.CompareAndSwap(false, true) {
		w.violations.Add(1)
	} else {
		time.Sleep(2 * time.Millisecond)
		w.inUse.Store(false)
	}
	return len(p), nil
}

// phaseTracker records the high-water mark of simultaneously-running phases.
// Without it, deleting the errgroup and running the two phases sequentially
// would satisfy every other assertion here — zero overlapping writes, and the
// write-count floor still met by the spinners — while silently destroying the
// concurrency the function exists for.
type phaseTracker struct {
	inFlight atomic.Int64
	maxSeen  atomic.Int64
}

func (p *phaseTracker) enter() {
	n := p.inFlight.Add(1)
	for {
		m := p.maxSeen.Load()
		if n <= m || p.maxSeen.CompareAndSwap(m, n) {
			return
		}
	}
}

func (p *phaseTracker) leave() { p.inFlight.Add(-1) }

// TestRunConsolidationPipeline_StdoutWritesAreSerialised asserts the pipeline
// never has two writers inside the caller's io.Writer at once.
//
// The subtests split by which writers are in play, because they are fixed by
// DIFFERENT code and a fix for one does not imply the other:
//
//	spinners only    — the two spinner goroutines (spinner.go's Fprintf).
//	                   Passes if the spinner alone is made thread-safe.
//	phase error path — additionally exercises the direct fmt.Fprintf calls in
//	                   the errgroup bodies (postrun.go's warning branches), which
//	                   are unreachable when both phase stubs return nil. Only a
//	                   fix that serialises the CALLER'S writer covers this.
func TestRunConsolidationPipeline_StdoutWritesAreSerialised(t *testing.T) {
	// Long enough for the 150ms spinner tick to fire several times in each phase,
	// so the spinners are real concurrent writers rather than goroutines that
	// start and stop without ever emitting a frame.
	//
	// Note the minWrites floors below are partly WALL-CLOCK-DERIVED: only two
	// writes are structurally guaranteed (one stop() line-clear per spinner); the
	// rest are tick frames. 800ms leaves room for 5 ticks per spinner against a
	// floor that needs 2, so a heavily loaded box would have to stall ~500ms to
	// make the floor misfire — and a misfire is a loud t.Fatalf, not a silent
	// pass.
	const phaseDuration = 800 * time.Millisecond

	cases := []struct {
		name string
		// phaseErr is returned by both phase stubs; non-nil reaches the direct
		// fmt.Fprintf warning branches inside the errgroup goroutines.
		phaseErr error
		// minWrites is the non-vacuity floor. Spinners alone produce ~8 writes
		// (3 ticks x 2 spinners + 2 stop() line-clears); the error case adds
		// one warning write per phase.
		minWrites int64
		// minWarnings pins that the errgroup bodies' own fmt.Fprintf calls were
		// reached — one per phase. This is what distinguishes the two subtests;
		// minWrites alone cannot, since the spinners overshoot it.
		minWarnings int64
	}{
		{name: "spinners only", phaseErr: nil, minWrites: 4, minWarnings: 0},
		{name: "phase error path", phaseErr: errors.New("boom"), minWrites: 6, minWarnings: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := newTestDeps(t)
			tracker := &phaseTracker{}

			deps.ConsolidateExcluding = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.TimelineCompressionConfig, now func() time.Time, excludeIDs map[string]bool) error {
				tracker.enter()
				defer tracker.leave()
				time.Sleep(phaseDuration)
				return tc.phaseErr
			}
			deps.UpdatePersistentKnowledge = func(ctx context.Context, root string, inv memory.ClaudeInvoker, cfg *memory.PersistentKnowledgeConfig, summary, bullets string) error {
				tracker.enter()
				defer tracker.leave()
				time.Sleep(phaseDuration)
				return tc.phaseErr
			}

			w := &concurrencyDetectingWriter{}
			runConsolidationPipeline(context.Background(), deps, "/fake/root", w, nil)

			// Non-vacuity legs. Both must run in the same invocation as the
			// violation assertion below: a stub that returns instantly, a
			// spinner that never ticks, or phases that ran one after the other
			// all produce zero violations while measuring nothing.
			if got := w.writes.Load(); got < tc.minWrites {
				t.Fatalf("only %d writes reached stdout; this run measured nothing (want >= %d)", got, tc.minWrites)
			}
			if got := w.warnings.Load(); got < tc.minWarnings {
				t.Fatalf("only %d phase-warning writes reached stdout, want >= %d; the errgroup bodies' own "+
					"fmt.Fprintf calls were not reached, so this subtest is measuring the same spinner-only "+
					"writers as the other one", got, tc.minWarnings)
			}
			if got := tracker.maxSeen.Load(); got != 2 {
				t.Fatalf("peak simultaneous phases was %d, want 2; the phases are no longer concurrent, "+
					"so the absence of overlapping writes proves nothing about writer safety", got)
			}

			if got := w.violations.Load(); got != 0 {
				t.Errorf("two consolidation phases wrote to the caller's stdout concurrently: %d overlapping writes out of %d total; "+
					"the spinner line is garbled and any non-thread-safe writer the caller passes is corrupted",
					got, w.writes.Load())
			}
		})
	}
}
