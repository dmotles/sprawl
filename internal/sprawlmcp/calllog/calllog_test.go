package calllog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// jsonlLines reads all newline-delimited JSON objects from a file and
// returns them as []map[string]any. Returns an error if any line fails
// to parse — useful for catching torn writes.
func jsonlLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var out []map[string]any
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			t.Fatalf("malformed JSONL line %q: %v", string(line), err)
		}
		out = append(out, obj)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func callLogPath(root string) string {
	return filepath.Join(root, ".sprawl", "logs", "mcp-calls.jsonl")
}

func inFlightPath(root string) string {
	return filepath.Join(root, ".sprawl", "runtime", "in-flight.json")
}

func TestLogger_BeginEndWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	args := map[string]any{"agent_name": "finn"}
	_, callID := l.Begin(context.Background(), "retire", "weave", args)
	if callID == "" {
		t.Fatal("Begin returned empty callID")
	}
	l.End(callID, "ok", "")

	lines := jsonlLines(t, callLogPath(dir))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %+v", len(lines), lines)
	}

	start := lines[0]
	end := lines[1]

	if start["phase"] != "start" {
		t.Errorf("line[0].phase = %v, want start", start["phase"])
	}
	if end["phase"] != "end" {
		t.Errorf("line[1].phase = %v, want end", end["phase"])
	}
	if start["call_id"] != callID {
		t.Errorf("start.call_id = %v, want %s", start["call_id"], callID)
	}
	if end["call_id"] != callID {
		t.Errorf("end.call_id = %v, want %s", end["call_id"], callID)
	}
	if start["tool"] != "retire" {
		t.Errorf("start.tool = %v, want retire", start["tool"])
	}
	if start["caller"] != "weave" {
		t.Errorf("start.caller = %v, want weave", start["caller"])
	}
	if start["args"] == nil {
		t.Errorf("start.args missing")
	}
	if end["status"] != "ok" {
		t.Errorf("end.status = %v, want ok", end["status"])
	}
	dur, ok := end["duration_s"].(float64)
	if !ok {
		t.Errorf("end.duration_s not numeric: %v", end["duration_s"])
	} else if dur < 0 {
		t.Errorf("end.duration_s = %v, want >= 0", dur)
	}
}

func TestLogger_CheckpointBetween(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	_, id := l.Begin(context.Background(), "merge", "weave", nil)
	l.Checkpoint(id, "merge.lock-acquired")
	l.Checkpoint(id, "merge.validate-started", "cmd", "make validate")
	l.End(id, "ok", "")

	lines := jsonlLines(t, callLogPath(dir))
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4: %+v", len(lines), lines)
	}

	if lines[1]["phase"] != "checkpoint" || lines[1]["step"] != "merge.lock-acquired" {
		t.Errorf("line[1] = %+v, want checkpoint/lock-acquired", lines[1])
	}
	if lines[2]["phase"] != "checkpoint" || lines[2]["step"] != "merge.validate-started" {
		t.Errorf("line[2] = %+v, want checkpoint/validate-started", lines[2])
	}

	kv, ok := lines[2]["kv"].(map[string]any)
	if !ok {
		t.Fatalf("line[2].kv missing or not an object: %v", lines[2]["kv"])
	}
	if kv["cmd"] != "make validate" {
		t.Errorf("kv.cmd = %v, want 'make validate'", kv["cmd"])
	}
}

func TestLogger_ConcurrentCalls(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	const N = 50
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, id := l.Begin(context.Background(), "tool", "caller", map[string]any{"i": i})
			l.Checkpoint(id, "step")
			l.End(id, "ok", "")
		}(i)
	}
	wg.Wait()

	lines := jsonlLines(t, callLogPath(dir))
	if len(lines) != N*3 {
		t.Fatalf("got %d lines, want %d", len(lines), N*3)
	}

	counts := map[string]int{}
	for _, ln := range lines {
		id, _ := ln["call_id"].(string)
		if id == "" {
			t.Fatalf("line missing call_id: %+v", ln)
		}
		counts[id]++
	}
	if len(counts) != N {
		t.Errorf("unique call_ids = %d, want %d", len(counts), N)
	}
	for id, c := range counts {
		if c != 3 {
			t.Errorf("call_id %s appeared %d times, want 3", id, c)
		}
	}
}

func TestLogger_InFlightRegistryReflectsActive(t *testing.T) {
	dir := t.TempDir()
	l, triggerTick, err := OpenForTest(dir, TestOptions{})
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	defer l.Close()

	_, id := l.Begin(context.Background(), "retire", "weave", map[string]any{"agent_name": "finn"})
	l.Checkpoint(id, "merge.validate-started")

	// Drive a single heartbeat iteration synchronously.
	triggerTick()

	data, err := os.ReadFile(inFlightPath(dir))
	if err != nil {
		t.Fatalf("read in-flight.json: %v", err)
	}

	var reg struct {
		TS    string      `json:"ts"`
		Calls []CallState `json:"calls"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("unmarshal: %v (data=%s)", err, string(data))
	}
	if len(reg.Calls) != 1 {
		t.Fatalf("calls len = %d, want 1: %+v", len(reg.Calls), reg.Calls)
	}
	c := reg.Calls[0]
	if c.CallID != id {
		t.Errorf("call_id = %q, want %q", c.CallID, id)
	}
	if c.Tool != "retire" {
		t.Errorf("tool = %q, want retire", c.Tool)
	}
	if c.Caller != "weave" {
		t.Errorf("caller = %q, want weave", c.Caller)
	}
	if c.CurrentStep != "merge.validate-started" {
		t.Errorf("current_step = %q, want merge.validate-started", c.CurrentStep)
	}

	// Atomic write should not leave a stray .tmp behind.
	if _, err := os.Stat(inFlightPath(dir) + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".tmp file should not persist after write: err=%v", err)
	}
}

func TestLogger_RegistryClearedAfterEnd(t *testing.T) {
	dir := t.TempDir()
	l, triggerTick, err := OpenForTest(dir, TestOptions{})
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	defer l.Close()

	_, id := l.Begin(context.Background(), "retire", "weave", nil)
	l.End(id, "ok", "")

	triggerTick()

	data, err := os.ReadFile(inFlightPath(dir))
	if err != nil {
		t.Fatalf("read in-flight.json: %v", err)
	}
	var reg struct {
		Calls []CallState `json:"calls"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(reg.Calls) != 0 {
		t.Errorf("calls = %+v, want empty after End", reg.Calls)
	}
}

func TestLogger_KillMidValidate(t *testing.T) {
	dir := t.TempDir()
	l, triggerTick, err := OpenForTest(dir, TestOptions{})
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}

	_, id := l.Begin(context.Background(), "retire", "weave", map[string]any{"agent_name": "finn"})
	l.Checkpoint(id, "merge.lock-acquired")
	l.Checkpoint(id, "merge.validate-started", "cmd", "make validate")
	// Intentionally do NOT call End — simulating SIGKILL mid-validate.

	triggerTick()

	// Read JSONL — last line should be the validate-started checkpoint.
	lines := jsonlLines(t, callLogPath(dir))
	if len(lines) == 0 {
		t.Fatal("no lines written")
	}
	last := lines[len(lines)-1]
	if last["phase"] != "checkpoint" {
		t.Errorf("last line phase = %v, want checkpoint", last["phase"])
	}
	if last["step"] != "merge.validate-started" {
		t.Errorf("last line step = %v, want merge.validate-started", last["step"])
	}

	// Read in-flight registry — should still show the call with current_step.
	data, err := os.ReadFile(inFlightPath(dir))
	if err != nil {
		t.Fatalf("read in-flight.json: %v", err)
	}
	var reg struct {
		Calls []CallState `json:"calls"`
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(reg.Calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(reg.Calls))
	}
	if reg.Calls[0].CallID != id {
		t.Errorf("call_id = %q, want %q", reg.Calls[0].CallID, id)
	}
	if reg.Calls[0].CurrentStep != "merge.validate-started" {
		t.Errorf("current_step = %q, want merge.validate-started", reg.Calls[0].CurrentStep)
	}

	_ = l.Close()
}

func TestLogger_PanicRecover(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	_, id := l.Begin(context.Background(), "spawn", "weave", nil)
	l.End(id, "panic", "boom")

	lines := jsonlLines(t, callLogPath(dir))
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	end := lines[1]
	if end["phase"] != "end" {
		t.Errorf("phase = %v, want end", end["phase"])
	}
	if end["status"] != "panic" {
		t.Errorf("status = %v, want panic", end["status"])
	}
	if end["error"] != "boom" {
		t.Errorf("error = %v, want boom", end["error"])
	}
}

func TestLogger_ContextCallID(t *testing.T) {
	ctx := WithCallID(context.Background(), "abc")
	if got := CallID(ctx); got != "abc" {
		t.Errorf("CallID(ctx) = %q, want abc", got)
	}
	if got := CallID(context.Background()); got != "" {
		t.Errorf("CallID(empty) = %q, want empty", got)
	}
}

func TestLogger_CheckpointFnBindsCallID(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	_, id := l.Begin(context.Background(), "merge", "weave", nil)
	cp := l.CheckpointFn(id)
	cp("step1")
	l.End(id, "ok", "")

	lines := jsonlLines(t, callLogPath(dir))
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	mid := lines[1]
	if mid["phase"] != "checkpoint" {
		t.Errorf("phase = %v, want checkpoint", mid["phase"])
	}
	if mid["call_id"] != id {
		t.Errorf("call_id = %v, want %q", mid["call_id"], id)
	}
	if mid["step"] != "step1" {
		t.Errorf("step = %v, want step1", mid["step"])
	}
}

func TestLogger_NoopLoggerSafeToUse(t *testing.T) {
	// Switch to t.TempDir() as cwd so any accidental file create is detected.
	cwd, _ := os.Getwd()
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(cwd)

	l := NewNoop()
	_, id := l.Begin(context.Background(), "tool", "caller", map[string]any{"a": 1})
	l.Checkpoint(id, "step", "k", "v")
	l.End(id, "ok", "")
	cp := l.CheckpointFn(id)
	cp("step2")
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// No files should have been created in cwd.
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("noop logger created files in cwd: %v", names)
	}
}

func TestLogger_CreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	logsDir := filepath.Join(dir, ".sprawl", "logs")
	runtimeDir := filepath.Join(dir, ".sprawl", "runtime")

	if info, err := os.Stat(logsDir); err != nil || !info.IsDir() {
		t.Errorf("logs dir not created: err=%v", err)
	}
	if info, err := os.Stat(runtimeDir); err != nil || !info.IsDir() {
		t.Errorf("runtime dir not created: err=%v", err)
	}
}

// sanity: ensure Begin emits a context that carries the callID
func TestLogger_BeginContextCarriesCallID(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	ctx, id := l.Begin(context.Background(), "tool", "caller", nil)
	if got := CallID(ctx); got != id {
		t.Errorf("CallID(ctx) = %q, want %q", got, id)
	}
	if len(id) < 16 {
		t.Errorf("expected UUID-shaped id (>=16 chars), got %q", id)
	}
	l.End(id, "ok", "")
}

// TestLogger_RotatesOversizedLogOnOpen seeds an oversized current log plus
// an existing .1 and .2, then opens the logger and asserts the ring shifted:
// current is fresh, old current is now .1, old .1 is .2, old .2 is .3.
// QUM-502.
func TestLogger_RotatesOversizedLogOnOpen(t *testing.T) {
	restore := SetMaxLogBytesForTest(128) // tiny threshold so we don't write 64MiB
	defer restore()

	dir := t.TempDir()
	logsDir := filepath.Join(dir, ".sprawl", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := callLogPath(dir)

	curContent := []byte("CURRENT-OVERSIZED-" + string(make([]byte, 256)))
	gen1Content := []byte("GEN-1")
	gen2Content := []byte("GEN-2")

	if err := os.WriteFile(logPath, curContent, 0o644); err != nil {
		t.Fatalf("write current: %v", err)
	}
	if err := os.WriteFile(logPath+".1", gen1Content, 0o644); err != nil {
		t.Fatalf("write .1: %v", err)
	}
	if err := os.WriteFile(logPath+".2", gen2Content, 0o644); err != nil {
		t.Fatalf("write .2: %v", err)
	}

	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	// Current file must be fresh (zero bytes — Open hasn't written yet).
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat current: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("current log size = %d, want 0 (fresh after rotation)", info.Size())
	}

	// .1 should be the previous current.
	got, err := os.ReadFile(logPath + ".1")
	if err != nil {
		t.Fatalf("read .1: %v", err)
	}
	if string(got) != string(curContent) {
		t.Errorf(".1 content mismatch: got %q, want previous current", string(got))
	}

	// .2 should be the previous .1.
	got, err = os.ReadFile(logPath + ".2")
	if err != nil {
		t.Fatalf("read .2: %v", err)
	}
	if string(got) != string(gen1Content) {
		t.Errorf(".2 content = %q, want %q", string(got), string(gen1Content))
	}

	// .3 should be the previous .2.
	got, err = os.ReadFile(logPath + ".3")
	if err != nil {
		t.Fatalf("read .3: %v", err)
	}
	if string(got) != string(gen2Content) {
		t.Errorf(".3 content = %q, want %q", string(got), string(gen2Content))
	}
}

// TestLogger_DropsOldestGenerationOnRotate ensures a full ring drops the
// oldest file rather than growing unbounded. QUM-502.
func TestLogger_DropsOldestGenerationOnRotate(t *testing.T) {
	restore := SetMaxLogBytesForTest(64)
	defer restore()

	dir := t.TempDir()
	logsDir := filepath.Join(dir, ".sprawl", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := callLogPath(dir)

	// Seed current + a full ring of .1/.2/.3.
	if err := os.WriteFile(logPath, make([]byte, 256), 0o644); err != nil {
		t.Fatalf("write current: %v", err)
	}
	for i := 1; i <= 3; i++ {
		path := logPath + "." + string(rune('0'+i))
		if err := os.WriteFile(path, []byte("gen-"+string(rune('0'+i))), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// Record what .3 contained — after rotation that data should be gone.
	oldGen3, err := os.ReadFile(logPath + ".3")
	if err != nil {
		t.Fatalf("read old .3: %v", err)
	}

	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	// .4 must not exist — ring depth is fixed at 3.
	if _, err := os.Stat(logPath + ".4"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".4 should not exist (ring depth=3); stat err=%v", err)
	}

	// New .3 should be the previous .2 (gen-2), not the previous .3.
	got, err := os.ReadFile(logPath + ".3")
	if err != nil {
		t.Fatalf("read new .3: %v", err)
	}
	if string(got) == string(oldGen3) {
		t.Errorf(".3 still holds the dropped generation: %q", string(got))
	}
	if string(got) != "gen-2" {
		t.Errorf(".3 content = %q, want gen-2 (shifted from old .2)", string(got))
	}
}

// TestLogger_DoesNotRotateUndersizedLog confirms Open is a no-op on small
// files: content is preserved and no .1 is created. QUM-502.
func TestLogger_DoesNotRotateUndersizedLog(t *testing.T) {
	restore := SetMaxLogBytesForTest(1024)
	defer restore()

	dir := t.TempDir()
	logsDir := filepath.Join(dir, ".sprawl", "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logPath := callLogPath(dir)

	original := []byte(`{"phase":"end"}` + "\n")
	if err := os.WriteFile(logPath, original, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	l, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer l.Close()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("content mutated: got %q, want %q", string(got), string(original))
	}
	if _, err := os.Stat(logPath + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".1 should not exist when current is undersized; stat err=%v", err)
	}
}

// TestLogger_FsyncEveryLine verifies the JSONL file is fsync'd after each
// writeline (Begin / Checkpoint / End — three writes per call).
func TestLogger_FsyncEveryLine(t *testing.T) {
	dir := t.TempDir()
	var syncCount int
	l, _, err := OpenForTest(dir, TestOptions{SyncCounter: &syncCount})
	if err != nil {
		t.Fatalf("OpenForTest: %v", err)
	}
	defer l.Close()

	_, id := l.Begin(context.Background(), "retire", "weave", nil)
	l.Checkpoint(id, "step1")
	l.End(id, "ok", "")

	if syncCount != 3 {
		t.Errorf("syncCount = %d, want 3 (one fsync per writeline)", syncCount)
	}
}
