package memory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
		"2026-03-15 " + uuidD + " | " + strings.Repeat("a", 250),                      // exactly 250 chars (upper bound)
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
		{"summary too long (251 chars)", "2026-01-01 " + uuidA + " | " + strings.Repeat("a", 251)},
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
	// The model now returns plain summary TEXT only — our code builds the row.
	inv := &recordingInvoker{responses: []string{"First-try success summary"}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "session body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := RenderTimelineRow(s.Timestamp, id, "First-try success summary")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(inv.calls) != 1 {
		t.Errorf("invoker called %d times, want 1", len(inv.calls))
	}
}

func TestSummarizeSession_RetryOnEmpty(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	// First response is whitespace-only → sanitizes to "" → forces a retry.
	// Second response is a plain, usable summary.
	good := "Retry produced a good summary"
	inv := &recordingInvoker{responses: []string{
		"   ",
		good,
	}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := RenderTimelineRow(s.Timestamp, id, sanitizeSummary(good))
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if err := ValidateTimelineRow(got); err != nil {
		t.Errorf("retry row %q failed validation: %v", got, err)
	}
	if !strings.HasPrefix(got, "2026-05-07 "+id+" | ") {
		t.Errorf("row prefix wrong: %q", got)
	}
	if len(inv.calls) != 2 {
		t.Errorf("invoker called %d times, want 2", len(inv.calls))
	}
}

func TestSummarizeSession_FallbackOnEmpty(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	// Both attempts sanitize to "" → placeholder fallback.
	inv := &recordingInvoker{responses: []string{
		"  \n\t ",
		"",
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

func TestSanitizeSummary_CollapsesWhitespace(t *testing.T) {
	t.Parallel()
	out := sanitizeSummary("line one\nline two\twith\rtabs   and   spaces")
	if strings.ContainsAny(out, "\n\r\t") {
		t.Errorf("output still contains newline/tab/cr: %q", out)
	}
	if strings.Contains(out, "  ") {
		t.Errorf("output still contains double space: %q", out)
	}
	if out == "" {
		t.Errorf("output unexpectedly empty")
	}
}

func TestSanitizeSummary_TrimsSurrounding(t *testing.T) {
	t.Parallel()
	if out := sanitizeSummary("  padded summary  "); out != "padded summary" {
		t.Errorf("got %q, want %q", out, "padded summary")
	}
}

func TestSanitizeSummary_TruncatesWithMarker(t *testing.T) {
	t.Parallel()
	out := sanitizeSummary(strings.Repeat("a", 400))
	if len(out) > 250 {
		t.Errorf("output len %d exceeds 250: %q", len(out), out)
	}
	if !strings.HasSuffix(out, "… (see session file)") {
		t.Errorf("truncated output missing marker suffix: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("output is not valid UTF-8: %q", out)
	}
	ts := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	id := "550e8400-e29b-41d4-a716-446655440000"
	if err := ValidateTimelineRow(RenderTimelineRow(ts, id, out)); err != nil {
		t.Errorf("row built from truncated summary failed validation: %v", err)
	}
}

func TestSanitizeSummary_TruncationRuneBoundary(t *testing.T) {
	t.Parallel()
	// 227 ASCII bytes + 20 two-byte runes (é) = 267 bytes; truncation must
	// land on a rune boundary, never splitting a multibyte rune.
	out := sanitizeSummary(strings.Repeat("a", 227) + strings.Repeat("é", 20))
	if !utf8.ValidString(out) {
		t.Errorf("truncation split a multibyte rune: %q", out)
	}
	if len(out) > 250 {
		t.Errorf("output len %d exceeds 250", len(out))
	}
}

func TestSanitizeSummary_Empty(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "   ", "\n\t\r"} {
		if out := sanitizeSummary(in); out != "" {
			t.Errorf("sanitizeSummary(%q) = %q, want empty", in, out)
		}
	}
}

func TestSummarizeSession_PrefixDeterministicEvenIfModelEchoesDateUUID(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	// The model echoes a foreign date + UUID from the session body. Our code
	// builds the prefix deterministically from the session, so the foreign
	// date/uuid must NOT appear as the row prefix.
	inv := &recordingInvoker{responses: []string{
		"2099-12-31 ffffffff-ffff-ffff-ffff-ffffffffffff | injected",
	}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "2026-05-07 "+id+" | ") {
		t.Errorf("row prefix not deterministic from session: %q", got)
	}
	if err := ValidateTimelineRow(got); err != nil {
		t.Errorf("row failed validation: %v", err)
	}
	if !strings.Contains(got, "injected") {
		t.Errorf("row should retain the model summary text; got %q", got)
	}
}

func TestSummarizeSession_MultiLineModelOutputBecomesValidRow(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	inv := &recordingInvoker{responses: []string{"First line.\nSecond line."}}

	got, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := RenderTimelineRow(s.Timestamp, id, "First line. Second line.")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if err := ValidateTimelineRow(got); err != nil {
		t.Errorf("multi-line model output produced invalid row: %v", err)
	}
	if len(inv.calls) != 1 {
		t.Errorf("invoker called %d times, want 1", len(inv.calls))
	}
}

func TestSummarizeSession_PromptOmitsSessionID(t *testing.T) {
	t.Parallel()
	id := "550e8400-e29b-41d4-a716-446655440000"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)}
	inv := &recordingInvoker{responses: []string{"ok summary"}}

	if _, err := SummarizeSession(context.Background(), inv, RegenerateConfig{}, s, "body"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.calls) == 0 {
		t.Fatal("invoker not called")
	}
	prompt := inv.calls[0].prompt
	// The prompt should ask for summary TEXT ONLY — no prefix to reproduce —
	// so neither the session id nor the date should be embedded in it.
	if strings.Contains(prompt, id) {
		t.Errorf("prompt should not contain session id %q; got: %q", id, prompt)
	}
	if strings.Contains(prompt, "2026-05-07") {
		t.Errorf("prompt should not contain the session date; got: %q", prompt)
	}
}

// echoInvoker returns a fixed, non-empty summary for any session. Under the
// summary-only contract (QUM-639) the model emits summary TEXT only and the
// caller constructs the deterministic row prefix, so the fake no longer needs
// to recover the session id/date from the prompt — it just returns prose that
// sanitizeSummary keeps verbatim, letting tests assert on row order/validity
// without caring about LLM-specific wording.
type echoInvoker struct{}

func (e *echoInvoker) Invoke(_ context.Context, _ string, _ ...InvokeOption) (string, error) {
	return "Echo summary", nil
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
	inv := &echoInvoker{}

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

	inv := &echoInvoker{}

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
	inv := &echoInvoker{}

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
	inv := &echoInvoker{}

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
	// Both attempts sanitize to "" → placeholder fallback.
	inv := &recordingInvoker{responses: []string{
		"",
		"   ",
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

// TestRegenerateTimeline_PerSessionInvokerErrorDegradesToPlaceholder seeds
// three sessions and configures an invoker that errors for exactly one of
// them (a transient claude failure). The run must NOT abort: the other two
// sessions are still summarized and the failing one degrades to a
// PlaceholderRow, so a single flaky LLM call cannot discard the whole batch
// (QUM-641).
func TestRegenerateTimeline_PerSessionInvokerErrorDegradesToPlaceholder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	idA := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	idB := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	idC := "cccccccc-cccc-cccc-cccc-cccccccccccc"
	sA := Session{SessionID: idA, Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	sB := Session{SessionID: idB, Timestamp: time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)}
	sC := Session{SessionID: idC, Timestamp: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	if err := WriteSessionSummary(root, sA, "body A"); err != nil {
		t.Fatalf("WriteSessionSummary(A): %v", err)
	}
	if err := WriteSessionSummary(root, sB, "body B"); err != nil {
		t.Fatalf("WriteSessionSummary(B): %v", err)
	}
	if err := WriteSessionSummary(root, sC, "body C"); err != nil {
		t.Fatalf("WriteSessionSummary(C): %v", err)
	}

	// keyedInvoker errors on the session whose body contains "body B"; the
	// others return valid summary text.
	inv := &keyedInvoker{
		responses: map[string]string{
			"body A": "Summary A",
			"body C": "Summary C",
		},
		errs: map[string]error{
			"body B": fmt.Errorf("transient claude failure for B"),
		},
	}

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	if err := RegenerateTimeline(context.Background(), RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Invoker:    inv,
		Stdout:     io.Discard,
	}); err != nil {
		t.Fatalf("RegenerateTimeline must not abort on a single per-session error: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 rows (A, B-placeholder, C), got %d: %q", len(lines), string(data))
	}
	// Rows are sorted ascending by date: A (Jan) < B (Feb) < C (Mar).
	wantA := RenderTimelineRow(sA.Timestamp, idA, "Summary A")
	wantB := PlaceholderRow(sB)
	wantC := RenderTimelineRow(sC.Timestamp, idC, "Summary C")
	if lines[0] != wantA {
		t.Errorf("row 0 = %q, want %q", lines[0], wantA)
	}
	if lines[1] != wantB {
		t.Errorf("failed session B should degrade to placeholder; row 1 = %q, want %q", lines[1], wantB)
	}
	if lines[2] != wantC {
		t.Errorf("row 2 = %q, want %q", lines[2], wantC)
	}
}

// TestRegenerateTimeline_AbortsOnContextCancellation confirms that a
// cancellation of the overall operation still aborts the run — we must not
// mask a genuine ctx cancellation as a per-session degradation (QUM-641).
func TestRegenerateTimeline_AbortsOnContextCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	id := "11111111-1111-1111-1111-111111111111"
	s := Session{SessionID: id, Timestamp: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
	if err := WriteSessionSummary(root, s, "body"); err != nil {
		t.Fatalf("WriteSessionSummary: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // overall operation canceled before we start

	out := filepath.Join(root, ".sprawl", "memory", "timeline.md.next")
	inv := &recordingInvoker{errs: []error{fmt.Errorf("invoker call under canceled ctx")}}
	err := RegenerateTimeline(ctx, RegenerateOptions{
		SprawlRoot: root,
		OutPath:    out,
		Invoker:    inv,
		Stdout:     io.Discard,
	})
	if err == nil {
		t.Fatal("expected RegenerateTimeline to abort when the overall context is canceled")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("aborted run should not have written output file (statErr=%v)", statErr)
	}
}
