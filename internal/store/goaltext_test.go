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
	// A 2-byte rune STRADDLING spillTextPrefixBytes: 511 ASCII bytes then 'é'
	// puts byte 512 on the rune's continuation byte. One fewer lead byte and
	// the boundary lands on a rune START, where a bare slice is also valid
	// UTF-8 and the assertion cannot fail — measured: lead=510 -> valid,
	// lead=511 -> invalid.
	long := strings.Repeat("x", spillTextPrefixBytes-1) + "é" + strings.Repeat("y", eventPayloadMaxBytes)

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

// TestPutTextField_SizesOnTheEncodedLengthNotTheRawBytes.
//
// The spill decision has to be made in the units the CHECK measures. `payload`
// is stored as JSON, and encoding/json writes a control byte as `\u00XX` — six
// bytes for one. A file of control bytes (an agent redirecting a binary or a
// terminal capture into its result file is the realistic shape) is therefore
// small by len() and enormous by the time it reaches the column, and sizing on
// len() puts it inline and lets the insert die on events_payload_thin_ck: a
// close that cannot be recorded, which is the exact failure this file exists to
// prevent.
func TestPutTextField_SizesOnTheEncodedLengthNotTheRawBytes(t *testing.T) {
	l, pool := newTextLedger(t)
	payload := map[string]any{}
	// Under the cap by len(), six times over it once encoded.
	dense := strings.Repeat("\x01", eventPayloadMaxBytes-100)

	id, err := l.putTextField(context.Background(), payload, "summary", artifactKindGoalResult, dense)
	if err != nil {
		t.Fatalf("putTextField: %v", err)
	}
	if id == nil {
		t.Fatalf("a %d-byte field that encodes to %d bytes of JSON was left in the payload; the insert would be refused by events_payload_thin_ck",
			len(dense), len(mustMarshalString(t, dense)))
	}
	if _, ok := artifactInsertArgs(pool); !ok {
		t.Errorf("no artifact was written; statements were %v", pool.log())
	}
	// The remnant is what the CHECK will actually measure, so it is measured the
	// same way here.
	remnant, _ := payload["summary"].(string)
	if n := len(mustMarshalString(t, remnant)); n > eventPayloadMaxBytes {
		t.Errorf("the inline remnant encodes to %d bytes, over the %d-byte payload cap", n, eventPayloadMaxBytes)
	}
}

// TestPutTextField_AFieldThatFitsThePayloadStaysInline.
//
// The spill is keyed on the PAYLOAD, not on the field, and not on where the
// text came from. A brief of a few thousand bytes typed inline has always been
// appended whole — it fits events_payload_thin_ck — and it is read straight out
// of `payload.text` by the spawn handler, which has no artifact read path. So a
// threshold set below the CHECK does not merely add an artifact row: it
// truncates, silently, on a path that worked before this change.
func TestPutTextField_AFieldThatFitsThePayloadStaysInline(t *testing.T) {
	l, pool := newTextLedger(t)
	payload := map[string]any{"goal_type": "research", "owner": "weave"}
	brief := strings.Repeat("b", 5000) // over half the CHECK, well under it

	id, err := l.putTextField(context.Background(), payload, "text", artifactKindGoalText, brief)
	if err != nil {
		t.Fatalf("putTextField: %v", err)
	}
	if id != nil {
		t.Errorf("a %d-byte brief that fits the payload was spilled to artifact %s; the spawn handler reads payload.text and would get the truncated remnant", len(brief), id)
	}
	if payload["text"] != brief {
		remnant, _ := payload["text"].(string)
		t.Errorf("the brief reaches the log as %d of its %d bytes: %q", len(remnant), len(brief), truncateAtRuneBoundary(remnant, 120))
	}
	if _, ok := artifactInsertArgs(pool); ok {
		t.Error("an artifact row was written for a brief that fits the payload")
	}
	// The other half of the claim: "fits" has to be true of the whole payload,
	// or this test is asserting an append the database would refuse.
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(encoded) > eventPayloadMaxBytes {
		t.Errorf("the inline payload is %d bytes, over the %d-byte CHECK", len(encoded), eventPayloadMaxBytes)
	}
}

// TestPutTextField_SpillsOnTheWHOLEPayload, the boundary partner of the test
// above: a field that would fit on its own still has to spill when its siblings
// push the payload over the CHECK, because the CHECK measures the payload.
func TestPutTextField_SpillsOnTheWHOLEPayload(t *testing.T) {
	l, _ := newTextLedger(t)
	payload := map[string]any{"outcome": "success", "filler": strings.Repeat("f", eventPayloadMaxBytes-1000)}
	summary := strings.Repeat("s", 2000)

	id, err := l.putTextField(context.Background(), payload, "summary", artifactKindGoalResult, summary)
	if err != nil {
		t.Fatalf("putTextField: %v", err)
	}
	if id == nil {
		t.Fatal("a field small enough alone was left inline in a payload already near the cap; the append would be refused by events_payload_thin_ck")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(encoded) > eventPayloadMaxBytes {
		t.Errorf("the payload is still %d bytes after the spill, over the %d-byte CHECK", len(encoded), eventPayloadMaxBytes)
	}
}

// TestPutTextField_RefusesWhenEvenTheSpilledRemnantDoesNotFit.
//
// The spill writes a ~700-byte remnant (prefix, digest, byte count) AFTER the
// size decision, so a payload whose siblings are already that close to the cap
// is still over it once the spill is done — and there is no second lever. The
// function's contract is "make this payload appendable"; when it cannot, it
// has to say so rather than hand back a payload the CHECK will refuse.
//
// Latent from today's two callers (goal_opened/goal_closed siblings are tens
// of bytes), and pinned anyway: the failure mode is a contract event that
// cannot be appended at all, discovered as a raw constraint violation.
func TestPutTextField_RefusesWhenEvenTheSpilledRemnantDoesNotFit(t *testing.T) {
	l, _ := newTextLedger(t)
	payload := map[string]any{"filler": strings.Repeat("f", eventPayloadMaxBytes-200)}
	summary := strings.Repeat("s", 20_000)

	_, err := l.putTextField(context.Background(), payload, "summary", artifactKindGoalResult, summary)
	if err == nil {
		encoded, _ := json.Marshal(payload)
		t.Fatalf("a %d-byte payload was reported as a successful spill; the append would be refused by events_payload_thin_ck", len(encoded))
	}
	for _, key := range []string{"summary", "summary_sha256", "summary_bytes"} {
		if _, set := payload[key]; set {
			t.Errorf("%s was left on a payload that cannot be appended; a caller ignoring the error would append it", key)
		}
	}
}

// TestOpenGoal_RefusesWhenTheBriefCannotBeSpilled.
//
// The caller half of TestPutTextField_RefusesWhenTheArtifactCannotBeStored,
// which pins only the helper. `OpenGoal` ignoring that error opens a goal whose
// brief was dropped — the spawned agent is then handed an empty task — and no
// assertion anywhere else notices.
func TestOpenGoal_RefusesWhenTheBriefCannotBeSpilled(t *testing.T) {
	l, pool := newTextLedger(t)
	pool.queryRowErr = errors.New("dial tcp: connection refused")

	_, err := l.OpenGoal(context.Background(), GoalResearch, strings.Repeat("brief. ", 3000), "weave")
	if err == nil {
		t.Fatal("a goal was opened although its brief could not be stored")
	}
	if !strings.Contains(err.Error(), "artifact") {
		t.Errorf("the error does not say the brief could not be stored: %v", err)
	}
	if _, appended := pool.argsFor("insert_event"); appended {
		t.Error("goal_opened was appended without its brief; the agent would be spawned with an empty task")
	}
}

func mustMarshalString(t *testing.T, s string) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}
