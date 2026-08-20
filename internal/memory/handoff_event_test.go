package memory

import (
	"path/filepath"
	"testing"
	"time"
)

// The memory side of the plan's "dual-write then replace" decision: a HANDOFF
// summary is written to .sprawl/memory as it always was, and additionally
// recorded in the event log.
//
// The file write stays authoritative until M6, so the ordering and the failure
// policy both matter and are asserted: the event is recorded only AFTER the file
// lands, and nothing the event log does can fail the write.

func TestWriteSessionSummary_DualWritesOnlyForAHandoff(t *testing.T) {
	root := t.TempDir()
	var calls []string
	restore := setHandoffEventHookForTest(func(sprawlRoot string, s Session, body string) {
		calls = append(calls, s.SessionID)
	})
	defer restore()

	// A NON-handoff summary must not emit. Ordinary session summaries are
	// written on every session end; emitting for them would flood the log with
	// events the plan scopes to handoffs only.
	if err := WriteSessionSummary(root, Session{
		SessionID: "ordinary", Timestamp: time.Now(), Handoff: false,
	}, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("a non-handoff summary emitted %v; the plan scopes the dual-write to handoffs", calls)
	}

	// A handoff must emit exactly once.
	if err := WriteSessionSummary(root, Session{
		SessionID: "handoff-1", Timestamp: time.Now(), Handoff: true,
	}, "the summary"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}
	if len(calls) != 1 || calls[0] != "handoff-1" {
		t.Errorf("handoff emitted %v, want exactly [handoff-1]", calls)
	}
}

// TestWriteSessionSummary_EmitsOnlyAfterTheFileLands pins the ordering.
//
// The memory file is the system of record until M6. Emitting before the file
// exists would put an event in the log pointing at a summary that may never have
// been written — and summary_sha256 is what ties the two together, so a reader
// following it would find nothing.
func TestWriteSessionSummary_EmitsOnlyAfterTheFileLands(t *testing.T) {
	root := t.TempDir()
	var fileExistedAtEmit bool
	restore := setHandoffEventHookForTest(func(sprawlRoot string, s Session, body string) {
		matches, _ := filepath.Glob(filepath.Join(sprawlRoot, ".sprawl", "memory", "sessions", "*.md"))
		fileExistedAtEmit = len(matches) > 0
	})
	defer restore()

	if err := WriteSessionSummary(root, Session{
		SessionID: "ordered", Timestamp: time.Now(), Handoff: true,
	}, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}
	if !fileExistedAtEmit {
		t.Error("the event was recorded before the summary file existed; a reader following summary_sha256 would find nothing")
	}
}

// TestWriteSessionSummary_HookPanicDoesNotLoseTheHandoff is the failure-policy
// assertion, and it is deliberately harsher than the real hook's contract.
//
// RecordHandoff is documented never to return an error, but "documented" is not
// "enforced" — a future edit, or a nil map deep inside the store, would panic on
// the handoff path and take the session summary with it. The summary is the one
// artifact a handoff exists to produce, so an observability component must not be
// able to destroy it.
func TestWriteSessionSummary_HookPanicDoesNotLoseTheHandoff(t *testing.T) {
	root := t.TempDir()
	restore := setHandoffEventHookForTest(func(string, Session, string) {
		panic("the event log exploded")
	})
	defer restore()

	if err := WriteSessionSummary(root, Session{
		SessionID: "survives", Timestamp: time.Now(), Handoff: true,
	}, "the precious summary"); err != nil {
		t.Fatalf("a panicking event-log hook must not fail the handoff: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".sprawl", "memory", "sessions", "*.md"))
	if len(matches) != 1 {
		t.Errorf("the summary file was not written (%d matches) — the event log destroyed the handoff", len(matches))
	}
}
