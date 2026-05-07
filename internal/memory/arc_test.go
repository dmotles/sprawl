package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// arcRecordingInvoker is a fake ClaudeInvoker for arc tests. It returns
// canned responses in order and records each call. When canned responses
// are exhausted, it returns the last response (or zero-value if none).
type arcRecordingInvoker struct {
	responses []string
	errs      []error
	calls     []arcRecordedCall
}

type arcRecordedCall struct {
	prompt string
	model  string
}

func (r *arcRecordingInvoker) Invoke(_ context.Context, prompt string, opts ...InvokeOption) (string, error) {
	var cfg invokeConfig
	for _, o := range opts {
		o(&cfg)
	}
	r.calls = append(r.calls, arcRecordedCall{prompt: prompt, model: cfg.model})
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

const sampleTimeline = `2026-05-01 11111111-1111-1111-1111-111111111111 | Started memory consolidation umbrella
2026-05-02 22222222-2222-2222-2222-222222222222 | Landed timeline regenerate slice
2026-05-03 33333333-3333-3333-3333-333333333333 | Began arc summarizer slice
`

func writeTimeline(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write timeline: %v", err)
	}
}

func defaultTimelinePath(root string) string {
	return filepath.Join(root, ".sprawl", "memory", "timeline.md")
}

func TestValidateArcSummary_OK(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("Milestone line under budget.\n", 6)
	s = strings.TrimRight(s, "\n")
	if err := ValidateArcSummary(s, 10, 1200); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateArcSummary_TooManyLines(t *testing.T) {
	t.Parallel()
	lines := make([]string, 11)
	for i := range lines {
		lines[i] = "line"
	}
	s := strings.Join(lines, "\n")
	if err := ValidateArcSummary(s, 10, 1200); err == nil {
		t.Error("expected error for 11 lines, got nil")
	}
}

func TestValidateArcSummary_TooManyChars(t *testing.T) {
	t.Parallel()
	s := strings.Repeat("a", 1201)
	if err := ValidateArcSummary(s, 10, 1200); err == nil {
		t.Error("expected error for 1201 chars, got nil")
	}
}

func TestValidateArcSummary_BoundaryLines(t *testing.T) {
	t.Parallel()
	// Build 10 short lines.
	parts := make([]string, 10)
	for i := range parts {
		parts[i] = "line"
	}
	ten := strings.Join(parts, "\n")
	if err := ValidateArcSummary(ten, 10, 1200); err != nil {
		t.Errorf("10 lines should be OK, got: %v", err)
	}
	parts11 := make([]string, 11)
	for i := range parts11 {
		parts11[i] = "line"
	}
	eleven := strings.Join(parts11, "\n")
	if err := ValidateArcSummary(eleven, 10, 1200); err == nil {
		t.Error("11 lines should fail")
	}
}

func TestValidateArcSummary_BoundaryChars(t *testing.T) {
	t.Parallel()
	exact := strings.Repeat("a", 1200)
	if err := ValidateArcSummary(exact, 10, 1200); err != nil {
		t.Errorf("1200 chars should be OK, got: %v", err)
	}
	over := strings.Repeat("a", 1201)
	if err := ValidateArcSummary(over, 10, 1200); err == nil {
		t.Error("1201 chars should fail")
	}
}

func TestSummarizeProjectArc_HappyPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	writeTimeline(t, tlPath, sampleTimeline)

	resp := "Milestone 1\nMilestone 2\nMilestone 3\nMilestone 4\nMilestone 5\nMilestone 6"
	inv := &arcRecordingInvoker{responses: []string{resp}}

	got, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != strings.TrimSpace(resp) {
		t.Errorf("got %q, want %q", got, strings.TrimSpace(resp))
	}
	if len(inv.calls) != 1 {
		t.Errorf("invoker called %d times, want 1", len(inv.calls))
	}
	if !strings.Contains(inv.calls[0].prompt, "Started memory consolidation umbrella") {
		t.Errorf("prompt should contain timeline content; got: %q", inv.calls[0].prompt)
	}
}

func TestSummarizeProjectArc_RetryOnOverflow(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	writeTimeline(t, tlPath, sampleTimeline)

	overflow := strings.Repeat("line\n", 15)
	good := "L1\nL2\nL3\nL4\nL5"
	inv := &arcRecordingInvoker{responses: []string{overflow, good}}

	got, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.calls) != 2 {
		t.Fatalf("invoker called %d times, want 2", len(inv.calls))
	}
	if !strings.Contains(inv.calls[1].prompt, "RETRY") {
		t.Errorf("second prompt should contain RETRY marker; got: %q", inv.calls[1].prompt)
	}
	if got != strings.TrimSpace(good) {
		t.Errorf("got %q, want %q", got, strings.TrimSpace(good))
	}
}

func TestSummarizeProjectArc_FallbackAfterTwoFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	writeTimeline(t, tlPath, sampleTimeline)

	overflow := strings.Repeat("line\n", 20)
	inv := &arcRecordingInvoker{responses: []string{overflow, overflow}}

	got, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(got, "summarization failed") {
		t.Errorf("fallback should have prefix 'summarization failed'; got: %q", got)
	}
	// Should contain some content from the timeline (best-effort truncation).
	if !strings.Contains(got, "memory consolidation") && !strings.Contains(got, "Started") &&
		!strings.Contains(got, "regenerate") && !strings.Contains(got, "arc summarizer") {
		t.Errorf("fallback should contain some timeline content; got: %q", got)
	}
	// Fallback must satisfy the char bound (≤1200).
	if len(got) > 1200 {
		t.Errorf("fallback length %d exceeds 1200", len(got))
	}
}

func TestSummarizeProjectArc_InvokerError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	writeTimeline(t, tlPath, sampleTimeline)

	wantErr := errors.New("invoker boom")
	inv := &arcRecordingInvoker{
		responses: []string{""},
		errs:      []error{wantErr},
	}

	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err == nil {
		t.Fatal("expected error to propagate, got nil")
	}
	if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), "invoker boom") {
		t.Errorf("error should propagate invoker error; got: %v", err)
	}
}

func TestSummarizeProjectArc_DefaultsModelHaiku(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	writeTimeline(t, tlPath, sampleTimeline)

	resp := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	inv := &arcRecordingInvoker{responses: []string{resp}}

	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.calls) == 0 {
		t.Fatal("invoker not called")
	}
	if inv.calls[0].model != "haiku" {
		t.Errorf("default model = %q, want %q", inv.calls[0].model, "haiku")
	}
}

func TestSummarizeProjectArc_CustomTimelinePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	customPath := filepath.Join(root, "custom-timeline.md")
	customBody := "2026-05-04 44444444-4444-4444-4444-444444444444 | CUSTOM SENTINEL CONTENT XYZ\n"
	writeTimeline(t, customPath, customBody)

	resp := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	inv := &arcRecordingInvoker{responses: []string{resp}}

	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot:   root,
		TimelinePath: customPath,
		Invoker:      inv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(inv.calls) == 0 {
		t.Fatal("invoker not called")
	}
	if !strings.Contains(inv.calls[0].prompt, "CUSTOM SENTINEL CONTENT XYZ") {
		t.Errorf("prompt should contain custom timeline body; got: %q", inv.calls[0].prompt)
	}
}

func TestSummarizeProjectArc_DefaultTimelinePathFromRoot(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	body := "2026-05-05 55555555-5555-5555-5555-555555555555 | DEFAULT-PATH-MARKER\n"
	writeTimeline(t, tlPath, body)

	resp := "Line 1\nLine 2\nLine 3\nLine 4\nLine 5"
	inv := &arcRecordingInvoker{responses: []string{resp}}

	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(inv.calls[0].prompt, "DEFAULT-PATH-MARKER") {
		t.Errorf("prompt should reflect default-path timeline; got: %q", inv.calls[0].prompt)
	}
}

func TestSummarizeProjectArc_TimelineMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// No timeline file written anywhere.
	inv := &arcRecordingInvoker{responses: []string{""}}

	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    inv,
	})
	if err == nil {
		t.Fatal("expected error when timeline file missing, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "timeline") && !strings.Contains(msg, "timeline.md") {
		t.Errorf("error should mention timeline file; got: %v", err)
	}
}

func TestSummarizeProjectArc_NilInvoker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tlPath := defaultTimelinePath(root)
	writeTimeline(t, tlPath, sampleTimeline)

	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot: root,
		Invoker:    nil,
	})
	if err == nil {
		t.Fatal("expected error when Invoker is nil, got nil")
	}
}

func TestSummarizeProjectArc_EmptyRoot(t *testing.T) {
	t.Parallel()
	inv := &arcRecordingInvoker{responses: []string{""}}
	_, err := SummarizeProjectArc(context.Background(), ArcOptions{
		SprawlRoot:   "",
		TimelinePath: "",
		Invoker:      inv,
	})
	if err == nil {
		t.Fatal("expected error when SprawlRoot and TimelinePath both empty, got nil")
	}
}
