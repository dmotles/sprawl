//go:build store_pg

package store

import (
	"context"
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

// bigText is comfortably over both the 4KiB spill threshold and the 8KiB
// payload CHECK, with newlines because a markdown brief is mostly newlines and
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
