package usage

import (
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
)

// costEpsilon bounds float comparison of dollar amounts. Deltas are built by
// subtraction, so exact equality is not safe even for tidy decimal inputs.
const costEpsilon = 1e-9

func closeTo(got, want float64) bool {
	d := got - want
	return d < costEpsilon && d > -costEpsilon
}

// driveTurns feeds one assistant frame + one turn-completed frame per entry in
// cumulative, using the session-cumulative cost value Claude's result frame
// carries, and returns the rows the Recorder wrote.
func driveTurns(t *testing.T, sessionID string, cumulative []float64) []Record {
	t.Helper()
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	for _, c := range cumulative {
		rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
		rec.Handle(turnCompletedEvent(sessionID, c))
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return readNDJSONLines(t, usageLogPath(tmp, "finn", sessionID))
}

func sumCost(recs []Record) float64 {
	var total float64
	for _, r := range recs {
		total += r.TotalCostUsd
	}
	return total
}

// TestRecorder_PerTurnCostIsDeltaOfCumulative pins the core QUM-1247 fix:
// Claude's result frame reports total_cost_usd as session-cumulative, so each
// NDJSON row must store the DELTA over the previous turn. The sum of the
// stored column then equals the session's final cumulative figure instead of
// the ~N/2x inflation the old verbatim-store produced.
func TestRecorder_PerTurnCostIsDeltaOfCumulative(t *testing.T) {
	cumulative := []float64{0.10, 0.35, 0.35, 0.80}
	wantDeltas := []float64{0.10, 0.25, 0.00, 0.45}

	records := driveTurns(t, "sess-delta", cumulative)
	if len(records) != len(cumulative) {
		t.Fatalf("got %d records, want %d", len(records), len(cumulative))
	}
	for i, r := range records {
		if !closeTo(r.TotalCostUsd, wantDeltas[i]) {
			t.Errorf("record[%d].TotalCostUsd = %v, want delta %v (cumulative fed in: %v)",
				i, r.TotalCostUsd, wantDeltas[i], cumulative[i])
		}
		// The raw cumulative is retained for auditability.
		if !closeTo(r.SessionCostUsd, cumulative[i]) {
			t.Errorf("record[%d].SessionCostUsd = %v, want raw cumulative %v",
				i, r.SessionCostUsd, cumulative[i])
		}
	}

	got := sumCost(records)
	if !closeTo(got, 0.80) {
		t.Errorf("sum of per-turn deltas = %v, want final cumulative 0.80", got)
	}
	// The defect this test exists to catch: summing the cumulative column.
	if closeTo(got, 1.60) {
		t.Errorf("sum = %v, which is the raw sum of the cumulative column — the QUM-1247 inflation bug", got)
	}
}

// TestRecorder_MidSessionCostResetDoesNotLoseSpend covers the case measured in
// this host's real logs (weave/6c09ca75…): total_cost_usd restarts from a low
// value partway through a single session file after a context reset. The row
// at the reset IS the new segment's first cumulative value, so it must be
// stored whole — clamping the negative difference to 0 would silently drop
// that turn's spend.
func TestRecorder_MidSessionCostResetDoesNotLoseSpend(t *testing.T) {
	cumulative := []float64{0.10, 0.30, 0.05, 0.20}
	wantDeltas := []float64{0.10, 0.20, 0.05, 0.15}

	records := driveTurns(t, "sess-reset", cumulative)
	if len(records) != len(cumulative) {
		t.Fatalf("got %d records, want %d", len(records), len(cumulative))
	}
	for i, r := range records {
		if !closeTo(r.TotalCostUsd, wantDeltas[i]) {
			t.Errorf("record[%d].TotalCostUsd = %v, want delta %v (cumulative fed in: %v)",
				i, r.TotalCostUsd, wantDeltas[i], cumulative[i])
		}
	}

	// The raw cumulative is stored verbatim even at the reset row — storing the
	// baseline or the delta there would destroy the audit trail.
	if !closeTo(records[2].SessionCostUsd, 0.05) {
		t.Errorf("record[2].SessionCostUsd = %v, want the raw post-reset cumulative 0.05",
			records[2].SessionCostUsd)
	}

	// Truth for a reset-carrying session is the sum of each segment's FINAL
	// cumulative value: 0.30 (pre-reset) + 0.20 (post-reset).
	got := sumCost(records)
	if !closeTo(got, 0.50) {
		t.Errorf("sum of per-turn deltas = %v, want sum-of-segment-finals 0.50", got)
	}
	if closeTo(got, 0.65) {
		t.Errorf("sum = %v, which is the raw sum of the cumulative column — the QUM-1247 inflation bug", got)
	}
	if closeTo(got, 0.30) {
		t.Errorf("sum = %v, which is max-per-session (the 0.30 row) — that under-reports "+
			"reset-carrying sessions", got)
	}
	if closeTo(got, 0.20) {
		t.Errorf("sum = %v, which is the LAST cumulative value — that drops the pre-reset segment", got)
	}
	if closeTo(got, 0.45) {
		t.Errorf("sum = %v: the reset row's spend was clamped to 0 instead of stored whole", got)
	}
}

// TestRecorder_SessionRotationResetsCostBaseline verifies the delta baseline is
// cleared when the session_id changes. Without the reset, the first turn of a
// cheaper new session computes against the old session's high-water mark and
// records a negative (or clamped-to-zero) cost.
func TestRecorder_SessionRotationResetsCostBaseline(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	for _, c := range []float64{0.20, 0.50} {
		rec.Handle(assistantEvent(t, "sess-a", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
		rec.Handle(turnCompletedEvent("sess-a", c))
	}
	rec.Handle(assistantEvent(t, "sess-b", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-b", 0.10))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	a := readNDJSONLines(t, usageLogPath(tmp, "finn", "sess-a"))
	if len(a) != 2 {
		t.Fatalf("sess-a: got %d records, want 2", len(a))
	}
	if !closeTo(sumCost(a), 0.50) {
		t.Errorf("sess-a sum = %v, want 0.50", sumCost(a))
	}

	b := readNDJSONLines(t, usageLogPath(tmp, "finn", "sess-b"))
	if len(b) != 1 {
		t.Fatalf("sess-b: got %d records, want 1", len(b))
	}
	if !closeTo(b[0].TotalCostUsd, 0.10) {
		t.Errorf("sess-b first row = %v, want the full 0.10: the delta baseline must reset "+
			"on session rotation, not carry sess-a's 0.50 high-water mark", b[0].TotalCostUsd)
	}
}

// TestRecorder_SchemaVersionStampedOnEveryRow ensures aggregation can tell
// corrected rows from historical inflated ones.
func TestRecorder_SchemaVersionStampedOnEveryRow(t *testing.T) {
	records := driveTurns(t, "sess-schema", []float64{0.10, 0.20})
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	for i, r := range records {
		if r.SchemaVersion != RecordSchemaVersion {
			t.Errorf("record[%d].SchemaVersion = %d, want %d", i, r.SchemaVersion, RecordSchemaVersion)
		}
	}
}

// TestRecorder_InterruptedTurnSpendIsAbsorbedByNextTurn documents the deferred
// secondary issue (a) from QUM-1247: an interrupted turn writes no row, so its
// TOKENS are lost — but its SPEND is not, because the next successful turn's
// cumulative still includes it and the delta picks it up. This only holds if
// the interrupt arm leaves the cost baseline alone; resetting it there would
// double-count the absorbed spend.
func TestRecorder_InterruptedTurnSpendIsAbsorbedByNextTurn(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	rec.Handle(assistantEvent(t, "sess-int", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-int", 0.10))

	// Interrupted turn: assistant frames arrive, then an interrupt. No row.
	rec.Handle(assistantEvent(t, "sess-int", protocol.Usage{InputTokens: 5, OutputTokens: 5}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventInterrupted})

	// Next successful turn. Claude's cumulative includes the interrupted spend.
	rec.Handle(assistantEvent(t, "sess-int", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-int", 0.45))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", "sess-int"))
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (the interrupted turn writes none)", len(records))
	}
	if !closeTo(records[1].TotalCostUsd, 0.35) {
		t.Errorf("record[1].TotalCostUsd = %v, want 0.35 — the interrupted turn's spend must be "+
			"absorbed here, not dropped or double-counted", records[1].TotalCostUsd)
	}
	if !closeTo(sumCost(records), 0.45) {
		t.Errorf("sum = %v, want the session's final cumulative 0.45", sumCost(records))
	}
}

// TestRecorder_FaultedTurnLeavesCostBaselineIntact mirrors the interrupt case
// for the other arm of recorder.go's EventInterrupted/EventBackendFaulted
// switch. Splitting the arms and clearing the baseline on fault would pass
// every other test here while double-counting the faulted turn's spend.
func TestRecorder_FaultedTurnLeavesCostBaselineIntact(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	rec, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	rec.Handle(assistantEvent(t, "sess-fault", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-fault", 0.10))

	rec.Handle(assistantEvent(t, "sess-fault", protocol.Usage{InputTokens: 5, OutputTokens: 5}, "claude-opus-4-7"))
	rec.Handle(runtime.RuntimeEvent{Type: runtime.EventBackendFaulted})

	rec.Handle(assistantEvent(t, "sess-fault", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent("sess-fault", 0.45))
	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", "sess-fault"))
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2 (the faulted turn writes none)", len(records))
	}
	if !closeTo(records[1].TotalCostUsd, 0.35) {
		t.Errorf("record[1].TotalCostUsd = %v, want 0.35 — a fault must not clear the cost baseline",
			records[1].TotalCostUsd)
	}
	if !closeTo(sumCost(records), 0.45) {
		t.Errorf("sum = %v, want the session's final cumulative 0.45", sumCost(records))
	}
}

// TestRecorder_ResumedSessionReseedsBaselineFromLastRow covers process restart.
// The delta baseline lives in memory but openWriter appends (O_APPEND) to the
// existing session file, so a new Recorder resuming the SAME session_id would
// otherwise score its first turn against a zero baseline and re-charge the
// whole session. The baseline is re-seeded from the last row's session_cost_usd.
func TestRecorder_ResumedSessionReseedsBaselineFromLastRow(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{Name: "finn", Status: "active"}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	first, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	for _, c := range []float64{0.10, 0.40} {
		first.Handle(assistantEvent(t, "sess-resume", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
		first.Handle(turnCompletedEvent("sess-resume", c))
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Process restart: fresh Recorder, same session, cumulative continues.
	second, err := NewRecorder(tmp, "finn")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	second.Handle(assistantEvent(t, "sess-resume", protocol.Usage{InputTokens: 1, OutputTokens: 1}, "claude-opus-4-7"))
	second.Handle(turnCompletedEvent("sess-resume", 0.55))
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "finn", "sess-resume"))
	if len(records) != 3 {
		t.Fatalf("got %d records, want 3", len(records))
	}
	if !closeTo(records[2].TotalCostUsd, 0.15) {
		t.Errorf("record[2].TotalCostUsd = %v, want 0.15 — the resumed Recorder must re-seed its "+
			"baseline from the last row's session_cost_usd, not restart from 0", records[2].TotalCostUsd)
	}
	if !closeTo(sumCost(records), 0.55) {
		t.Errorf("sum = %v, want the session's final cumulative 0.55", sumCost(records))
	}
}
