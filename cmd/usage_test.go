// QUM-370: TDD red-phase tests for `sprawl usage` CLI.
// The runUsageTail / runUsageSummary / runUsageExport functions, the
// usageDeps struct, and the helpers (parseDurationExt, formatTokens,
// formatCost) are not yet implemented. These tests should fail to
// compile until the implementation lands.
package cmd

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/usage"
)

// updateGoldens controls whether the goldens are re-written from `got`.
// Use `go test ./cmd/ -update-usage-goldens` once the implementation is
// stable to refresh the expected fixtures.
var updateGoldens = flag.Bool("update-usage-goldens", false, "rewrite usage golden files from actual output")

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "usage", name)
	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %q: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("read golden %q: %v", path, err)
	}
	if string(want) != got {
		t.Errorf("golden %q mismatch\n--- want ---\n%s\n--- got ---\n%s", path, string(want), got)
	}
}

// --- DI shape (red-phase: implementation must provide these symbols) ---
//
// type usageDeps struct {
//     sprawlRoot func() (string, error)
//     now        func() time.Time
//     stdout     io.Writer
//     stderr     io.Writer
//     sleep      func(time.Duration)
//     followStop func() bool
// }
//
// func runUsageTail(deps *usageDeps, agent string, follow bool, last int, quiet bool) error
// func runUsageSummary(deps *usageDeps, by, group, sinceStr, untilStr string, quiet bool) error
// func runUsageExport(deps *usageDeps, format, sinceStr, agent string) error
//
// Tail human-readable output format (one record per line):
//   <RFC3339> <agent_name> <model> in=<N> out=<N> cache_read=<N> cache_creation=<N> cost=$0.0000
// Filters/tests rely on column 2 being agent_name exactly.
// func parseDurationExt(s string) (time.Duration, error)
// func formatTokens(n int) string
// func formatCost(c float64) string

// syncBuf is a goroutine-safe io.Writer that captures output as it is written.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func newUsageDeps(t *testing.T) (*usageDeps, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	root := t.TempDir()
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	deps := &usageDeps{
		sprawlRoot: func() (string, error) { return root, nil },
		now:        func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		stdout:     out,
		stderr:     errOut,
		sleep:      func(time.Duration) {},
		followStop: func() bool { return true },
	}
	return deps, out, errOut, root
}

// writeNDJSON writes records to .sprawl/logs/usage/<agent>/<session>.ndjson.
func writeNDJSON(t *testing.T, root, agent, session string, recs []usage.Record) {
	t.Helper()
	dir := filepath.Join(root, ".sprawl", "logs", "usage", agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, session+".ndjson")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // test fixture
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(&r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

func mkRec(ts, agent, sess, model string, in, out, cr, cc int, cost float64) usage.Record {
	return usage.Record{
		Timestamp:                ts,
		AgentName:                agent,
		AgentType:                "claude",
		AgentFamily:              "anthropic",
		ParentName:               "weave",
		SessionID:                sess,
		Branch:                   "main",
		Model:                    model,
		InputTokens:              in,
		OutputTokens:             out,
		CacheReadInputTokens:     cr,
		CacheCreationInputTokens: cc,
		TotalCostUsd:             cost,
	}
}

// --- helpers ---

func TestParseDurationExt(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"1h", time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"2d12h", 0, true},
		{"notaduration", 0, true},
		{"d", 0, true},
	}
	for _, c := range cases {
		got, err := parseDurationExt(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDurationExt(%q) wanted error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDurationExt(%q) err = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseDurationExt(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in      string
		want    time.Time // zero == all time
		wantErr bool
	}{
		{"", time.Time{}, false},
		{"all", time.Time{}, false},
		{"ALL", time.Time{}, false},
		{"24h", now.Add(-24 * time.Hour), false},
		{"7d", now.Add(-7 * 24 * time.Hour), false},
		{"30d", now.Add(-30 * 24 * time.Hour), false},
		{"365d", now.Add(-365 * 24 * time.Hour), false},
		{"2026-05-01T10:00:00Z", time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC), false},
		{"notaduration", time.Time{}, true},
		{"2d12h", time.Time{}, true},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, now)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSince(%q) wanted error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSince(%q) err = %v", c.in, err)
			continue
		}
		if !got.Equal(c.want) {
			t.Errorf("parseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{999, "999"},
		{1000, "1,000"},
		{4521, "4,521"},
		{1234567, "1,234,567"},
		{-1234, "-1,234"},
	}
	for _, c := range cases {
		got := formatTokens(c.in)
		if got != c.want {
			t.Errorf("formatTokens(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatCost(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "$0.0000"},
		{0.0421, "$0.0421"},
		{1.5, "$1.5000"},
	}
	for _, c := range cases {
		got := formatCost(c.in)
		if got != c.want {
			t.Errorf("formatCost(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// --- registration ---

func TestUsageCmd_RegisteredOnRoot(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "usage" {
			found = true
			subs := map[string]bool{}
			for _, sc := range c.Commands() {
				subs[sc.Name()] = true
			}
			for _, want := range []string{"tail", "summary", "export"} {
				if !subs[want] {
					t.Errorf("usage missing subcommand %q (have %v)", want, subs)
				}
			}
			break
		}
	}
	if !found {
		t.Fatalf("usage command not registered on rootCmd")
	}
}

// --- tail ---

func TestRunUsageTail_DefaultLast50(t *testing.T) {
	deps, out, errOut, root := newUsageDeps(t)
	var aliceRecs, bobRecs []usage.Record
	// 30 alice records + 30 bob records, monotonically increasing timestamps
	// interleaved across agents.
	base := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		if i%2 == 0 {
			aliceRecs = append(aliceRecs, mkRec(ts, "alice", "s1", "sonnet", 100, 10, 0, 0, 0.01))
		} else {
			bobRecs = append(bobRecs, mkRec(ts, "bob", "s1", "sonnet", 100, 10, 0, 0, 0.01))
		}
	}
	writeNDJSON(t, root, "alice", "s1", aliceRecs)
	writeNDJSON(t, root, "bob", "s1", bobRecs)

	if err := runUsageTail(deps, "", false, 50, false); err != nil {
		t.Fatalf("runUsageTail: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("got %d lines, want 50", len(lines))
	}
	// Ascending order: first line must start with the earliest timestamp
	// (record index 10 = 00:10:00 — the earliest of the last 50).
	if !strings.HasPrefix(lines[0], "2026-05-01T00:10:00Z ") {
		t.Errorf("first line not earliest (expected RFC3339 prefix '2026-05-01T00:10:00Z '): %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "2026-05-01T00:59:00Z") {
		t.Errorf("last line not latest: %q", lines[len(lines)-1])
	}
	if errOut.Len() == 0 {
		t.Errorf("expected a next-action hint on stderr")
	}
}

func TestRunUsageTail_FilterAgent(t *testing.T) {
	deps, out, _, root := newUsageDeps(t)
	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0, 0, 0.01),
	})
	writeNDJSON(t, root, "bob", "s1", []usage.Record{
		mkRec("2026-05-01T11:00:00Z", "bob", "s1", "sonnet", 50, 5, 0, 0, 0.005),
	})

	if err := runUsageTail(deps, "alice", false, 50, false); err != nil {
		t.Fatalf("runUsageTail: %v", err)
	}
	got := out.String()
	// Per the documented tail format (see file header), column 2 is the
	// agent_name. Every output line must have column 2 == "alice".
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("expected at least one tail line, got %q", got)
	}
	for i, ln := range lines {
		cols := strings.Fields(ln)
		if len(cols) < 2 {
			t.Errorf("line %d: not enough columns: %q", i, ln)
			continue
		}
		if cols[1] != "alice" {
			t.Errorf("line %d: agent_name column = %q, want %q (line=%q)", i, cols[1], "alice", ln)
		}
	}
}

func TestRunUsageTail_Follow_AppendsLines(t *testing.T) {
	root := t.TempDir()
	out := &syncBuf{}
	errOut := &bytes.Buffer{}

	// Use a stop function that flips to true once the appended record
	// has been observed on stdout.
	stopOnSecond := func() bool {
		return strings.Contains(out.String(), "2026-05-01T10:05:00Z")
	}

	deps := &usageDeps{
		sprawlRoot: func() (string, error) { return root, nil },
		now:        func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		stdout:     out,
		stderr:     errOut,
		sleep:      func(time.Duration) {},
		followStop: stopOnSecond,
	}

	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 100, 10, 0, 0, 0.01),
	})

	done := make(chan error, 1)
	go func() {
		done <- runUsageTail(deps, "", true, 50, false)
	}()

	// Give the tailer a beat to print the existing record, then append.
	time.Sleep(20 * time.Millisecond)
	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:05:00Z", "alice", "s1", "sonnet", 200, 20, 0, 0, 0.02),
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runUsageTail follow err: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("follow timed out; appended record never observed")
	}

	got := out.String()
	if !strings.Contains(got, "2026-05-01T10:00:00Z") {
		t.Errorf("missing initial record: %q", got)
	}
	if !strings.Contains(got, "2026-05-01T10:05:00Z") {
		t.Errorf("missing appended record: %q", got)
	}
}

func TestRunUsageTail_EmptyDir_FriendlyMessage(t *testing.T) {
	deps, out, errOut, _ := newUsageDeps(t)
	if err := runUsageTail(deps, "", false, 50, false); err != nil {
		t.Fatalf("runUsageTail empty: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no stdout, got %q", out.String())
	}
	msg := errOut.String()
	if !strings.Contains(strings.ToLower(msg), "no usage data") {
		t.Errorf("expected 'no usage data' message, got %q", msg)
	}
	if !strings.Contains(msg, "QUM-368") {
		t.Errorf("expected QUM-368 reference, got %q", msg)
	}
}

// --- summary (golden) ---

// seedSummaryFixture writes a small, deterministic dataset used by all the
// summary golden tests. 2 agents, 2 sessions for alice, 2 distinct models,
// 2 distinct UTC days.
func seedSummaryFixture(t *testing.T, root string) {
	t.Helper()
	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 1000, 100, 50, 20, 0.0100),
		mkRec("2026-05-01T11:00:00Z", "alice", "s1", "sonnet", 2000, 200, 60, 30, 0.0200),
	})
	writeNDJSON(t, root, "alice", "s2", []usage.Record{
		mkRec("2026-05-02T10:00:00Z", "alice", "s2", "opus", 500, 50, 10, 5, 0.0500),
	})
	writeNDJSON(t, root, "bob", "s1", []usage.Record{
		mkRec("2026-05-02T12:00:00Z", "bob", "s1", "sonnet", 300, 30, 5, 2, 0.0030),
	})
}

func TestRunUsageSummary_DefaultByAgent_GoldenMatch(t *testing.T) {
	deps, out, errOut, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "tokens", "agent", "", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	assertGolden(t, "summary_default.golden", out.String())
	if errOut.Len() == 0 {
		t.Error("expected next-action hint on stderr")
	}
}

func TestRunUsageSummary_ByCost_GoldenMatch(t *testing.T) {
	deps, out, errOut, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "cost", "agent", "", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	got := out.String()
	// The subscription-credit footer must be on stderr (pipe-safe stdout).
	// The golden file contains ONLY the table (no footer).
	if !strings.Contains(errOut.String(), "subscription") {
		t.Errorf("--by cost should emit subscription footer on stderr; got stderr=%q", errOut.String())
	}
	if strings.Contains(got, "subscription") {
		t.Errorf("stdout must not contain 'subscription' footer (must be on stderr); got %q", got)
	}
	assertGolden(t, "summary_by_cost.golden", got)
}

func TestRunUsageSummary_ByAll_GoldenMatch(t *testing.T) {
	deps, out, errOut, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "all", "agent", "", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	got := out.String()
	if !strings.Contains(errOut.String(), "subscription") {
		t.Errorf("--by all should emit subscription footer on stderr; got stderr=%q", errOut.String())
	}
	if strings.Contains(got, "subscription") {
		t.Errorf("stdout must not contain 'subscription' footer (must be on stderr); got %q", got)
	}
	assertGolden(t, "summary_by_all.golden", got)
}

// TestRunUsageSummary_Quiet_SuppressesFooter verifies that when quiet=true,
// the next-action hint and subscription-credit footer are NOT written to
// stderr. stdout still gets the data table so the command remains useful
// in pipelines.
func TestRunUsageSummary_Quiet_SuppressesFooter(t *testing.T) {
	deps, out, errOut, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "cost", "agent", "", "", true); err != nil {
		t.Fatalf("runUsageSummary quiet: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("quiet should still emit data on stdout; got empty")
	}
	if errOut.Len() != 0 {
		t.Errorf("quiet should suppress stderr (hint + subscription footer); got %q", errOut.String())
	}
}

func TestRunUsageSummary_GroupModel_Golden(t *testing.T) {
	deps, out, _, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "tokens", "model", "", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	assertGolden(t, "summary_group_model.golden", out.String())
}

func TestRunUsageSummary_GroupSession_Golden(t *testing.T) {
	deps, out, _, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "tokens", "session", "", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	assertGolden(t, "summary_group_session.golden", out.String())
}

func TestRunUsageSummary_GroupDay_Golden(t *testing.T) {
	deps, out, _, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageSummary(deps, "tokens", "day", "", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	assertGolden(t, "summary_group_day.golden", out.String())
}

func TestRunUsageSummary_SinceFilter(t *testing.T) {
	// Fixed now = 2026-05-01T12:00:00Z (from newUsageDeps). Records at t-2h and
	// t-30m → since=1h excludes the t-2h record.
	deps, out, _, root := newUsageDeps(t)
	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 999, 99, 0, 0, 0.99), // 2h ago
		mkRec("2026-05-01T11:30:00Z", "alice", "s1", "sonnet", 100, 10, 0, 0, 0.01), // 30m ago
	})

	if err := runUsageSummary(deps, "tokens", "agent", "1h", "", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	got := out.String()
	// The 999/99 record must have been excluded by --since 1h.
	if strings.Contains(got, "999") || strings.Contains(got, "1,098") {
		t.Errorf("expected since=1h to exclude t-2h record, got %q", got)
	}
	if !strings.Contains(got, "100") {
		t.Errorf("expected since=1h to include t-30m record, got %q", got)
	}
}

func TestRunUsageSummary_UntilFilter(t *testing.T) {
	// Fixed now = 2026-05-01T12:00:00Z (from newUsageDeps). Records at t-2h,
	// t-30m, and t now → until=1h keeps only the t-2h record (ts <= now-1h).
	deps, out, _, root := newUsageDeps(t)
	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 999, 99, 0, 0, 0.99), // 2h ago — KEEP
		mkRec("2026-05-01T11:30:00Z", "alice", "s1", "sonnet", 100, 10, 0, 0, 0.01), // 30m ago — DROP
		mkRec("2026-05-01T12:00:00Z", "alice", "s1", "sonnet", 7, 1, 0, 0, 0.001),   // now    — DROP
	})

	if err := runUsageSummary(deps, "tokens", "agent", "", "1h", false); err != nil {
		t.Fatalf("runUsageSummary: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "999") {
		t.Errorf("expected until=1h to include t-2h record (999), got %q", got)
	}
	if strings.Contains(got, "100") || strings.Contains(got, "1,106") {
		t.Errorf("expected until=1h to exclude t-30m record (100), got %q", got)
	}
	if strings.Contains(got, "\t7\t") {
		t.Errorf("expected until=1h to exclude t-now record (7), got %q", got)
	}
}

func TestRunUsageTail_LastZero_Errors(t *testing.T) {
	deps, _, _, root := newUsageDeps(t)
	writeNDJSON(t, root, "alice", "s1", []usage.Record{
		mkRec("2026-05-01T10:00:00Z", "alice", "s1", "sonnet", 1, 1, 0, 0, 0.01),
	})
	err := runUsageTail(deps, "", false, 0, false)
	if err == nil {
		t.Fatalf("--last=0 should error, got nil")
	}
	if !strings.Contains(err.Error(), "must be positive") {
		t.Errorf("error should explain --last must be positive, got %v", err)
	}
}

func TestRunUsageSummary_EmptyDir_FriendlyMessage(t *testing.T) {
	deps, out, errOut, _ := newUsageDeps(t)
	if err := runUsageSummary(deps, "tokens", "agent", "", "", false); err != nil {
		t.Fatalf("runUsageSummary empty: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("expected no stdout, got %q", out.String())
	}
	msg := errOut.String()
	if !strings.Contains(strings.ToLower(msg), "no usage data") {
		t.Errorf("expected 'no usage data' message, got %q", msg)
	}
	if !strings.Contains(msg, "QUM-368") {
		t.Errorf("expected QUM-368 reference, got %q", msg)
	}
}

// --- export ---

func TestRunUsageExport_CSV_Golden(t *testing.T) {
	deps, out, errOut, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageExport(deps, "csv", "", "", false); err != nil {
		t.Fatalf("runUsageExport: %v", err)
	}
	got := out.String()
	wantHeader := "timestamp,agent_name,agent_type,agent_family,parent_name,session_id,branch,model,input_tokens,output_tokens,cache_read_input_tokens,cache_creation_input_tokens,total_cost_usd"
	firstLine := strings.SplitN(got, "\n", 2)[0]
	if firstLine != wantHeader {
		t.Errorf("CSV header mismatch:\n got: %q\nwant: %q", firstLine, wantHeader)
	}
	assertGolden(t, "export.csv.golden", got)
	if errOut.Len() == 0 {
		t.Error("expected next-action hint on stderr")
	}
	// Parse CSV and verify timestamp column is monotonically non-decreasing.
	r := csv.NewReader(strings.NewReader(got))
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv parse: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected header + data rows, got %d rows", len(rows))
	}
	// rows[0] is the header; find timestamp column index.
	tsIdx := -1
	for i, h := range rows[0] {
		if h == "timestamp" {
			tsIdx = i
			break
		}
	}
	if tsIdx < 0 {
		t.Fatalf("timestamp column not found in header: %v", rows[0])
	}
	prev := ""
	for i, row := range rows[1:] {
		ts := row[tsIdx]
		if prev != "" && ts < prev {
			t.Errorf("row %d: timestamp %q < prev %q (not monotonically non-decreasing)", i, ts, prev)
		}
		prev = ts
	}
}

func TestRunUsageExport_JSON_NDJSON_PassThrough(t *testing.T) {
	deps, out, _, root := newUsageDeps(t)
	seedSummaryFixture(t, root)

	if err := runUsageExport(deps, "json", "", "", false); err != nil {
		t.Fatalf("runUsageExport: %v", err)
	}
	got := out.String()
	// Each non-empty line must decode back into a Record.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4 (one per record): %q", len(lines), got)
	}
	prev := ""
	for i, ln := range lines {
		var r usage.Record
		if err := json.Unmarshal([]byte(ln), &r); err != nil {
			t.Errorf("line %d invalid JSON: %v (%q)", i, err, ln)
			continue
		}
		if r.Timestamp == "" {
			t.Errorf("line %d missing timestamp", i)
		}
		if prev != "" && r.Timestamp < prev {
			t.Errorf("lines not sorted ascending: prev=%q this=%q", prev, r.Timestamp)
		}
		prev = r.Timestamp
	}
	assertGolden(t, "export.ndjson.golden", got)

	// Byte-equality (modulo whitespace): each output line's JSON map must
	// deep-equal the on-disk record's JSON map. Catches a buggy re-encode
	// that drops/renames fields.
	onDisk := map[string]map[string]any{} // timestamp -> parsed map
	usageDir := filepath.Join(root, ".sprawl", "logs", "usage")
	agents, err := os.ReadDir(usageDir)
	if err != nil {
		t.Fatalf("read usage dir: %v", err)
	}
	for _, ag := range agents {
		sessions, err := os.ReadDir(filepath.Join(usageDir, ag.Name()))
		if err != nil {
			t.Fatalf("read agent dir: %v", err)
		}
		for _, sess := range sessions {
			b, err := os.ReadFile(filepath.Join(usageDir, ag.Name(), sess.Name())) //nolint:gosec // test fixture
			if err != nil {
				t.Fatalf("read session file: %v", err)
			}
			for _, dl := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
				if dl == "" {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal([]byte(dl), &m); err != nil {
					t.Fatalf("on-disk decode: %v", err)
				}
				ts, _ := m["timestamp"].(string)
				onDisk[ts] = m
			}
		}
	}
	for i, ln := range lines {
		var outMap map[string]any
		if err := json.Unmarshal([]byte(ln), &outMap); err != nil {
			continue
		}
		ts, _ := outMap["timestamp"].(string)
		diskMap, ok := onDisk[ts]
		if !ok {
			t.Errorf("output line %d (ts=%q) has no matching on-disk record", i, ts)
			continue
		}
		if !reflect.DeepEqual(outMap, diskMap) {
			t.Errorf("output line %d (ts=%q) does not deep-equal on-disk record\n out: %#v\ndisk: %#v",
				i, ts, outMap, diskMap)
		}
	}
}

// compile-time guard: ensure usageDeps satisfies the minimal io.Writer wiring
// shape the tests rely on.
var _ io.Writer = (*bytes.Buffer)(nil)
