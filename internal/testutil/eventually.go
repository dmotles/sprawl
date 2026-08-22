// Package testutil provides shared test helpers. It is imported only by tests,
// but lives in a non-test file so that several packages can share it.
//
// The helpers here replace fixed waits (time.Sleep, or a select against
// time.After that only returns when the deadline fires) with polls that exit as
// soon as the condition holds. A poll cannot be slower than a fixed wait of the
// same ceiling, so converting a call site keeps its worst case while usually
// paying a fraction of it.
//
// The one thing that must never be true of this package: a wait that reports
// success because it gave up. Every exit path below either observes the
// condition or fails the test, and eventually_test.go proves that by running
// each assertion against an implementation where the defect IS present.
package testutil

import (
	"fmt"
	"time"
)

// TB is the subset of testing.TB these helpers need.
//
// It is declared here rather than using testing.TB because testing.TB has an
// unexported method and so cannot be implemented outside package testing —
// which would leave the timeout path of every helper below unprovable.
// *testing.T satisfies this interface.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// DefaultInterval is the poll period. Small enough that converting a
// several-hundred-millisecond wait costs a fraction of it, large enough to stay
// well above scheduler granularity under -race rather than busy-spinning.
const DefaultInterval = 2 * time.Millisecond

// Poll evaluates cond until it returns true, and reports nil in that case. It
// reports a non-nil error if timeout elapses first.
//
// cond is always evaluated at least once, and always on the calling goroutine,
// so it may touch state that is not safe for concurrent access from elsewhere.
//
// This is the pure core of the package, split out from Eventually so that its
// timeout path is testable without a test framework.
func Poll(timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("condition did not hold within %s", timeout)
		}
		time.Sleep(DefaultInterval)
	}
}

// Eventually polls cond until it returns true, failing tb if timeout elapses
// first. desc must describe the condition being waited for, because it is all
// the failure message has to go on.
func Eventually(tb TB, timeout time.Duration, desc string, cond func() bool) {
	tb.Helper()
	if err := Poll(timeout, cond); err != nil {
		tb.Fatalf("timed out after %s waiting for %s", timeout, desc)
	}
}

// EventuallyStable polls cond until it returns true, then requires it to REMAIN
// true for settle. The settle window is spent on top of however long the
// condition took to become true; timeout bounds only the initial wait.
//
// This is the replacement for "drain a channel for a fixed window, then tally".
// Such a wait looks like a candidate for a plain Eventually, but the window is
// usually load-bearing for a NEGATIVE half of the assertion — "one event
// arrived, and no second one did". Converting those to Eventually would drop
// the negative half silently and leave a test that passes for the wrong reason,
// so they get this instead: the fast path still exits early once the condition
// holds, but a condition that later breaks is still caught.
func EventuallyStable(tb TB, timeout, settle time.Duration, desc string, cond func() bool) {
	tb.Helper()
	if err := Poll(timeout, cond); err != nil {
		tb.Fatalf("timed out after %s waiting for %s", timeout, desc)
		return
	}
	settleDeadline := time.Now().Add(settle)
	for {
		if !cond() {
			tb.Fatalf("%s held, then stopped holding during the %s settle window", desc, settle)
			return
		}
		if !time.Now().Before(settleDeadline) {
			return
		}
		time.Sleep(DefaultInterval)
	}
}
