package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/google/uuid"
)

// The weave handoff dual-write.
//
// The plan's recorded decision for memory is "dual-write then replace": from M1
// weave handoff summaries ALSO emit as log events, and at M6 the database takes
// over spawn-time injection and file memory is frozen. So this is additive — the
// summary is still written to .sprawl/memory, which remains the system of record
// until M6, and this records it in the log alongside.
//
// THE BODY IS AN ARTIFACT, NOT THE PAYLOAD. `events` carries a CHECK capping
// payloads at 8KiB and a handoff summary is a full markdown document, so putting
// the body inline would fail the append — in production, on the one event whose
// entire purpose is to outlive the session that produced it. The body goes to
// content-addressed `artifacts` and the event references it, which is the
// thin-event / fat-artifact split this milestone exists to provide.

// handoffEventType is the pinned seed event type name.
const handoffEventType = "handoff_recorded"

// HandoffRecord is one handoff summary.
type HandoffRecord struct {
	SessionID    string
	AgentsActive []string
	// Body is the full markdown summary. It is stored as an artifact, never in
	// the event payload.
	Body string
	// GitSHA is optional provenance for the artifact.
	GitSHA string
}

// handoffPayload builds the thin event payload: a digest and a size, never the
// document. Split out so a test can assert the size bound directly rather than
// inferring it from a database refusal.
func handoffPayload(rec HandoffRecord) (json.RawMessage, error) {
	digest := sha256.Sum256([]byte(rec.Body))
	payload := map[string]any{
		"session_id":     rec.SessionID,
		"summary_sha256": hex.EncodeToString(digest[:]),
		"summary_bytes":  len(rec.Body),
		"agent_count":    len(rec.AgentsActive),
	}
	// agents_active is included but is NOT what bounds the payload — a fleet of
	// 200 agents would still be small, while the summary body would not be.
	if len(rec.AgentsActive) > 0 {
		payload["agents_active"] = rec.AgentsActive
	}
	return json.Marshal(payload)
}

// RecordHandoff records a handoff summary in the event log.
//
// IT NEVER RETURNS AN ERROR TO ITS CALLER, and that is the contract rather than
// laziness: the caller is the handoff path, whose job is to persist a session
// summary and restart weave. The memory file is authoritative until M6, so a
// handoff that failed because the event log rejected something would lose the
// one artifact a handoff exists to produce — to an observability component.
// Failures are logged by the Ledger and surfaced by `sprawl store doctor`.
//
// Safe on a nil Ledger, which is the default (the feature flag is off).
func RecordHandoff(ctx context.Context, l *Ledger, rec HandoffRecord) error {
	if !l.Enabled() {
		return nil
	}
	payload, err := handoffPayload(rec)
	if err != nil {
		l.logger().Warn("could not build the handoff event payload", "error", err)
		return nil
	}

	// The artifact first, so the event can reference it. Skipped when the store
	// is degraded: there is no connection to write it through, and nothing is
	// lost — the body is on disk in .sprawl/memory, and summary_sha256 in the
	// spilled event ties the two together on replay.
	var artifactID *uuid.UUID
	if pool := l.appender.pgPool(); l.DegradedError() == nil && pool != nil {
		id, aerr := PutArtifact(ctx, pool, "handoff_summary", rec.Body, rec.GitSHA)
		if aerr != nil {
			// Non-fatal: record the event without the body rather than losing
			// the event too. The digest still identifies the memory file.
			l.logger().Warn("could not store the handoff summary artifact; recording the event without it",
				"session_id", rec.SessionID, "error", aerr)
		} else {
			artifactID = &id
		}
	}

	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:    handoffEventType,
		TypeVersion: 1,
		ArtifactID:  artifactID,
		Payload:     payload,
	}); err != nil {
		l.logger().Warn("could not record the handoff event",
			"session_id", rec.SessionID, "error", err)
	}
	return nil
}
