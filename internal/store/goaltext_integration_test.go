//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The goal-text spill against a real Postgres (QUM-1347).
//
// Only a real server can establish this one: the property is that a
// file-sized brief and a file-sized result ACTUALLY APPEND, and what would
// refuse them is events_payload_thin_ck — a CHECK the hermetic fake does not
// have, so the unit suite cannot tell a spilled payload from an oversized one.
// The read-back half matters just as much: an artifact nothing can reach is a
// result that was recorded and lost.

// bigText is comfortably over the 8KiB payload CHECK, which is where the spill
// begins, with newlines because a markdown brief is mostly newlines and
// each one costs two bytes once JSON-escaped.
func bigText(t *testing.T) string {
	t.Helper()
	s := strings.Repeat("a line of a very long research brief.\n", 700) // ~26KB
	if len(s) <= eventPayloadMaxBytes {
		t.Fatalf("the fixture is only %d bytes, so it would fit the payload and assert nothing", len(s))
	}
	return s
}

// artifactContentFor reads the content of the artifact one event references.
// Answered by SQL rather than by a Go helper because `artifacts` has no read
// path in this package — which is exactly why the reference is load-bearing.
func (e *spawnEnv) artifactContentFor(t *testing.T, eventID uuid.UUID) string {
	t.Helper()
	var content string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT a.content FROM events e JOIN artifacts a ON a.id = e.artifact_id WHERE e.id = $1`,
		eventID).Scan(&content); err != nil {
		t.Fatalf("reading the artifact %s references: %v", eventID, err)
	}
	return content
}

func TestGoalTextPg_ABriefLargerThanThePayloadCapRoundTrips(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()
	brief := bigText(t)

	goal, err := e.ledger.OpenGoal(ctx, GoalResearch, brief, "weave")
	if err != nil {
		t.Fatalf("opening a goal with a %d-byte brief: %v", len(brief), err)
	}
	if got := e.artifactContentFor(t, goal.GoalEventID); got != brief {
		t.Errorf("the brief read back as %d bytes, want the %d that were passed in", len(got), len(brief))
	}

	var payload string
	if err := e.pool.QueryRow(ctx, `SELECT payload->>'text' FROM events WHERE id = $1`, goal.GoalEventID).Scan(&payload); err != nil {
		t.Fatalf("reading the payload back: %v", err)
	}
	if !strings.HasPrefix(brief, payload[:64]) {
		t.Errorf("the inline remnant is not the start of the brief: %q", payload[:64])
	}
	if !strings.Contains(payload, "truncated") {
		t.Errorf("the inline remnant does not say the rest is elsewhere: %q", payload)
	}
}

func TestGoalTextPg_AResultLargerThanThePayloadCapRoundTrips(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()
	result := bigText(t)

	goal := e.openGoalFor(t, "finn", "research", uuid.New())
	closeID, err := e.ledger.CloseGoalForAgent(ctx, "finn", goal, GoalSucceeded, result)
	if err != nil {
		t.Fatalf("closing a goal with a %d-byte summary: %v", len(result), err)
	}
	if got := e.artifactContentFor(t, closeID); got != result {
		t.Errorf("the result read back as %d bytes, want the %d that were passed in", len(got), len(result))
	}

	// The contract is still discharged: a spilled summary must not change what
	// the close DOES, or a large result would leave the goal open forever.
	open, err := e.goals.OpenGoalsForAgent(ctx, e.projectID, "finn")
	if err != nil {
		t.Fatalf("re-reading finn's goals: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("finn has %d open goals after a large-summary close, want 0", len(open))
	}
}

// TestGoalTextPg_AnOrdinarySummaryStaysInThePayload is the negative control for
// both tests above: without it, an implementation that spilled EVERY field
// passes them and quietly turns every one-line result into an artifact row and
// a truncation marker.
func TestGoalTextPg_AnOrdinarySummaryStaysInThePayload(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	goal := e.openGoalFor(t, "finn", "research", uuid.New())
	closeID, err := e.ledger.CloseGoalForAgent(ctx, "finn", goal, GoalSucceeded, "did the thing")
	if err != nil {
		t.Fatalf("CloseGoalForAgent: %v", err)
	}

	var summary string
	var artifactID *uuid.UUID
	if err := e.pool.QueryRow(ctx,
		`SELECT payload->>'summary', artifact_id FROM events WHERE id = $1`, closeID).Scan(&summary, &artifactID); err != nil {
		t.Fatalf("reading the close back: %v", err)
	}
	if summary != "did the thing" {
		t.Errorf("a short summary was rewritten to %q", summary)
	}
	if artifactID != nil {
		t.Errorf("a short summary was spilled to artifact %s", artifactID)
	}
}

// TestGoalTextPg_AMidSizedSummaryTheCHECKAcceptsStaysInline.
//
// The regression guard for "inline input unchanged". A few-thousand-byte
// summary is over half the payload cap but well inside it, and the database
// has always accepted it whole; a spill keyed on the field rather than on the
// payload silently replaced it with a 617-byte remnant, which the spawn and
// rework prompts read as the entire text. Only a real server can say what the
// CHECK actually accepts, so the claim is settled here.
func TestGoalTextPg_AMidSizedSummaryTheCHECKAcceptsStaysInline(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()
	summary := strings.Repeat("a line of an ordinary but not short result.\n", 110) // ~4.8KB
	if len(summary) <= eventPayloadMaxBytes/2 {
		t.Fatalf("the fixture is %d bytes, under the old half-the-cap threshold, so it would not have spilled and asserts nothing", len(summary))
	}
	if len(summary) >= eventPayloadMaxBytes {
		t.Fatalf("the fixture is %d bytes, over the payload cap, so staying inline would be the wrong answer", len(summary))
	}

	goal := e.openGoalFor(t, "finn", "research", uuid.New())
	closeID, err := e.ledger.CloseGoalForAgent(ctx, "finn", goal, GoalSucceeded, summary)
	if err != nil {
		t.Fatalf("closing with a %d-byte summary the payload CHECK accepts: %v", len(summary), err)
	}

	var got string
	var artifactID *uuid.UUID
	if err := e.pool.QueryRow(ctx,
		`SELECT payload->>'summary', artifact_id FROM events WHERE id = $1`, closeID).Scan(&got, &artifactID); err != nil {
		t.Fatalf("reading the close back: %v", err)
	}
	if got != summary {
		t.Errorf("the summary came back as %d of its %d bytes — a reader of payload.summary sees a truncated result", len(got), len(summary))
	}
	if artifactID != nil {
		t.Errorf("a summary that fits the payload was spilled to artifact %s", artifactID)
	}
}

// TestGoalTextPg_ACloseRefusesWhenTheSummaryCannotBeStored.
//
// The caller half of the spill-failure contract. `putTextField` returning an
// error is pinned by the unit suite; that `CloseGoalForAgent` PROPAGATES it is
// not, and a close is irreversible — ignoring the error records a permanent
// result whose body was dropped and leaves nobody able to tell.
//
// The failure is injected with a real one rather than a fake: Postgres refuses
// a NUL byte in a text column, and a file an agent redirected a binary into is
// exactly how a NUL reaches this path.
func TestGoalTextPg_ACloseRefusesWhenTheSummaryCannotBeStored(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()
	unstorable := bigText(t) + "\x00"

	goal := e.openGoalFor(t, "finn", "research", uuid.New())
	if _, err := e.ledger.CloseGoalForAgent(ctx, "finn", goal, GoalSucceeded, unstorable); err == nil {
		t.Fatal("a goal was closed although its summary could not be stored; the result is permanently lost")
	}
	// The close must not have happened AT ALL: an agent that sees an error can
	// retry, but only if the goal is still open.
	open, err := e.goals.OpenGoalsForAgent(ctx, e.projectID, "finn")
	if err != nil {
		t.Fatalf("re-reading finn's goals: %v", err)
	}
	if len(open) != 1 {
		t.Errorf("finn has %d open goals after a refused close, want 1 — the goal was closed without its summary", len(open))
	}
}

// TestGoalTextPg_ASummaryAtTheEXACTGoMeasuredCapStillAppends.
//
// The column is jsonb, and `jsonb::text` renders a space after every `:` and
// every `,` while encoding/json emits none — so Postgres measures ~2 bytes per
// key MORE than Go does. A spill decision taken at exactly the Go-measured
// 8192 therefore admits a payload the CHECK refuses, and on a close that is a
// result that cannot be recorded at all. The window is a few bytes wide, which
// is why it needs a fixture tuned to sit in it rather than a big round number.
func TestGoalTextPg_ASummaryAtTheEXACTGoMeasuredCapStillAppends(t *testing.T) {
	e := newGoalReaderEnv(t)
	ctx := context.Background()

	// ASCII only, so one byte of summary is one byte of JSON: pad until the
	// payload the store will build encodes to exactly the cap.
	base := len(mustMarshalPayload(t, map[string]any{"outcome": string(GoalSucceeded), "summary": ""}))
	summary := strings.Repeat("z", eventPayloadMaxBytes-base)
	if n := len(mustMarshalPayload(t, map[string]any{"outcome": string(GoalSucceeded), "summary": summary})); n != eventPayloadMaxBytes {
		t.Fatalf("the fixture encodes to %d bytes, not the %d-byte cap it is supposed to sit exactly on", n, eventPayloadMaxBytes)
	}

	goal := e.openGoalFor(t, "finn", "research", uuid.New())
	closeID, err := e.ledger.CloseGoalForAgent(ctx, "finn", goal, GoalSucceeded, summary)
	if err != nil {
		t.Fatalf("closing with a summary at exactly the Go-measured cap: %v", err)
	}

	// However it was stored — inline or spilled — the full text must be
	// recoverable, because the close is permanent.
	var inline string
	var artifactID *uuid.UUID
	if err := e.pool.QueryRow(ctx,
		`SELECT payload->>'summary', artifact_id FROM events WHERE id = $1`, closeID).Scan(&inline, &artifactID); err != nil {
		t.Fatalf("reading the close back: %v", err)
	}
	if inline != summary {
		if artifactID == nil {
			t.Fatalf("the summary came back as %d of its %d bytes and no artifact holds the rest", len(inline), len(summary))
		}
		if got := e.artifactContentFor(t, closeID); got != summary {
			t.Errorf("the artifact holds %d bytes, want the %d passed in", len(got), len(summary))
		}
	}
}

func mustMarshalPayload(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}
