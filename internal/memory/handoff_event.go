package memory

import (
	"context"
	"log/slog"

	"github.com/dmotles/sprawl/internal/store"
)

// The memory half of the plan's "dual-write then replace" decision (QUM-1249):
// from M1 weave handoff summaries ALSO emit as event-log events, and at M6 the
// database takes over and file memory is frozen.
//
// It is a HOOK rather than a direct call so the ordering and failure policy can
// be asserted without a database, and so this package's existing tests are not
// coupled to the store. Defaults to the real emitter; the store is off by
// default, in which case that emitter is itself a no-op.

// handoffEventHook records a handoff summary somewhere other than the filesystem.
type handoffEventHook func(sprawlRoot string, session Session, body string)

var handoffEvent handoffEventHook = recordHandoffInEventLog

// setHandoffEventHookForTest swaps the hook and returns a restore func.
func setHandoffEventHookForTest(h handoffEventHook) func() {
	prev := handoffEvent
	handoffEvent = h
	return func() { handoffEvent = prev }
}

// recordHandoffInEventLog is the production hook.
//
// Every failure mode ends in a log line, never an error: the caller is the
// handoff path, the summary file is authoritative until M6, and the event log is
// observability. Losing the session summary because the log was unhappy would
// trade the thing a handoff exists to produce for the thing that merely observes
// it.
func recordHandoffInEventLog(sprawlRoot string, session Session, body string) {
	ctx := context.Background()
	ledger, err := store.Process(ctx, sprawlRoot)
	if err != nil {
		// Enabled but unusable. Loud enough to find in a log, quiet enough not
		// to interfere with the handoff.
		warnLedgerUnusable(slog.Default(), err)
		return
	}
	if ledger == nil {
		return // the store is off, which is the default
	}
	_ = store.RecordHandoff(ctx, ledger, store.HandoffRecord{
		SessionID:    session.SessionID,
		AgentsActive: session.AgentsActive,
		Body:         body,
	})
}

// emitHandoffEvent runs the hook for a handoff summary, absorbing any panic.
//
// The panic guard is not paranoia about the store's current code — RecordHandoff
// is documented never to return an error — it is about the CALLER's guarantee.
// This runs after the summary file has landed, on the path that persists the one
// artifact a handoff exists to produce, and a panic here would propagate out of
// WriteSessionSummary and lose it. A future edit anywhere under store.Process
// should not be able to do that.
func emitHandoffEvent(sprawlRoot string, session Session, body string) {
	if handoffEvent == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			warnHandoffPanic(slog.Default(), r)
		}
	}()
	handoffEvent(sprawlRoot, session, body)
}

// warnLedgerUnusable and warnHandoffPanic are the two log sinks of this file,
// extracted behind a *slog.Logger so they can be pinned by a test.
//
// WHY A SEAM AT ALL. The error these print comes from store.Process, which is a
// sync.Once over package globals logging through slog.Default(); there is no way
// to pin what it prints by calling it. A logger parameter is the smallest thing
// that makes the PRINT testable without making the singleton testable.

// warnLedgerUnusable reports a store.Process failure with DSN secrets removed.
//
// THE DECISION, and the two options it was chosen over (QUM-1294). This is the
// fourth unredacted DSN sink found in this chain, so "route this one line
// through RedactError" needed an argument beyond being the smallest diff.
//
// REJECTED: have store.Process return a pre-redacted error, so every caller is
// safe however it prints. Redaction then happens at the RETURN, which replaces
// the value and breaks errors.Is for in-process callers —
// TestStoreDispatch_PropagatesAnOpenFailure depends on that identity. Rejected
// on the record in QUM-1294 rather than by omission.
//
// REJECTED: wrap slog.Default()'s handler process-wide, the only option that
// pre-empts sink number five. Measured rather than argued: the cheap form,
// slog.SetDefault(slog.New(wrap(slog.Default().Handler()))), DEADLOCKS the
// process on its first log line. slog.SetDefault installs a log.SetOutput
// pointing at the new handler for any handler that is not slog's internal
// *defaultHandler, but slog.Default()'s handler IS that defaultHandler, and it
// writes through the log package — so the wrapper's inner Handle re-enters
// log.Logger's own mutex. The only working form installs a FRESH concrete
// handler, which changes the output format of every existing slog.Default() call
// in the binary at once (idlereap, drain, runtime, sweep_coordinator, and
// store/process.go itself) and is a repo-wide output decision, not a leak fix.
// Recorded on QUM-1296, which owns the durable class guard, so it is not
// designed around a construction that cannot work.
//
// CHOSEN: wrap the LOGGER here, with store.RedactingLogger — the same mechanism
// and the same shape as the two existing wrap points (store.Open's cfg.Logger,
// cmd/store_dispatch.go's dispatch logger). This is a third wrap point of an
// audited kind rather than a fourth kind of fix, and unlike
// `"error", store.RedactError(err)` it also covers the message and any attr a
// later edit adds to this line. It redacts at the PRINT, so the error value
// itself is untouched and errors.Is is unaffected by construction.
//
// NOT a coverage claim: this covers THIS sink. cmd/hubd is a separate main with
// its own print (QUM-1292), and nothing here stops a fifth sink being added.
func warnLedgerUnusable(log *slog.Logger, err error) {
	store.RedactingLogger(log).Warn("event log unusable, handoff recorded to memory only", "error", err)
}

// warnHandoffPanic reports a recovered panic from the event-log hook.
//
// Redacted for the same reason and by the same mechanism: the recovered value
// can be a pgx error from anywhere under store.Process, and a sibling line in
// the same function that prints the same class of value unredacted is how the
// next sink gets born.
func warnHandoffPanic(log *slog.Logger, r any) {
	store.RedactingLogger(log).Warn("recording the handoff event panicked; the summary file is unaffected", "panic", r)
}
