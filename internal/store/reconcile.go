package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Startup reconciliation: local AgentState <-> spawn intents (QUM-1250, AC4).
//
// A host that restarts has two sources of truth that may disagree — the log,
// which says what was meant to happen, and its own disk, which says what did.
// Reconciliation makes them agree again, and it is the second half of what makes
// claims.go's lease takeover safe: the write-ahead records the intention, and
// this is what reads it back.
//
// ===========================================================================
// THE DELIBERATE DEVIATION FROM THE SPEC. DO NOT "FIX" THIS BACK.
// ===========================================================================
//
// The issue says "GC local resources matching none [no intent]". TAKEN
// LITERALLY, THE FIRST RECONCILE PASS ON ANY REAL HOST DELETES EVERY AGENT'S
// WORKTREE, and it is worth being precise about why, because the reasoning is
// what stops someone restoring the literal rule as a bug fix:
//
//   - Every agent that exists today was created through the legacy MCP `spawn`
//     path, which writes no spawn_intent.
//   - The M1b dispatcher ships UNWIRED (approved decision #3 on QUM-1250), so it
//     has created exactly zero agents.
//
// So on every host in existence right now, "local resources matching no intent"
// is not an edge case: it is the entire fleet, weave included.
//
// The rule implemented instead is GC ONLY WHAT IS ATTRIBUTABLE — a local
// resource is reclaimed only when THIS SYSTEM already declared its spawn failed:
//
//	open intent + matching local agent            -> ADOPT (append spawn_committed)
//	open intent + no local agent + past grace     -> spawn_failed
//	open intent + no local agent + inside grace   -> leave alone (spawn in flight)
//	local agent whose intent was closed FAILED    -> RECLAIM (the stray)
//	local agent matching NO intent                -> LEAVE ALONE. Always.
//
// That still satisfies all three legs of AC4 — the stray is real, and it is
// GC'd — while making it impossible for this code to destroy something it did
// not create. Approved on QUM-1250; the last row is asserted by
// TestReconcile_LocalAgentMatchingNoIntentIsNeverTouched, because a comment
// saying "we don't do that" is not a check, and the diff that widens this back
// to spec would otherwise be green.
//
// Everything here is scoped by HOST AFFINITY. Reconciling another host's intent
// would emit spawn_failed for an agent that exists perfectly well where it
// belongs — closing a contract that host is still working under, and making its
// eventual spawn_committed dead-letter on ErrNoOpenContract.

// LocalAgent is this host's view of one agent it hosts.
//
// A projection rather than internal/state.AgentState: this package must not
// import the supervisor, and the reconciler needs four fields rather than the
// whole lifecycle taxonomy. The adapter that fills it lives outside.
type LocalAgent struct {
	Name     string
	Status   string
	Branch   string
	Worktree string
	// SessionID is empty when this agent has no backend session. Read by the
	// owner-dead check: "died with no session" is the permanent case, where
	// "died but a session is recorded" may still be revivable.
	SessionID string
	// InTurn and InTurnObserved are the sweeper's in-turn gate, and they are a
	// TRI-STATE on purpose: in-turn, not-in-turn, and NOT KNOWN.
	//
	// A plain bool here was a real defect, found while writing the adapters and
	// fixed in the same commit. Turn state lives only in the supervisor's
	// in-memory phase machine (internal/runtime's UnifiedRuntime.State().InTurn)
	// and has NO on-disk representation — internal/state.AgentState carries no
	// such field. So a process that is not the supervisor cannot observe it, and
	// a plain bool forces such a process to report `false`, which the sweeper
	// would read as "not in turn" and poke a working agent.
	//
	// That is precisely the defect internal/supervisor/runtime.go's
	// InTurnObserved already names: "Accepting the session probe's 'not in turn'
	// when the authority is absent would be a negative answer derived from an
	// unavailable observation." Same shape, one layer up, so the same tri-state
	// answer — and the sweeper SKIPS an agent whose turn state is unobserved
	// rather than assuming it is idle.
	InTurn bool
	// InTurnObserved is false when this view cannot see turn state at all.
	// A view that reports InTurn: false with InTurnObserved: false is saying
	// "I do not know", not "it is idle".
	InTurnObserved bool
}

// LocalAgents is the host's own view of itself.
type LocalAgents interface {
	Snapshot(ctx context.Context) ([]LocalAgent, error)
	// Reclaim removes the local resources for an agent. DESTRUCTIVE, and the
	// only destructive operation in the dispatch layer.
	Reclaim(ctx context.Context, name string) error
}

// OpenIntent is a spawn_intent with no close.
type OpenIntent struct {
	EventID      uuid.UUID
	AgentName    string
	AgentType    string
	Branch       string
	HostAffinity string
	At           time.Time
	WorkflowID   uuid.UUID
}

// FailedIntent is a spawn_intent already closed by spawn_failed. The only
// evidence that licenses a reclaim.
type FailedIntent struct {
	EventID      uuid.UUID
	AgentName    string
	HostAffinity string
	Worktree     string
	Branch       string
	WorkflowID   uuid.UUID
}

// IntentReader reads intent state out of the log.
type IntentReader interface {
	OpenIntents(ctx context.Context, projectID uuid.UUID, host string) ([]OpenIntent, error)
	FailedIntents(ctx context.Context, projectID uuid.UUID, host string) ([]FailedIntent, error)
}

// ReconcileDeps follows the repo's deps-struct convention.
type ReconcileDeps struct {
	Intents   IntentReader
	Local     LocalAgents
	Emitter   EventEmitter
	ProjectID uuid.UUID
	Host      string
	// Grace is how long a traceless intent is left alone before it is declared
	// failed. It must comfortably exceed a real spawn, because "the worktree does
	// not exist yet" is the NORMAL state for the first seconds of one — a grace
	// that is too short kills spawns that are still in progress.
	Grace  time.Duration
	Now    func() time.Time
	Logger *slog.Logger
}

// DefaultReconcileGrace is how long an intent with no local trace is given.
//
// Sized above a worst-case spawn (worktree creation plus a session launch) with
// room for a loaded host, and it fails in the safe direction: too long merely
// delays a spawn_failed for something that was never going to appear, while too
// short destroys work in flight.
const DefaultReconcileGrace = 10 * time.Minute

// ReconcileResult is what a pass did. Unattributed is reported rather than
// silently skipped: a refusal nobody can see is indistinguishable from a
// reconciler that never ran.
type ReconcileResult struct {
	Adopted      int
	Failed       int
	Reclaimed    int
	Unattributed int
	InFlight     int
}

// Reconcile brings the log and this host's disk back into agreement.
func Reconcile(ctx context.Context, d ReconcileDeps) (ReconcileResult, error) {
	var res ReconcileResult
	switch {
	case d.Intents == nil:
		return res, fmt.Errorf("store: reconcile needs an intent reader")
	case d.Local == nil:
		return res, fmt.Errorf("store: reconcile needs a view of local agents")
	case d.Emitter == nil:
		return res, fmt.Errorf("store: reconcile needs an event emitter")
	case d.ProjectID == uuid.Nil:
		return res, fmt.Errorf("store: reconcile needs a project id")
	case d.Host == "":
		return res, fmt.Errorf("store: reconcile needs a host identity; without one it cannot tell its own intents from another host's, and would close contracts another host is working under")
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	grace := d.Grace
	if grace <= 0 {
		grace = DefaultReconcileGrace
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Local state is read FIRST and a failure STOPS the pass.
	//
	// This ordering is load-bearing. An unreadable snapshot that degraded to "no
	// local agents" would make every open intent look traceless — so every one
	// past its grace period would be declared failed — and every failed intent
	// look already-cleaned. The difference between doing nothing and destroying
	// everything is one swallowed error.
	locals, err := d.Local.Snapshot(ctx)
	if err != nil {
		return res, fmt.Errorf("store: reconcile cannot read local agent state, so it is refusing to act on the log alone: %w", err)
	}
	localByName := make(map[string]LocalAgent, len(locals))
	for _, l := range locals {
		localByName[l.Name] = l
	}

	open, err := d.Intents.OpenIntents(ctx, d.ProjectID, d.Host)
	if err != nil {
		return res, fmt.Errorf("store: reading open spawn intents: %w", err)
	}
	failed, err := d.Intents.FailedIntents(ctx, d.ProjectID, d.Host)
	if err != nil {
		return res, fmt.Errorf("store: reading failed spawn intents: %w", err)
	}

	// Names any intent mentions. What is NOT in here is what the deviation above
	// protects.
	mentioned := make(map[string]bool, len(open)+len(failed))

	for _, in := range open {
		mentioned[in.AgentName] = true
		if local, exists := localByName[in.AgentName]; exists {
			// ADOPTION. The resource is already there — the crash was between
			// the spawn and the commit event. Close the intent and create
			// nothing.
			if _, err := d.Emitter.Emit(ctx, EmitRequest{
				TypeName:           "spawn_committed",
				TypeVersion:        1,
				WorkflowInstanceID: in.WorkflowID,
				ClosesEventID:      &in.EventID,
				Payload: map[string]any{
					"agent_name": in.AgentName,
					"host":       d.Host,
					"branch":     local.Branch,
					"worktree":   local.Worktree,
					"adopted":    true,
				},
			}); err != nil {
				return res, fmt.Errorf("store: adopting orphan %q: %w", in.AgentName, err)
			}
			res.Adopted++
			log.Info("adopted an orphaned agent whose spawn intent was still open",
				"agent", in.AgentName, "intent", in.EventID, "worktree", local.Worktree)
			continue
		}

		// No local trace. Inside the grace window this is the NORMAL state of a
		// spawn in flight, so it is left alone.
		age := now().Sub(in.At)
		if age < grace {
			res.InFlight++
			log.Debug("spawn intent has no local trace yet and is inside the grace window",
				"agent", in.AgentName, "intent", in.EventID, "age", age, "grace", grace)
			continue
		}

		if _, err := d.Emitter.Emit(ctx, EmitRequest{
			TypeName:           "spawn_failed",
			TypeVersion:        1,
			WorkflowInstanceID: in.WorkflowID,
			ClosesEventID:      &in.EventID,
			Payload: map[string]any{
				"agent_name": in.AgentName,
				"reason":     fmt.Sprintf("no local trace of this agent on %s after %s", d.Host, age.Round(time.Second)),
				"host":       d.Host,
				"traceless":  true,
			},
		}); err != nil {
			return res, fmt.Errorf("store: failing traceless intent for %q: %w", in.AgentName, err)
		}
		res.Failed++
		log.Warn("spawn intent has no local trace past the grace period; recording it as failed",
			"agent", in.AgentName, "intent", in.EventID, "age", age)
	}

	for _, in := range failed {
		mentioned[in.AgentName] = true
		local, exists := localByName[in.AgentName]
		if !exists {
			continue
		}

		// THE STRAY, and the only reclaim this code will ever perform: we
		// already said this spawn failed, so a resource for it must not survive.
		//
		// The RESOURCE GOES FIRST and the record second. The reverse order writes
		// a claim into an append-only log that can never be retracted — an
		// operator would read "reclaimed" while the worktree is still on disk.
		if err := d.Local.Reclaim(ctx, in.AgentName); err != nil {
			return res, fmt.Errorf("store: reclaiming the stray resources of %q, whose spawn was already recorded as failed: %w", in.AgentName, err)
		}
		if _, err := d.Emitter.Emit(ctx, EmitRequest{
			TypeName:           "stray_reclaimed",
			TypeVersion:        1,
			WorkflowInstanceID: in.WorkflowID,
			Payload: map[string]any{
				"agent_name":      in.AgentName,
				"reason":          "local resources survived a spawn already recorded as failed",
				"host":            d.Host,
				"worktree":        local.Worktree,
				"branch":          local.Branch,
				"intent_event_id": in.EventID.String(),
			},
		}); err != nil {
			// The resource is GONE and the record failed. Loud, because this is
			// now an unattributable deletion — exactly what stray_reclaimed
			// exists to prevent — and no retry can undo it.
			log.Error("reclaimed a stray agent's resources but could not record it; the deletion is now unattributable in the log",
				"agent", in.AgentName, "intent", in.EventID, "error", err)
			return res, fmt.Errorf("store: recording the reclaim of %q: %w", in.AgentName, err)
		}
		res.Reclaimed++
		log.Warn("reclaimed the local resources of an agent whose spawn was recorded as failed",
			"agent", in.AgentName, "intent", in.EventID, "worktree", local.Worktree)
	}

	// What is left is every local agent no intent mentions — which today is
	// every agent on the host. Counted and logged, never touched. See the
	// deviation note at the top of this file.
	for _, l := range locals {
		if mentioned[l.Name] {
			continue
		}
		res.Unattributed++
		log.Debug("local agent is not mentioned by any spawn intent; leaving it strictly alone",
			"agent", l.Name, "worktree", l.Worktree)
	}
	if res.Unattributed > 0 {
		log.Info("reconcile left agents alone because no spawn intent mentions them (this is expected: agents created outside the dispatcher have no intent)",
			"count", res.Unattributed, "host", d.Host)
	}
	return res, nil
}
