package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Unit tests for the goal-text spill (QUM-1347).
//
// The property under test is the one the database enforces and nothing else
// does: `events` caps a payload at 8KiB, and a goal's text or a result's summary
// can now be a whole file's contents. A long field must therefore land in
// `artifacts` with only a digest, a size and a readable prefix left in the
// payload — and a SHORT one must not, or every ordinary goal grows an artifact
// row and a truncation marker it does not need.

func newTextLedger(t *testing.T) (*Ledger, *recordingPool) {
	t.Helper()
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	return &Ledger{
		enabled:   true,
		registry:  reg,
		projectID: uuid.New(),
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
	}, pool
}

// TestPutTextField_ShortTextStaysInThePayload is the negative control for the
// spill: without it, a spill that fires unconditionally passes every assertion
// in the test below it.
func TestPutTextField_ShortTextStaysInThePayload(t *testing.T) {
	l, pool := newTextLedger(t)
	payload := map[string]any{}

	id, err := l.putTextField(context.Background(), payload, "text", artifactKindGoalText, "find out how the cursor is derived")
	if err != nil {
		t.Fatalf("putTextField: %v", err)
	}
	if id != nil {
		t.Errorf("a short text was given artifact %s; only oversized fields spill", id)
	}
	if payload["text"] != "find out how the cursor is derived" {
		t.Errorf("the payload field was rewritten: %v", payload["text"])
	}
	if _, spilled := payload["text_sha256"]; spilled {
		t.Error("a short text carries digest metadata it does not need")
	}
	if _, ok := artifactInsertArgs(pool); ok {
		t.Error("a short text wrote an artifact row")
	}
}

// TestPutTextField_LongTextGoesToAnArtifact.
//
// Three assertions, and all three are load-bearing: the artifact must hold the
// content VERBATIM (it is the source of truth), the payload must stay under the
// database's cap (or the append fails), and the payload's remnant must say where
// the rest went (or a reader sees a silently truncated result).
func TestPutTextField_LongTextGoesToAnArtifact(t *testing.T) {
	l, pool := newTextLedger(t)
	payload := map[string]any{"outcome": "success"}
	long := strings.Repeat("a very long research result. ", 1000) // ~29KB

	id, err := l.putTextField(context.Background(), payload, "summary", artifactKindGoalResult, long)
	if err != nil {
		t.Fatalf("putTextField: %v", err)
	}
	if id == nil {
		t.Fatal("an oversized summary was left in the payload; the append would be refused by events_payload_thin_ck")
	}
	if *id != pool.artifactID {
		t.Errorf("the returned id is %s, want the id the artifacts insert returned (%s)", id, pool.artifactID)
	}

	args, ok := artifactInsertArgs(pool)
	if !ok {
		t.Fatalf("no artifact was inserted; statements were %v", pool.log())
	}
	content, isStr := args[2].(string)
	if !isStr {
		t.Fatalf("the artifact content argument is %T, not a string", args[2])
	}
	if content != long {
		t.Errorf("the artifact holds %d bytes, want the %d bytes of the original — the ledger is the source of truth", len(content), len(long))
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(encoded) > eventPayloadMaxBytes {
		t.Errorf("the payload is %d bytes, over the %d-byte events CHECK", len(encoded), eventPayloadMaxBytes)
	}
	remnant, _ := payload["summary"].(string)
	if !strings.Contains(remnant, id.String()) {
		t.Errorf("the payload remnant does not name the artifact holding the rest: %q", remnant)
	}
	if payload["summary_bytes"] != len(long) {
		t.Errorf("summary_bytes = %v, want %d", payload["summary_bytes"], len(long))
	}
	if payload["outcome"] != "success" {
		t.Error("the spill clobbered an unrelated payload field")
	}
}

// TestPutTextField_RefusesWhenTheArtifactCannotBeStored. A goal event is NOT
// spillable, so there is no honest degraded behaviour here: recording the event
// with the body dropped would report a result nobody can read back.
func TestPutTextField_RefusesWhenTheArtifactCannotBeStored(t *testing.T) {
	l, pool := newTextLedger(t)
	pool.queryRowErr = errors.New("dial tcp: connection refused")
	payload := map[string]any{}

	_, err := l.putTextField(context.Background(), payload, "summary", artifactKindGoalResult, strings.Repeat("x", 20_000))
	if err == nil {
		t.Fatal("an unstorable artifact was reported as a successful spill")
	}
	if _, set := payload["summary"]; set {
		t.Error("the payload was mutated despite the refusal; a partial write is worse than none")
	}
	if !strings.Contains(err.Error(), "artifact") {
		t.Errorf("the error does not say what failed: %v", err)
	}
}

// TestPutTextField_PrefixNeverSplitsARune. Watched failing by replacing
// truncateAtRuneBoundary with a bare slice: the payload prefix then ends in a
// half-encoded rune and json.Marshal rewrites it to U+FFFD.
func TestPutTextField_PrefixNeverSplitsARune(t *testing.T) {
	l, _ := newTextLedger(t)
	payload := map[string]any{}
	// A 3-byte rune straddling spillTextPrefixBytes: 510 ASCII bytes then 'é'
	// puts the boundary inside the multi-byte sequence.
	long := strings.Repeat("x", spillTextPrefixBytes-2) + "é" + strings.Repeat("y", spillTextThreshold)

	if _, err := l.putTextField(context.Background(), payload, "summary", artifactKindGoalResult, long); err != nil {
		t.Fatalf("putTextField: %v", err)
	}
	remnant, _ := payload["summary"].(string)
	if !utf8.ValidString(remnant) {
		t.Errorf("the payload remnant is not valid UTF-8: %q", remnant)
	}
}

// TestOpenGoal_LongTextIsRecordedAsAnArtifact — the tool-facing property: a
// file-sized brief must reach the log, not be refused by the payload CHECK.
func TestOpenGoal_LongTextIsRecordedAsAnArtifact(t *testing.T) {
	l, pool := newTextLedger(t)
	long := strings.Repeat("brief. ", 3000) // ~21KB

	if _, err := l.OpenGoal(context.Background(), GoalResearch, long, "weave"); err != nil {
		t.Fatalf("OpenGoal: %v", err)
	}
	args, ok := artifactInsertArgs(pool)
	if !ok {
		t.Fatalf("the brief was not stored as an artifact; statements were %v", pool.log())
	}
	if args[2] != long {
		t.Error("the artifact does not hold the brief verbatim")
	}
	ev, ok := pool.argsFor("insert_event")
	if !ok {
		t.Fatal("no event was appended")
	}
	assertEventCarriesArtifact(t, ev, pool.artifactID)
}

// payloadArg is payload's position in the events INSERT ($8), next to the
// artifactArg index lifecycle_test.go pins; insertEventArgc guards both.
const payloadArg = 7

// assertEventCarriesArtifact pins the two halves that make the split work: the
// payload fits the CHECK, and the event references the artifact holding the
// rest. Either alone is satisfiable by a broken implementation — a thin payload
// with no reference loses the content, a reference with a fat payload never
// appends.
func assertEventCarriesArtifact(t *testing.T, eventArgs []any, want uuid.UUID) {
	t.Helper()
	if len(eventArgs) != insertEventArgc {
		t.Fatalf("insert_event ran with %d argument(s), want %d — a column added before artifact_id would move the index this assertion reads", len(eventArgs), insertEventArgc)
	}
	payload, isBytes := eventArgs[payloadArg].([]byte)
	if !isBytes {
		t.Fatalf("the payload argument is %T, not []byte", eventArgs[payloadArg])
	}
	if len(payload) > eventPayloadMaxBytes {
		t.Errorf("the appended payload is %d bytes, over the %d-byte events CHECK", len(payload), eventPayloadMaxBytes)
	}
	ref, isRef := eventArgs[artifactArg].(*uuid.UUID)
	if !isRef || ref == nil || *ref != want {
		t.Errorf("the event's artifact_id is %#v, want %s — without the reference the content is unreachable", eventArgs[artifactArg], want)
	}
}
