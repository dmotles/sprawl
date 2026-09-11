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

// jsonbSeparatorBytesPerKey is how much LONGER `payload::text` is than Go's
// encoding of the same map.
//
// The column is jsonb, and `jsonb::text` renders a space after every `:` and
// every `,` — `{"a": 1, "b": 2}` — while encoding/json emits none. That is
// 2k-1 bytes for k keys; rounded up to 2k so the estimate never runs short.
// Go escaping runs the other way (it writes `<`, `>`, `&` as six bytes where
// jsonb writes one), so the total estimate is conservative in both directions
// only if this term is included: without it, a payload measured at exactly the
// cap is refused by the CHECK, which on a close is a result that cannot be
// recorded at all.
const jsonbSeparatorBytesPerKey = 2

// payloadFitsTheCHECK reports whether payload will satisfy
// events_payload_thin_ck once Postgres renders it.
//
// Deciding on the payload rather than on one field is what leaves
// previously-working input alone. A brief of a few thousand bytes typed inline
// has always been appended whole, and `GoalSpawnHandler` reads it straight out
// of `payload.text` with no artifact read path — so a threshold below the CHECK
// does not merely add an artifact row, it truncates a path that worked before.
// Spilling exactly when the payload would not fit keeps every input the
// database used to accept inline, and gives everything else — which used to be
// an append the database refused — somewhere to go.
//
// Marshalling cannot fail for the shapes used here (invalid UTF-8 is replaced,
// not rejected), so an error can only mean the encoder changed under us, and
// the safe reading of "I cannot tell how big this is" is "it does not fit".
func payloadFitsTheCHECK(payload map[string]any) bool {
	b, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return len(b)+len(payload)*jsonbSeparatorBytesPerKey <= eventPayloadMaxBytes
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
// the payload. On failure the payload is left UNTOUCHED, and on success it is
// guaranteed to satisfy the CHECK.
func (l *Ledger) putTextField(ctx context.Context, payload map[string]any, key, kind, text string) (*uuid.UUID, error) {
	payload[key] = text
	if payloadFitsTheCHECK(payload) {
		return nil, nil
	}
	// Too big for the payload. Take it back out first: on every path from here
	// the caller is about to append a contract event, and a payload left
	// half-populated by a failed spill would be appended by any caller that
	// ignored the error.
	delete(payload, key)

	pool := l.appender.pgPool()
	if l.DegradedError() != nil || pool == nil {
		return nil, fmt.Errorf("store: %s is %d bytes, too long for an event payload (the whole payload must encode to under %d bytes), and has to be stored as an artifact — but the event log has no usable connection right now; retry when the store is reachable, or pass a shorter %s", key, len(text), eventPayloadMaxBytes, key)
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
	// The remnant, the digest and the byte count are ~700 bytes added AFTER the
	// only size decision above, so the spill has to answer for its own output:
	// if the siblings were already that close to the cap, spilling produced a
	// payload the CHECK still refuses and there is no second lever to pull.
	// Refusing here names the cause; letting it through makes the caller read a
	// bare constraint violation on an append it thought it had made safe.
	if !payloadFitsTheCHECK(payload) {
		delete(payload, key)
		delete(payload, key+"_sha256")
		delete(payload, key+"_bytes")
		return nil, fmt.Errorf("store: %s was stored as artifact %s, but the event payload is still over the %d-byte limit with the rest of its fields; this event carries too much besides %s", key, id, eventPayloadMaxBytes, key)
	}
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
