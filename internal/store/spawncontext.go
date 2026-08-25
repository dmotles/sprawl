package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// The spawn_context artifact (QUM-1251 AC6, plan-of-record Appendix A's
// artifact kinds and Appendix B item 9).
//
// It exists so a bench replay can reproduce a run: the rendered prompt is the
// one input no later reader can reconstruct, because it is a function of the
// card, the agent's on-disk state and the environment at launch, none of which
// is preserved. The card's name, version and content digest are recorded
// alongside it so a replay can say WHICH definition ran, and the card SOURCE
// because — per cardresolve's package comment — a published card and the seed it
// was extracted from are byte-identical in production, so the prompt alone
// cannot say whether the database answered.
//
// Deliberately NOT recorded: a memory block and the run's referenced artifact
// ids, both of which Appendix B item 9 mentions. Neither has a source at the
// launch seam today, and writing them empty would put a plausible zero in the
// replay record — a reader could not tell "there was no memory block" from "we
// did not look".

// spawnContextKind is the artifacts.kind these rows carry. `artifacts` is one
// table for every kind, so this string is how a replay finds them.
const spawnContextKind = "spawn_context"

// spawnContextWriteTimeout bounds the artifact write.
//
// RunStarted is called with context.Background() from the launch path, so
// without a bound this would be an unlimited DB round-trip in front of an
// agent's first turn. The store is an observability component: a slow database
// must cost the launch a bounded pause and then be dropped.
const spawnContextWriteTimeout = 2 * time.Second

// SpawnContext is the launch record one run's artifact holds.
//
// A struct rather than a map so the encoding is a function of the fields alone:
// artifacts are content-addressed on sha256, so a non-deterministic encoding
// would fork the artifact chain and make two identical launches look different.
type SpawnContext struct {
	AgentName         string `json:"agent_name"`
	AgentType         string `json:"agent_type"`
	Branch            string `json:"branch,omitempty"`
	SessionID         string `json:"session_id"`
	Model             string `json:"model,omitempty"`
	Effort            string `json:"effort,omitempty"`
	CardName          string `json:"card_name,omitempty"`
	CardVersion       int    `json:"card_version,omitempty"`
	CardContentSHA256 string `json:"card_content_sha256,omitempty"`
	CardSource        string `json:"card_source,omitempty"`
	// RenderedPrompt is the system prompt EXACTLY as written to disk and handed
	// to the backend.
	//
	// Stored unredacted, deliberately: replay fidelity is the artifact's entire
	// purpose, redaction would change the sha256 and therefore the
	// content-address, and the rendered prompt carries no credential — its
	// environment section is the workdir, platform, shell and branch.
	RenderedPrompt string `json:"rendered_prompt"`
}

// Marshal encodes the artifact body.
func (s SpawnContext) Marshal() ([]byte, error) { return json.Marshal(s) }

// PutSpawnContext stores one spawn context and returns its artifact id, or nil.
//
// Nil covers every case in which there is nothing to reference: the store is off
// (a nil Ledger, which is the default), the store is degraded (enabled with no
// usable connection), or the write failed. None of them is reported as an error,
// because this runs on the launch path and agents never brick on the store —
// the same reason RecordHandoff skips its artifact when degraded.
func PutSpawnContext(ctx context.Context, l *Ledger, sc SpawnContext) *uuid.UUID {
	if !l.Enabled() {
		return nil
	}
	pool := l.appender.pgPool()
	if l.DegradedError() != nil || pool == nil {
		return nil
	}
	body, err := sc.Marshal()
	if err != nil {
		l.logger().Warn("could not encode the spawn context", "agent", sc.AgentName, "error", err)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, spawnContextWriteTimeout)
	defer cancel()
	id, err := PutArtifact(ctx, pool, spawnContextKind, string(body), "")
	if err != nil {
		l.logger().Warn("could not store the spawn context artifact; the run is recorded without it",
			"agent", sc.AgentName, "session_id", sc.SessionID, "error", err)
		return nil
	}
	return &id
}
