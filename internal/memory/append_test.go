// internal/memory/append_test.go — failing red-phase tests for QUM-515
// (memory append-session). The implementation lives in
// internal/memory/append.go (not yet written); these tests therefore fail
// to compile until the implementer fills in AppendSession,
// AppendSessionWithOptions, AppendOptions, AppendResult,
// DefaultAppendLockTimeout, and ErrTimelineLockContended.
package memory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// uuidA..uuidE are syntactically-valid v4 UUIDs (the regex requires the
// 8-4-4-4-12 hex shape; we don't actually need RFC-correct version bits).
const (
	tuuidA = "11111111-1111-1111-1111-111111111111"
	tuuidB = "22222222-2222-2222-2222-222222222222"
	tuuidC = "33333333-3333-3333-3333-333333333333"
	tuuidD = "44444444-4444-4444-4444-444444444444"
	tuuidE = "55555555-5555-5555-5555-555555555555"
)

// appendTestTimelinePath returns the canonical timeline.md path for a given
// root. (Not named `timelinePath` to avoid collision with the existing
// helper in timeline.go.)
func appendTestTimelinePath(root string) string {
	return filepath.Join(root, ".sprawl", "memory", "timeline.md")
}

// appendTestLockPath returns the sibling flock path AppendSession is
// expected to use.
func appendTestLockPath(root string) string {
	return filepath.Join(root, ".sprawl", "memory", "timeline.md.lock")
}

// seedTimeline writes the given body to timeline.md, creating parent dirs.
func seedTimeline(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(appendTestTimelinePath(root)), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(appendTestTimelinePath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("seed timeline: %v", err)
	}
}

// seedSession writes a session summary file at the canonical path so
// AppendSession can find it.
func seedSession(t *testing.T, root string, s Session, body string) {
	t.Helper()
	if err := WriteSessionSummary(root, s, body); err != nil {
		t.Fatalf("WriteSessionSummary(%s): %v", s.SessionID, err)
	}
}

// summaryFor returns the fixed summary TEXT a fake invoker should return for
// s under the summary-only contract (QUM-639). The caller constructs the row
// deterministically, so feeding this through SummarizeSession yields exactly
// validRowFor(s).
func summaryFor(s Session) string {
	return "Append-session test row " + s.SessionID[:8]
}

// validRowFor returns the canonical row our code constructs for s from
// summaryFor(s). Used as the expected output and to seed pre-existing rows.
func validRowFor(s Session) string {
	return RenderTimelineRow(s.Timestamp, s.SessionID, summaryFor(s))
}

func mkSession(id string, year int, month time.Month, day int) Session {
	return Session{
		SessionID: id,
		Timestamp: time.Date(year, month, day, 12, 0, 0, 0, time.UTC),
	}
}

func runAppend(ctx context.Context, root string, s Session, inv ClaudeInvoker) (AppendResult, error) {
	return AppendSessionWithOptions(ctx, AppendOptions{
		SprawlRoot:  root,
		SessionID:   s.SessionID,
		Invoker:     inv,
		Cfg:         RegenerateConfig{Model: "haiku"},
		LockTimeout: DefaultAppendLockTimeout,
	})
}

// ---- tests ----------------------------------------------------------

func TestAppendSession_CreatesFileWhenMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "session body A")

	want := validRowFor(s)
	inv := &recordingInvoker{responses: []string{summaryFor(s)}}

	res, err := runAppend(context.Background(), root, s, inv)
	if err != nil {
		t.Fatalf("AppendSessionWithOptions: %v", err)
	}
	if res.NoOp {
		t.Errorf("res.NoOp = true, want false (file did not exist)")
	}
	if res.Row != want {
		t.Errorf("res.Row = %q, want %q", res.Row, want)
	}
	data, err := os.ReadFile(appendTestTimelinePath(root))
	if err != nil {
		t.Fatalf("read timeline.md: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), string(data))
	}
	if err := ValidateTimelineRow(lines[0]); err != nil {
		t.Errorf("written row failed validation: %v (%q)", err, lines[0])
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("file should end in newline; got %q", string(data))
	}
}

func TestAppendSession_Idempotent_NoOpWhenIDPresent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "body")

	existing := validRowFor(s) + "\n"
	seedTimeline(t, root, existing)

	before, err := os.ReadFile(appendTestTimelinePath(root))
	if err != nil {
		t.Fatalf("read pre: %v", err)
	}

	inv := &recordingInvoker{}
	res, err := runAppend(context.Background(), root, s, inv)
	if err != nil {
		t.Fatalf("AppendSessionWithOptions: %v", err)
	}
	if !res.NoOp {
		t.Errorf("res.NoOp = false, want true (id already present)")
	}
	if len(inv.calls) != 0 {
		t.Errorf("invoker called %d times, want 0 (no-op should not summarize)", len(inv.calls))
	}
	after, err := os.ReadFile(appendTestTimelinePath(root))
	if err != nil {
		t.Fatalf("read post: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("file mutated on no-op:\n  before=%q\n  after =%q", before, after)
	}
}

func TestAppendSession_SortedInsertMiddle(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	sA := mkSession(tuuidA, 2026, 1, 1)
	sB := mkSession(tuuidB, 2026, 2, 15)
	sC := mkSession(tuuidC, 2026, 3, 1)

	rowA := validRowFor(sA)
	rowC := validRowFor(sC)
	seedTimeline(t, root, rowA+"\n"+rowC+"\n")
	seedSession(t, root, sB, "body B")

	rowB := validRowFor(sB)
	inv := &recordingInvoker{responses: []string{summaryFor(sB)}}

	if _, err := runAppend(context.Background(), root, sB, inv); err != nil {
		t.Fatalf("AppendSessionWithOptions: %v", err)
	}
	data, err := os.ReadFile(appendTestTimelinePath(root))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), string(data))
	}
	if lines[0] != rowA || lines[1] != rowB || lines[2] != rowC {
		t.Errorf("out of order:\n  %q\n  %q\n  %q", lines[0], lines[1], lines[2])
	}
	for i, l := range lines {
		if err := ValidateTimelineRow(l); err != nil {
			t.Errorf("row %d invalid: %v", i, err)
		}
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("missing trailing newline")
	}
}

func TestAppendSession_SortedInsertHead(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	sB := mkSession(tuuidB, 2026, 5, 1)
	sC := mkSession(tuuidC, 2026, 5, 10)
	rowB := validRowFor(sB)
	rowC := validRowFor(sC)
	seedTimeline(t, root, rowB+"\n"+rowC+"\n")

	sA := mkSession(tuuidA, 2026, 1, 1)
	seedSession(t, root, sA, "body A")
	rowA := validRowFor(sA)
	inv := &recordingInvoker{responses: []string{summaryFor(sA)}}

	if _, err := runAppend(context.Background(), root, sA, inv); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _ := os.ReadFile(appendTestTimelinePath(root))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 || lines[0] != rowA || lines[1] != rowB || lines[2] != rowC {
		t.Errorf("head-insert wrong: %q", string(data))
	}
}

func TestAppendSession_SortedInsertTail(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	sA := mkSession(tuuidA, 2026, 1, 1)
	sB := mkSession(tuuidB, 2026, 2, 1)
	rowA := validRowFor(sA)
	rowB := validRowFor(sB)
	seedTimeline(t, root, rowA+"\n"+rowB+"\n")

	sC := mkSession(tuuidC, 2026, 12, 31)
	seedSession(t, root, sC, "body C")
	rowC := validRowFor(sC)
	inv := &recordingInvoker{responses: []string{summaryFor(sC)}}

	if _, err := runAppend(context.Background(), root, sC, inv); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _ := os.ReadFile(appendTestTimelinePath(root))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 || lines[0] != rowA || lines[1] != rowB || lines[2] != rowC {
		t.Errorf("tail-insert wrong: %q", string(data))
	}
}

func TestAppendSession_SameDateAppendsAfterEqualRows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	sA := mkSession(tuuidA, 2026, 2, 15)
	sB := mkSession(tuuidB, 2026, 2, 15)
	rowA := validRowFor(sA)
	rowB := validRowFor(sB)
	seedTimeline(t, root, rowA+"\n"+rowB+"\n")

	sC := mkSession(tuuidC, 2026, 2, 15)
	seedSession(t, root, sC, "body C")
	rowC := validRowFor(sC)
	inv := &recordingInvoker{responses: []string{summaryFor(sC)}}

	if _, err := runAppend(context.Background(), root, sC, inv); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _ := os.ReadFile(appendTestTimelinePath(root))
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), string(data))
	}
	if lines[0] != rowA || lines[1] != rowB || lines[2] != rowC {
		t.Errorf("stable same-date insert wrong:\n  %q\n  %q\n  %q", lines[0], lines[1], lines[2])
	}
}

func TestAppendSession_LockContention_ReturnsErrorWithinTimeout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Hold the lock externally.
	external := flock.New(appendTestLockPath(root))
	locked, err := external.TryLock()
	if err != nil {
		t.Fatalf("external TryLock: %v", err)
	}
	if !locked {
		t.Fatalf("could not acquire external lock for test setup")
	}
	defer func() { _ = external.Unlock() }()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "body")

	inv := &recordingInvoker{responses: []string{summaryFor(s)}}

	start := time.Now()
	_, err = AppendSessionWithOptions(context.Background(), AppendOptions{
		SprawlRoot:  root,
		SessionID:   s.SessionID,
		Invoker:     inv,
		Cfg:         RegenerateConfig{Model: "haiku"},
		LockTimeout: 200 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error on contended lock; got nil")
	}
	if !errors.Is(err, ErrTimelineLockContended) {
		t.Errorf("error not ErrTimelineLockContended: %v", err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("waited %v, expected timeout near 200ms", elapsed)
	}
}

func TestAppendSession_LockReleasedOnSuccess(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "body")
	inv := &recordingInvoker{responses: []string{summaryFor(s)}}

	if _, err := runAppend(context.Background(), root, s, inv); err != nil {
		t.Fatalf("append: %v", err)
	}
	fl := flock.New(appendTestLockPath(root))
	got, err := fl.TryLock()
	if err != nil {
		t.Fatalf("post-append TryLock: %v", err)
	}
	if !got {
		t.Errorf("lock was not released after successful append")
	}
	_ = fl.Unlock()
}

func TestAppendSession_DryRun_DoesNotWrite_FileMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "body")
	row := validRowFor(s)
	inv := &recordingInvoker{responses: []string{summaryFor(s)}}

	var stdout bytes.Buffer
	res, err := AppendSessionWithOptions(context.Background(), AppendOptions{
		SprawlRoot:  root,
		SessionID:   s.SessionID,
		DryRun:      true,
		Stdout:      &stdout,
		Invoker:     inv,
		Cfg:         RegenerateConfig{Model: "haiku"},
		LockTimeout: DefaultAppendLockTimeout,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if res.Row != row {
		t.Errorf("res.Row = %q, want %q", res.Row, row)
	}
	if _, err := os.Stat(appendTestTimelinePath(root)); !os.IsNotExist(err) {
		t.Errorf("dry-run created timeline.md (err=%v)", err)
	}
	if !strings.Contains(stdout.String(), row) {
		t.Errorf("stdout should contain candidate row; got %q", stdout.String())
	}
}

func TestAppendSession_DryRun_DoesNotMutateExisting(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	sA := mkSession(tuuidA, 2026, 1, 1)
	rowA := validRowFor(sA)
	seedTimeline(t, root, rowA+"\n")
	before, _ := os.ReadFile(appendTestTimelinePath(root))

	sB := mkSession(tuuidB, 2026, 6, 1)
	seedSession(t, root, sB, "body B")
	rowB := validRowFor(sB)
	inv := &recordingInvoker{responses: []string{summaryFor(sB)}}

	var stdout bytes.Buffer
	if _, err := AppendSessionWithOptions(context.Background(), AppendOptions{
		SprawlRoot:  root,
		SessionID:   sB.SessionID,
		DryRun:      true,
		Stdout:      &stdout,
		Invoker:     inv,
		LockTimeout: DefaultAppendLockTimeout,
	}); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	after, _ := os.ReadFile(appendTestTimelinePath(root))
	if !bytes.Equal(before, after) {
		t.Errorf("dry-run mutated existing file")
	}
	if !strings.Contains(stdout.String(), rowB) {
		t.Errorf("stdout missing candidate row %q; got %q", rowB, stdout.String())
	}
}

func TestAppendSession_EmptySummaryFallsBackToPlaceholder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "body")

	// Under the summary-only contract (QUM-639) the placeholder is produced
	// only when the sanitized model summary is empty/whitespace-only on both
	// the initial attempt and the single retry.
	inv := &recordingInvoker{responses: []string{"   ", ""}}
	res, err := runAppend(context.Background(), root, s, inv)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	want := PlaceholderRow(s)
	if res.Row != want {
		t.Errorf("res.Row = %q, want placeholder %q", res.Row, want)
	}
	if err := ValidateTimelineRow(res.Row); err != nil {
		t.Errorf("placeholder row failed validation: %v", err)
	}
	if len(inv.calls) != 2 {
		t.Errorf("invoker calls = %d, want 2 (one + retry)", len(inv.calls))
	}
	data, err := os.ReadFile(appendTestTimelinePath(root))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), want) {
		t.Errorf("timeline does not contain placeholder row; got %q", string(data))
	}
}

func TestAppendSession_InvokerError_Propagates_NoFileWrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	s := mkSession(tuuidA, 2026, 5, 1)
	seedSession(t, root, s, "body")

	inv := &recordingInvoker{
		responses: []string{""},
		errs:      []error{errors.New("network down")},
	}
	_, err := runAppend(context.Background(), root, s, inv)
	if err == nil {
		t.Fatal("expected error from invoker; got nil")
	}
	if !strings.Contains(err.Error(), "network down") {
		t.Errorf("error should wrap invoker error; got %v", err)
	}
	if _, statErr := os.Stat(appendTestTimelinePath(root)); !os.IsNotExist(statErr) {
		t.Errorf("timeline.md was created on invoker failure (statErr=%v)", statErr)
	}
}

func TestAppendSession_MissingSessionFile_ReturnsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Don't seed the session file.
	inv := &recordingInvoker{}
	_, err := AppendSessionWithOptions(context.Background(), AppendOptions{
		SprawlRoot:  root,
		SessionID:   tuuidA,
		Invoker:     inv,
		LockTimeout: DefaultAppendLockTimeout,
	})
	if err == nil {
		t.Fatal("expected error for missing session file")
	}
	if !strings.Contains(err.Error(), tuuidA) && !strings.Contains(err.Error(), "session") {
		t.Errorf("error should mention session/id; got %v", err)
	}
}

func TestAppendSession_PreservesUnrelatedRows(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	sA := mkSession(tuuidA, 2026, 1, 1)
	sB := mkSession(tuuidB, 2026, 2, 1)
	sC := mkSession(tuuidC, 2026, 3, 1)
	sD := mkSession(tuuidD, 2026, 4, 1)
	rowA := validRowFor(sA)
	rowB := validRowFor(sB)
	rowC := validRowFor(sC)
	rowD := validRowFor(sD)

	seedTimeline(t, root, rowA+"\n"+rowB+"\n"+rowC+"\n"+rowD+"\n")

	sE := mkSession(tuuidE, 2026, 5, 1)
	seedSession(t, root, sE, "body E")
	rowE := validRowFor(sE)
	inv := &recordingInvoker{responses: []string{summaryFor(sE)}}

	if _, err := runAppend(context.Background(), root, sE, inv); err != nil {
		t.Fatalf("append: %v", err)
	}
	data, _ := os.ReadFile(appendTestTimelinePath(root))
	for _, row := range []string{rowA, rowB, rowC, rowD, rowE} {
		if !strings.Contains(string(data), row) {
			t.Errorf("missing row after append: %q", row)
		}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("expected 5 lines, got %d", len(lines))
	}
}

// TestAppendSession_PublicWrapperUsesDefaults is a smoke check that the
// 3-arg public wrapper exists and is wired. It requires a real claude
// binary (which the implementer's NewCLIInvoker depends on); skip if
// unavailable.
func TestAppendSession_PublicWrapperUsesDefaults(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude binary not on PATH; skipping public wrapper smoke test")
	}
	root := t.TempDir()
	// Don't actually call claude — pass a session id without a file so we
	// fail fast inside the wrapper before any LLM call.
	err := AppendSession(root, tuuidA, "haiku")
	if err == nil {
		t.Error("expected error (no session file); got nil")
	}
}
