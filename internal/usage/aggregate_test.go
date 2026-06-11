package usage

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeFixtureFile creates .sprawl/logs/usage/<agent>/<session>.ndjson with the
// given records.
func writeFixtureFile(t *testing.T, sprawlRoot, agent, session string, recs []Record) {
	t.Helper()
	dir := filepath.Join(sprawlRoot, ".sprawl", "logs", "usage", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, session+".ndjson")
	f, err := os.Create(path) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("Create %q: %v", path, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(&r); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
}

func nearly(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestSumByAgent_TwoAgents(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		{InputTokens: 100, OutputTokens: 10, CacheReadInputTokens: 1, CacheCreationInputTokens: 2, TotalCostUsd: 0.10},
		{InputTokens: 200, OutputTokens: 20, CacheReadInputTokens: 3, CacheCreationInputTokens: 4, TotalCostUsd: 0.20},
	})
	writeFixtureFile(t, tmp, "alice", "s2", []Record{
		{InputTokens: 50, OutputTokens: 5, CacheReadInputTokens: 5, CacheCreationInputTokens: 6, TotalCostUsd: 0.05},
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		{InputTokens: 9, OutputTokens: 1, CacheReadInputTokens: 0, CacheCreationInputTokens: 0, TotalCostUsd: 0.01},
	})

	got, err := SumByAgent(tmp, time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d agents, want 2: %+v", len(got), got)
	}
	alice := got["alice"]
	if alice.InputTokens != 350 || alice.OutputTokens != 35 {
		t.Errorf("alice tokens = (%d,%d), want (350,35)", alice.InputTokens, alice.OutputTokens)
	}
	if alice.CacheReadInputTokens != 9 || alice.CacheCreationInputTokens != 12 {
		t.Errorf("alice cache = (read=%d, creation=%d), want (9,12)",
			alice.CacheReadInputTokens, alice.CacheCreationInputTokens)
	}
	if !nearly(alice.TotalCostUsd, 0.35) {
		t.Errorf("alice cost = %v, want 0.35", alice.TotalCostUsd)
	}
	bob := got["bob"]
	if bob.InputTokens != 9 || !nearly(bob.TotalCostUsd, 0.01) {
		t.Errorf("bob = %+v, want input=9 cost=0.01", bob)
	}
}

func TestSumByAgent_SinceFilter(t *testing.T) {
	tmp := t.TempDir()
	// Anchor relative to a fixed "now" so the windows are deterministic.
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec(ts(1*time.Hour), "alice", "s1", "sonnet", 1, 0, 0),       // within 24h
		rec(ts(3*24*time.Hour), "alice", "s1", "sonnet", 10, 0, 0),   // within 7d, not 24h
		rec(ts(15*24*time.Hour), "alice", "s1", "sonnet", 100, 0, 0), // within 30d, not 7d
		rec(ts(100*24*time.Hour), "alice", "s1", "sonnet", 1000, 0, 0),
		rec(ts(300*24*time.Hour), "alice", "s1", "sonnet", 10000, 0, 0), // within 365d, not 30d
		rec(ts(400*24*time.Hour), "alice", "s1", "sonnet", 100000, 0, 0),
	})

	cases := []struct {
		name  string
		since time.Time
		want  int
	}{
		{"24h", now.Add(-24 * time.Hour), 1},
		{"7d", now.Add(-7 * 24 * time.Hour), 11},
		{"30d", now.Add(-30 * 24 * time.Hour), 111},
		{"365d", now.Add(-365 * 24 * time.Hour), 11111},
		{"all", time.Time{}, 111111},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SumByAgent(tmp, tc.since)
			if err != nil {
				t.Fatalf("SumByAgent: %v", err)
			}
			if got["alice"].InputTokens != tc.want {
				t.Errorf("window %s: alice InputTokens = %d, want %d", tc.name, got["alice"].InputTokens, tc.want)
			}
		})
	}
}

func TestSumForAgent_ReturnsSingleAgentTotals(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		{InputTokens: 100, OutputTokens: 10, TotalCostUsd: 0.10},
		{InputTokens: 200, OutputTokens: 20, TotalCostUsd: 0.20},
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		{InputTokens: 999, OutputTokens: 999, TotalCostUsd: 9.99},
	})

	got, err := SumForAgent(tmp, "alice")
	if err != nil {
		t.Fatalf("SumForAgent: %v", err)
	}
	if got.InputTokens != 300 || got.OutputTokens != 30 {
		t.Errorf("tokens = (%d,%d), want (300,30)", got.InputTokens, got.OutputTokens)
	}
	if !nearly(got.TotalCostUsd, 0.30) {
		t.Errorf("cost = %v, want 0.30", got.TotalCostUsd)
	}
}

func TestSumByAgent_MissingDirReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	got, err := SumByAgent(tmp, time.Time{})
	if err != nil {
		t.Fatalf("SumByAgent on empty root: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d agents, want 0", len(got))
	}
}

func TestSumForAgent_MissingDirReturnsZero(t *testing.T) {
	tmp := t.TempDir()
	got, err := SumForAgent(tmp, "nobody")
	if err != nil {
		t.Fatalf("SumForAgent on missing dir: %v", err)
	}
	if got != (TokenTotals{}) {
		t.Errorf("got %+v, want zero TokenTotals", got)
	}
}

// --- QUM-370: SumGrouped / LoadRecords / TailRecords ---

func rec(ts, agent, sess, model string, in, out int, cost float64) Record {
	return Record{
		Timestamp:    ts,
		AgentName:    agent,
		SessionID:    sess,
		Model:        model,
		InputTokens:  in,
		OutputTokens: out,
		TotalCostUsd: cost,
	}
}

func TestSumGrouped_ByAgent(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
		rec("2026-05-01T11:00:00Z", "alice", "s1", "sonnet", 200, 20, 0.20),
	})
	writeFixtureFile(t, tmp, "alice", "s2", []Record{
		rec("2026-05-01T12:00:00Z", "alice", "s2", "opus", 50, 5, 0.05),
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		rec("2026-05-01T13:00:00Z", "bob", "s1", "opus", 9, 1, 0.01),
	})

	got, err := SumGrouped(tmp, GroupAgent, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d keys, want 2: %+v", len(got), got)
	}
	if got["alice"].InputTokens != 350 || got["alice"].OutputTokens != 35 {
		t.Errorf("alice tokens = (%d,%d), want (350,35)", got["alice"].InputTokens, got["alice"].OutputTokens)
	}
	if !nearly(got["alice"].TotalCostUsd, 0.35) {
		t.Errorf("alice cost = %v, want 0.35", got["alice"].TotalCostUsd)
	}
	if got["bob"].InputTokens != 9 {
		t.Errorf("bob input = %d, want 9", got["bob"].InputTokens)
	}
}

func TestSumGrouped_ByModel(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
		rec("2026-05-01T11:00:00Z", "alice", "s1", "opus", 200, 20, 0.20),
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		rec("2026-05-01T12:00:00Z", "bob", "s1", "sonnet", 50, 5, 0.05),
	})

	got, err := SumGrouped(tmp, GroupModel, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if got["sonnet"].InputTokens != 150 {
		t.Errorf("sonnet input = %d, want 150", got["sonnet"].InputTokens)
	}
	if got["opus"].InputTokens != 200 {
		t.Errorf("opus input = %d, want 200", got["opus"].InputTokens)
	}
}

func TestSumGrouped_BySession(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
	})
	writeFixtureFile(t, tmp, "alice", "s2", []Record{
		rec("2026-05-01T11:00:00Z", "alice", "s2", "sonnet", 50, 5, 0.05),
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		rec("2026-05-01T12:00:00Z", "bob", "s1", "sonnet", 9, 1, 0.01),
	})

	got, err := SumGrouped(tmp, GroupSession, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if got["alice/s1"].InputTokens != 100 {
		t.Errorf("alice/s1 = %d, want 100", got["alice/s1"].InputTokens)
	}
	if got["alice/s2"].InputTokens != 50 {
		t.Errorf("alice/s2 = %d, want 50", got["alice/s2"].InputTokens)
	}
	if got["bob/s1"].InputTokens != 9 {
		t.Errorf("bob/s1 = %d, want 9", got["bob/s1"].InputTokens)
	}
}

func TestSumGrouped_ByDay(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T23:50:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
		rec("2026-05-02T00:10:00Z", "alice", "s1", "sonnet", 200, 20, 0.20),
		rec("2026-05-02T05:00:00Z", "alice", "s1", "sonnet", 50, 5, 0.05),
	})

	got, err := SumGrouped(tmp, GroupDay, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if got["2026-05-01"].InputTokens != 100 {
		t.Errorf("2026-05-01 = %d, want 100", got["2026-05-01"].InputTokens)
	}
	if got["2026-05-02"].InputTokens != 250 {
		t.Errorf("2026-05-02 = %d, want 250", got["2026-05-02"].InputTokens)
	}
}

func TestSumGrouped_FilterAgent(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		rec("2026-05-01T11:00:00Z", "bob", "s1", "sonnet", 999, 99, 9.99),
	})

	got, err := SumGrouped(tmp, GroupAgent, Filter{Agent: "alice"})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if _, ok := got["bob"]; ok {
		t.Errorf("bob should not be present: %+v", got)
	}
	if got["alice"].InputTokens != 100 {
		t.Errorf("alice = %d, want 100", got["alice"].InputTokens)
	}
}

func TestSumGrouped_FilterSince(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
		rec("2026-05-01T12:00:00Z", "alice", "s1", "sonnet", 200, 20, 0.20),
	})

	since := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	got, err := SumGrouped(tmp, GroupAgent, Filter{Since: since})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if got["alice"].InputTokens != 200 {
		t.Errorf("alice with since-filter = %d, want 200", got["alice"].InputTokens)
	}
}

func TestSumGrouped_FilterUntil(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0.10),
		rec("2026-05-01T12:00:00Z", "alice", "s1", "sonnet", 200, 20, 0.20),
	})

	until := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	got, err := SumGrouped(tmp, GroupAgent, Filter{Until: until})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if got["alice"].InputTokens != 100 {
		t.Errorf("alice with until-filter = %d, want 100", got["alice"].InputTokens)
	}
}

func TestSumGrouped_EmptyRoot(t *testing.T) {
	tmp := t.TempDir()
	got, err := SumGrouped(tmp, GroupAgent, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %+v", got)
	}
}

func TestLoadRecords_SortedByTimestamp(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T12:00:00Z", "alice", "s1", "sonnet", 1, 1, 0),
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 2, 2, 0),
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		rec("2026-05-01T11:00:00Z", "bob", "s1", "sonnet", 3, 3, 0),
		rec("2026-05-01T13:00:00Z", "bob", "s1", "sonnet", 4, 4, 0),
	})

	got, err := LoadRecords(tmp, Filter{})
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d records, want 4", len(got))
	}
	want := []string{
		"2026-05-01T10:00:00Z",
		"2026-05-01T11:00:00Z",
		"2026-05-01T12:00:00Z",
		"2026-05-01T13:00:00Z",
	}
	for i, w := range want {
		if got[i].Timestamp != w {
			t.Errorf("got[%d].Timestamp = %q, want %q", i, got[i].Timestamp, w)
		}
	}
}

func TestLoadRecords_FilterAgent(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 1, 1, 0),
	})
	writeFixtureFile(t, tmp, "bob", "s1", []Record{
		rec("2026-05-01T11:00:00Z", "bob", "s1", "sonnet", 2, 2, 0),
	})

	got, err := LoadRecords(tmp, Filter{Agent: "alice"})
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].AgentName != "alice" {
		t.Errorf("got agent %q, want alice", got[0].AgentName)
	}
}

func TestTailRecords_LastN(t *testing.T) {
	tmp := t.TempDir()
	var recsAlice []Record
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 5, 1, 10+i, 0, 0, 0, time.UTC).Format(time.RFC3339)
		recsAlice = append(recsAlice, rec(ts, "alice", "s1", "sonnet", i, i, 0))
	}
	writeFixtureFile(t, tmp, "alice", "s1", recsAlice)
	var recsBob []Record
	for i := 0; i < 5; i++ {
		ts := time.Date(2026, 5, 1, 15+i, 0, 0, 0, time.UTC).Format(time.RFC3339)
		recsBob = append(recsBob, rec(ts, "bob", "s1", "sonnet", i, i, 0))
	}
	writeFixtureFile(t, tmp, "bob", "s1", recsBob)

	got, err := TailRecords(tmp, Filter{}, 3)
	if err != nil {
		t.Fatalf("TailRecords: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3", len(got))
	}
	want := []string{
		"2026-05-01T17:00:00Z",
		"2026-05-01T18:00:00Z",
		"2026-05-01T19:00:00Z",
	}
	for i, w := range want {
		if got[i].Timestamp != w {
			t.Errorf("got[%d].Timestamp = %q, want %q", i, got[i].Timestamp, w)
		}
	}
}

// TestLoadRecords_IgnoresUnknownFields verifies that a future-added schema
// field does not break readers — LoadRecords decodes known fields and skips
// the rest, returning the record without error.
func TestLoadRecords_IgnoresUnknownFields(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".sprawl", "logs", "usage", "alice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "s1.ndjson")
	line := `{"timestamp":"2026-05-01T10:00:00Z","agent_name":"alice","input_tokens":10,"future_field":"x"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := LoadRecords(tmp, Filter{})
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if got[0].InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", got[0].InputTokens)
	}
	if got[0].AgentName != "alice" {
		t.Errorf("AgentName = %q, want alice", got[0].AgentName)
	}
}

// TestLoadRecords_SkipsMalformedLines verifies that garbage / blank lines
// in an NDJSON file are tolerated: valid records are still returned, no
// error is raised. This protects against a single bad write corrupting
// the entire usage history.
func TestLoadRecords_SkipsMalformedLines(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".sprawl", "logs", "usage", "alice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "s1.ndjson")
	body := `{"timestamp":"2026-05-01T10:00:00Z","agent_name":"alice","input_tokens":1}` + "\n" +
		`not json garbage` + "\n" +
		"\n" +
		`{"timestamp":"2026-05-01T11:00:00Z","agent_name":"alice","input_tokens":2}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := LoadRecords(tmp, Filter{})
	if err != nil {
		t.Fatalf("LoadRecords: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(got), got)
	}
	if got[0].InputTokens != 1 || got[1].InputTokens != 2 {
		t.Errorf("got tokens = (%d,%d), want (1,2)", got[0].InputTokens, got[1].InputTokens)
	}
}

func TestSumGrouped_SkipsMalformedLines(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, ".sprawl", "logs", "usage", "alice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "s1.ndjson")
	body := `{"timestamp":"2026-05-01T10:00:00Z","agent_name":"alice","input_tokens":100,"total_cost_usd":0.10}` + "\n" +
		`}{ not json` + "\n" +
		"\n" +
		`{"timestamp":"2026-05-01T11:00:00Z","agent_name":"alice","input_tokens":200,"total_cost_usd":0.20}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := SumGrouped(tmp, GroupAgent, Filter{})
	if err != nil {
		t.Fatalf("SumGrouped: %v", err)
	}
	if got["alice"].InputTokens != 300 {
		t.Errorf("alice InputTokens = %d, want 300 (malformed line skipped)", got["alice"].InputTokens)
	}
	if !nearly(got["alice"].TotalCostUsd, 0.30) {
		t.Errorf("alice cost = %v, want 0.30", got["alice"].TotalCostUsd)
	}
}

func TestTailRecords_ZeroReturnsEmpty(t *testing.T) {
	tmp := t.TempDir()
	writeFixtureFile(t, tmp, "alice", "s1", []Record{
		rec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 1, 1, 0),
	})
	got, err := TailRecords(tmp, Filter{}, 0)
	if err != nil {
		t.Fatalf("TailRecords: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want empty", len(got))
	}
}
