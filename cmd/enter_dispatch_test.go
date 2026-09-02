package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/config"
)

// enterDepsWithDispatch is enterDepsForRoot plus a recording dispatch hook.
//
// It reports the sprawlRoot and *config.Config the hook was handed, whether it
// was called at all, and whether the stop func it returned was called before
// runEnter returned. Those last two are the whole subject of this file: the AC
// is "the dispatcher and sweeper START AND STOP with `sprawl enter`", and a
// start with no stop is the failure that leaks a pgx pool and a claim-taking
// consumer past the session that owns it.
type dispatchWitness struct {
	calls atomic.Int32
	stops atomic.Int32
	root  string
	cfg   *config.Config
}

func enterDepsWithDispatch(root string, ran *bool, w *dispatchWitness) *enterDeps {
	deps := enterDepsForRoot(root, ran)
	deps.startEventDispatch = func(sprawlRoot string, cfg *config.Config, _ io.Writer) func() {
		w.calls.Add(1)
		w.root, w.cfg = sprawlRoot, cfg
		return func() { w.stops.Add(1) }
	}
	return deps
}

// TestRunEnter_EventLogEnabled_StartsAndStopsTheDispatcher is the positive leg
// of the Option A gate.
func TestRunEnter_EventLogEnabled_StartsAndStopsTheDispatcher(t *testing.T) {
	root := writeEnterConfig(t, "event_log.enabled: true\n")

	ranProgram := false
	var w dispatchWitness
	if err := runEnter(enterDepsWithDispatch(root, &ranProgram, &w)); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	if !ranProgram {
		t.Fatal("runProgram was never reached, so this test proves nothing about the dispatch hook")
	}
	if got := w.calls.Load(); got != 1 {
		t.Fatalf("the dispatch hook was called %d time(s), want exactly 1 — with event_log.enabled true the session owes a running dispatcher", got)
	}
	if got := w.stops.Load(); got != 1 {
		t.Errorf("the dispatch stop func was called %d time(s), want exactly 1 — a dispatcher that outlives its session keeps taking event claims and holding a pgx pool after the process that owns it has torn down", got)
	}
	if w.root != root {
		t.Errorf("the hook got sprawlRoot %q, want %q", w.root, root)
	}
	// The config must be the SINGLE authoritative load from runEnter, not a
	// re-load: QUM-1086 made that a rejected disposition, and a re-load inside
	// the starter would read a different file under SPRAWL_ROOT vs cwd.
	if w.cfg == nil {
		t.Error("the hook got a nil *config.Config; it must be handed runEnter's single authoritative load, not left to re-load")
	} else if !w.cfg.EventLogEnabled() {
		t.Error("the hook got a config that reads event_log.enabled as OFF, so it was not handed the config the gate was decided on")
	}
}

// TestRunEnter_EventLogDisabled_DoesNotStartTheDispatcher is the negative
// control, and the actual gate the AC names.
//
// Without it, a runEnter that starts the dispatcher UNCONDITIONALLY passes the
// positive test above. That is the whole reason the flag exists: on a host with
// no event log, `store.Process` opens nothing, and a dispatcher started anyway
// would report a disabled-store refusal into the session log on every launch.
func TestRunEnter_EventLogDisabled_DoesNotStartTheDispatcher(t *testing.T) {
	root := writeEnterConfig(t, "validate: make validate\n")

	ranProgram := false
	var w dispatchWitness
	if err := runEnter(enterDepsWithDispatch(root, &ranProgram, &w)); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	if !ranProgram {
		t.Fatal("runProgram was never reached, so this test proves nothing about the dispatch hook")
	}
	if got := w.calls.Load(); got != 0 {
		t.Errorf("the dispatch hook was called %d time(s) with event_log.enabled absent, want 0 — the lifecycle is gated on the flag", got)
	}
}

// TestRunEnter_EventLogExplicitlyFalse_DoesNotStartTheDispatcher pins the
// difference between "absent" and "present and false".
//
// config.EventLogEnabled treats everything outside {true,1,yes,on} as OFF, so
// this cannot diverge from the test above through the accessor — but it CAN
// diverge through the gate, if the gate is ever written as a
// presence/non-empty-string check rather than a call to the accessor.
func TestRunEnter_EventLogExplicitlyFalse_DoesNotStartTheDispatcher(t *testing.T) {
	root := writeEnterConfig(t, "event_log.enabled: false\n")

	ranProgram := false
	var w dispatchWitness
	if err := runEnter(enterDepsWithDispatch(root, &ranProgram, &w)); err != nil {
		t.Fatalf("runEnter: %v", err)
	}
	if got := w.calls.Load(); got != 0 {
		t.Errorf("the dispatch hook was called %d time(s) with event_log.enabled explicitly false, want 0", got)
	}
}

// TestRunEnter_NilDispatchHookIsTolerated: every other enterDeps field is
// nil-tolerant in the config-gate tests, and a nil hook must not panic the
// session. This is what lets the ~34 existing enter tests keep their harness.
func TestRunEnter_NilDispatchHookIsTolerated(t *testing.T) {
	root := writeEnterConfig(t, "event_log.enabled: true\n")

	ranProgram := false
	deps := enterDepsForRoot(root, &ranProgram)
	deps.startEventDispatch = nil
	if err := runEnter(deps); err != nil {
		t.Fatalf("runEnter with a nil dispatch hook: %v", err)
	}
	if !ranProgram {
		t.Error("runProgram must still be reached with a nil dispatch hook")
	}
}

// TestResolveEnterDeps_WiresTheDispatchHook: the hook is worthless if the real
// resolve function leaves it nil. This is the seam between the tested gate and
// production, and nothing above this line would notice its absence.
func TestResolveEnterDeps_WiresTheDispatchHook(t *testing.T) {
	if resolveEnterDeps().startEventDispatch == nil {
		t.Error("resolveEnterDeps left startEventDispatch nil, so no real session ever starts a dispatcher no matter what event_log.enabled says")
	}
}

// ---------------------------------------------------------------------------
// The starter's own lifecycle
// ---------------------------------------------------------------------------

// TestStartEventDispatch_StopCancelsAndJoins: the contract of the returned stop
// func is that the runner has ACTUALLY FINISHED when it returns. A stop that
// only cancels leaves the dispatcher writing to a stderr that `sprawl enter` is
// about to restore to the user's terminal.
func TestStartEventDispatch_StopCancelsAndJoins(t *testing.T) {
	var errOut bytes.Buffer
	started := make(chan struct{})
	finished := make(chan struct{})

	stop := startEventDispatch("/tmp/does-not-matter", &errOut, func(ctx context.Context, _ string, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	})
	<-started
	stop()

	select {
	case <-finished:
	default:
		t.Fatal("stop() returned before the runner finished; the join is what makes the dispatcher's stderr writes safe to order against the TUI's stderr restore")
	}
	// context.Canceled is the ORDINARY stop path and must not be reported as a
	// failure — a line on every clean session exit trains readers to ignore the
	// one that matters.
	if errOut.Len() != 0 {
		t.Errorf("a clean cancel wrote to the error surface: %q", errOut.String())
	}
}

// TestStartEventDispatch_ReportsARunnerFailure: the counterpart. A dispatcher
// that dies for a real reason must say so, or an event log that stopped being
// consumed mid-session is indistinguishable from an idle one.
func TestStartEventDispatch_ReportsARunnerFailure(t *testing.T) {
	var errOut bytes.Buffer
	done := make(chan struct{})

	stop := startEventDispatch("/tmp/does-not-matter", &errOut, func(context.Context, string, io.Writer) error {
		defer close(done)
		return errors.New("the pool went away")
	})
	<-done
	stop()

	if !strings.Contains(errOut.String(), "the pool went away") {
		t.Errorf("a runner failure was not reported; got %q", errOut.String())
	}
}

// TestStartEventDispatch_AbandonsAWedgedRunner: a stop that blocks forever on a
// runner ignoring its context turns a session exit into a hang. The repo has a
// standing shape for this (joinWithTimeout / the QUM-925 abandon), and the
// session teardown path is exactly where it matters.
func TestStartEventDispatch_AbandonsAWedgedRunner(t *testing.T) {
	prev := sessionDispatchJoinTimeout.get()
	sessionDispatchJoinTimeout.set(50 * time.Millisecond)
	defer sessionDispatchJoinTimeout.set(prev)

	var errOut bytes.Buffer
	release := make(chan struct{})
	defer close(release)

	stop := startEventDispatch("/tmp/does-not-matter", &errOut, func(context.Context, string, io.Writer) error {
		<-release // deliberately ignores cancellation
		return nil
	})

	returned := make(chan struct{})
	go func() { stop(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() never returned against a runner that ignores its context; a session exit must not hang on the dispatcher")
	}
	if !strings.Contains(errOut.String(), "abandon") {
		t.Errorf("abandoning a wedged dispatcher was not reported; got %q", errOut.String())
	}
}

// TestStartEventDispatch_StopIsIdempotent: the call site is a plain `stop()`
// alongside a deferred teardown, and the repo's other stop funcs are
// sync.Once-guarded for exactly that reason. A second call must not re-join or
// re-report — and it must not spend the join timeout a second time.
//
// Asserted against a WEDGED runner, because that is the only shape where a
// second stop is observable at all: against a cooperative runner the second call
// finds `done` already closed and is silently free, so the test would pass with
// or without the guard.
func TestStartEventDispatch_StopIsIdempotent(t *testing.T) {
	prev := sessionDispatchJoinTimeout.get()
	sessionDispatchJoinTimeout.set(50 * time.Millisecond)
	defer sessionDispatchJoinTimeout.set(prev)

	var errOut bytes.Buffer
	release := make(chan struct{})
	defer close(release)

	stop := startEventDispatch("/tmp/does-not-matter", &errOut, func(context.Context, string, io.Writer) error {
		<-release
		return nil
	})
	stop()
	stop()

	if got := strings.Count(errOut.String(), "abandoning"); got != 1 {
		t.Errorf("the abandon notice was reported %d time(s) across two stops, want exactly 1; got %q", got, errOut.String())
	}
}
