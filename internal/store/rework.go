// rework.go — the rework path (QUM-1252, M3a, AC3).
//
// A close is final and the log is monotone, so an owner who finds a defect in a
// result cannot reopen the goal. It emits `rework_requested`, which OPENS a new
// contract linked to the rejected event by the `follows_event_id` COLUMN — a
// chain the database enforces as a foreign key rather than one a payload field
// merely describes. The dispatcher drives that request into the next spawn
// exactly as it drives `goal_opened`, and the reworking agent discharges the new
// contract with its own `goal_closed`.
//
// Two halves live here because they are one mechanism read from both ends: the
// owner-facing WRITE (Ledger.RequestRework) and the dispatcher-side CONSUMER
// (ReworkHandler). Splitting them across files would let the payload they agree
// on drift with nothing to notice.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ReEngagement selects who redoes the work.
//
// The wire spelling, deliberately duplicated rather than shared with
// internal/engine's ReEngagement: the import direction is engine -> store, so
// store cannot name the engine's type, and these strings are the LOG's contract
// (they are written into a payload and read back by a different process),
// whereas the engine's is an in-memory enum. Two representations of one
// decision, and the seed's prose is the authority on the spelling.
type ReEngagement string

const (
	// ReEngageOriginal wakes the same agent with the miss pointed out.
	ReEngageOriginal ReEngagement = "re_engage_original"
	// DiscardAndRedo abandons the attempt and starts a fresh agent.
	DiscardAndRedo ReEngagement = "discard_and_redo"
)

// RequestedRework is what the caller needs to refer to the new contract.
type RequestedRework struct {
	// ReworkEventID is the CONTRACT the reworking agent closes. Not the goal it
	// follows: that one is already closed and closing it again is refused.
	ReworkEventID uuid.UUID
	WorkflowID    uuid.UUID
}

// RequestRework rejects a landed result and asks for the work to be redone.
//
// It appends and returns; nothing is running when it does. That is the same
// log-driven shape OpenGoal has, for the same reason — going through the log
// first makes the rejection a durable fact before any side effect, and the
// dispatcher picks it up under the act-once claim that already exists.
//
// The rejected event is identified by its own id and the link is written to the
// follows_event_id column, so a request naming an event that does not exist is
// refused by the foreign key rather than recorded as a chain that dangles.
//
// `owner` is the REQUESTER of the rework, which is who the redone result gets
// announced to. It is deliberately not read off the original goal: the agent
// rejecting a result is the one who will act on the replacement, and inheriting
// the first requester would send the redone answer to somebody who is no longer
// waiting for it.
func (l *Ledger) RequestRework(ctx context.Context, owner string, rejectedEventID uuid.UUID, reason string, re ReEngagement) (RequestedRework, error) {
	// Not redundant with Emit's disabled check: this function mints its ids
	// before emitting, so inheriting Emit's disabled-safe (0, nil) would hand
	// back an id for an event that does not exist.
	if !l.Enabled() {
		return RequestedRework{}, fmt.Errorf("store: the event log is disabled on this host, so rework cannot be recorded; enable it with `sprawl config set event_log.enabled true`")
	}
	if owner == "" {
		return RequestedRework{}, fmt.Errorf("store: rework needs an owner; it is who the redone result is reported to")
	}
	if reason == "" {
		return RequestedRework{}, fmt.Errorf("store: rework needs a reason; it is the only thing that tells the next agent what was wrong with the last result")
	}
	switch re {
	case DiscardAndRedo:
	case ReEngageOriginal, "never":
		// Refused rather than accepted-and-approximated. Waking the original
		// agent means finding it, proving it is still alive, and re-prompting a
		// live session — none of which the dispatcher can do — and falling
		// through to a fresh name would perform discard_and_redo while the log
		// said re_engage_original. Carried on QUM-1252.
		return RequestedRework{}, fmt.Errorf("store: re_engagement %q is not implemented: nothing downstream can wake the original agent, so the request would be recorded and then silently redone by a fresh one; use %q", ReEngageOriginal, DiscardAndRedo)
	default:
		return RequestedRework{}, fmt.Errorf("store: %q is not a re-engagement policy; use %q", re, DiscardAndRedo)
	}

	// The rejected event's instance, read back rather than taken from the
	// caller: a rework on a fresh instance is invisible in the goal's own log,
	// which is where anyone looking at the goal will look for it.
	r, err := l.eventLookup()
	if err != nil {
		return RequestedRework{}, err
	}
	rejected, err := r.ByID(ctx, rejectedEventID)
	if err != nil {
		return RequestedRework{}, fmt.Errorf("store: reading the event %s being reworked: %w", rejectedEventID, err)
	}

	out := RequestedRework{ReworkEventID: uuid.New(), WorkflowID: rejected.WorkflowInstanceID}
	if _, err := l.Emit(ctx, EmitRequest{
		TypeName:           "rework_requested",
		TypeVersion:        1,
		EventID:            out.ReworkEventID,
		WorkflowInstanceID: out.WorkflowID,
		FollowsEventID:     &rejectedEventID,
		Payload: map[string]any{
			"goal_event_id": rejectedEventID.String(),
			"owner":         owner,
			"reason":        reason,
			"re_engagement": string(re),
			"requested_by":  owner,
		},
	}); err != nil {
		return RequestedRework{}, fmt.Errorf("store: requesting rework of %s: %w", rejectedEventID, err)
	}
	return out, nil
}

// eventLookup builds a by-id reader bound to this Ledger's pool.
//
// Guarded exactly as goalReader is, and for the same reason: a disabled or
// unreachable store must not be able to answer a question about an event whose
// absence the caller will act on.
func (l *Ledger) eventLookup() (*PgEventReader, error) {
	if !l.Enabled() {
		return nil, fmt.Errorf("store: the event log is disabled on this host, so it cannot read the event being reworked; enable it with `sprawl config set event_log.enabled true`")
	}
	if l.degradedErr != nil {
		return nil, fmt.Errorf("store: the event log is unreachable, so rework would be recorded against an event nobody could confirm exists: %w", l.degradedErr)
	}
	return &PgEventReader{Pool: l.pool, Registry: l.registry}, nil
}

type ReworkHandlerDeps struct {
	Emitter EventEmitter
	Names   NameAllocator
	Lookup  EventLookup
	Logger  *slog.Logger
}

// ReworkHandler turns a rework_requested event into the next spawn request.
type ReworkHandler struct {
	emitter EventEmitter
	names   NameAllocator
	lookup  EventLookup
	log     *slog.Logger
}

var _ Handler = (*ReworkHandler)(nil)

func NewReworkHandler(d ReworkHandlerDeps) (*ReworkHandler, error) {
	switch {
	case d.Emitter == nil:
		return nil, fmt.Errorf("store: the rework handler needs an event emitter; without one it would consume rework_requested events and report success while requesting nothing")
	case d.Names == nil:
		return nil, fmt.Errorf("store: the rework handler needs a name allocator; spawn_requested requires an agent_name and the reconciler matches intents to local agents by name")
	case d.Lookup == nil:
		return nil, fmt.Errorf("store: the rework handler needs an event lookup; the rework payload carries the reason but not the task, which lives on the goal it follows")
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &ReworkHandler{emitter: d.Emitter, names: d.Names, lookup: d.Lookup, log: log}, nil
}

type reworkRequestedPayload struct {
	GoalEventID  string `json:"goal_event_id"`
	Owner        string `json:"owner"`
	Reason       string `json:"reason"`
	ReEngagement string `json:"re_engagement"`
}

// maxReworkChain bounds the walk back to the originating goal_opened.
//
// The walk is over follows_event_id, and a rework of a rework is expected —
// internal/engine's bug-investigation definition backtracks uncapped by design.
// The cap is therefore not a policy limit on rework, it is a termination
// guarantee for THIS walk: a cycle the foreign key cannot see (A follows B
// follows A) would otherwise make every dispatch of that event non-terminating.
const maxReworkChain = 32

func (h *ReworkHandler) Handle(ctx context.Context, ev DispatchedEvent) error {
	var rw reworkRequestedPayload
	if err := json.Unmarshal(ev.Payload, &rw); err != nil {
		return fmt.Errorf("store: rework_requested %s has an unreadable payload: %w", ev.ID, err)
	}
	if re := ReEngagement(rw.ReEngagement); re != DiscardAndRedo {
		// The write side refuses these too. Refused again here because the log
		// is a shared surface: an event appended by an older build, or by
		// anything other than RequestRework, reaches this handler unchecked.
		return fmt.Errorf("store: rework_requested %s asks for re_engagement %q, which this dispatcher does not implement; only %q is driveable",
			ev.ID, rw.ReEngagement, DiscardAndRedo)
	}
	if rw.Owner == "" {
		return fmt.Errorf("store: rework_requested %s names no owner, so a spawned agent would have no parent to report to", ev.ID)
	}
	if rw.Reason == "" {
		return fmt.Errorf("store: rework_requested %s carries no reason, so the next agent would be told to redo the work without being told what was wrong with it", ev.ID)
	}

	goal, goalID, err := h.originatingGoal(ctx, ev)
	if err != nil {
		return err
	}
	spec, ok := goalAgentTypes[GoalType(goal.GoalType)]
	if !ok {
		return fmt.Errorf("store: rework_requested %s follows a %q goal, which no agent type is mapped to (mapped: %v)",
			ev.ID, goal.GoalType, mappedGoalTypes())
	}

	// Named before the append, for the reason GoalSpawnHandler gives: an
	// unnamed spawn_requested is unreconcilable by construction.
	name, err := h.names.AllocateName(ctx, spec.agentType)
	if err != nil {
		return fmt.Errorf("store: allocating a %s name for rework_requested %s: %w", spec.agentType, ev.ID, err)
	}

	if _, err := h.emitter.Emit(ctx, EmitRequest{
		TypeName:    "spawn_requested",
		TypeVersion: 1,
		// The rework's instance, which is the goal's — it is how the fresh
		// agent finds the contract it has to close.
		WorkflowInstanceID: ev.WorkflowInstanceID,
		Payload: map[string]any{
			"agent_name": name,
			"agent_type": spec.agentType,
			"family":     spec.family,
			"parent":     rw.Owner,
			"branch":     goalBranch(name, ev.ID),
			// The contract in the prompt is the REWORK's id, not the goal's: the
			// goal is already closed, and an agent told to close it would be
			// refused by CloseGoalForAgent and have nowhere left to report.
			"prompt": reworkPrompt(GoalType(goal.GoalType), goal, rw, ev, goalID),
		},
	}); err != nil {
		return fmt.Errorf("store: requesting a %s for rework_requested %s: %w", spec.agentType, ev.ID, err)
	}
	h.log.Info("requested a fresh agent to redo rejected work",
		"rework_event_id", ev.ID, "goal_event_id", goalID, "agent", name, "agent_type", spec.agentType)
	return nil
}

// originatingGoal walks follows_event_id back to the goal_opened that started
// the chain, and returns it with its id.
//
// The walk exists because the rework payload carries the REASON but not the
// TASK: the work itself is only ever stated once, on the goal. A rework of a
// rework therefore has to chase the chain rather than read its immediate
// predecessor.
func (h *ReworkHandler) originatingGoal(ctx context.Context, ev DispatchedEvent) (goalOpenedPayload, uuid.UUID, error) {
	var zero goalOpenedPayload
	cur := ev
	for i := 0; i < maxReworkChain; i++ {
		if cur.FollowsEventID == nil {
			return zero, uuid.Nil, fmt.Errorf("store: rework_requested %s has no follows_event_id, so the goal it rejects cannot be identified and the next agent would have no task", cur.ID)
		}
		prev, err := h.lookup.ByID(ctx, *cur.FollowsEventID)
		if err != nil {
			return zero, uuid.Nil, fmt.Errorf("store: reading the event %s that rework_requested %s follows: %w", *cur.FollowsEventID, ev.ID, err)
		}
		if prev.SchemaName == "goal_opened" {
			var goal goalOpenedPayload
			if err := json.Unmarshal(prev.Payload, &goal); err != nil {
				return zero, uuid.Nil, fmt.Errorf("store: goal_opened %s has an unreadable payload: %w", prev.ID, err)
			}
			if goal.Text == "" {
				return zero, uuid.Nil, fmt.Errorf("store: goal_opened %s has empty text, which is the entire task the reworking agent would be given", prev.ID)
			}
			return goal, prev.ID, nil
		}
		cur = prev
	}
	return zero, uuid.Nil, fmt.Errorf("store: the follows chain from rework_requested %s did not reach a goal_opened within %d hops; refusing rather than walking a cycle the foreign key cannot see",
		ev.ID, maxReworkChain)
}

// reworkPrompt is the task the fresh agent gets: the original goal, plus what
// was wrong with the last attempt.
//
// It says the previous attempt was rejected rather than hiding it. An agent
// handed the same task with no explanation is likely to produce the same answer,
// which is the failure mode rework exists to break.
func reworkPrompt(gt GoalType, goal goalOpenedPayload, rw reworkRequestedPayload, ev DispatchedEvent, goalID uuid.UUID) string {
	return fmt.Sprintf(`%s

A previous attempt at this work was completed and REJECTED. What was wrong with it:

  %s

Start from that. Read the earlier attempt's log with `+"`get_workflow_log`"+` before you
begin — it is on this same workflow instance — and do not simply repeat it.

This work is an engine-driven rework (goal_type %q). Its identifiers:
  rework_event_id:      %s   <- the contract YOU close
  goal_event_id:        %s   (already closed; the original request)
  workflow_instance_id: %s

When you are done, close the rework with `+"`report_result`"+`. Report to %s.`,
		goal.Text, rw.Reason, string(gt), ev.ID, goalID, ev.WorkflowInstanceID, rw.Owner)
}
