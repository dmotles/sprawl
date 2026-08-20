package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// The SPAWN_INTENT write-ahead — Appendix B item 2 (QUM-1250, M1b).
//
// WHY THIS EXISTS, because read cold it looks like bookkeeping and it is not.
// claims.go's TakeoverExpired deliberately trades exactly-once for liveness: an
// expired lease does not prove its holder is dead, so a second handler CAN end up
// running for one event. The intent is the thing that makes that survivable. A
// handler arriving after a takeover finds a record saying "somebody was about to
// create this agent" and reconciles against local reality, rather than creating a
// second worktree for the same agent.
//
// SO THE ORDER IS THE WHOLE MECHANISM:
//
//	append spawn_intent   ->   create the local resource   ->   append spawn_committed
//
// Reversed, the crash window moves to exactly the wrong place: a worktree exists
// that no event mentions, nothing can attribute it, and the reconciler is
// structurally unable to tell it from an agent a human spawned. Which is why
// "the intent could not be appended" means the spawn DOES NOT HAPPEN — a
// write-ahead you are willing to skip is not a write-ahead.
//
// That refusal is also consistent with degraded mode rather than an exception to
// it: spawn_intent is a contract event and therefore not spillable, so an
// unreachable database means no new coordination starts. Running agents keep
// running; nothing new is created behind the log's back.

// EventEmitter is the append surface the spawn handler and the reconciler need.
//
// Narrower than *Ledger on purpose, and it returns the appended event's ID —
// which Ledger.Emit does not — because a write-ahead is useless if the writer
// cannot then reference what it wrote. The intent's own event id IS the intent's
// identity, and spawn_committed has to close it.
type EventEmitter interface {
	Emit(ctx context.Context, req EmitRequest) (uuid.UUID, error)
}

// SpawnRequest is what a spawn needs locally. Deliberately a plain struct with
// no behaviour: the local spawn machinery lives in internal/agentops and
// internal/supervisor, and this package must not import either (the import
// direction is supervisor -> store, and reversing it would drag the dispatcher
// into eight e2e matrix rows).
type SpawnRequest struct {
	AgentName string
	AgentType string
	Family    string
	Parent    string
	Branch    string
	Prompt    string
	Model     string
	Subagent  bool
}

// Spawner performs the local, non-transactional side effect: worktree, branch,
// session. Implemented outside this package (internal/dispatchadapt) over the
// existing supervisor surface.
type Spawner interface {
	Spawn(ctx context.Context, req SpawnRequest) error
}

// SpawnHandlerDeps follows the repo's deps-struct convention.
type SpawnHandlerDeps struct {
	Emitter EventEmitter
	Spawner Spawner
	// Host is written onto the intent as its affinity, so every worktree-bound
	// follow-up is claimable only where the worktree is.
	Host   string
	Logger *slog.Logger
}

// SpawnHandler turns a spawn_requested event into an agent, write-ahead first.
type SpawnHandler struct {
	emitter EventEmitter
	spawner Spawner
	host    string
	log     *slog.Logger
}

var _ Handler = (*SpawnHandler)(nil)

func NewSpawnHandler(d SpawnHandlerDeps) (*SpawnHandler, error) {
	switch {
	case d.Emitter == nil:
		return nil, fmt.Errorf("store: spawn handler needs an event emitter; without one there is no write-ahead and a crash leaves an unattributable worktree")
	case d.Spawner == nil:
		return nil, fmt.Errorf("store: spawn handler needs a spawner")
	case d.Host == "":
		return nil, fmt.Errorf("store: spawn handler needs a host identity; it is written onto the intent as its affinity, and an unaffined intent makes worktree-bound work claimable anywhere")
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &SpawnHandler{emitter: d.Emitter, spawner: d.Spawner, host: d.Host, log: log}, nil
}

// spawnRequestPayload is the shape of a spawn_requested payload.
type spawnRequestPayload struct {
	AgentName string `json:"agent_name"`
	AgentType string `json:"agent_type"`
	Family    string `json:"family"`
	Parent    string `json:"parent"`
	Branch    string `json:"branch"`
	Prompt    string `json:"prompt"`
	Model     string `json:"model"`
	Subagent  bool   `json:"subagent"`
}

// Handle runs the write-ahead sequence.
func (h *SpawnHandler) Handle(ctx context.Context, ev DispatchedEvent) error {
	var req spawnRequestPayload
	if err := json.Unmarshal(ev.Payload, &req); err != nil {
		return fmt.Errorf("store: spawn_requested %s has an unreadable payload: %w", ev.ID, err)
	}

	// VALIDATED BEFORE ANYTHING IS WRITTEN. The log is append-only, so a bad
	// intent can never be cleaned up — and an intent for an unnamed agent is
	// unreconcilable by construction, because the reconciler matches intents to
	// local agents BY NAME. It would match nothing, never adopt, and after the
	// grace period produce a spawn_failed nobody can act on. Permanently.
	if req.AgentName == "" || req.AgentType == "" {
		return fmt.Errorf("store: spawn_requested %s names no agent (agent_name=%q agent_type=%q); refusing before writing an intent that could never be reconciled",
			ev.ID, req.AgentName, req.AgentType)
	}

	// 1. The write-ahead. Under the claim the dispatcher already holds.
	intentID, err := h.emitter.Emit(ctx, EmitRequest{
		TypeName:           "spawn_intent",
		TypeVersion:        1,
		WorkflowInstanceID: ev.WorkflowInstanceID,
		Payload: map[string]any{
			"agent_name": req.AgentName,
			"agent_type": req.AgentType,
			"family":     req.Family,
			"parent":     req.Parent,
			"branch":     req.Branch,
			"subagent":   req.Subagent,
			// The affinity originates HERE because this is the step that creates
			// the host-bound resource.
			"host_affinity":    h.host,
			"request_event_id": ev.ID.String(),
		},
	})
	if err != nil {
		return fmt.Errorf("store: recording the spawn intent for %q: %w (nothing was created locally)", req.AgentName, err)
	}

	// 2. The local, non-transactional side effect.
	//
	// A struct CONVERSION rather than a field-by-field literal, which Go allows
	// only when the field names, types and order match exactly. That makes it
	// the safer form here, not merely the shorter one: adding a field to one
	// struct and forgetting the other becomes a compile error instead of a
	// silently unpopulated spawn parameter.
	if spawnErr := h.spawner.Spawn(ctx, SpawnRequest(req)); spawnErr != nil {
		// CLOSE THE INTENT. An intent left open by a failed spawn is worse than
		// no intent: the reconciler chases it forever, and after the grace period
		// emits its own spawn_failed for it.
		if _, err := h.emitter.Emit(ctx, EmitRequest{
			TypeName:           "spawn_failed",
			TypeVersion:        1,
			WorkflowInstanceID: ev.WorkflowInstanceID,
			ClosesEventID:      &intentID,
			Payload: map[string]any{
				"agent_name": req.AgentName,
				"reason":     spawnErr.Error(),
				"host":       h.host,
			},
		}); err != nil {
			// Both failures matter and neither may hide the other: the spawn
			// failure is what the caller needs, and an unclosed intent is what
			// the reconciler will trip over later.
			h.log.Error("the spawn failed AND its intent could not be closed; startup reconciliation will resolve the intent after the grace period",
				"agent", req.AgentName, "intent", intentID, "spawn_error", spawnErr, "close_error", err)
		}
		return fmt.Errorf("store: spawning %q: %w", req.AgentName, spawnErr)
	}

	// 3. Close the intent: the resources exist.
	if _, err := h.emitter.Emit(ctx, EmitRequest{
		TypeName:           "spawn_committed",
		TypeVersion:        1,
		WorkflowInstanceID: ev.WorkflowInstanceID,
		ClosesEventID:      &intentID,
		Payload: map[string]any{
			"agent_name": req.AgentName,
			"host":       h.host,
			"branch":     req.Branch,
		},
	}); err != nil {
		// The agent EXISTS. Reporting this as a failure would be accurate about
		// the log and misleading about the world, and the dispatcher would then
		// retry — spawning a second time. Startup reconciliation is designed for
		// exactly this state: it finds the orphan, matches the open intent, and
		// adopts it.
		return fmt.Errorf("store: %q was spawned but its intent could not be closed; startup reconciliation will adopt it: %w", req.AgentName, err)
	}
	return nil
}
