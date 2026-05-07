// internal/memory/consolidate_test.go — red-phase tests for QUM-517 cutover.
// The new Consolidate is append-only: it walks every session on disk, and
// for any session id NOT already present in timeline.md it calls
// AppendSessionWithOptions (which makes a single LLM call and merges one
// canonical row). Per-session errors are non-fatal — they must not abort
// the loop.
package memory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// consolidateUUIDs are syntactically-valid v4-shaped UUIDs (matching
// TimelineRowRE). Reusing the same shape as the rest of the package's
// test fixtures.
const (
	cuidA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	cuidB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	cuidC = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	cuidD = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	cuidE = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
)

// keyedInvoker is a ClaudeInvoker fake that returns canned responses keyed
// by a substring of the prompt (typically the session UUID, since
// SummarizeSession embeds the session id directly in the prompt). It can
// also fail per-key when an entry is registered in errs.
type keyedInvoker struct {
	responses map[string]string
	errs      map[string]error
	calls     int
}

func (k *keyedInvoker) Invoke(_ context.Context, prompt string, _ ...InvokeOption) (string, error) {
	k.calls++
	for key, err := range k.errs {
		if strings.Contains(prompt, key) {
			return "", err
		}
	}
	for key, resp := range k.responses {
		if strings.Contains(prompt, key) {
			return resp, nil
		}
	}
	return "", errors.New("keyedInvoker: no response registered for prompt")
}

func consolidateNow() func() time.Time {
	return func() time.Time { return time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC) }
}

// writeUUIDSession writes a session summary file at the canonical path so
// ListRecentSessions can find it. UUID-shaped session ids are required for
// rows to match TimelineRowRE.
func writeUUIDSession(t *testing.T, root, id string, ts time.Time, body string) Session {
	t.Helper()
	s := Session{
		SessionID:    id,
		Timestamp:    ts,
		Handoff:      false,
		AgentsActive: []string{"weave"},
	}
	if err := WriteSessionSummary(root, s, body); err != nil {
		t.Fatalf("WriteSessionSummary(%s): %v", id, err)
	}
	return s
}

// canonicalRow returns a TimelineRowRE-valid row for s.
func canonicalRow(s Session, summary string) string {
	return RenderTimelineRow(s.Timestamp, s.SessionID, summary)
}

// readTimelineFile returns timeline.md contents (or empty string if absent).
func readTimelineFile(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".sprawl", "memory", "timeline.md"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read timeline.md: %v", err)
	}
	return string(data)
}

func nonEmptyLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ---------------------------------------------------------------------
// EmptyTimelinePopulatedWithAllSessions
// ---------------------------------------------------------------------
func TestConsolidate_EmptyTimelinePopulatedWithAllSessions(t *testing.T) {
	root := t.TempDir()

	sA := writeUUIDSession(t, root, cuidA, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "body A")
	sB := writeUUIDSession(t, root, cuidB, time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC), "body B")
	sC := writeUUIDSession(t, root, cuidC, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), "body C")

	rowA := canonicalRow(sA, "Summary of session A")
	rowB := canonicalRow(sB, "Summary of session B")
	rowC := canonicalRow(sC, "Summary of session C")

	inv := &keyedInvoker{responses: map[string]string{
		cuidA: rowA,
		cuidB: rowB,
		cuidC: rowC,
	}}

	if err := Consolidate(context.Background(), root, inv, nil, consolidateNow()); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	got := readTimelineFile(t, root)
	if got == "" {
		t.Fatal("timeline.md not created")
	}
	lines := nonEmptyLines(got)
	if len(lines) != 3 {
		t.Fatalf("got %d rows, want 3:\n%s", len(lines), got)
	}
	for i, ln := range lines {
		if err := ValidateTimelineRow(ln); err != nil {
			t.Errorf("row %d invalid: %v (%q)", i, err, ln)
		}
	}
	// Sorted ascending by date prefix (2026-01 < 2026-02 < 2026-03).
	if lines[0][:10] > lines[1][:10] || lines[1][:10] > lines[2][:10] {
		t.Errorf("rows not sorted ascending by date:\n%s", got)
	}
}

// ---------------------------------------------------------------------
// AllSessionsAlreadyPresent_NoOp
// ---------------------------------------------------------------------
func TestConsolidate_AllSessionsAlreadyPresent_NoOp(t *testing.T) {
	root := t.TempDir()

	sA := writeUUIDSession(t, root, cuidA, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "body A")
	sB := writeUUIDSession(t, root, cuidB, time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC), "body B")
	sC := writeUUIDSession(t, root, cuidC, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), "body C")

	existing := canonicalRow(sA, "old A") + "\n" +
		canonicalRow(sB, "old B") + "\n" +
		canonicalRow(sC, "old C") + "\n"
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "memory", "timeline.md"), []byte(existing), 0o644); err != nil {
		t.Fatalf("seed timeline: %v", err)
	}

	before := readTimelineFile(t, root)
	inv := &keyedInvoker{} // no responses registered → must not be called

	if err := Consolidate(context.Background(), root, inv, nil, consolidateNow()); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	if inv.calls != 0 {
		t.Errorf("invoker called %d times, want 0 (all sessions already present)", inv.calls)
	}
	after := readTimelineFile(t, root)
	if !bytes.Equal([]byte(before), []byte(after)) {
		t.Errorf("timeline mutated on no-op:\nbefore=%q\nafter =%q", before, after)
	}
}

// ---------------------------------------------------------------------
// AppendsOnlyMissingSessions
// ---------------------------------------------------------------------
func TestConsolidate_AppendsOnlyMissingSessions(t *testing.T) {
	root := t.TempDir()

	sA := writeUUIDSession(t, root, cuidA, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "body A")
	sB := writeUUIDSession(t, root, cuidB, time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC), "body B")
	sC := writeUUIDSession(t, root, cuidC, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), "body C")
	sD := writeUUIDSession(t, root, cuidD, time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC), "body D")
	sE := writeUUIDSession(t, root, cuidE, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC), "body E")

	// Seed with A,B,C only; D,E are missing.
	rowA := canonicalRow(sA, "kept A")
	rowB := canonicalRow(sB, "kept B")
	rowC := canonicalRow(sC, "kept C")
	existing := rowA + "\n" + rowB + "\n" + rowC + "\n"
	if err := os.MkdirAll(filepath.Join(root, ".sprawl", "memory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sprawl", "memory", "timeline.md"), []byte(existing), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rowD := canonicalRow(sD, "appended D")
	rowE := canonicalRow(sE, "appended E")
	inv := &keyedInvoker{responses: map[string]string{
		cuidD: rowD,
		cuidE: rowE,
	}}

	if err := Consolidate(context.Background(), root, inv, nil, consolidateNow()); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	if inv.calls != 2 {
		t.Errorf("invoker calls = %d, want 2 (only D and E missing)", inv.calls)
	}

	got := readTimelineFile(t, root)
	lines := nonEmptyLines(got)
	if len(lines) != 5 {
		t.Fatalf("got %d rows, want 5:\n%s", len(lines), got)
	}

	// Original 3 rows preserved verbatim.
	for _, want := range []string{rowA, rowB, rowC} {
		if !strings.Contains(got, want) {
			t.Errorf("original row missing after consolidate: %q\nfile:\n%s", want, got)
		}
	}
	// The two new sessions are appended.
	for _, want := range []string{rowD, rowE} {
		if !strings.Contains(got, want) {
			t.Errorf("missing appended row %q\nfile:\n%s", want, got)
		}
	}
}

// ---------------------------------------------------------------------
// NoSessionsOnDisk_NoOp
// ---------------------------------------------------------------------
func TestConsolidate_NoSessionsOnDisk_NoOp(t *testing.T) {
	root := t.TempDir()
	// No sessions written.

	inv := &keyedInvoker{}
	if err := Consolidate(context.Background(), root, inv, nil, consolidateNow()); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if inv.calls != 0 {
		t.Errorf("invoker called %d times, want 0", inv.calls)
	}
	if got := readTimelineFile(t, root); got != "" {
		t.Errorf("timeline created with no sessions: %q", got)
	}
}

// ---------------------------------------------------------------------
// PerSessionInvocationFailure_OthersStillAppend
// ---------------------------------------------------------------------
func TestConsolidate_PerSessionInvocationFailure_OthersStillAppend(t *testing.T) {
	root := t.TempDir()

	sA := writeUUIDSession(t, root, cuidA, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), "body A")
	_ = writeUUIDSession(t, root, cuidB, time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC), "body B")
	sC := writeUUIDSession(t, root, cuidC, time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC), "body C")

	rowA := canonicalRow(sA, "Summary A")
	rowC := canonicalRow(sC, "Summary C")
	inv := &keyedInvoker{
		responses: map[string]string{
			cuidA: rowA,
			cuidC: rowC,
		},
		errs: map[string]error{
			cuidB: errors.New("transient invoker failure for B"),
		},
	}

	// Best-effort: per-session errors must not abort the run; Consolidate
	// returns nil. (If implementer chooses to surface the error instead,
	// this assertion is the correct point of failure to revisit.)
	if err := Consolidate(context.Background(), root, inv, nil, consolidateNow()); err != nil {
		t.Fatalf("Consolidate must be best-effort across session failures, got: %v", err)
	}

	got := readTimelineFile(t, root)
	if !strings.Contains(got, rowA) {
		t.Errorf("session A row missing despite B failing\n%s", got)
	}
	if !strings.Contains(got, rowC) {
		t.Errorf("session C row missing despite B failing\n%s", got)
	}
	// B should NOT be present (its summarization failed).
	if strings.Contains(got, cuidB) {
		// AppendSessionWithOptions falls back to PlaceholderRow only on
		// validation failure — a transient invoker error is propagated, so
		// B should be absent from the timeline entirely.
		t.Errorf("session B should be absent after invoker error; got:\n%s", got)
	}
}
