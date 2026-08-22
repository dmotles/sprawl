package testutil

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// recorderTB is a fake TB that records Fatalf calls instead of aborting.
//
// It exists because testing.TB cannot be implemented outside package testing
// (it has an unexported method), which would leave the timeout path of every
// helper here unprovable. Recording rather than aborting is what lets a test
// assert "this helper DID fail" — the property that matters most, since a poll
// helper whose timeout path silently succeeded would turn every converted call
// site green-and-meaningless.
//
// Divergence from the real thing, deliberately: testing.T.Fatalf calls
// runtime.Goexit, so it never returns. This one panics with fatalSentinel so
// that an implementation which fatals and then keeps working is still visible
// as a difference, while remaining recoverable by the test.
//
// fatals is unsynchronised on purpose. Both Fatalf and cond are contractually
// invoked on the caller's goroutine, and running these tests under -race is
// what asserts that contract.
type recorderTB struct {
	fatals []string
}

type fatalPanic struct{}

func (r *recorderTB) Helper() {}

func (r *recorderTB) Fatalf(format string, args ...any) {
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
	panic(fatalPanic{})
}

// Compile-time proof that the interface the helpers accept is satisfiable by
// both the fake and a real *testing.T. Without the second assertion the
// package could compile and pass while every real call site failed to build.
var (
	_ TB = (*recorderTB)(nil)
	_ TB = (*testing.T)(nil)
)

// run invokes fn, absorbing the panic that recorderTB.Fatalf raises, and
// reports whether fn returned normally. A helper that fatals must NOT return
// normally, so returnedNormally is itself an assertable property.
func run(fn func()) (returnedNormally bool) {
	defer func() {
		if p := recover(); p != nil {
			if _, ok := p.(fatalPanic); !ok {
				panic(p)
			}
		}
	}()
	fn()
	return true
}

// --- Poll: the pure core. Its timeout path is the dominant risk. ---

func TestPoll_TimesOutWhenCondNeverHolds(t *testing.T) {
	t.Parallel()

	const timeout = 100 * time.Millisecond
	start := time.Now()
	err := Poll(timeout, func() bool { return false })
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Poll returned nil for a condition that never held: the timeout path silently succeeded")
	}
	// Lower bound: without it, a Poll that ignores its timeout and errors
	// instantly also passes, and every converted call site would lose its wait.
	if elapsed < 90*time.Millisecond {
		t.Fatalf("Poll gave up after %v, before its %v timeout elapsed", elapsed, timeout)
	}
}

func TestPoll_ReturnsNilAndExitsEarlyWhenCondHolds(t *testing.T) {
	t.Parallel()

	calls := 0
	start := time.Now()
	err := Poll(2*time.Second, func() bool {
		calls++
		return calls >= 3
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Poll returned %v for a condition that held after %d calls", err, calls)
	}
	// Without this bound the test also passes for a helper that always waits
	// out the full timeout, which is the exact defect being fixed.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Poll did not exit early: took %v of a 2s timeout", elapsed)
	}
}

// A tight unconditional loop satisfies the call-count test above. This one is
// driven by wall clock instead, so it requires real re-evaluation over time.
func TestPoll_ReEvaluatesCondOverTime(t *testing.T) {
	t.Parallel()

	trueAfter := time.Now().Add(50 * time.Millisecond)
	if err := Poll(5*time.Second, func() bool { return time.Now().After(trueAfter) }); err != nil {
		t.Fatalf("Poll returned %v for a condition that became true after 50ms", err)
	}
}

// Pins the poll interval. A busy-spin passes every other test here and, at
// -count=20 under -race on a shared host, starves the rest of the suite.
func TestPoll_DoesNotBusySpin(t *testing.T) {
	t.Parallel()

	evaluations := 0
	_ = Poll(100*time.Millisecond, func() bool {
		evaluations++
		return false
	})

	if evaluations > 200 {
		t.Fatalf("Poll evaluated cond %d times in 100ms: the poll interval is too small or absent", evaluations)
	}
	// Negative control for the bound above: an implementation that evaluates
	// cond once and then sleeps would satisfy it while polling nothing.
	if evaluations < 5 {
		t.Fatalf("Poll evaluated cond only %d times in 100ms: it is not really polling", evaluations)
	}
}

func TestPoll_ChecksCondAtLeastOnceWhenTimeoutIsZero(t *testing.T) {
	t.Parallel()

	called := false
	err := Poll(0, func() bool {
		called = true
		return true
	})

	if !called {
		t.Fatal("Poll never evaluated cond with a zero timeout")
	}
	if err != nil {
		t.Fatalf("Poll returned %v though cond was true on first evaluation", err)
	}
}

// Negative control for the above: proves the zero-timeout path is not simply
// hardcoded to succeed.
func TestPoll_ZeroTimeoutStillFailsWhenCondIsFalse(t *testing.T) {
	t.Parallel()

	if err := Poll(0, func() bool { return false }); err == nil {
		t.Fatal("Poll(0, false) returned nil: the zero-timeout path silently succeeds")
	}
}

// --- Eventually: the Poll -> Fatalf wiring. ---

func TestEventually_FatalfsOnTimeout(t *testing.T) {
	t.Parallel()

	rec := &recorderTB{}
	returned := run(func() {
		Eventually(rec, 50*time.Millisecond, "the thing that never happens", func() bool { return false })
	})

	if len(rec.fatals) != 1 {
		t.Fatalf("want exactly 1 Fatalf on timeout, got %d: %q", len(rec.fatals), rec.fatals)
	}
	if returned {
		t.Fatal("Eventually returned normally after fataling; with a real *testing.T it must not resume")
	}
	if !strings.Contains(rec.fatals[0], "the thing that never happens") {
		t.Fatalf("Fatalf message does not name the condition, so a timeout would be undiagnosable: %q", rec.fatals[0])
	}
}

// Negative control for the above: a helper that always Fatalfs would satisfy
// TestEventually_FatalfsOnTimeout.
func TestEventually_StaysQuietWhenCondHolds(t *testing.T) {
	t.Parallel()

	rec := &recorderTB{}
	returned := run(func() {
		Eventually(rec, 2*time.Second, "immediately true", func() bool { return true })
	})

	if len(rec.fatals) != 0 {
		t.Fatalf("Eventually failed a passing condition: %q", rec.fatals)
	}
	if !returned {
		t.Fatal("Eventually did not return for a passing condition")
	}
}

// --- EventuallyStable: preserves the negative half of a converted window. ---

// The helper's primary shape: false, then true, then stays true. An
// implementation that skips the "poll until true" phase and only checks the
// settle window fails here.
func TestEventuallyStable_StaysQuietWhenCondBecomesTrueThenHolds(t *testing.T) {
	t.Parallel()

	evaluations := 0
	rec := &recorderTB{}
	returned := run(func() {
		EventuallyStable(rec, 5*time.Second, 100*time.Millisecond, "becomes true then holds", func() bool {
			evaluations++
			return evaluations > 3
		})
	})

	if len(rec.fatals) != 0 {
		t.Fatalf("EventuallyStable failed a condition that became true and stayed true: %q", rec.fatals)
	}
	if !returned {
		t.Fatal("EventuallyStable did not return for a passing condition")
	}
}

// The single most important assertion in this file. The condition holds, then
// breaks partway through the settle window, then recovers. An implementation
// that only samples cond at the END of settle passes every other test here and
// misses exactly the case this helper exists for: a second, unwanted event
// arriving inside the window. Driven by evaluation count, not wall clock, so a
// descheduled goroutine cannot turn this into a vacuous pass.
func TestEventuallyStable_FatalfsWhenCondDipsMidSettleAndRecovers(t *testing.T) {
	t.Parallel()

	evaluations := 0
	rec := &recorderTB{}
	returned := run(func() {
		EventuallyStable(rec, 5*time.Second, time.Second, "stays settled", func() bool {
			evaluations++
			return evaluations != 3
		})
	})

	if len(rec.fatals) != 1 {
		t.Fatalf("want exactly 1 Fatalf when cond dipped mid-settle, got %d: %q", len(rec.fatals), rec.fatals)
	}
	if returned {
		t.Fatal("EventuallyStable returned normally after fataling")
	}
	if !strings.Contains(rec.fatals[0], "stays settled") {
		t.Fatalf("Fatalf message does not name the condition: %q", rec.fatals[0])
	}
	// The two failure modes must be distinguishable, or whoever hits one at a
	// converted call site cannot tell "never happened" from "happened twice".
	if !strings.Contains(rec.fatals[0], "settle") {
		t.Fatalf("settle-break message is indistinguishable from a plain timeout: %q", rec.fatals[0])
	}
}

// The other direction: cond never holds at all.
func TestEventuallyStable_FatalfsWhenCondNeverHolds(t *testing.T) {
	t.Parallel()

	rec := &recorderTB{}
	returned := run(func() {
		EventuallyStable(rec, 50*time.Millisecond, 50*time.Millisecond, "never true", func() bool { return false })
	})

	if len(rec.fatals) != 1 {
		t.Fatalf("want exactly 1 Fatalf when cond never held, got %d: %q", len(rec.fatals), rec.fatals)
	}
	if returned {
		t.Fatal("EventuallyStable returned normally after fataling")
	}
	if strings.Contains(rec.fatals[0], "settle") {
		t.Fatalf("never-became-true message wrongly reports a settle break: %q", rec.fatals[0])
	}
}

// Negative control for both failure tests above.
func TestEventuallyStable_StaysQuietWhenCondRemainsTrue(t *testing.T) {
	t.Parallel()

	rec := &recorderTB{}
	returned := run(func() {
		EventuallyStable(rec, 2*time.Second, 100*time.Millisecond, "always true", func() bool { return true })
	})

	if len(rec.fatals) != 0 {
		t.Fatalf("EventuallyStable failed a condition that stayed true: %q", rec.fatals)
	}
	if !returned {
		t.Fatal("EventuallyStable did not return for a passing condition")
	}
}

// Pins the budget contract that the API sketch left undefined: timeout bounds
// the wait for cond to first become true, and settle is spent on top of it.
func TestEventuallyStable_SpendsSettleOnTopOfTimeout(t *testing.T) {
	t.Parallel()

	const settle = 300 * time.Millisecond
	rec := &recorderTB{}
	start := time.Now()
	run(func() {
		EventuallyStable(rec, 5*time.Second, settle, "always true", func() bool { return true })
	})
	elapsed := time.Since(start)

	if len(rec.fatals) != 0 {
		t.Fatalf("unexpected failure: %q", rec.fatals)
	}
	if elapsed < 270*time.Millisecond {
		t.Fatalf("EventuallyStable returned after %v, so it did not observe its %v settle window at all", elapsed, settle)
	}
}
