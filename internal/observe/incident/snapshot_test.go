package incident_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/observe/incident"
	"github.com/dmotles/sprawl/internal/observe/sigdump"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// fakeFDSource is a deterministic FDSource for unit tests.
type fakeFDSource struct {
	entries []sigdump.FDEntry
	err     error
}

func (f *fakeFDSource) Snapshot() ([]sigdump.FDEntry, error) { return f.entries, f.err }

// fixedNow returns a stable injectable clock.
func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// dirTimestamp formats the expected incident-bundle directory timestamp.
func dirTimestamp(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// writeFile is a small helper for fixture creation.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for fixture %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

// canonicalRunner returns a Runner stub keyed by command name.
func canonicalRunner(out map[string][]byte, errMap map[string]error) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := name
		if b, ok := out[key]; ok {
			return b, errMap[key]
		}
		return nil, errMap[key]
	}
}

func newSnapshotterWithFixtures(t *testing.T, now time.Time) (*incident.Snapshotter, string) {
	t.Helper()
	root := t.TempDir()
	// Fixture: mcp-calls.jsonl with 5 lines
	mcpLog := filepath.Join(root, ".sprawl", "logs", "mcp-calls.jsonl")
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, `{"seq":%d,"tool":"x"}`+"\n", i)
	}
	writeFile(t, mcpLog, b.String())

	// Fixture: activity.ndjson for agent "foo" — 2 recent + 1 old
	activityRoot := filepath.Join(root, ".sprawl", "agents")
	recentA := now.Add(-10 * time.Second).UTC().Format(time.RFC3339Nano)
	recentB := now.Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	old := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	fooActivity := filepath.Join(activityRoot, "foo", "activity.ndjson")
	writeFile(t, fooActivity, fmt.Sprintf(`{"ts":%q,"kind":"a"}`+"\n"+`{"ts":%q,"kind":"b"}`+"\n"+`{"ts":%q,"kind":"c"}`+"\n", recentA, recentB, old))

	s := &incident.Snapshotter{
		SprawlRoot: root,
		Now:        fixedNow(now),
		FDSource: &fakeFDSource{entries: []sigdump.FDEntry{
			{FD: 0, Target: "/dev/null"},
			{FD: 1, Target: "pipe:[42]"},
		}},
		StatusFn: func(ctx context.Context) ([]supervisor.AgentInfo, error) {
			return []supervisor.AgentInfo{{Name: "weave"}, {Name: "axis"}}, nil
		},
		WeavePid: os.Getpid(),
		Runner: canonicalRunner(
			map[string][]byte{
				"ps":   []byte("USER PID ...\nps auxf row 1\nps auxf row 2\n"),
				"free": []byte("              total        used        free\nMem:           16Gi         8Gi         8Gi\n"),
			},
			nil,
		),
		MCPLogPath:   mcpLog,
		ActivityRoot: activityRoot,
		TailLines:    0,
	}
	return s, root
}

func TestCapture_WritesAllArtifacts(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 30, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	wantPrefix := filepath.Join(root, ".sprawl", "incidents", dirTimestamp(now)+"-tui-snapshot")
	if dir != wantPrefix {
		t.Errorf("dir = %q, want %q", dir, wantPrefix)
	}

	// README.md exists and mentions timestamp.
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("README.md: %v", err)
	}
	if !strings.Contains(string(readme), dirTimestamp(now)) {
		t.Errorf("README missing timestamp %q; got:\n%s", dirTimestamp(now), readme)
	}

	// sprawl-status.json parses as array of 2.
	statusBytes, err := os.ReadFile(filepath.Join(dir, "sprawl-status.json"))
	if err != nil {
		t.Fatalf("sprawl-status.json: %v", err)
	}
	var agents []supervisor.AgentInfo
	if err := json.Unmarshal(statusBytes, &agents); err != nil {
		t.Fatalf("status JSON parse: %v\n%s", err, statusBytes)
	}
	if len(agents) != 2 {
		t.Errorf("status JSON len=%d, want 2", len(agents))
	}

	// goroutines-* and fds-* present
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var gFound, fFound bool
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "goroutines-") && strings.HasSuffix(n, ".txt") {
			gFound = true
		}
		if strings.HasPrefix(n, "fds-") && strings.HasSuffix(n, ".txt") {
			fFound = true
		}
	}
	if !gFound {
		t.Error("missing goroutines-*.txt")
	}
	if !fFound {
		t.Error("missing fds-*.txt")
	}

	// ps-auxf.txt contains canned output.
	psBytes, err := os.ReadFile(filepath.Join(dir, "ps-auxf.txt"))
	if err != nil {
		t.Fatalf("ps-auxf.txt: %v", err)
	}
	if !strings.Contains(string(psBytes), "ps auxf row 1") {
		t.Errorf("ps-auxf.txt missing row; got:\n%s", psBytes)
	}

	// proc-status-<pid>.txt was created.
	procPath := filepath.Join(dir, fmt.Sprintf("proc-status-%d.txt", os.Getpid()))
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("proc-status file missing: %v", err)
	}

	// mcp-calls-tail.jsonl contains all 5 lines.
	tailBytes, err := os.ReadFile(filepath.Join(dir, "mcp-calls-tail.jsonl"))
	if err != nil {
		t.Fatalf("mcp-calls-tail.jsonl: %v", err)
	}
	got := strings.Count(strings.TrimRight(string(tailBytes), "\n"), "\n") + 1
	if strings.TrimSpace(string(tailBytes)) == "" {
		got = 0
	}
	if got != 5 {
		t.Errorf("mcp-calls-tail.jsonl line count=%d, want 5; raw:\n%s", got, tailBytes)
	}

	// activity-rates.txt shows foo<TAB>2
	actBytes, err := os.ReadFile(filepath.Join(dir, "activity-rates.txt"))
	if err != nil {
		t.Fatalf("activity-rates.txt: %v", err)
	}
	if !strings.Contains(string(actBytes), "foo\t2") {
		t.Errorf("activity-rates.txt missing 'foo\\t2'; got:\n%s", actBytes)
	}

	// mem-load.txt has both free output and loadavg header.
	memBytes, err := os.ReadFile(filepath.Join(dir, "mem-load.txt"))
	if err != nil {
		t.Fatalf("mem-load.txt: %v", err)
	}
	mem := string(memBytes)
	if !strings.Contains(mem, "Mem:") {
		t.Errorf("mem-load.txt missing free -h output; got:\n%s", mem)
	}
	if !strings.Contains(mem, "/proc/loadavg") {
		t.Errorf("mem-load.txt missing /proc/loadavg header; got:\n%s", mem)
	}
}

func TestCapture_TailLineLimit(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	// Overwrite mcp log with 25 lines.
	mcpLog := filepath.Join(root, ".sprawl", "logs", "mcp-calls.jsonl")
	var b strings.Builder
	for i := 1; i <= 25; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	writeFile(t, mcpLog, b.String())
	s.TailLines = 10

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	tail, err := os.ReadFile(filepath.Join(dir, "mcp-calls-tail.jsonl"))
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(tail), "\n"), "\n")
	if len(lines) != 10 {
		t.Fatalf("tail line count=%d, want 10; raw:\n%s", len(lines), tail)
	}
	if lines[0] != "line16" {
		t.Errorf("first line=%q, want line16", lines[0])
	}
	if lines[9] != "line25" {
		t.Errorf("last line=%q, want line25", lines[9])
	}
}

func TestCapture_DefaultTailIs10k(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.TailLines = 0 // default

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	tail, err := os.ReadFile(filepath.Join(dir, "mcp-calls-tail.jsonl"))
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	// Fixture has 5 lines; default cap is 10000; all 5 should be present.
	lines := strings.Split(strings.TrimRight(string(tail), "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("default tail produced %d lines, want 5 (all fixture lines); raw:\n%s", len(lines), tail)
	}
}

func TestCapture_TwoInvocationsDifferentDirs(t *testing.T) {
	now1 := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now1)

	dir1, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture #1: %v", err)
	}

	now2 := now1.Add(1 * time.Second)
	s.Now = fixedNow(now2)
	dir2, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture #2: %v", err)
	}
	if dir1 == dir2 {
		t.Fatalf("expected distinct dirs, got identical %q", dir1)
	}
	for _, d := range []string{dir1, dir2} {
		if _, err := os.Stat(filepath.Join(d, "README.md")); err != nil {
			t.Errorf("dir %s missing README.md: %v", d, err)
		}
	}
}

func TestCapture_StatusFnError_RecordsAndContinues(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.StatusFn = func(ctx context.Context) ([]supervisor.AgentInfo, error) {
		return nil, errors.New("status broken")
	}

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture should succeed despite status err: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	body := string(readme)
	if !strings.Contains(body, "## Errors") {
		t.Errorf("README missing '## Errors' section; got:\n%s", body)
	}
	if !strings.Contains(strings.ToLower(body), "status") {
		t.Errorf("README errors section should mention 'status'; got:\n%s", body)
	}
	// Other artifacts still produced.
	if _, err := os.ReadFile(filepath.Join(dir, "ps-auxf.txt")); err != nil {
		t.Errorf("ps-auxf.txt should still be written: %v", err)
	}
}

func TestCapture_FDSourceError_RecordsAndContinues(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.FDSource = &fakeFDSource{err: errors.New("fd broken")}

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture should succeed despite fd err: %v", err)
	}
	// goroutines-*.txt should still exist (sigdump writes it before fd).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var gFound bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "goroutines-") {
			gFound = true
			break
		}
	}
	if !gFound {
		t.Error("goroutines-*.txt missing — should still be written when fd snapshot errors")
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	if !strings.Contains(string(readme), "fd broken") && !strings.Contains(strings.ToLower(string(readme)), "fd") {
		t.Errorf("README errors should mention fd snapshot failure; got:\n%s", readme)
	}
	// Status artifact still written.
	if _, err := os.Stat(filepath.Join(dir, "sprawl-status.json")); err != nil {
		t.Errorf("status JSON should still be written: %v", err)
	}
}

func TestCapture_RunnerError_RecordsAndContinues(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.Runner = canonicalRunner(
		map[string][]byte{
			"free": []byte("free -h ok output\n"),
		},
		map[string]error{
			"ps": errors.New("ps broken"),
		},
	)

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(readme)), "ps") {
		t.Errorf("README errors should mention ps failure; got:\n%s", readme)
	}
	mem, err := os.ReadFile(filepath.Join(dir, "mem-load.txt"))
	if err != nil {
		t.Fatalf("mem-load: %v", err)
	}
	if !strings.Contains(string(mem), "free -h ok output") {
		t.Errorf("mem-load.txt should still contain free output despite ps failure; got:\n%s", mem)
	}
}

func TestCapture_MissingMCPLog_NoError(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	s.MCPLogPath = filepath.Join(root, ".sprawl", "logs", "does-not-exist.jsonl")

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(readme)), "mcp") {
		t.Errorf("README errors should mention missing mcp log; got:\n%s", readme)
	}
	// Capture succeeded — that's the contract.
}

func TestCapture_NoActivityRoot_NoError(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	s.ActivityRoot = filepath.Join(root, ".sprawl", "no-such-agents-dir")

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(readme)), "activity") {
		t.Errorf("README errors should mention missing activity root; got:\n%s", readme)
	}
}

func TestCapture_ActivityRate60sWindow(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	// Replace foo activity with exactly 3 lines: now-10s, now-30s, now-2m.
	activityRoot := filepath.Join(root, ".sprawl", "agents")
	s.ActivityRoot = activityRoot
	ts1 := now.Add(-10 * time.Second).UTC().Format(time.RFC3339Nano)
	ts2 := now.Add(-30 * time.Second).UTC().Format(time.RFC3339Nano)
	ts3 := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	fixture := fmt.Sprintf(`{"ts":%q}`+"\n"+`{"ts":%q}`+"\n"+`{"ts":%q}`+"\n", ts1, ts2, ts3)
	writeFile(t, filepath.Join(activityRoot, "foo", "activity.ndjson"), fixture)

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	act, err := os.ReadFile(filepath.Join(dir, "activity-rates.txt"))
	if err != nil {
		t.Fatalf("activity-rates: %v", err)
	}
	if !strings.Contains(string(act), "foo\t2") {
		t.Errorf("expected 'foo\\t2' in activity-rates.txt; got:\n%s", act)
	}
}
