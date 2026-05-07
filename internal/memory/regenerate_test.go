package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingInvoker is a fake ClaudeInvoker that returns canned responses in
// order and records each call's prompt + options. When responses are
// exhausted it returns the last response (or zero-value if none) — tests
// that care about call counts assert on len(calls).
type recordingInvoker struct {
	responses []string
	errs      []error
	calls     []recordedInvokerCall
}

type recordedInvokerCall struct {
	prompt string
	model  string
}

func (r *recordingInvoker) Invoke(_ context.Context, prompt string, opts ...InvokeOption) (string, error) {
	var cfg invokeConfig
	for _, o := range opts {
		o(&cfg)
	}
	r.calls = append(r.calls, recordedInvokerCall{prompt: prompt, model: cfg.model})
	idx := len(r.calls) - 1
	var resp string
	if idx < len(r.responses) {
		resp = r.responses[idx]
	} else if len(r.responses) > 0 {
		resp = r.responses[len(r.responses)-1]
	}
	var err error
	if idx < len(r.errs) {
		err = r.errs[idx]
	}
	return resp, err
}

func TestTimelineRowRegex(t *testing.T) {
	t.Parallel()

	uuidA := "550e8400-e29b-41d4-a716-446655440000"
	uuidB := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	uuidC := "f47ac10c-58cc-4372-a567-0e02b2c3d479"
	uuidD := "01890c1c-0a4d-7e8e-9b2a-1234567890ab"
	uuidE := "abcdef01-2345-6789-abcd-ef0123456789"
	uuidF := "00000000-0000-0000-0000-000000000000"

	valid := []string{
		"2026-01-01 " + uuidA + " | Implemented timeline regeneration command",
		"2026-05-07 " + uuidB + " | Fixed QUM-511: merge follows worktree branch",
		"2025-12-31 " + uuidC + " | x",                                                // single-char summary (matches \S branch)
		"2026-03-15 " + uuidD + " | " + strings.Repeat("a", 120),                      // exactly 120 chars
		"2026-02-29 " + uuidE + " | Multi-word summary with: punctuation, commas; OK", // punctuation OK
		"2026-04-01 " + uuidF + " | Began QUM-513 umbrella; first slice is regenerate-timeline",
	}
	for i, row := range valid {
		if err := ValidateTimelineRow(row); err != nil {
			t.Errorf("valid[%d] %q rejected: %v", i, row, err)
		}
	}

	invalid := []struct {
		name string
		row  string
	}{
		{"trailing newline", "2026-01-01 " + uuidA + " | Summary\n"},
		{"embedded newline", "2026-01-01 " + uuidA + " | Line one\nLine two"},
		{"missing pipe", "2026-01-01 " + uuidA + " Summary without pipe"},
		{"bad date format", "26-01-01 " + uuidA + " | Two-digit year"},
		{"month out of range — wrong length date", "2026-1-01 " + uuidA + " | Short month"},
		{"malformed UUID — too short", "2026-01-01 deadbeef | Bad id"},
		{"malformed UUID — wrong segment lengths", "2026-01-01 550e8400-e29b-41d4-a716-44665544 | Truncated id"},
		{"summary too long (121 chars)", "2026-01-01 " + uuidA + " | " + strings.Repeat("a", 121)},
		{"empty summary", "2026-01-01 " + uuidA + " | "},
		{"summary with leading whitespace", "2026-01-01 " + uuidA + " |  leading space"},
		{"leading bullet dash", "- 2026-01-01 " + uuidA + " | Looks like markdown bullet"},
		{"model preamble before row", "Here is your row:\n2026-01-01 " + uuidA + " | Summary"},
		{"trailing whitespace in summary", "2026-01-01 " + uuidA + " | trailing space "},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTimelineRow(tc.row); err == nil {
				t.Errorf("invalid row %q accepted", tc.row)
			}
		})
	}
}

func TestRenderTimelineRow_RoundTrips(t *testing.T) {
	t.Parallel()
	d := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	id := "550e8400-e29b-41d4-a716-446655440000"
	row := RenderTimelineRow(d, id, "Implemented regenerate-timeline command")
	if err := ValidateTimelineRow(row); err != nil {
		t.Fatalf("rendered row %q failed validation: %v", row, err)
	}
	if !strings.HasPrefix(row, "2026-05-07 "+id+" | ") {
		t.Errorf("row prefix wrong: %q", row)
	}
}

func TestPlaceholderRow_PassesRegex(t *testing.T) {
	t.Parallel()
	s := Session{
		SessionID: "550e8400-e29b-41d4-a716-446655440000",
		Timestamp: time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
	}
	row := PlaceholderRow(s)
	if err := ValidateTimelineRow(row); err != nil {
		t.Errorf("placeholder row %q failed regex: %v", row, err)
	}
	// And it should mention the failure mode so a human can find the session.
	if !strings.Contains(row, "regenerate failed") {
		t.Errorf("placeholder should signal failure: %q", row)
	}
}

func TestSummarizeSession_ValidFirstTry(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	want := "2026-05-07 " + id + " | First-try success summary"
	inv := &recordingInvoker{responses: []string{want}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "session body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(inv.calls) != 1 {
		t.Errorf("invoker called %d times, want 1", len(inv.calls))
	}
}

func TestSummarizeSession_RetryOnMalformed(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	good := "2026-05-07 " + id + " | Retry succeeded"
	inv := &recordingInvoker{responses: []string{
		"Here is your row:\n2026-05-07 " + id + " | Bad preamble",
		good,
	}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != good {
		t.Errorf("got %q, want %q", got, good)
	}
	if len(inv.calls) != 2 {
		t.Errorf("invoker called %d times, want 2", len(inv.calls))
	}
}

func TestSummarizeSession_FallbackAfterTwoFailures(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	inv := &recordingInvoker{responses: []string{
		"garbage attempt 1",
		"still garbage attempt 2",
	}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "body")
	if err != nil {
		t.Fatalf("expected nil error on placeholder fallback, got: %v", err)
	}
	if got != PlaceholderRow(s) {
		t.Errorf("got %q, want placeholder %q", got, PlaceholderRow(s))
	}
	if len(inv.calls) != 2 {
		t.Errorf("invoker called %d times, want 2 (one + retry)", len(inv.calls))
	}
}

// echoInvoker returns a deterministic valid row for any session it sees,
// keyed by the session ID embedded in the prompt. The implementer's prompt
// MUST include the session id and date in a recoverable form; this fake
// just emits a canonical row with a fixed summary so tests can assert on
// row order/validity without caring about LLM-specific prose.
type echoInvoker struct {
	sessions map[string]Session // sessionID -> Session, for date lookup
}

func (e *echoInvoker) Invoke(_ context.Context, prompt string, _ ...InvokeOption) (string, error) {
	for id, s := range e.sessions {
		if strings.Contains(prompt, id) {
			return RenderTimelineRow(s.Timestamp, id, "Echo summary for "+id[:8]), nil
		}
	}
	return "", errors.New("echoInvoker: no matching session in prompt")
}

func TestRegenerateTimeline_SortAndWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Seed sessions out of date order.
	mkSession := func(id string, day int) Session {
		return Session{
			SessionID:    id,
			Timestamp:    time.Date(2026, 5, day, 12, 0, 0, 0, time.UTC),
			AgentsActive: []string{"weave"},
		}
	}
	idA := "11111111-1111-1111-1111-111111111111"
	idB := "22222222-2222-2222-2222-222222222222"
	idC := "33333333-3333-3333-3333-333333333333"

	sessions := map[string]Session{
		idA: mkSession(idA, 7),
		idB: mkSession(idB, 1),
		idC: mkSession(idC, 4),
	}

	// Write in non-chronological order.
	for _, id := range []string{idA, idC, idB} {
		if err := WriteSessionSummary(root, sessions[id], "body of "+id); err != nil {
			t.Fatalf("WriteSessionSummary(%s): %v", id, err)
		}
	}

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	inv := &echoInvoker{sessions: sessions}

	err := RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Invoker:    inv,
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("RegenerateTimeline: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), string(data))
	}

	// Each row must validate.
	for i, l := range lines {
		if err := ValidateTimelineRow(l); err != nil {
			t.Errorf("row %d invalid: %q (%v)", i, l, err)
		}
	}

	// Rows must be ascending by date — extract leading YYYY-MM-DD.
	prev := ""
	for i, l := range lines {
		date := l[:10]
		if i > 0 && date < prev {
			t.Errorf("row %d date %q < previous %q (not ascending)", i, date, prev)
		}
		prev = date
	}

	// No temp/leftover files in memory dir.
	memDir := filepath.Join(root, ".sprawl", "memory")
	entries, _ := os.ReadDir(memDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestRegenerateTimeline_RefusesOverwrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	id := "11111111-1111-1111-1111-111111111111"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	if err := WriteSessionSummary(root, s, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	preexisting := []byte("PRE-EXISTING TIMELINE — DO NOT CLOBBER\n")
	if err := os.WriteFile(out, preexisting, 0o644); err != nil {
		t.Fatalf("seed out: %v", err)
	}

	inv := &echoInvoker{sessions: map[string]Session{id: s}}

	// Without Force: must refuse.
	err := RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Invoker:    inv,
		Stdout:     io.Discard,
	})
	if err == nil {
		t.Fatal("expected error refusing to overwrite existing OutPath without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force flag, got: %v", err)
	}
	if data, _ := os.ReadFile(out); !bytes.Equal(data, preexisting) {
		t.Errorf("OutPath was clobbered without --force; got %q", data)
	}

	// With Force: must succeed and replace.
	err = RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Force:      true,
		Invoker:    inv,
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("RegenerateTimeline with --force: %v", err)
	}
	if data, _ := os.ReadFile(out); bytes.Equal(data, preexisting) {
		t.Error("OutPath was not replaced even with --force")
	}
}

func TestRegenerateTimeline_DryRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	id := "11111111-1111-1111-1111-111111111111"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	if err := WriteSessionSummary(root, s, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	var stdout bytes.Buffer
	inv := &echoInvoker{sessions: map[string]Session{id: s}}

	err := RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		DryRun:     true,
		Invoker:    inv,
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatalf("RegenerateTimeline dry-run: %v", err)
	}

	// File MUST NOT exist.
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote a file at %s (err=%v)", out, err)
	}

	// Stdout must contain the rendered row.
	if !strings.Contains(stdout.String(), id) {
		t.Errorf("dry-run stdout should contain rendered row mentioning session id %s; got: %q", id, stdout.String())
	}
}

func TestRegenerateTimeline_PassesModelOverride(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	id := "11111111-1111-1111-1111-111111111111"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	if err := WriteSessionSummary(root, s, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	row := RenderTimelineRow(s.Timestamp, id, "Echo summary")

	// Default: cfg.Model unset → expect haiku (the documented default for
	// regenerate-timeline, which prefers a fast model for the simple
	// per-session summarization task).
	t.Run("default model is haiku", func(t *testing.T) {
		inv := &recordingInvoker{responses: []string{row}}
		err := RegenerateTimeline(context.Background(), RegenerateOptions{
			SprawlRoot: root,
			OutPath:    filepath.Join(root, "out-default.md"),
			Invoker:    inv,
			Stdout:     io.Discard,
		})
		if err != nil {
			t.Fatalf("RegenerateTimeline: %v", err)
		}
		if len(inv.calls) == 0 {
			t.Fatal("invoker not called")
		}
		if inv.calls[0].model != "haiku" {
			t.Errorf("default model = %q, want %q", inv.calls[0].model, "haiku")
		}
	})

	t.Run("override propagates", func(t *testing.T) {
		inv := &recordingInvoker{responses: []string{row}}
		err := RegenerateTimeline(context.Background(), RegenerateOptions{
			SprawlRoot: root,
			OutPath:    filepath.Join(root, "out-override.md"),
			Invoker:    inv,
			Stdout:     io.Discard,
			Cfg:        RegenerateConfig{Model: "sonnet"},
		})
		if err != nil {
			t.Fatalf("RegenerateTimeline: %v", err)
		}
		if len(inv.calls) == 0 {
			t.Fatal("invoker not called")
		}
		if inv.calls[0].model != "sonnet" {
			t.Errorf("override model = %q, want %q", inv.calls[0].model, "sonnet")
		}
	})
}

// TestRegenerateTimeline_AllSessionsRendered seeds a large number of sessions
// (>= 39, the headline acceptance criterion for QUM-514) and asserts that
// every single one ends up as a row in the regenerated timeline, that every
// row passes ValidateTimelineRow, and that the rows are sorted ascending by
// date. This guards against silent truncation, dedup-on-id mistakes, and any
// off-by-one in batching/looping.
func TestRegenerateTimeline_AllSessionsRendered(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	const n = 40
	sessions := make(map[string]Session, n)
	for i := 0; i < n; i++ {
		// Build a syntactically-valid UUID using the loop index in the
		// final segment so each id is unique and shape-conforming.
		id := fmt.Sprintf("11111111-1111-1111-1111-%012x", i+1)
		// Spread over multiple months to exercise sort ordering across
		// month/day boundaries.
		day := (i % 28) + 1
		month := time.Month((i % 12) + 1)
		s := Session{
			SessionID:    id,
			Timestamp:    time.Date(2026, month, day, 12, 0, 0, 0, time.UTC),
			AgentsActive: []string{"weave"},
		}
		sessions[id] = s
		if err := WriteSessionSummary(root, s, "body of "+id); err != nil {
			t.Fatalf("WriteSessionSummary(%s): %v", id, err)
		}
	}

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	inv := &echoInvoker{sessions: sessions}

	err := RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Invoker:    inv,
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("RegenerateTimeline: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != n {
		t.Fatalf("expected %d lines, got %d", n, len(lines))
	}
	for i, l := range lines {
		if err := ValidateTimelineRow(l); err != nil {
			t.Errorf("row %d invalid: %q (%v)", i, l, err)
		}
	}
	prev := ""
	for i, l := range lines {
		date := l[:10]
		if i > 0 && date < prev {
			t.Errorf("row %d date %q < previous %q (not ascending)", i, date, prev)
		}
		prev = date
	}
}

// TestRegenerateTimeline_PlaceholderOnSummarizerFailure seeds a single
// session and configures an invoker that returns malformed rows on both
// attempts. The end-to-end pipeline must NOT abort or drop the session — it
// must emit PlaceholderRow(s) for that session into the output file, so a
// human can find and fix it later.
func TestRegenerateTimeline_PlaceholderOnSummarizerFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	id := "11111111-1111-1111-1111-111111111111"
	s := Session{
		SessionID: id,
		Timestamp: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	}
	if err := WriteSessionSummary(root, s, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	// Always-malformed invoker — both attempts return junk that fails
	// ValidateTimelineRow.
	inv := &recordingInvoker{responses: []string{
		"garbage attempt 1",
		"still garbage attempt 2",
	}}

	err := RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Invoker:    inv,
		Stdout:     io.Discard,
	})
	if err != nil {
		t.Fatalf("RegenerateTimeline: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), string(data))
	}
	want := PlaceholderRow(s)
	if lines[0] != want {
		t.Errorf("row = %q, want placeholder %q", lines[0], want)
	}
	if !strings.Contains(lines[0], "regenerate failed") {
		t.Errorf("row should signal placeholder; got %q", lines[0])
	}
}
