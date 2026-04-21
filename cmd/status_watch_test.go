package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/state"
)

// writeActivity writes the given entries as NDJSON to the activity file for
// agentName under sprawlRoot. Directories are created.
func writeActivity(t *testing.T, sprawlRoot, agentName string, entries []agentloop.ActivityEntry) {
	t.Helper()
	path := agentloop.ActivityPath(sprawlRoot, agentName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	ring := agentloop.NewActivityRing(len(entries)+1, f)
	for _, e := range entries {
		ring.Append(e)
	}
}

// agentStatePath returns the JSON state file path for agentName.
func agentStatePath(sprawlRoot, agentName string) string {
	return filepath.Join(sprawlRoot, ".sprawl", "agents", agentName+".json")
}

func TestRunStatusAgent_Success(t *testing.T) {
	root := t.TempDir()

	// Persist agent state via state.SaveAgent so LoadAgent works.
	st := &state.AgentState{
		Name:              "alpha",
		Type:              "engineer",
		Family:            "engineering",
		Parent:            "weave",
		Status:            "active",
		LastReportType:    "status",
		LastReportMessage: "working on tests",
		LastReportAt:      "2026-04-21T10:00:00Z",
	}
	if err := state.SaveAgent(root, st); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// Write two activity entries.
	ts := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	writeActivity(t, root, "alpha", []agentloop.ActivityEntry{
		{TS: ts, Kind: "assistant_text", Summary: "thinking hard"},
		{TS: ts.Add(time.Second), Kind: "tool_use", Tool: "Bash", Summary: `Bash {"cmd":"ls"}`},
	})

	buf := &bytes.Buffer{}
	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}

	if err := runStatusAgent(deps, "alpha", 50); err != nil {
		t.Fatalf("runStatusAgent: %v", err)
	}

	out := buf.String()
	// Header shows agent name and status.
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected agent name in output, got:\n%s", out)
	}
	// Last report rendered.
	if !strings.Contains(out, "working on tests") {
		t.Errorf("expected last_report message in output, got:\n%s", out)
	}
	if !strings.Contains(out, "[STATUS]") {
		t.Errorf("expected [STATUS] tag in output, got:\n%s", out)
	}
	// Activity entries rendered.
	if !strings.Contains(out, "thinking hard") {
		t.Errorf("expected activity summary in output, got:\n%s", out)
	}
	if !strings.Contains(out, "tool_use") || !strings.Contains(out, "Bash") {
		t.Errorf("expected tool_use entry with tool name in output, got:\n%s", out)
	}
}

func TestRunStatusAgent_NoActivity(t *testing.T) {
	root := t.TempDir()
	st := &state.AgentState{Name: "alpha", Status: "active", Parent: "weave"}
	if err := state.SaveAgent(root, st); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	buf := &bytes.Buffer{}
	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}

	if err := runStatusAgent(deps, "alpha", 50); err != nil {
		t.Fatalf("runStatusAgent: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "alpha") {
		t.Errorf("expected agent name in output, got:\n%s", out)
	}
	// Should note there's no activity.
	if !strings.Contains(strings.ToLower(out), "no activity") {
		t.Errorf("expected 'no activity' message, got:\n%s", out)
	}
}

func TestRunStatusAgent_UnknownAgent(t *testing.T) {
	root := t.TempDir()

	buf := &bytes.Buffer{}
	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}

	err := runStatusAgent(deps, "ghost", 50)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("expected error to mention agent name, got: %v", err)
	}
}

func TestRunStatusAgent_InvalidName(t *testing.T) {
	root := t.TempDir()

	buf := &bytes.Buffer{}
	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}

	err := runStatusAgent(deps, "../bad", 50)
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}

func TestRunStatusAgent_TailLimit(t *testing.T) {
	root := t.TempDir()
	st := &state.AgentState{Name: "alpha", Status: "active", Parent: "weave"}
	if err := state.SaveAgent(root, st); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// Write 10 entries with distinguishable summaries.
	ts := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	var entries []agentloop.ActivityEntry
	for i := 0; i < 10; i++ {
		entries = append(entries, agentloop.ActivityEntry{
			TS:      ts.Add(time.Duration(i) * time.Second),
			Kind:    "assistant_text",
			Summary: fmt.Sprintf("entry-%d", i),
		})
	}
	writeActivity(t, root, "alpha", entries)

	buf := &bytes.Buffer{}
	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}

	if err := runStatusAgent(deps, "alpha", 3); err != nil {
		t.Fatalf("runStatusAgent: %v", err)
	}

	out := buf.String()
	// Most recent 3 should be present: entry-7, entry-8, entry-9.
	for _, want := range []string{"entry-7", "entry-8", "entry-9"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
	// Older should NOT be present.
	for _, unwanted := range []string{"entry-0", "entry-1", "entry-5", "entry-6"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("did not expect %q in tail=3 output, got:\n%s", unwanted, out)
		}
	}
}

func TestRunStatusWatch_StreamsNewEntries(t *testing.T) {
	root := t.TempDir()

	// Seed alpha with no entries, bravo with one.
	stA := &state.AgentState{Name: "alpha", Status: "active", Parent: "weave"}
	stB := &state.AgentState{Name: "bravo", Status: "active", Parent: "weave"}
	if err := state.SaveAgent(root, stA); err != nil {
		t.Fatalf("SaveAgent alpha: %v", err)
	}
	if err := state.SaveAgent(root, stB); err != nil {
		t.Fatalf("SaveAgent bravo: %v", err)
	}

	ts := time.Date(2026, 4, 21, 10, 0, 0, 0, time.UTC)
	writeActivity(t, root, "bravo", []agentloop.ActivityEntry{
		{TS: ts, Kind: "assistant_text", Summary: "seed-entry"},
	})

	buf := &threadSafeBuffer{}
	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: buf,
		stderr: io.Discard,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		runErr = runStatusWatch(ctx, deps, 20*time.Millisecond)
	}()

	// Give the watcher one poll cycle to ingest initial state (pre-existing
	// entries should be considered "already seen" and NOT streamed).
	time.Sleep(100 * time.Millisecond)

	// Append a new entry to alpha.
	writeActivity(t, root, "alpha", []agentloop.ActivityEntry{
		{TS: ts.Add(time.Second), Kind: "tool_use", Tool: "Read", Summary: "alpha-new-1"},
	})

	// Append another to bravo.
	writeActivity(t, root, "bravo", []agentloop.ActivityEntry{
		{TS: ts.Add(2 * time.Second), Kind: "assistant_text", Summary: "bravo-new-1"},
	})

	// Wait for the poller to pick them up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out := buf.String()
		if strings.Contains(out, "alpha-new-1") && strings.Contains(out, "bravo-new-1") {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	cancel()
	wg.Wait()
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		t.Fatalf("runStatusWatch: %v", runErr)
	}

	out := buf.String()
	if !strings.Contains(out, "alpha-new-1") {
		t.Errorf("expected alpha-new-1 in watch output, got:\n%s", out)
	}
	if !strings.Contains(out, "bravo-new-1") {
		t.Errorf("expected bravo-new-1 in watch output, got:\n%s", out)
	}
	// Seeded entry should NOT appear (it was pre-existing when watch started).
	if strings.Contains(out, "seed-entry") {
		t.Errorf("did not expect pre-existing 'seed-entry' in watch output, got:\n%s", out)
	}
	// Each streamed line should be prefixed with the agent name.
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "bravo") {
		t.Errorf("expected agent-name prefixes in output, got:\n%s", out)
	}
}

func TestRunStatusWatch_ContextCancelled(t *testing.T) {
	root := t.TempDir()

	deps := &statusDeps{
		getenv: func(k string) string {
			if k == "SPRAWL_ROOT" {
				return root
			}
			return ""
		},
		stdout: io.Discard,
		stderr: io.Discard,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := runStatusWatch(ctx, deps, 20*time.Millisecond)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected nil or context.Canceled, got: %v", err)
	}
}

func TestRunStatusWatch_MissingSprawlRoot(t *testing.T) {
	deps := &statusDeps{
		getenv: func(string) string { return "" },
		stdout: io.Discard,
		stderr: io.Discard,
	}
	err := runStatusWatch(context.Background(), deps, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("expected SPRAWL_ROOT error, got: %v", err)
	}
}

// threadSafeBuffer is a bytes.Buffer protected by a mutex so concurrent reads
// during the watch test don't race with writes from the poll goroutine.
type threadSafeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *threadSafeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *threadSafeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Sanity: agentStatePath helper used only in test fixtures below.
var _ = agentStatePath
