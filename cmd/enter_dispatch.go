package cmd

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dmotles/sprawl/internal/config"
	"github.com/dmotles/sprawl/internal/store"
)

// Lifecycle wiring for the event-log dispatcher (QUM-1252, M3a, Option A).
//
// `sprawl store dispatch` has always been able to run the dispatch loop, and
// until now nothing ever did in a real session: the loop shipped unwired, so
// every owner_notify contract stayed open forever on any host where nobody
// remembered to start a second process by hand. Option A resolves that — the
// dispatcher and both sweepers start and stop with `sprawl enter`, gated on
// `event_log.enabled`.
//
// WHAT THIS DOES NOT YET FIX, stated because a reader who sees the sweeper
// started from inside a session will reasonably assume the opposite: the sweeper
// is still INERT. Its in-turn gate needs live turn state, which lives in the
// supervisor's in-memory phase machine, and the observer wired here is the same
// disk-reading dispatchadapt.DiskAgents the standalone command uses — so every
// candidate is still skipped on the unobserved-turn-state gate. Being
// in-process makes a supervisor-backed observer POSSIBLE; it does not supply
// one. That is tracked separately (QUM-1328) and is deliberately not this
// slice's change. The notification re-delivery leg, by contrast, does real work
// from here, because it needs no turn state.
//
// Three properties this file exists to hold, each of which was a considered
// alternative rather than an obvious default:
//
//   - IT REUSES THE MEMOIZED LEDGER. store.Process is memoized per root and
//     `sprawl enter` already calls it to wire the goal MCP tools, so going
//     through it again costs nothing and, critically, does NOT open a second pgx
//     pool against the same database for the lifetime of the session.
//   - IT NEVER WRITES TO STDOUT. The session owns the terminal through Bubble
//     Tea's alternate screen; a stray `fmt.Fprintf(os.Stdout, ...)` from a
//     background goroutine corrupts the frame. Only the error surface is wired,
//     and by the time this starts it has been redirected to
//     `.sprawl/logs/tui-stderr-*.log`.
//   - IT NEVER READS THE `store dispatch` FLAG GLOBALS. `dispatchHost`,
//     `dispatchOnce` and `dispatchNoSweeper` belong to that command's cobra
//     parse. A session that honoured `--once` would dispatch exactly one pass
//     and go silent for hours while looking healthy.

// sessionDispatchJoinTimeout bounds how long session teardown waits for the
// dispatch loop to return.
//
// An atomicDuration rather than a plain var because production reads it from the
// teardown path while a test overrides it — the repo-wide convention from
// QUM-972, deliberately duplicated and unexported per package.
var sessionDispatchJoinTimeout = newAtomicDuration(5 * time.Second)

type atomicDuration struct{ ns atomic.Int64 }

func newAtomicDuration(d time.Duration) *atomicDuration {
	v := &atomicDuration{}
	v.set(d)
	return v
}

func (v *atomicDuration) get() time.Duration  { return time.Duration(v.ns.Load()) }
func (v *atomicDuration) set(d time.Duration) { v.ns.Store(int64(d)) }

// startEventDispatch runs one dispatch loop for the life of the session and
// returns the func that stops it.
//
// The returned stop func CANCELS AND THEN JOINS: when it returns, the loop has
// finished, so its last write to errOut cannot land after `sprawl enter` has
// restored stderr to the user's terminal. It is idempotent, and it ABANDONS a
// loop that ignores its cancellation rather than hanging the session exit
// forever — a wedged background goroutine is a leak, but a wedged teardown is a
// terminal the user cannot get back.
func startEventDispatch(sprawlRoot string, errOut io.Writer, run func(context.Context, string, io.Writer) error) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		if err := run(ctx, sprawlRoot, errOut); err != nil && ctx.Err() == nil {
			// ctx.Err() != nil is the ordinary stop, and context.Canceled arriving
			// on every clean session exit would train readers to skip the line that
			// actually means the log stopped being consumed mid-session.
			fmt.Fprintf(errOut, "[enter] event dispatch stopped: %s\n", store.RedactError(err))
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(sessionDispatchJoinTimeout.get()):
				fmt.Fprintf(errOut, "[enter] event dispatch did not stop within %s; abandoning it\n", sessionDispatchJoinTimeout.get())
			}
		})
	}
}

// defaultStartEventDispatch is the production hook installed on enterDeps.
//
// The cfg parameter is runEnter's SINGLE authoritative config load, handed down
// rather than re-read here: `sprawl enter` resolves its root from SPRAWL_ROOT or
// the cwd, and a second config.Load inside a session-scoped helper is how the
// two come to disagree.
func defaultStartEventDispatch(sprawlRoot string, _ *config.Config, errOut io.Writer) func() {
	return startEventDispatch(sprawlRoot, errOut, runSessionDispatch)
}

// runSessionDispatch is the session's dispatch loop.
//
// It shares buildDispatchStack with `sprawl store dispatch`, so the two cannot
// drift on which handlers are registered. Every unusable-store state is a quiet
// return rather than an error: the gate upstream already read
// `event_log.enabled` as true, so reaching one of these means the log is
// configured but not usable — which `sprawl store doctor` diagnoses properly and
// a line in a session log file does not.
func runSessionDispatch(ctx context.Context, sprawlRoot string, errOut io.Writer) error {
	ledger, err := store.Process(ctx, sprawlRoot)
	if err != nil {
		return fmt.Errorf("opening the event log for dispatch: %w", err)
	}
	if !ledger.Enabled() {
		return nil
	}
	if derr := ledger.DegradedError(); derr != nil {
		return fmt.Errorf("the event log is unreachable, so this session is not dispatching; run `sprawl store doctor`: %w", derr)
	}
	if ledger.Pool() == nil {
		return nil
	}

	host := defaultHostIdentity(&storeDeps{SprawlRoot: sprawlRoot})
	if host == "" {
		// Two hosts sharing an identity is a data-loss configuration (see
		// defaultHostIdentity). Declining to dispatch is the safe direction, and
		// unlike the CLI there is no --host to suggest.
		return fmt.Errorf("could not determine a host identity (os.Hostname failed), so this session is not dispatching")
	}

	logger := dispatchLogger(errOut)
	stack, err := buildDispatchStack(ledger, sprawlRoot, host, logger)
	if err != nil {
		return err
	}

	// No startup reconciliation. It is the CLI's job on a deliberate restart of a
	// long-lived dispatcher; running it on every `sprawl enter` would let an
	// ordinary session declare another host's agent failed, and reconcile's
	// grace-period logic is not written against a process that restarts as often
	// as a TUI does.
	go runSweepTicker(ctx, io.Discard, errOut, stack.sweeper, stack.notifySweeper)
	return stack.dispatcher.Run(ctx)
}
