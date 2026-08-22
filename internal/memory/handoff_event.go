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
// REDACTION AT THE PRINT, NEVER AT THE RETURN. This wraps the LOGGER rather than
// stringifying the error, so the error value is untouched and errors.Is still
// matches for in-process callers — TestStoreDispatch_PropagatesAnOpenFailure
// depends on that identity. Wrapping the logger (rather than
// `"error", store.RedactError(err)`) also covers the message and any attr a
// later edit adds to this line, and leaves a DSN-free error byte-identical in
// structured output.
//
// This is the THIRD wrap point of one audited mechanism, not a fourth kind of
// fix: store.Open wraps cfg.Logger and cmd/store_dispatch.go wraps the dispatch
// logger the same way. The two options rejected to get here — a pre-redacted
// return value, and wrapping slog.Default() process-wide, which deadlocks in its
// cheap form — are recorded with their measurements on QUM-1294 and in
// CHANGELOG.md, and the process-wide one on QUM-1296, which owns the durable
// class guard.
//
// NOT a coverage claim: this covers THIS sink. cmd/hubd is a separate main with
// its own print (QUM-1292), and nothing here stops a fifth sink being added.
// The production WIRING of this helper is pinned by
// TestRecordHandoffInEventLog_WiresTheRedactedSink — without it, inlining a raw
// slog.Warn back into the caller would leave the seam tests green.
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
