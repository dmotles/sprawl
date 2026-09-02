// legacy.go — the paired `legacy: true` lifecycle events that keep the
// outstanding-work view complete while prose `spawn` still exists (QUM-1252,
// M3a slice 10; Appendix B item 12).
//
// WHY THIS IS NOT COSMETIC. `sprawl goals` answers "what does the fleet still
// owe?", and an operator acts on an empty answer by walking away. Until the
// whole fleet is engine-driven, most work is started by the prose `spawn` tool,
// which opens no goal contract — so without these two events the honest answer
// to that question and the answer the command gives are different, and the
// command is the one that looks authoritative.
//
// So a legacy spawn opens an `agent_spawned` contract and a retire closes it.
// The AGENT'S EXISTENCE is the contract, not one of its runs: run_started /
// run_finished (M1a) already record runs, and an agent that is idle between
// runs still owes its work.
//
// Everything here is BEST-EFFORT AT THE CALL SITE. These are observability
// events, and the standing requirement is that agents never brick on the store,
// so a caller records the failure and spawns anyway. That is the caller's
// decision to make, not this file's: the functions below return their errors
// honestly rather than swallowing them.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LegacyAgent is the identity of one prose-spawned agent.
//
// Deliberately a plain struct of strings mirroring state.AgentState's fields
// rather than a reference to it: internal/state must not become a dependency of
// the store, and the payload is a snapshot taken at spawn time — a later rename
// or rebase must not retroactively change what the log says happened.
type LegacyAgent struct {
	AgentName string
	AgentType string
	Family    string
	Parent    string
	Branch    string
	Subagent  bool
}

// OpenLegacySpawn is one agent_spawned contract with no retire against it.
type OpenLegacySpawn struct {
	EventID    uuid.UUID
	WorkflowID uuid.UUID
	AgentName  string
	AgentType  string
	SpawnedAt  time.Time
}

// openLegacySpawnSQL finds the open agent_spawned contract for one agent name.
//
// Keyed on the NAME rather than on an id the caller remembers, because the
// caller here is `retire`, which is handed a name by an operator and has no
// event id to offer. Agent names are unique among live agents (the allocator
// enforces it), and a name that has been reused after a retire cannot collide:
// the earlier contract is closed and therefore not in open_contracts at all.
const openLegacySpawnSQL = `
	SELECT e.id, e.workflow_instance_id,
	       COALESCE(e.payload->>'agent_type', ''), oc.opened_at
	  FROM open_contracts oc
	  JOIN events e ON e.id = oc.event_id
	 WHERE e.project_id = $1
	   AND e.schema_id = ANY($2)
	   AND e.payload->>'agent_name' = $3
	 ORDER BY e.seq
	 LIMIT 1`

// PgLegacyReader reads open legacy spawn contracts through a pgx pool.
type PgLegacyReader struct {
	Pool interface {
		Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	}
	Registry *Registry
}

// OpenLegacySpawn returns the open agent_spawned contract for agent, or nil
// when there is none.
//
// A missing contract is (nil, nil) rather than an error: the common causes are
// an agent spawned before this slice landed and an agent spawned while the log
// was off, and neither is a fault the retire path should refuse over.
func (r *PgLegacyReader) OpenLegacySpawn(ctx context.Context, projectID uuid.UUID, agent string) (*OpenLegacySpawn, error) {
	if agent == "" {
		// Refused rather than matched: payload->>'agent_name' is NULL on events
		// that carry no name, and an empty string is a surprising match for a
		// caller that has lost track of which agent it is closing.
		return nil, fmt.Errorf("store: looking up a legacy spawn requires an agent name")
	}
	rows, err := r.Pool.Query(ctx, openLegacySpawnSQL, projectID, schemaIDsFor(r.Registry, "agent_spawned"), agent)
	if err != nil {
		return nil, fmt.Errorf("store: reading the spawn record for %q: %w", agent, err)
	}
	defer rows.Close()

	var out *OpenLegacySpawn
	if rows.Next() {
		s := OpenLegacySpawn{AgentName: agent}
		if err := rows.Scan(&s.EventID, &s.WorkflowID, &s.AgentType, &s.SpawnedAt); err != nil {
			return nil, fmt.Errorf("store: scanning the spawn record for %q: %w", agent, err)
		}
		out = &s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the spawn record for %q: %w", agent, err)
	}
	return out, nil
}

// legacyReader refuses a store that cannot answer, for goalReader's reason.
func (l *Ledger) legacyReader() (*PgLegacyReader, error) {
	if !l.Enabled() {
		return nil, fmt.Errorf("store: the event log is disabled on this host, so spawns are not recorded; enable it with `sprawl config set event_log.enabled true`")
	}
	if l.degradedErr != nil {
		return nil, fmt.Errorf("store: the event log is unreachable, so a spawn cannot be recorded or closed: %w", l.degradedErr)
	}
	return &PgLegacyReader{Pool: l.pool, Registry: l.registry}, nil
}

// RecordLegacySpawn opens an agent_spawned contract for a prose-spawned agent.
//
// `legacy` is written true unconditionally, which is correct because nothing
// else reaches this function: the engine's spawn path is spawn_requested ->
// spawn_intent -> spawn_committed. Slice 11 DID wire a Spawner to the supervisor
// (cmd/enter_dispatch.go, dispatchadapt.SupervisorSpawner), so the condition this
// comment once described as hypothetical has already happened (QUM-1340) — the
// engine path calls sup.Spawn directly and bypasses toolSpawn, which is what
// keeps it out of here. Any future engine path must stay out too: an
// engine-driven agent's work is already visible as its goal, and recording it
// again as legacy would double-count the same obligation.
func (l *Ledger) RecordLegacySpawn(ctx context.Context, a LegacyAgent) (uuid.UUID, error) {
	if a.AgentName == "" {
		return uuid.Nil, fmt.Errorf("store: recording a spawn requires an agent name; the retire that closes it is looked up by name")
	}
	if a.AgentType == "" {
		// Required by the seed, and load-bearing rather than pedantic: the type
		// is what `sprawl goals` prints in place of a goal_type, so a blank one
		// makes the row unreadable as work.
		return uuid.Nil, fmt.Errorf("store: recording the spawn of %q requires an agent type", a.AgentName)
	}
	if _, err := l.legacyReader(); err != nil {
		return uuid.Nil, err
	}

	payload := map[string]any{
		"agent_name": a.AgentName,
		"agent_type": a.AgentType,
		"subagent":   a.Subagent,
		"legacy":     true,
	}
	// Absent rather than empty: an empty `parent` is indistinguishable from a
	// root-spawned agent, and an empty `branch` from a sub-agent that shares one.
	if a.Family != "" {
		payload["family"] = a.Family
	}
	if a.Parent != "" {
		payload["parent"] = a.Parent
	}
	if a.Branch != "" {
		payload["branch"] = a.Branch
	}

	id := uuid.New()
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:    "agent_spawned",
		TypeVersion: 1,
		EventID:     id,
		// Zero mints a fresh instance: a legacy agent's existence is its own
		// unit of work, with no engine workflow to belong to.
		Payload: payload,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: recording the spawn of %q: %w", a.AgentName, err)
	}
	return id, nil
}

// RecordLegacyRetire closes the agent_spawned contract for agent.
//
// Returns (uuid.Nil, nil) when the agent has no open spawn contract. That is not
// an error and must not be treated as one: an agent that predates this slice, or
// one spawned while the log was off, has nothing to close, and a retire that
// failed on it would make the log's own gaps into an operator's problem.
func (l *Ledger) RecordLegacyRetire(ctx context.Context, agent, outcome string, merged bool) (uuid.UUID, error) {
	if outcome == "" {
		return uuid.Nil, fmt.Errorf("store: closing the spawn of %q requires an outcome", agent)
	}
	r, err := l.legacyReader()
	if err != nil {
		return uuid.Nil, err
	}
	open, err := r.OpenLegacySpawn(ctx, l.projectID, agent)
	if err != nil {
		return uuid.Nil, err
	}
	if open == nil {
		return uuid.Nil, nil
	}

	closeID := uuid.New()
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "agent_retired",
		TypeVersion:        1,
		EventID:            closeID,
		WorkflowInstanceID: open.WorkflowID,
		ClosesEventID:      &open.EventID,
		Payload: map[string]any{
			"agent_name": agent,
			"outcome":    outcome,
			"merged":     merged,
			"legacy":     true,
		},
	}); err != nil {
		return uuid.Nil, fmt.Errorf("store: recording the retire of %q: %w", agent, err)
	}
	return closeID, nil
}
