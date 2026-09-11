// goaltext.go — the goal-text / result-summary spill (QUM-1347).
//
// A goal's brief and a result's summary may now be the contents of a FILE the
// agent composed piecewise, because emitting a very large tool-call argument in
// one turn is what the upstream API occasionally refuses. That makes both fields
// unbounded, while `events` carries a CHECK capping a payload at 8KiB — so the
// long case has to go where handoff.go and spawncontext.go already put long
// bodies: content-addressed `artifacts`, referenced by the event's artifact_id.
//
// THE CONTENT IS THE RECORD, NOT THE PATH. The file is read at call time and
// only its bytes are persisted; the path is never stored, because a path is a
// claim about one host's disk at one moment and the ledger has to answer for a
// result years later.
//
// Unlike every other artifact write in this package this one FAILS LOUDLY when
// it cannot happen. `goal_opened` and `goal_closed` are not spillable, and a
// close is irreversible: recording one whose body was silently dropped would
// leave a permanent result nobody can read back.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
)

// eventPayloadMaxBytes mirrors events_payload_thin_ck in
// migrations/00001_m1a_event_log.sql. Duplicated as a constant so the spill
// below fires at the real bound rather than at a number in prose;
// schema_shape_integration_test.go pins the database side.
const eventPayloadMaxBytes = 8192

// encodedPayloadLen is what events_payload_thin_ck will measure: the whole
// payload, encoded, siblings included.
//
// Sizing on the payload rather than on the field is the only rule that leaves
// previously-working input alone. A brief of a few thousand bytes typed inline
// has always been appended whole, and `GoalSpawnHandler` reads it straight out
// of `payload.text` with no artifact read path — so a threshold set below the
// CHECK does not merely add an artifact row, it truncates a path that worked
// before. Spilling exactly when the payload would not fit means every input the
// database used to accept is still stored inline, and everything else — which
// used to be an append the database refused — now has somewhere to go.
//
// It is an ENCODED length, not a byte count: the CHECK measures `payload::text`
// and encoding/json writes a control byte as `\u00XX`, six bytes for one.
// Postgres escapes a strict subset of what Go's encoder does, so this
// over-estimates the column and errs toward spilling — the safe direction.
//
// Marshalling cannot fail for the shapes used here (invalid UTF-8 is replaced,
// not rejected), so an error can only mean the encoder changed under us, and
// the safe reading of "I cannot tell how big this is" is "too big for inline".
func encodedPayloadLen(payload map[string]any) int {
	b, err := json.Marshal(payload)
	if err != nil {
		return eventPayloadMaxBytes + 1
	}
	return len(b)
}

// spillTextPrefixBytes is how much of a spilled field stays readable inline.
//
// Not zero: `sprawl store` readers and the operability views show the payload,
// and a field replaced entirely by a uuid makes every long result look
// identical at a glance.
const spillTextPrefixBytes = 512

// Artifact kinds for the two spilled fields. `artifacts` is one table for every
// kind, so these strings are how a reader finds them.
const (
	artifactKindGoalText   = "goal_text"
	artifactKindGoalResult = "goal_result"
)

// putTextField writes text into payload[key], spilling to an artifact when it
// is too long to carry inline.
//
// Returns the artifact id to put on the event, or nil when the text stayed in
// the payload. On failure the payload is left UNTOUCHED — the caller is about
// to append a contract event, and a payload half-populated by a failed spill
// would be appended by any caller that ignored the error.
func (l *Ledger) putTextField(ctx context.Context, payload map[string]any, key, kind, text string) (*uuid.UUID, error) {
	payload[key] = text
	if encodedPayloadLen(payload) <= eventPayloadMaxBytes {
		return nil, nil
	}
	// Too big for the payload. Take it back out first: on every path from here
	// the caller is about to append a contract event, and a payload left
	// half-populated by a failed spill would be appended by any caller that
	// ignored the error.
	delete(payload, key)

	pool := l.appender.pgPool()
	if l.DegradedError() != nil || pool == nil {
		return nil, fmt.Errorf("store: %s is %d bytes, too long for an event payload, and has to be stored as an artifact — but the event log has no usable connection right now; retry when the store is reachable, or pass a shorter %s", key, len(text), key)
	}
	id, err := PutArtifact(ctx, pool, kind, text, "")
	if err != nil {
		return nil, fmt.Errorf("store: storing the %d-byte %s as an artifact: %w", len(text), key, err)
	}

	digest := sha256.Sum256([]byte(text))
	payload[key] = fmt.Sprintf("%s\n\n[truncated: the full %s is %d bytes and is stored as artifact %s]",
		truncateAtRuneBoundary(text, spillTextPrefixBytes), key, len(text), id)
	payload[key+"_sha256"] = hex.EncodeToString(digest[:])
	payload[key+"_bytes"] = len(text)
	return &id, nil
}

// truncateAtRuneBoundary cuts s to at most n bytes without splitting a rune.
//
// A byte slice through a multi-byte rune leaves an invalid UTF-8 string, which
// encoding/json silently rewrites to U+FFFD — so the payload prefix would differ
// from the artifact's first bytes for a reason no reader could see.
func truncateAtRuneBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
