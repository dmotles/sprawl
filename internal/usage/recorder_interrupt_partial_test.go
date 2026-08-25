package usage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// This file covers QUM-1257, which SUPERSEDES QUM-368 AC §5. That AC required
// EventInterrupted / EventBackendFaulted to discard the in-flight accumulator
// and write nothing, which meant an interrupted turn's tokens were attributed
// to nothing and vanished from every aggregation. They are now flushed as a
// PARTIAL row: real tokens, zero per-turn cost, cost baseline untouched so the
// next successful turn's delta still absorbs the spend.
//
// The half of the old tests that survives — and matters more now, not less — is
// that the interrupted turn's tokens must not LEAK into the next turn's row.
// That is the guard against flushing the row and forgetting to reset the
// accumulator.

// newTestRecorder builds a Recorder rooted at a fresh temp dir with an agent
// state file already saved, and returns both. Every metadata field is
// populated with a distinct non-empty value: with the empty AgentState the
// other tests in this package use, a row that silently dropped its metadata
// would be indistinguishable from a correct one.
func newTestRecorder(t *testing.T) (*Recorder, string) {
	t.Helper()
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{
		Name:   "finn",
		Status: "active",
		Type:   "engineer",
		Family: "engineering",
		Parent: "weave",
		Branch: "dmotles/finn-work",
	}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	return rec, tmp
}

func TestRecorder_InterruptedTurnFlushesPartialRow(t *testing.T) {
	rec, tmp := newTestRecorder(t)
	defer rec.Close()

	sessionID := "sess-interrupted"
	// Frames accumulate, then the interrupt arrives → partial row. Cache token
	// fields are non-zero and distinct so a row that dropped them is caught.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{
		InputTokens: 7, OutputTokens: 8, CacheReadInputTokens: 900, CacheCreationInputTokens: 40,
	}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})

	// New turn after the interrupt → succeeds.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.01))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 2 {
		t.Fatalf("got %d records after one interrupt + one success, want 2 "+
			"(the interrupted turn's tokens must land in a partial row)", len(records))
	}
	p := records[0]
	if p.InputTokens != 7 || p.OutputTokens != 8 || p.CacheReadInputTokens != 900 || p.CacheCreationInputTokens != 40 {
		t.Errorf("partial record tokens = in=%d out=%d cache_read=%d cache_create=%d, "+
			"want 7/8/900/40 — every accumulated token field must be preserved, not discarded",
			p.InputTokens, p.OutputTokens, p.CacheReadInputTokens, p.CacheCreationInputTokens)
	}
	if !p.Partial {
		t.Errorf("partial record Partial = false, want true — a flushed interrupt row must be " +
			"distinguishable from a legitimately zero-cost completed turn")
	}
	if p.TotalCostUsd != 0 {
		t.Errorf("partial record TotalCostUsd = %v, want 0 — the interrupted turn's spend stays in "+
			"Claude's cumulative and is absorbed by the next turn's delta", p.TotalCostUsd)
	}
	// A partial row that decodes as schema_version 0 is treated as a legacy row
	// by repairLegacyCosts, which then resets its running baseline to this row's
	// session_cost_usd and re-charges every following row in the file.
	if p.SchemaVersion != RecordSchemaVersion {
		t.Errorf("partial record SchemaVersion = %d, want %d — an unstamped row is read as pre-QUM-1247 "+
			"legacy and sends the aggregate cost repair path off a zero baseline",
			p.SchemaVersion, RecordSchemaVersion)
	}
	if p.Timestamp == "" || p.Model != "claude-opus-4-7" || p.SessionID != sessionID ||
		p.AgentName != "finn" || p.AgentType != "engineer" || p.AgentFamily != "engineering" ||
		p.ParentName != "weave" || p.Branch != "dmotles/finn-work" {
		t.Errorf("partial record = %+v, want timestamp/model/session and all agent metadata carried "+
			"through exactly as on a completed row", p)
	}
	// Carried forward from the superseded QUM-368 AC §5 test: the interrupted
	// turn's tokens must not leak into the next turn's row.
	if records[1].InputTokens != 1 || records[1].OutputTokens != 1 || records[1].TotalCostUsd != 0.01 {
		t.Errorf("surviving record = %+v, want input=1 output=1 cost=0.01 (interrupted tokens must not leak)", records[1])
	}
	if records[1].Partial {
		t.Errorf("completed record Partial = true, want false")
	}
}

func TestRecorder_FaultedTurnFlushesPartialRow(t *testing.T) {
	rec, tmp := newTestRecorder(t)
	defer rec.Close()

	sessionID := "sess-faulted"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 4, OutputTokens: 4}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventBackendFaulted})

	// Drive a fresh, successful turn after the fault. Mirrors the interrupt case.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 2, OutputTokens: 3}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.02))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 2 {
		t.Fatalf("got %d records after fault + fresh success, want 2: %+v", len(records), records)
	}
	if records[0].InputTokens != 4 || records[0].OutputTokens != 4 || !records[0].Partial || records[0].TotalCostUsd != 0 {
		t.Errorf("partial record = %+v, want in=4 out=4 partial=true cost=0", records[0])
	}
	// Carried forward from the superseded QUM-368 AC §5 test.
	if records[1].InputTokens != 2 || records[1].OutputTokens != 3 || records[1].TotalCostUsd != 0.02 {
		t.Errorf("surviving record = %+v, want input=2 output=3 cost=0.02 (faulted accumulator must not leak)", records[1])
	}
}

// TestRecorder_PartialRowCarriesSessionCostBaselineForward pins the choice of
// session_cost_usd on a partial row. Storing 0 there would be the natural
// reading of "this row cost nothing", but session_cost_usd is the CUMULATIVE
// series, not the row's own cost — and both seedCostBaseline and
// repairLegacyCosts read it as the running baseline.
func TestRecorder_PartialRowCarriesSessionCostBaselineForward(t *testing.T) {
	rec, tmp := newTestRecorder(t)

	sessionID := "sess-partial-baseline"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.40))

	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 9, OutputTokens: 9}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if !closeTo(records[1].SessionCostUsd, 0.40) {
		t.Errorf("partial record SessionCostUsd = %v, want the carried baseline 0.40 — storing 0 makes "+
			"the last row of the file misreport the cumulative high-water mark, and every reader of "+
			"session_cost_usd (seedCostBaseline, repairLegacyCosts) uses the LAST row",
			records[1].SessionCostUsd)
	}
}

// TestRecorder_PartialRowPreservesResumeBaseline is the consequence of the
// choice above, exercised end to end: a partial row left as the last row in a
// session file must not make a restarting Recorder re-charge the session.
func TestRecorder_PartialRowPreservesResumeBaseline(t *testing.T) {
	first, tmp := newTestRecorder(t)

	sessionID := "sess-partial-resume"
	for _, c := range []float64{0.10, 0.40} {
		first.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
		first.Handle(turnCompletedEvent(sessionID, c))
	}
	// The process dies mid-turn: the last thing written is a partial row.
	first.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 5, OutputTokens: 5}, "claude-opus-4-7"))
	first.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	second.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	second.Handle(turnCompletedEvent(sessionID, 0.55))
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 4 {
		t.Fatalf("got %d records, want 4 (2 completed + 1 partial + 1 post-restart)", len(records))
	}
	if !closeTo(records[3].TotalCostUsd, 0.15) {
		t.Errorf("record[3].TotalCostUsd = %v, want 0.15 — the resumed Recorder must seed its baseline "+
			"from the partial row's carried session_cost_usd (0.40), not from 0", records[3].TotalCostUsd)
	}
	if !closeTo(sumCost(records), 0.55) {
		t.Errorf("sum = %v, want the session's final cumulative 0.55", sumCost(records))
	}
}

// TestRecorder_PartialRowSeedsBaselineOnAResumedSession covers the narrower
// case where the FIRST event a resumed Recorder sees is an interrupt: its
// in-memory baseline is still 0 and unseeded, so it must seed from disk before
// writing, or the partial row clobbers the file's own cumulative baseline.
func TestRecorder_PartialRowSeedsBaselineOnAResumedSession(t *testing.T) {
	first, tmp := newTestRecorder(t)

	sessionID := "sess-partial-seed"
	first.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	first.Handle(turnCompletedEvent(sessionID, 0.40))
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	second.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 5, OutputTokens: 5}, "claude-opus-4-7"))
	second.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	if !closeTo(records[1].SessionCostUsd, 0.40) {
		t.Errorf("partial record SessionCostUsd = %v, want 0.40 seeded from the existing row — an "+
			"unseeded in-memory baseline of 0 must not be written to disk", records[1].SessionCostUsd)
	}
}

// TestRecorder_PartialRowIsVisibleThroughAggregation is the end-to-end form of
// this issue's premise: the interrupted turn's tokens "vanish from every
// aggregation". Every other test here reads the raw NDJSON, so an
// implementation that writes the row correctly but is mishandled on the
// aggregate read path would pass all of them.
func TestRecorder_PartialRowIsVisibleThroughAggregation(t *testing.T) {
	rec, tmp := newTestRecorder(t)

	sessionID := "sess-partial-aggregate"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 10, OutputTokens: 20}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.10))

	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 100, OutputTokens: 200}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})

	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 2}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.45))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	totals, err := SumForAgentSession(tmp, "finn", sessionID)
	if err != nil {
		t.Fatalf("SumForAgentSession: %v", err)
	}
	if totals.InputTokens != 111 || totals.OutputTokens != 222 {
		t.Errorf("aggregated tokens = in=%d out=%d, want in=111 out=222 — the interrupted turn's "+
			"100/200 must be counted, which is the whole point of QUM-1257",
			totals.InputTokens, totals.OutputTokens)
	}
	// The cost total is untouched by the partial row: it contributes 0, and the
	// interrupted spend is still absorbed by the final turn's delta.
	if !closeTo(totals.TotalCostUsd, 0.45) {
		t.Errorf("aggregated cost = %v, want the session's final cumulative 0.45 — a partial row must "+
			"neither add spend of its own nor disturb the delta baseline of the rows around it",
			totals.TotalCostUsd)
	}
}

// TestRecorder_ConsecutiveInterruptsWriteOnePartialRow: the flush resets the
// accumulator, so a second interrupt arriving with nothing new accumulated has
// nothing to attribute and must not write a duplicate (or all-zero) row.
func TestRecorder_ConsecutiveInterruptsWriteOnePartialRow(t *testing.T) {
	rec, tmp := newTestRecorder(t)

	sessionID := "sess-double-interrupt"
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 6, OutputTokens: 6}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 1 {
		t.Fatalf("got %d records after two consecutive interrupts, want 1 — the flush must reset the "+
			"accumulator so the same tokens are not attributed twice: %+v", len(records), records)
	}
	if records[0].InputTokens != 6 || records[0].OutputTokens != 6 {
		t.Errorf("partial record = in=%d out=%d, want in=6 out=6",
			records[0].InputTokens, records[0].OutputTokens)
	}
}

// TestRecorder_InterruptWithNoAccumulatedDataWritesNothing: an interrupt whose
// accumulator has already been flushed by a completed turn has no tokens left
// to attribute, so it must not manufacture an all-zero row.
//
// Positive control (this test passes at HEAD, where no partial row exists at
// all, so it discharges nothing until the feature lands): drop the
// accum.HasData() guard from the interrupt arm and a second, all-zero row
// appears — the assertion below fires.
func TestRecorder_InterruptWithNoAccumulatedDataWritesNothing(t *testing.T) {
	rec, tmp := newTestRecorder(t)
	defer rec.Close()

	sessionID := "sess-empty-interrupt"
	// Establish the session id without accumulating a turn's worth of tokens,
	// then interrupt an already-flushed accumulator.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.01))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 — an interrupt with an empty accumulator must write nothing: %+v",
			len(records), records)
	}
}

// TestRecorder_InterruptBeforeAnySessionIDWritesNothing: interrupt/fault events
// carry Result == nil, so unlike handleTurnCompleted there is no
// ev.Result.SessionID fallback. With no session id there is no file to write
// to, and writing anyway would create "<agent>/.ndjson".
//
// Positive control: drop the sessID == "" early return from the interrupt arm
// and openWriter creates a nameless log — the assertion below fires.
func TestRecorder_InterruptBeforeAnySessionIDWritesNothing(t *testing.T) {
	rec, tmp := newTestRecorder(t)
	defer rec.Close()

	// A bare interrupt as the very first event: nothing accumulated, no session.
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	// And an interrupt with tokens accumulated but still no session id.
	rec.Handle(assistantEvent(t, "", protocol.Usage{InputTokens: 3, OutputTokens: 3}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dir := filepath.Join(tmp, ".sprawl", "logs", "usage", "finn")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return // nothing written at all: correct
		}
		t.Fatalf("ReadDir %q: %v", dir, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("wrote %v, want no usage file — an interrupt with no known session id has nowhere "+
			"to write and must not create a nameless log", names)
	}
}
