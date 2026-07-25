package incident_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
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

// cpuProfilingAvailable reports whether the process-global CPU profiler is
// free. runtime/pprof allows exactly one active profile per process, so
// `go test -cpuprofile` or a live --pprof scrape (QUM-678) makes the real
// path unavailable and the profile tests must skip.
//
// This probe is TOCTOU — it releases the profiler before the caller acquires
// it. Safe only because no test in this file calls t.Parallel(); do not add
// t.Parallel() to a test that profiles for real.
func cpuProfilingAvailable() bool {
	if err := pprof.StartCPUProfile(io.Discard); err != nil {
		return false
	}
	pprof.StopCPUProfile()
	return true
}

// assertGzipProfile fails unless path holds a non-empty gzip stream that
// decompresses to non-empty bytes. Both pprof profiles are gzipped protobuf;
// this is the strongest available check without adding github.com/google/pprof.
func assertGzipProfile(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile %s: %v", path, err)
	}
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		t.Fatalf("profile %s is not gzip (len=%d)", path, len(raw))
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader %s: %v", path, err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	if len(decoded) == 0 {
		t.Fatalf("profile %s decompressed to zero bytes", path)
	}
}

// findMatching returns the names of dir entries with the given prefix.
func findMatching(t *testing.T, dir, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// assertNoProfileErrors fails if the bundle's "## Errors" section mentions
// the CPU or heap profile. Scoped rather than asserting an empty error set:
// Capture unconditionally reads /proc, which legitimately fails off Linux.
func assertNoProfileErrors(t *testing.T, dir string) {
	t.Helper()
	errs := strings.ToLower(readmeErrors(t, dir))
	for _, bad := range []string{"cpu", "heap"} {
		if strings.Contains(errs, bad) {
			t.Errorf("README errors mention %q; want no profile errors. Got:%s", bad, errs)
		}
	}
}

// binaryInfoField returns the value of a line-anchored "<key>: " field in
// binary.txt. Anchoring on line start matters: an unanchored search for
// "version: " would also match inside another field's value or key.
func binaryInfoField(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "binary.txt"))
	if err != nil {
		t.Fatalf("binary.txt: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, key+": "); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

// readmeErrors returns the "## Errors" section body of a bundle README.
func readmeErrors(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	_, after, found := strings.Cut(string(b), "## Errors")
	if !found {
		t.Fatalf("README has no '## Errors' section:\n%s", b)
	}
	// Bound to this section — later sections (e.g. the artifact legend, which
	// names cpu-*.pprof) must not leak into error assertions.
	if body, _, cut := strings.Cut(after, "\n## "); cut {
		return body
	}
	return after
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
		// Negative disables the CPU-profile window so the bulk of the
		// suite does not pay DefaultProfileDuration each. Profile tests
		// opt back in explicitly.
		ProfileDuration: -1,
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

	// heap-*.pprof and binary.txt are unconditional (the fixture disables
	// only the CPU window).
	if got := findMatching(t, dir, "heap-"); len(got) != 1 {
		t.Errorf("heap profile files = %v, want exactly one", got)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "binary.txt")); sErr != nil {
		t.Errorf("binary.txt missing: %v", sErr)
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

func TestDefaultProfileDurationIs5s(t *testing.T) {
	if incident.DefaultProfileDuration != 5*time.Second {
		t.Errorf("DefaultProfileDuration = %v, want 5s", incident.DefaultProfileDuration)
	}
}

func TestCapture_WritesCPUAndHeapProfiles(t *testing.T) {
	if !cpuProfilingAvailable() {
		t.Skip("process-global CPU profiler already in use")
	}
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 50 * time.Millisecond

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	wantCPU := "cpu-" + dirTimestamp(now) + ".pprof"
	wantHeap := "heap-" + dirTimestamp(now) + ".pprof"
	if got := findMatching(t, dir, "cpu-"); len(got) != 1 || got[0] != wantCPU {
		t.Fatalf("cpu profile files = %v, want exactly [%s]", got, wantCPU)
	}
	if got := findMatching(t, dir, "heap-"); len(got) != 1 || got[0] != wantHeap {
		t.Fatalf("heap profile files = %v, want exactly [%s]", got, wantHeap)
	}
	assertGzipProfile(t, filepath.Join(dir, wantCPU))
	assertGzipProfile(t, filepath.Join(dir, wantHeap))
	assertNoProfileErrors(t, dir)
}

// TestCapture_ZeroProfileDurationEnablesCPUWindow pins the zero-value
// contract: the production call site (cmd/enter.go) leaves ProfileDuration
// unset, so 0 must mean DefaultProfileDuration — never "disabled".
// TestDefaultProfileDurationIs5s pins the constant's value; this pins the
// mapping. The window is cut short from inside the seam so the test does not
// pay the full default duration.
func TestCapture_ZeroProfileDurationEnablesCPUWindow(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 0

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var startCalled, stopCalled bool
	s.StartCPUProfile = func(w io.Writer) error {
		startCalled = true
		_, _ = w.Write([]byte("fake-cpu-profile"))
		// Cancel only once the window is actually open — cancelling on a
		// wall-clock timer would race the other collectors.
		cancel()
		return nil
	}
	s.StopCPUProfile = func() { stopCalled = true }

	start := time.Now()
	dir, err := s.Capture(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !startCalled {
		t.Fatal("ProfileDuration=0 must enable CPU profiling (StartCPUProfile not called)")
	}
	if !stopCalled {
		t.Error("StopCPUProfile must be called after a successful start")
	}
	if elapsed > incident.DefaultProfileDuration/2 {
		t.Errorf("Capture took %v; ctx cancellation should have cut the window short", elapsed)
	}
	cpuFiles := findMatching(t, dir, "cpu-")
	if len(cpuFiles) != 1 {
		t.Fatalf("cpu profile files = %v, want exactly one", cpuFiles)
	}
	// The seam must be handed the bundle file itself, not a throwaway buffer.
	body, rErr := os.ReadFile(filepath.Join(dir, cpuFiles[0]))
	if rErr != nil {
		t.Fatalf("read cpu profile: %v", rErr)
	}
	if string(body) != "fake-cpu-profile" {
		t.Errorf("cpu profile contents = %q, want the bytes written to the seam's writer", body)
	}
}

func TestCapture_NegativeProfileDurationDisablesCPUOnly(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = -1

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got := findMatching(t, dir, "cpu-"); len(got) != 0 {
		t.Errorf("cpu profile should be skipped, got %v", got)
	}
	// Heap is unconditional — it does not depend on the CPU window.
	if got := findMatching(t, dir, "heap-"); len(got) != 1 {
		t.Fatalf("heap profile files = %v, want exactly one", got)
	}
	assertGzipProfile(t, filepath.Join(dir, "heap-"+dirTimestamp(now)+".pprof"))
	if errs := readmeErrors(t, dir); strings.Contains(strings.ToLower(errs), "cpu") {
		t.Errorf("disabled CPU profiling must not record an error; got:%s", errs)
	}
}

// TestCapture_ReadmeListsProfilesAndPprofHint runs with the CPU window
// disabled: the README table is a fixed legend of what a bundle can contain,
// not a manifest of this bundle's files.
func TestCapture_ReadmeListsProfilesAndPprofHint(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("readme: %v", err)
	}
	body := string(readme)
	for _, want := range []string{"cpu-*.pprof", "heap-*.pprof", "binary.txt", "go tool pprof"} {
		if !strings.Contains(body, want) {
			t.Errorf("README missing %q; got:\n%s", want, body)
		}
	}
}

func TestCapture_CPUProfileStartError_RecordsAndContinues(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second
	s.StartCPUProfile = func(io.Writer) error { return errors.New("cpu busy") }
	s.StopCPUProfile = func() { t.Error("StopCPUProfile must not run when start failed") }

	start := time.Now()
	dir, err := s.Capture(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Capture should succeed despite cpu profile error: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Capture waited %v after a failed CPU start; must not wait out the window", elapsed)
	}
	if errs := readmeErrors(t, dir); !strings.Contains(errs, "cpu busy") {
		t.Errorf("README errors should mention 'cpu busy'; got:%s", errs)
	}
	if got := findMatching(t, dir, "cpu-"); len(got) != 0 {
		t.Errorf("failed CPU start must not leave a profile file behind, got %v", got)
	}
	if got := findMatching(t, dir, "heap-"); len(got) != 1 {
		t.Errorf("heap profile should still be written, got %v", got)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "ps-auxf.txt")); sErr != nil {
		t.Errorf("ps-auxf.txt should still be written: %v", sErr)
	}
}

func TestCapture_HeapProfileError_RecordsAndContinues(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.WriteHeapProfile = func(io.Writer) error { return errors.New("heap boom") }

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture should succeed despite heap profile error: %v", err)
	}
	if errs := readmeErrors(t, dir); !strings.Contains(errs, "heap boom") {
		t.Errorf("README errors should mention 'heap boom'; got:%s", errs)
	}
	if got := findMatching(t, dir, "heap-"); len(got) != 0 {
		t.Errorf("failed heap write must not leave a truncated profile behind, got %v", got)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "ps-auxf.txt")); sErr != nil {
		t.Errorf("ps-auxf.txt should still be written: %v", sErr)
	}
}

func TestCapture_PreCancelledContext_SkipsCPUWindow(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	dir, err := s.Capture(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Capture should succeed with a cancelled ctx: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Capture took %v with a pre-cancelled ctx; must not wait out the window", elapsed)
	}
	errs := readmeErrors(t, dir)
	if !strings.Contains(errs, context.Canceled.Error()) {
		t.Errorf("README errors should mention ctx cancellation; got:%s", errs)
	}
	if got := findMatching(t, dir, "cpu-"); len(got) != 0 {
		t.Errorf("cancelled ctx must not start a CPU profile, got %v", got)
	}
	// Cancellation suppresses the CPU window only — the instantaneous heap
	// profile is still worth having.
	if got := findMatching(t, dir, "heap-"); len(got) != 1 {
		t.Errorf("heap profile should still be written, got %v", got)
	}
}

func TestCapture_ContextCancelledMidWindow_KeepsPartialProfile(t *testing.T) {
	if !cpuProfilingAvailable() {
		t.Skip("process-global CPU profiler already in use")
	}
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel from inside the window rather than on a wall-clock timer: the
	// window opens only after the other collectors have run, so a timer
	// started before Capture could fire first and take the pre-cancelled
	// branch instead of the mid-window one this test exercises.
	s.StartCPUProfile = func(w io.Writer) error {
		if err := pprof.StartCPUProfile(w); err != nil {
			t.Fatalf("pprof.StartCPUProfile: %v", err)
		}
		time.AfterFunc(50*time.Millisecond, cancel)
		return nil
	}

	start := time.Now()
	dir, err := s.Capture(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Capture should succeed when ctx is cancelled mid-window: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Capture took %v; mid-window cancellation must cut the window short", elapsed)
	}
	// A short profile is still valid and useful — keep it.
	cpuPath := filepath.Join(dir, "cpu-"+dirTimestamp(now)+".pprof")
	if _, sErr := os.Stat(cpuPath); sErr != nil {
		t.Fatalf("partial CPU profile should be kept: %v", sErr)
	}
	assertGzipProfile(t, cpuPath)
	if errs := readmeErrors(t, dir); !strings.Contains(errs, context.Canceled.Error()) {
		t.Errorf("README errors should mention ctx cancellation; got:%s", errs)
	}
}

func TestCapture_WritesBinaryInfo(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	exe, exeErr := os.Executable()
	if exeErr != nil {
		t.Fatalf("os.Executable: %v", exeErr)
	}
	if got, ok := binaryInfoField(t, dir, "executable"); !ok || got != exe {
		t.Errorf("binary.txt executable = %q (found=%v), want %q", got, ok, exe)
	}
	if got, ok := binaryInfoField(t, dir, "go"); !ok || got != runtime.Version() {
		t.Errorf("binary.txt go = %q (found=%v), want %q", got, ok, runtime.Version())
	}
	// Version must always be a non-empty token: under `go test`,
	// debug.ReadBuildInfo().Main.Version is empty, so a placeholder is required.
	got, ok := binaryInfoField(t, dir, "version")
	if !ok {
		t.Fatal("binary.txt missing a line-anchored 'version: ' field")
	}
	if got == "" {
		t.Error("binary.txt 'version' field is empty; want a placeholder when build info has none")
	}
}

func TestCapture_VersionFieldOverridesBuildInfo(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.Version = "v9.9.9-test"

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if got, ok := binaryInfoField(t, dir, "version"); !ok || got != "v9.9.9-test" {
		t.Errorf("binary.txt version = %q (found=%v), want injected %q", got, ok, "v9.9.9-test")
	}
}

// TestCapture_SameSecondCapture_DoesNotClobberProfiles pins O_EXCL: the
// bundle dir is second-granular, so two captures in the same wall second
// share it. The second must never truncate or delete a profile the first
// owns — it records the collision and moves on.
func TestCapture_SameSecondCapture_DoesNotClobberProfiles(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)

	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture #1: %v", err)
	}
	heapPath := filepath.Join(dir, "heap-"+dirTimestamp(now)+".pprof")
	first, err := os.ReadFile(heapPath)
	if err != nil {
		t.Fatalf("first heap profile: %v", err)
	}

	// Second capture lands in the same dir and its heap write fails — the
	// path it must not touch is the first capture's file.
	s.WriteHeapProfile = func(io.Writer) error { return errors.New("heap boom") }
	dir2, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture #2: %v", err)
	}
	if dir2 != dir {
		t.Fatalf("same-second captures should share a dir: %q vs %q", dir, dir2)
	}
	after, err := os.ReadFile(heapPath)
	if err != nil {
		t.Fatalf("first capture's heap profile was destroyed: %v", err)
	}
	if string(after) != string(first) {
		t.Errorf("first capture's heap profile was rewritten (%d bytes -> %d bytes)", len(first), len(after))
	}
	assertGzipProfile(t, heapPath)
	if errs := readmeErrors(t, dir); !strings.Contains(strings.ToLower(errs), "heap") {
		t.Errorf("README errors should record the heap collision; got:%s", errs)
	}
}

// TestCapture_ExistingCPUProfile_NotStartedOrRemoved covers the CPU arm of the
// same-second collision: an in-progress cpu-<ts>.pprof owned by another
// capture must be left completely alone.
func TestCapture_ExistingCPUProfile_NotStartedOrRemoved(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second
	s.StartCPUProfile = func(io.Writer) error {
		t.Error("must not start a CPU profile when the artifact path is already taken")
		return nil
	}

	// Simulate a concurrent capture that already opened the CPU artifact.
	dir := filepath.Join(root, ".sprawl", "incidents", dirTimestamp(now)+"-tui-snapshot")
	cpuPath := filepath.Join(dir, "cpu-"+dirTimestamp(now)+".pprof")
	writeFile(t, cpuPath, "in-progress-by-another-capture")

	start := time.Now()
	if _, err := s.Capture(context.Background()); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Capture took %v; a collision must not wait out the window", elapsed)
	}
	body, err := os.ReadFile(cpuPath)
	if err != nil {
		t.Fatalf("other capture's CPU profile was removed: %v", err)
	}
	if string(body) != "in-progress-by-another-capture" {
		t.Errorf("other capture's CPU profile was overwritten; got %q", body)
	}
	if errs := readmeErrors(t, dir); !strings.Contains(strings.ToLower(errs), "cpu") {
		t.Errorf("README errors should record the cpu collision; got:%s", errs)
	}
}

// panicMessage is the panic value used by the panic-path tests.
const panicMessage = "collector exploded"

// captureNoPanic runs Capture and fails the test if the panic escapes rather
// than being recovered. Without this wrapper an escaped panic aborts the whole
// test binary, so every later test's result is lost.
func captureNoPanic(t *testing.T, s *incident.Snapshotter) (string, error) {
	t.Helper()
	var dir string
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Capture must not propagate a collector panic, got %v", r)
			}
		}()
		dir, err = s.Capture(context.Background())
	}()
	return dir, err
}

// assertUsableCPUProfile fails unless path holds a well-formed pprof CPU
// profile — not merely a gzip stream. It checks for the sample-type strings
// runtime/pprof writes into every CPU profile's string table, so a truncated
// or header-only file that `go tool pprof` would reject cannot pass. (The
// weaker gzip-only shape let an unsymbolizable profile through once already.)
// Sample *counts* are deliberately not asserted: a sub-second window on an
// idle process legitimately yields zero samples.
func assertUsableCPUProfile(t *testing.T, path string) {
	t.Helper()
	assertGzipProfile(t, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile %s: %v", path, err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip.NewReader %s: %v", path, err)
	}
	defer zr.Close()
	decoded, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	for _, want := range []string{"samples", "count", "cpu", "nanoseconds"} {
		if !bytes.Contains(decoded, []byte(want)) {
			t.Errorf("CPU profile %s lacks sample-type string %q — not a usable pprof profile", path, want)
		}
	}
}

// TestCapture_PanicMidWindow_ReleasesCPUProfilerAndWritesReadme pins the
// panic-safety net: a collector panicking between profile start and stop must
// not leave the process-global CPU profiler active (which would permanently
// break every later capture and any --pprof scrape, QUM-678), must not
// propagate the panic out of Capture, and must still write the README so the
// bundle explains itself.
func TestCapture_PanicMidWindow_ReleasesCPUProfilerAndWritesReadme(t *testing.T) {
	if !cpuProfilingAvailable() {
		t.Skip("process-global CPU profiler already in use")
	}
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second
	// Runner is invoked for `ps`, well inside the window.
	s.Runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		panic(panicMessage)
	}

	start := time.Now()
	dir, err := captureNoPanic(t, s)
	elapsed := time.Since(start)

	// (dir, nil): the TUI's failure arm drops the path, so a non-nil error
	// would hide a bundle that exists and names its own panic.
	if err != nil {
		t.Errorf("Capture err = %v, want nil after a recovered panic", err)
	}
	if dir == "" {
		t.Fatal("Capture must still return the bundle path after a recovered panic")
	}
	// A panic aborts the capture; it must not then sit out the remaining
	// window. 5s is a generous bound on a 30s window — this pins "returns
	// promptly", not a precise duration.
	if elapsed > 5*time.Second {
		t.Errorf("Capture took %v; a panic must abandon the remaining CPU window", elapsed)
	}
	if !cpuProfilingAvailable() {
		t.Error("CPU profiler still active after a panic — the window leaked")
	}
	if errs := readmeErrors(t, dir); !strings.Contains(errs, panicMessage) {
		t.Errorf("README errors must name the panic; got:%s", errs)
	}
	// The partial profile is kept and must still be usable: that requires
	// StopCPUProfile to have flushed it and the file to have been closed.
	cpuFiles := findMatching(t, dir, "cpu-")
	if len(cpuFiles) != 1 {
		t.Fatalf("cpu profile files = %v, want the partial profile kept", cpuFiles)
	}
	assertUsableCPUProfile(t, filepath.Join(dir, cpuFiles[0]))
}

// TestCapture_CollectorPanic_RecordsAndWritesReadme is the core contract: a
// panicking collector yields a complete, self-describing bundle — README index
// plus an "## Errors" entry carrying the panic value and its stack — and
// Capture returns (path, nil) rather than taking the process down.
func TestCapture_CollectorPanic_RecordsAndWritesReadme(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	s.StatusFn = func(ctx context.Context) ([]supervisor.AgentInfo, error) {
		panic(panicMessage)
	}

	dir, err := captureNoPanic(t, s)
	if err != nil {
		t.Errorf("Capture err = %v, want nil after a recovered collector panic", err)
	}
	// Asserting the returned dir explicitly, not just that a bundle exists: a
	// recover() in a defer cannot assign to unnamed results, so the natural
	// wrong implementation returns ("", nil) — which the TUI renders as
	// "snapshot saved → " with no path, losing the bundle entirely.
	want := filepath.Join(root, ".sprawl", "incidents", dirTimestamp(now)+"-tui-snapshot")
	if dir != want {
		t.Fatalf("dir = %q, want %q (path must survive a recovered panic)", dir, want)
	}

	errs := readmeErrors(t, dir)
	// Pin the entry's shape, not just the presence of the word "panic" — a
	// recovered stack always contains "panic(" and "runtime.gopanic", so a
	// bare substring check could never fail.
	wantEntry := "- panic: " + panicMessage
	if !strings.Contains(errs, wantEntry) {
		t.Errorf("README errors must contain a %q entry; got:%s", wantEntry, errs)
	}
	// The stack is indented so it renders as a block inside the bullet rather
	// than shattering the list — and so it can never forge a "## " section
	// boundary that readmeErrors would cut on.
	if !strings.Contains(errs, "\n    goroutine ") {
		t.Errorf("README errors must include an indented stack trace; got:%s", errs)
	}
	// The panicking collector's own frame, not just the recovery frame: a
	// check for "snapshot.go:" would be satisfied by the finisher's own
	// debug.Stack() call site and so could never fail.
	if !strings.Contains(errs, "snapshot_test.go:") {
		t.Errorf("README errors stack must reach the panicking collector's frame; got:%s", errs)
	}

	// The index itself was rendered, not just the Errors section.
	body, rErr := os.ReadFile(filepath.Join(dir, "README.md"))
	if rErr != nil {
		t.Fatalf("README.md: %v", rErr)
	}
	if !strings.Contains(string(body), "cpu-*.pprof") {
		t.Errorf("README index/legend missing after a panic; got:\n%s", body)
	}

	// Artifacts collected before the panic survive.
	if got := findMatching(t, dir, "heap-"); len(got) != 1 {
		t.Errorf("heap profile (collected before the panic) = %v, want exactly one", got)
	}
	if _, sErr := os.Stat(filepath.Join(dir, "binary.txt")); sErr != nil {
		t.Errorf("binary.txt (collected before the panic) missing: %v", sErr)
	}
}

// nthAgentCount panics with a runtime.Error (index out of range) for any n
// beyond counts. Indirected through a function so the panic is not statically
// provable — an inline out-of-range index or nil-map write is a lint error.
func nthAgentCount(counts []int, n int) int { return counts[n] }

// TestCapture_RuntimePanicValue_Recorded covers the realistic panic value in a
// forensic tool aimed at a sick process: a runtime error, not a string.
func TestCapture_RuntimePanicValue_Recorded(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.StatusFn = func(ctx context.Context) ([]supervisor.AgentInfo, error) {
		return nil, fmt.Errorf("unreachable: %d", nthAgentCount(nil, 3))
	}

	dir, err := captureNoPanic(t, s)
	if err != nil {
		t.Errorf("Capture err = %v, want nil after a recovered runtime panic", err)
	}
	if dir == "" {
		t.Fatal("Capture must return the bundle path after a recovered runtime panic")
	}
	errs := readmeErrors(t, dir)
	if !strings.Contains(errs, "index out of range") {
		t.Errorf("README errors must render a non-string panic value; got:%s", errs)
	}
	if strings.Contains(errs, "%!") {
		t.Errorf("README errors contain a formatting-verb error: %s", errs)
	}
}

// TestCapture_PanicInCPUStart_StillWritesReadme pins that the finisher covers
// the CPU-start block too, not just the collectors after it. Registered any
// later, a panic from StartCPUProfile would escape with the process-global
// profiler potentially active and no README written.
func TestCapture_PanicInCPUStart_StillWritesReadme(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second
	s.StartCPUProfile = func(w io.Writer) error { panic(panicMessage) }
	s.StopCPUProfile = func() {
		t.Error("StopCPUProfile must not run when the start itself panicked")
	}

	dir, err := captureNoPanic(t, s)
	if err != nil {
		t.Errorf("Capture err = %v, want nil", err)
	}
	if dir == "" {
		t.Fatal("Capture must return the bundle path")
	}
	if errs := readmeErrors(t, dir); !strings.Contains(errs, panicMessage) {
		t.Errorf("README errors must name the start-block panic; got:%s", errs)
	}
}

// TestCapture_ReadmeWriteFails_ReportsPanicInError covers the finisher's only
// error-returning arm, and the worst run there is: the collector panicked AND
// the bundle cannot index itself. With no README to name the panic, the
// returned error is its last channel, so it must carry it.
func TestCapture_ReadmeWriteFails_ReportsPanicInError(t *testing.T) {
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, root := newSnapshotterWithFixtures(t, now)
	s.StatusFn = func(ctx context.Context) ([]supervisor.AgentInfo, error) {
		panic(panicMessage)
	}
	// Pre-create the bundle dir (MkdirAll is idempotent) with README.md as a
	// directory, so the finisher's os.WriteFile fails with EISDIR.
	bundle := filepath.Join(root, ".sprawl", "incidents", dirTimestamp(now)+"-tui-snapshot")
	if err := os.MkdirAll(filepath.Join(bundle, "README.md"), 0o750); err != nil {
		t.Fatalf("fixture mkdir: %v", err)
	}

	dir, err := captureNoPanic(t, s)
	if err == nil {
		t.Fatal("Capture must return an error when the README cannot be written")
	}
	if dir != "" {
		t.Errorf("dir = %q, want \"\" when the bundle has no index", dir)
	}
	if !strings.Contains(err.Error(), panicMessage) {
		t.Errorf("error = %q, want it to name the panic %q — with no README, this is its last channel", err, panicMessage)
	}
	if !strings.Contains(err.Error(), "write README") {
		t.Errorf("error = %q, want it to name the README write failure", err)
	}
}

// TestCapture_PanicDoesNotWedgeLaterCapture is the end-to-end QUM-678 wedge
// test: after a panic inside a live CPU window, the *next* real capture must
// still produce a usable CPU profile with no profile errors recorded.
func TestCapture_PanicDoesNotWedgeLaterCapture(t *testing.T) {
	if !cpuProfilingAvailable() {
		t.Skip("process-global CPU profiler already in use")
	}
	now := time.Date(2026, 6, 9, 18, 0, 0, 0, time.UTC)
	s, _ := newSnapshotterWithFixtures(t, now)
	s.ProfileDuration = 30 * time.Second
	s.Runner = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		panic(panicMessage)
	}
	firstDir, firstErr := captureNoPanic(t, s)
	if firstErr != nil {
		t.Fatalf("first Capture err = %v, want nil after a recovered panic", firstErr)
	}
	if errs := readmeErrors(t, firstDir); !strings.Contains(errs, panicMessage) {
		t.Fatalf("first capture must have recorded the panic; got:%s", errs)
	}

	// Second capture: real profiler, short window, no panic.
	s.Runner = canonicalRunner(map[string][]byte{"ps": []byte("ok\n"), "free": []byte("ok\n")}, nil)
	s.ProfileDuration = 100 * time.Millisecond
	s.Now = fixedNow(now.Add(time.Second))
	dir, err := s.Capture(context.Background())
	if err != nil {
		t.Fatalf("second Capture after a recovered panic: %v", err)
	}
	assertNoProfileErrors(t, dir)
	if errs := readmeErrors(t, dir); strings.Contains(errs, panicMessage) {
		t.Errorf("second capture's README must not carry the earlier panic; got:%s", errs)
	}
	cpuFiles := findMatching(t, dir, "cpu-")
	if len(cpuFiles) != 1 {
		t.Fatalf("second capture cpu profile files = %v, want exactly one", cpuFiles)
	}
	assertUsableCPUProfile(t, filepath.Join(dir, cpuFiles[0]))
}
