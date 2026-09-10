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
// migrations/00001_m1a_event_log.sql. Duplicated as a constant so the threshold
// below is derived from the real bound rather than from a number in prose;
// schema_shape_integration_test.go pins the database side.
const eventPayloadMaxBytes = 8192

// spillTextThreshold is where a text field stops living in the payload.
//
// Half the CHECK, deliberately, and not a byte under it: the field is not the
// only thing in the payload (goal_opened also carries goal_type and owner, and
// a spilled field adds a digest and a byte count), so the remaining half is the
// siblings' room.
//
// It is compared against the field's ENCODED length, not its byte length. The
// CHECK measures `payload::text`, and encoding/json writes a control byte as
// `\u00XX` — six bytes for one. Sizing on len() would put a 4KB file of control
// bytes inline and let the insert die on events_payload_thin_ck, which on
// report_result is a close that cannot be recorded at all.
const spillTextThreshold = eventPayloadMaxBytes / 2

// encodedTextLen is the length text occupies once it is JSON, which is the unit
// events_payload_thin_ck counts in. Marshalling a string cannot fail — invalid
// UTF-8 is replaced, not rejected — so an error here can only mean the encoder
// changed under us, and the safe reading of "I cannot tell how big this is" is
// "too big to carry inline".
func encodedTextLen(text string) int {
	b, err := json.Marshal(text)
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
	if encodedTextLen(text) <= spillTextThreshold {
		payload[key] = text
		return nil, nil
	}

	pool := l.appender.pgPool()
	if l.DegradedError() != nil || pool == nil {
		return nil, fmt.Errorf("store: %s is %d bytes and has to be stored as an artifact, but the event log has no usable connection right now; retry when the store is reachable, or pass a %s under %d bytes", key, len(text), key, spillTextThreshold)
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
