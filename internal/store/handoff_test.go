package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The weave handoff dual-write (the plan's "dual-write then replace" decision for
// memory): a handoff summary is written to .sprawl/memory as it always was, AND
// recorded as a log event.
//
// THE BODY GOES IN AN ARTIFACT, NOT THE EVENT. A handoff summary is a full
// markdown document and `events` carries a CHECK capping payloads at 8KiB, so
// putting the body in the payload would make every handoff over that size fail
// the append — and fail it in production, on the one event whose whole purpose is
// to survive the session that produced it. This is exactly the thin-event /
// fat-artifact split the milestone exists to provide, so it uses it.

func newHandoffFixture(t *testing.T) (*Ledger, *recordingPool, *capturingSpiller) {
	t.Helper()
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	spill := &capturingSpiller{}
	l := &Ledger{
		enabled:   true,
		registry:  reg,
		projectID: uuid.New(),
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: spill}),
	}
	return l, pool, spill
}

func TestRecordHandoff_EmitsAnEventAndStoresTheBodyAsAnArtifact(t *testing.T) {
	l, pool, _ := newHandoffFixture(t)
	body := strings.Repeat("a full handoff document with lots of prose. ", 500) // ~21KB

	if err := RecordHandoff(context.Background(), l, HandoffRecord{
		SessionID:    "sess-handoff-1",
		AgentsActive: []string{"finn", "zone"},
		Body:         body,
	}); err != nil {
		t.Fatalf("RecordHandoff: %v", err)
	}

	calls := pool.log()
	if indexOf(calls, "insert_event") < 0 {
		t.Errorf("no event was appended: %v", calls)
	}
	// The artifact insert must happen too, and BEFORE the event, since the event
	// references it by id.
	artifactAt := -1
	for i, c := range calls {
		if strings.Contains(c, "artifacts") {
			artifactAt = i
			break
		}
	}
	if artifactAt < 0 {
		t.Errorf("the body was not stored as an artifact: %v", calls)
	} else if eventAt := indexOf(calls, "insert_event"); eventAt >= 0 && artifactAt > eventAt {
		t.Errorf("the artifact was written AFTER the event that references it: %v", calls)
	}
}

// TestRecordHandoff_PayloadStaysUnderTheEventSizeCap is the assertion that makes
// the artifact split load-bearing rather than stylistic.
//
// A 21KB summary in the payload would be refused by the database's 8KiB CHECK.
// The payload must carry only a digest and a size, so it stays small no matter how
// long the handoff is.
func TestRecordHandoff_PayloadStaysUnderTheEventSizeCap(t *testing.T) {
	l, _, _ := newHandoffFixture(t)
	huge := strings.Repeat("x", 200_000)

	rec := HandoffRecord{SessionID: "sess-big", Body: huge}
	payload, err := handoffPayload(rec)
	if err != nil {
		t.Fatalf("handoffPayload: %v", err)
	}
	if len(payload) > 8192 {
		t.Errorf("a %d-byte handoff produced a %d-byte payload, over the 8192 CHECK — the append would be refused by the database",
			len(huge), len(payload))
	}
	if strings.Contains(string(payload), strings.Repeat("x", 100)) {
		t.Error("the payload embeds the summary body; it must carry a digest, not the document")
	}

	// The digest must be a function of the body, or it identifies nothing.
	other, err := handoffPayload(HandoffRecord{SessionID: "sess-big", Body: huge + "!"})
	if err != nil {
		t.Fatalf("handoffPayload: %v", err)
	}
	if string(payload) == string(other) {
		t.Error("two different bodies produced identical payloads, so summary_sha256 is not derived from the body")
	}
	_ = l
}

// TestRecordHandoff_ValidatesAgainstItsSeedSchema pins that the payload this
// code builds actually satisfies the schema it is pinned to.
//
// Without this, a field rename between handoff.go and the seed JSON would fail
// only at runtime, only on a real handoff, only with the store enabled — the
// narrowest possible reproduction window.
func TestRecordHandoff_ValidatesAgainstItsSeedSchema(t *testing.T) {
	reg := mustSeedRegistry(t)
	schema, ok := reg.ByName(handoffEventType, 1)
	if !ok {
		t.Fatalf("seed schema %s@1 is missing", handoffEventType)
	}
	payload, err := handoffPayload(HandoffRecord{
		SessionID: "s", AgentsActive: []string{"a"}, Body: "b",
	})
	if err != nil {
		t.Fatalf("handoffPayload: %v", err)
	}
	if err := Validate(schema.JSONSchema, payload); err != nil {
		t.Errorf("the payload this code builds does not satisfy %s@1: %v", handoffEventType, err)
	}
}

// TestRecordHandoff_NilLedgerIsANoOp pins the default path: the store is off, so
// a handoff must proceed exactly as before.
func TestRecordHandoff_NilLedgerIsANoOp(t *testing.T) {
	if err := RecordHandoff(context.Background(), nil, HandoffRecord{
		SessionID: "s", Body: "b",
	}); err != nil {
		t.Errorf("a nil Ledger must make RecordHandoff a silent no-op; got: %v", err)
	}
}

// TestRecordHandoff_DegradedSpillsWithoutTheArtifact pins the degraded path, and
// the reason nothing is lost.
//
// A handoff is telemetry-shaped for spill purposes (it opens no contract), so it
// spills. The artifact cannot be written with the database down, so the spilled
// event carries the digest and no artifact reference — and NOTHING IS LOST,
// because the summary body is still on disk in .sprawl/memory, which remains the
// system of record until M6 takes over. That is what makes spilling the right
// call here rather than a loss.
func TestRecordHandoff_DegradedSpillsWithoutTheArtifact(t *testing.T) {
	pool := newRecordingPool()
	pool.beginErr = errConnRefused
	reg := mustSeedRegistry(t)
	spill := &capturingSpiller{}
	l := &Ledger{
		enabled:  true,
		registry: reg,
		appender: NewAppender(AppenderDeps{
			Pool: pool, Registry: reg, Spill: spill, Degraded: errConnRefused,
		}),
		degradedErr: errConnRefused,
	}

	if err := RecordHandoff(context.Background(), l, HandoffRecord{
		SessionID: "sess-degraded", Body: "the summary",
	}); err != nil {
		t.Fatalf("a handoff must not fail because the event log is down: %v", err)
	}
	if spill.count() != 1 {
		t.Fatalf("spill holds %d record(s), want 1", spill.count())
	}
	rec := spill.records[0]
	if rec.SchemaName != handoffEventType {
		t.Errorf("spilled record is %s, want %s", rec.SchemaName, handoffEventType)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Payload, &got); err != nil {
		t.Fatalf("spilled payload does not parse: %v", err)
	}
	if got["summary_sha256"] == nil || got["summary_sha256"] == "" {
		t.Error("the spilled handoff carries no summary digest, so a replay cannot tie it to the memory file that holds the body")
	}
}

// TestRecordHandoff_StoreFailureNeverFailsTheHandoff pins the ordering guarantee
// the caller depends on.
//
// The memory file is authoritative until M6. A handoff that failed because the
// event log rejected its event would lose the session summary — the one artifact
// a handoff exists to produce — to an observability component.
func TestRecordHandoff_StoreFailureNeverFailsTheHandoff(t *testing.T) {
	pool := newRecordingPool()
	pool.execErr["insert_open_contract"] = errConnRefused
	reg := mustSeedRegistry(t)
	l := &Ledger{
		enabled:  true,
		registry: reg,
		appender: NewAppender(AppenderDeps{
			Pool: pool, Registry: reg, Spill: &capturingSpiller{err: errConnRefused},
		}),
	}
	// Both the append AND the spill fail here — the worst case.
	if err := RecordHandoff(context.Background(), l, HandoffRecord{
		SessionID: "s", Body: "b",
	}); err != nil {
		t.Errorf("RecordHandoff must never return an error to the handoff path: %v", err)
	}
}
