package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRows writes rows as NDJSON to .sprawl/logs/usage/<agent>/<session>.ndjson
// in the given order — file order is what the legacy repair depends on. Unlike
// writeFixtureFile it writes SchemaVersion verbatim, so these tests can build
// pre-QUM-1247 rows and mixed old/new files.
func writeRows(t *testing.T, root, agent, session string, rows []Record) {
	t.Helper()
	path := usageLogPath(root, agent, session)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.Create(path) //nolint:gosec // test writes a path it constructed
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(&r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

// legacyRows builds pre-QUM-1247 rows: schema_version absent (0) and
// total_cost_usd carrying the raw session-cumulative value.
func legacyRows(agent, session string, cumulative []float64) []Record {
	out := make([]Record, 0, len(cumulative))
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for i, c := range cumulative {
		out = append(out, Record{
			SchemaVersion: 0,
			Timestamp:     base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			AgentName:     agent,
			SessionID:     session,
			InputTokens:   1,
			OutputTokens:  1,
			TotalCostUsd:  c,
		})
	}
	return out
}

// TestSumForAgentSession_RepairsLegacyCumulativeRows pins salvage-on-read:
// historical rows store the cumulative, so aggregation must reconstruct the
// per-turn delta rather than summing them into a ~N/2x inflated figure.
func TestSumForAgentSession_RepairsLegacyCumulativeRows(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-legacy", legacyRows("weave", "sess-legacy", []float64{0.10, 0.35, 0.80}))

	got, err := SumForAgentSession(root, "weave", "sess-legacy")
	if err != nil {
		t.Fatalf("SumForAgentSession: %v", err)
	}
	if !closeTo(got.TotalCostUsd, 0.80) {
		t.Errorf("TotalCostUsd = %v, want 0.80 (the session's final cumulative)", got.TotalCostUsd)
	}
	if closeTo(got.TotalCostUsd, 1.25) {
		t.Errorf("TotalCostUsd = %v — legacy rows were summed raw, the QUM-1247 bug", got.TotalCostUsd)
	}
	// Repair must touch cost only.
	if got.InputTokens != 3 || got.OutputTokens != 3 {
		t.Errorf("tokens = (%d,%d), want (3,3): the repair must not disturb token columns",
			got.InputTokens, got.OutputTokens)
	}
}

// TestSumForAgentSession_LegacyRepairHandlesMidFileReset covers the real shape
// seen in weave/6c09ca75…: the cumulative restarts inside one session file.
func TestSumForAgentSession_LegacyRepairHandlesMidFileReset(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-reset", legacyRows("weave", "sess-reset", []float64{0.10, 0.30, 0.05, 0.20}))

	got, err := SumForAgentSession(root, "weave", "sess-reset")
	if err != nil {
		t.Fatalf("SumForAgentSession: %v", err)
	}
	if !closeTo(got.TotalCostUsd, 0.50) {
		t.Errorf("TotalCostUsd = %v, want sum-of-segment-finals 0.50", got.TotalCostUsd)
	}
	if closeTo(got.TotalCostUsd, 0.45) {
		t.Errorf("TotalCostUsd = %v: the reset row was clamped to 0 instead of counted whole", got.TotalCostUsd)
	}
	if closeTo(got.TotalCostUsd, 0.30) {
		t.Errorf("TotalCostUsd = %v: only the first segment was counted", got.TotalCostUsd)
	}
}

// TestSumByAgent_MixedLegacyAndV1File covers a session file the old binary
// started and the new binary appended to: v0 rows must be repaired, and v1
// rows (already deltas) must pass through untouched.
func TestSumByAgent_MixedLegacyAndV1File(t *testing.T) {
	root := t.TempDir()
	rows := legacyRows("weave", "sess-mixed", []float64{0.10, 0.35})
	base := time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	for i, d := range []float64{0.05, 0.07} {
		rows = append(rows, Record{
			SchemaVersion:  RecordSchemaVersion,
			Timestamp:      base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			AgentName:      "weave",
			SessionID:      "sess-mixed",
			InputTokens:    1,
			OutputTokens:   1,
			TotalCostUsd:   d,
			SessionCostUsd: 0.35 + d,
		})
	}
	writeRows(t, root, "weave", "sess-mixed", rows)

	totals, err := SumByAgent(root, time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent: %v", err)
	}
	// 0.35 (repaired legacy segment) + 0.05 + 0.07 (already deltas).
	if !closeTo(totals["weave"].TotalCostUsd, 0.47) {
		t.Errorf("TotalCostUsd = %v, want 0.47", totals["weave"].TotalCostUsd)
	}
	if closeTo(totals["weave"].TotalCostUsd, 0.57) {
		t.Errorf("TotalCostUsd = %v: legacy rows were summed raw", totals["weave"].TotalCostUsd)
	}
	if closeTo(totals["weave"].TotalCostUsd, 0.35) {
		t.Errorf("TotalCostUsd = %v: the v1 delta rows were wrongly treated as cumulative and repaired away",
			totals["weave"].TotalCostUsd)
	}
}

// TestLoadRecords_LegacyRepairPrecedesSinceFilter guards the ordering trap: a
// delta is only computable from a row's predecessor, so repair must run over
// the whole file BEFORE --since drops rows. Filtering first would leave the
// surviving row carrying its raw cumulative.
func TestLoadRecords_LegacyRepairPrecedesSinceFilter(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-since", legacyRows("weave", "sess-since", []float64{0.10, 0.35, 0.80}))

	// Excludes the first two rows (12:00, 12:01), keeps the third (12:02).
	since := time.Date(2026, 8, 1, 12, 2, 0, 0, time.UTC)
	recs, err := LoadRecords(root, Filter{Since: since})
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if !closeTo(recs[0].TotalCostUsd, 0.45) {
		t.Errorf("TotalCostUsd = %v, want delta 0.45 computed against the filtered-out predecessor", recs[0].TotalCostUsd)
	}
	if closeTo(recs[0].TotalCostUsd, 0.80) {
		t.Errorf("TotalCostUsd = %v: the raw cumulative survived because --since ran before the repair",
			recs[0].TotalCostUsd)
	}
}

// TestCountLegacyRows reports how many rows aggregation had to reconstruct, so
// `sprawl usage summary` can say so instead of silently blending.
func TestCountLegacyRows(t *testing.T) {
	root := t.TempDir()
	rows := legacyRows("weave", "sess-count", []float64{0.10, 0.35})
	rows = append(rows, Record{
		SchemaVersion: RecordSchemaVersion,
		Timestamp:     time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC).Format(time.RFC3339),
		AgentName:     "weave",
		SessionID:     "sess-count",
		TotalCostUsd:  0.05,
	})
	writeRows(t, root, "weave", "sess-count", rows)

	legacy, total, err := CountLegacyRows(root, Filter{})
	if err != nil {
		t.Fatalf("CountLegacyRows: %v", err)
	}
	if legacy != 2 || total != 3 {
		t.Errorf("CountLegacyRows = (%d, %d), want (2, 3)", legacy, total)
	}
}

// TestSumByAgent_LegacyRepairDoesNotLeakBaselineAcrossFiles guards the most
// likely wrong implementation: hoisting the running-cumulative variable outside
// the per-file loop. Each session file is an independent cumulative series, so
// the baseline must reset at every file boundary.
func TestSumByAgent_LegacyRepairDoesNotLeakBaselineAcrossFiles(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-1", legacyRows("weave", "sess-1", []float64{0.10, 0.35}))
	writeRows(t, root, "weave", "sess-2", legacyRows("weave", "sess-2", []float64{0.50, 0.90}))

	totals, err := SumByAgent(root, time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent: %v", err)
	}
	// 0.35 + 0.90: each file's final cumulative.
	if !closeTo(totals["weave"].TotalCostUsd, 1.25) {
		t.Errorf("TotalCostUsd = %v, want 1.25", totals["weave"].TotalCostUsd)
	}
	if closeTo(totals["weave"].TotalCostUsd, 0.90) {
		t.Errorf("TotalCostUsd = %v: the cumulative baseline leaked across the file boundary, so "+
			"sess-2's first row was scored 0.50-0.35 instead of 0.50", totals["weave"].TotalCostUsd)
	}
}

// TestSumForAgent_RepairsLegacyRows covers the lifetime-sum reader. Repair has
// to reach every entry point, not just the ones with production callers.
func TestSumForAgent_RepairsLegacyRows(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-lifetime", legacyRows("weave", "sess-lifetime", []float64{0.10, 0.35, 0.80}))

	got, err := SumForAgent(root, "weave")
	if err != nil {
		t.Fatalf("SumForAgent: %v", err)
	}
	if !closeTo(got.TotalCostUsd, 0.80) {
		t.Errorf("TotalCostUsd = %v, want 0.80", got.TotalCostUsd)
	}
}

// TestSumGrouped_RepairsLegacyRows covers the Record-decode path, which is what
// `sprawl usage summary` actually calls. It is a separate scan path from the
// aggregateLine one used by SumByAgent/SumForAgent, so it needs its own pin.
func TestSumGrouped_RepairsLegacyRows(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-grouped", legacyRows("weave", "sess-grouped", []float64{0.10, 0.35, 0.80}))

	totals, err := SumGrouped(root, GroupSession, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	got := totals["weave/sess-grouped"].TotalCostUsd
	if !closeTo(got, 0.80) {
		t.Errorf("TotalCostUsd = %v, want 0.80", got)
	}
}

// TestLegacyRepair_ProductionShapeRowsOmitSchemaVersionKey uses the exact byte
// shape on disk today: historical rows have no schema_version key at all,
// rather than an explicit 0. Decoding treats them identically, but only a
// hand-written fixture proves that.
func TestLegacyRepair_ProductionShapeRowsOmitSchemaVersionKey(t *testing.T) {
	root := t.TempDir()
	path := usageLogPath(root, "weave", "sess-raw")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	raw := `{"timestamp":"2026-08-01T12:00:00Z","agent_name":"weave","session_id":"sess-raw","input_tokens":1,"output_tokens":1,"total_cost_usd":0.10}
{"timestamp":"2026-08-01T12:01:00Z","agent_name":"weave","session_id":"sess-raw","input_tokens":1,"output_tokens":1,"total_cost_usd":0.35}
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := SumForAgentSession(root, "weave", "sess-raw")
	if err != nil {
		t.Fatalf("SumForAgentSession: %v", err)
	}
	if !closeTo(got.TotalCostUsd, 0.35) {
		t.Errorf("TotalCostUsd = %v, want 0.35: rows with no schema_version key must be "+
			"treated as legacy and repaired", got.TotalCostUsd)
	}
}

// TestSumForAgentSession_LegacyRowAfterV1RowUsesV1Baseline covers the reverse
// of the mixed-file case: an OLDER binary appending to a file a newer one
// already wrote. The legacy rows continue the same cumulative series the v1
// rows were tracking, so the baseline entering the legacy stretch is the last
// v1 row's session_cost_usd — restarting it at 0 re-charges the whole session.
func TestSumForAgentSession_LegacyRowAfterV1RowUsesV1Baseline(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	rows := []Record{
		{
			SchemaVersion: RecordSchemaVersion, Timestamp: base.Format(time.RFC3339),
			AgentName: "weave", SessionID: "sess-v1first",
			TotalCostUsd: 10.0, SessionCostUsd: 10.0,
		},
		{
			SchemaVersion: 0, Timestamp: base.Add(time.Minute).Format(time.RFC3339),
			AgentName: "weave", SessionID: "sess-v1first", TotalCostUsd: 10.5,
		},
		{
			SchemaVersion: 0, Timestamp: base.Add(2 * time.Minute).Format(time.RFC3339),
			AgentName: "weave", SessionID: "sess-v1first", TotalCostUsd: 11.0,
		},
	}
	writeRows(t, root, "weave", "sess-v1first", rows)

	got, err := SumForAgentSession(root, "weave", "sess-v1first")
	if err != nil {
		t.Fatalf("SumForAgentSession: %v", err)
	}
	if !closeTo(got.TotalCostUsd, 11.0) {
		t.Errorf("TotalCostUsd = %v, want 11.0 (the session's final cumulative)", got.TotalCostUsd)
	}
	if closeTo(got.TotalCostUsd, 21.0) {
		t.Errorf("TotalCostUsd = %v: the legacy stretch restarted its baseline at 0 instead of "+
			"continuing from the preceding v1 row's session_cost_usd, re-charging the session", got.TotalCostUsd)
	}
}

// TestDeltaFrom_TinyDipIsNoiseNotReset guards the most expensive available
// wrong answer: a real context reset drops the cumulative to a near-zero
// restart, but a rounding or re-pricing wobble drops it by a hair. Treating the
// two alike re-charges an entire session over a fraction of a cent.
func TestDeltaFrom_TinyDipIsNoiseNotReset(t *testing.T) {
	for _, tc := range []struct {
		name            string
		cur, last, want float64
	}{
		{"normal increase", 54.40, 54.31, 0.09},
		{"flat", 54.31, 54.31, 0},
		{"one-microdollar dip is noise", 54.309999, 54.31, 0},
		{"genuine reset to near zero", 2.99, 54.31, 2.99},
		{"reset to exactly zero", 0, 54.31, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := deltaFrom(tc.cur, tc.last)
			if !closeTo(got, tc.want) {
				t.Errorf("deltaFrom(%v, %v) = %v, want %v", tc.cur, tc.last, got, tc.want)
			}
		})
	}
}

// TestLoadRecords_RepairedRowsAreStampedCurrentSchema keeps exported bytes
// self-consistent. `sprawl usage export` writes LoadRecords output verbatim, so
// a repaired row leaving with a reconstructed cost but schema_version 0 would
// be repaired a SECOND time by anything that re-ingests it.
func TestLoadRecords_RepairedRowsAreStampedCurrentSchema(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-stamp", legacyRows("weave", "sess-stamp", []float64{0.10, 0.35}))

	recs, err := LoadRecords(root, Filter{})
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	for i, r := range recs {
		if r.SchemaVersion != RecordSchemaVersion {
			t.Errorf("record[%d].SchemaVersion = %d, want %d: a repaired row carries v1 semantics "+
				"and must say so, or re-ingesting an export repairs it twice",
				i, r.SchemaVersion, RecordSchemaVersion)
		}
		if !closeTo(r.SessionCostUsd, legacyRows("", "", []float64{0.10, 0.35})[i].TotalCostUsd) {
			t.Errorf("record[%d].SessionCostUsd = %v, want the original cumulative", i, r.SessionCostUsd)
		}
	}
	// Re-repairing an already-repaired stream must be a no-op.
	if !closeTo(recs[0].TotalCostUsd+recs[1].TotalCostUsd, 0.35) {
		t.Errorf("sum = %v, want 0.35", recs[0].TotalCostUsd+recs[1].TotalCostUsd)
	}
}

// TestCountLegacyRows_HonoursSinceFilter pins the note's denominator against
// the rows the summary actually printed.
func TestCountLegacyRows_HonoursSinceFilter(t *testing.T) {
	root := t.TempDir()
	writeRows(t, root, "weave", "sess-since-count", legacyRows("weave", "sess-since-count", []float64{0.10, 0.35, 0.80}))

	since := time.Date(2026, 8, 1, 12, 2, 0, 0, time.UTC)
	legacy, total, err := CountLegacyRows(root, Filter{Since: since})
	if err != nil {
		t.Fatalf("CountLegacyRows: %v", err)
	}
	if legacy != 1 || total != 1 {
		t.Errorf("CountLegacyRows = (%d, %d), want (1, 1): only the row at or after --since counts",
			legacy, total)
	}
}
