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
		slog.Warn("event log unusable, handoff recorded to memory only", "error", err)
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
			slog.Warn("recording the handoff event panicked; the summary file is unaffected", "panic", r)
		}
	}()
	handoffEvent(sprawlRoot, session, body)
}
