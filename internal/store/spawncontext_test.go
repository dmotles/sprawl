package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The spawn_context artifact (QUM-1251, AC6).
//
// AC6 as written says "every spawn produces a spawn_context artifact". Three
// pre-existing gates make that false as stated and they are not being changed
// here: the event log is off by default (config.go), a disabled log yields a nil
// Ledger (process.go), and an ENABLED-but-DEGRADED log has no connection to
// write an artifact through (handoff.go does the same thing). So the property
// under test is the narrower true one: with the log enabled AND reachable, a run
// records its spawn context, and in every other case the launch proceeds with no
// artifact and no error.

// artifactInsertArgs finds the recorded arguments of the artifacts INSERT.
//
// Matched on a substring of the statement rather than on stmtLabel's output,
// which for this statement is the whole SQL text — keying an assertion on that
// would break on reformatting while catching nothing.
func artifactInsertArgs(pool *recordingPool) ([]any, bool) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for label, args := range pool.args {
		if strings.Contains(label, "insert into artifacts") {
			return args, true
		}
	}
	return nil, false
}

func testSpawnContext() SpawnContext {
	return SpawnContext{
		AgentName:         "finn",
		AgentType:         "engineer",
		Branch:            "dmotles/qum-1251",
		SessionID:         "sess-1",
		Model:             "opus",
		Effort:            "high",
		CardName:          "legacy-engineer",
		CardVersion:       1,
		CardContentSHA256: "e3b0c442",
		CardSource:        "db",
		RenderedPrompt:    "You are finn, an Engineer agent in Sprawl.",
	}
}

// TestSpawnContext_MarshalIsDeterministic. The artifact table is
// content-addressed on sha256 (PutArtifact), so a non-deterministic encoding
// does not merely waste rows — it forks the artifact chain, and two runs that
// launched from byte-identical inputs appear to have launched from different
// ones. What that rules out concretely is a wall-clock or uuid field minted
// inside Marshal: those differ on every call, which one repeat catches. (Map key
// order does NOT need ruling out — encoding/json sorts it.)
func TestSpawnContext_MarshalIsDeterministic(t *testing.T) {
	first, err := testSpawnContext().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// A SEPARATELY assembled but field-identical value, not the same variable
	// re-marshalled: the encoding must be a function of the fields alone.
	again, err := testSpawnContext().Marshal()
	if err != nil {
		t.Fatalf("Marshal (second): %v", err)
	}
	if !bytes.Equal(first, again) {
		t.Fatalf("two field-identical contexts encoded differently:\n%s\n%s", first, again)
	}

	// Aim control: a DIFFERENT context must encode differently. Without it, a
	// Marshal that returned a constant would satisfy the assertion above
	// perfectly.
	other := testSpawnContext()
	other.CardVersion = 2
	diff, err := other.Marshal()
	if err != nil {
		t.Fatalf("Marshal (control): %v", err)
	}
	if bytes.Equal(first, diff) {
		t.Errorf("changing the card version did not change the encoding, so the artifact is not the context it claims to be")
	}
}

// TestSpawnContext_CarriesWhatAReplayNeeds pins the fields Appendix B item 9
// names, one assertion per field so a dropped field says which.
//
// The card SOURCE is in here deliberately: cardresolve's package comment records
// that a published card and the seed it was extracted from are IDENTICAL in
// production, so the prompt alone cannot say whether the database answered. That
// distinction is what the contamination disclosure needs.
//
// The memory block and referenced artifact ids that Appendix B also mentions are
// deliberately ABSENT: neither has a source at the launch seam today, and
// emitting them empty would put a plausible zero in the replay record.
func TestSpawnContext_CarriesWhatAReplayNeeds(t *testing.T) {
	raw, err := testSpawnContext().Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the artifact body does not parse as JSON: %v", err)
	}
	for _, f := range []struct {
		key  string
		want any
	}{
		{"agent_name", "finn"},
		{"agent_type", "engineer"},
		{"branch", "dmotles/qum-1251"},
		{"session_id", "sess-1"},
		{"model", "opus"},
		{"effort", "high"},
		{"card_name", "legacy-engineer"},
		{"card_version", float64(1)},
		{"card_content_sha256", "e3b0c442"},
		{"card_source", "db"},
		{"rendered_prompt", "You are finn, an Engineer agent in Sprawl."},
	} {
		if got[f.key] != f.want {
			t.Errorf("%s = %#v, want %#v", f.key, got[f.key], f.want)
		}
	}
}

// TestPutSpawnContext_NilLedgerRecordsNothingAndDoesNotPanic. A nil Ledger is
// the DEFAULT (event_log.enabled is false), and this runs on the launch path, so
// the standing constraint applies: agents never brick on the store.
func TestPutSpawnContext_NilLedgerRecordsNothingAndDoesNotPanic(t *testing.T) {
	if id := PutSpawnContext(context.Background(), nil, testSpawnContext()); id != nil {
		t.Errorf("a nil Ledger returned artifact id %v, so a launch with the store off would reference a row nothing wrote", id)
	}
}

// TestPutSpawnContext_DegradedRecordsNothingAndDoesNotPanic is the fourth gate,
// and the one AC6's wording misses: an ENABLED log whose database is unreachable
// is Enabled() == true with no usable connection.
func TestPutSpawnContext_DegradedRecordsNothingAndDoesNotPanic(t *testing.T) {
	pool := newRecordingPool()
	pool.beginErr = errConnRefused
	reg := mustSeedRegistry(t)
	l := &Ledger{
		enabled:  true,
		registry: reg,
		appender: NewAppender(AppenderDeps{
			Pool: pool, Registry: reg, Spill: &capturingSpiller{}, Degraded: errConnRefused,
		}),
		degradedErr: errConnRefused,
	}
	if id := PutSpawnContext(context.Background(), l, testSpawnContext()); id != nil {
		t.Errorf("a degraded Ledger returned artifact id %v; there is no connection to have written it through", id)
	}
	for _, call := range pool.log() {
		if strings.Contains(call, "artifact") {
			t.Errorf("a degraded Ledger still issued %q", call)
		}
	}
}

// TestPutSpawnContext_AFailedArtifactWriteIsNotFatal. The failure is INJECTED,
// not inherited from an unstubbed double: a test that only fails because the
// fixture is unimplemented stops being a failure-path test the moment someone
// implements it, and says nothing at the time. The launch must continue with no
// id rather than taking the agent down over an observability row.
func TestPutSpawnContext_AFailedArtifactWriteIsNotFatal(t *testing.T) {
	pool := newRecordingPool()
	pool.queryRowErr = errConnRefused
	reg := mustSeedRegistry(t)
	l := &Ledger{
		enabled:  true,
		registry: reg,
		appender: NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
	}
	if id := PutSpawnContext(context.Background(), l, testSpawnContext()); id != nil {
		t.Errorf("the artifact write failed but PutSpawnContext returned id %v, so run_started would reference a row that does not exist", id)
	}
	// Aim control: it must have TRIED. Without this leg, a PutSpawnContext that
	// returned nil unconditionally would pass every assertion above.
	tried := false
	for _, call := range pool.log() {
		if strings.Contains(call, "artifact") {
			tried = true
		}
	}
	if !tried {
		t.Errorf("no artifact statement was issued on a healthy pool; calls were %v", pool.log())
	}
}

// TestPutSpawnContext_WritesTheMarshalledContextUnderItsOwnKind is the happy
// path, and it is the only test in this file that can see WHAT was written.
//
// The other legs are all refusals, and a PutSpawnContext that returned nil
// unconditionally satisfies every one of them. The kind is asserted because the
// artifacts table is one table for every kind and `spawn_context` is how a
// replay finds these rows; the content is asserted byte-for-byte against
// Marshal because an artifact whose body is the prompt alone — or "" — is a
// perfectly well-formed row that no replay can use.
func TestPutSpawnContext_WritesTheMarshalledContextUnderItsOwnKind(t *testing.T) {
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	l := &Ledger{
		enabled:  true,
		registry: reg,
		appender: NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: &capturingSpiller{}}),
	}
	sc := testSpawnContext()

	got := PutSpawnContext(context.Background(), l, sc)
	if got == nil {
		t.Fatalf("a healthy Ledger recorded no artifact; calls were %v", pool.log())
	}
	if *got != pool.artifactID {
		t.Errorf("returned artifact id %s, want the id the INSERT returned (%s) — a caller referencing the wrong id points run_started at nothing", *got, pool.artifactID)
	}

	args, ran := artifactInsertArgs(pool)
	if !ran {
		t.Fatalf("no artifacts INSERT was issued; calls were %v", pool.log())
	}
	if len(args) != 6 {
		t.Fatalf("the artifacts INSERT ran with %d argument(s), want 6", len(args))
	}
	if kind, _ := args[1].(string); kind != "spawn_context" {
		t.Errorf("artifact kind = %#v, want \"spawn_context\"", args[1])
	}
	want, err := sc.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if content, _ := args[2].(string); content != string(want) {
		t.Errorf("artifact content is not the marshalled spawn context:\ngot  %q\nwant %q", content, want)
	}
}
