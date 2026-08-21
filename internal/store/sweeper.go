package store

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/dmotles/sprawl/internal/state"
	"github.com/google/uuid"
)

// The stall sweeper (QUM-1250, AC5 and AC6; Appendix B items 3 and 4).
//
// It replaces what Claude's in-session task tracker used to do: notice that a
// goal has been open a long time with nothing happening, and nudge whoever owns
// it. What makes that hard is not the noticing — it is the four ways of being
// quiet that must NOT be nudged.
//
// LIVENESS COMES FROM TURN BOUNDARIES, NOT FROM A CLOCK (Appendix B item 4). The
// supervisor's 30-minute heartbeat was deliberately DELETED (QUM-730/QUM-1071)
// because a long quiet turn is legitimate — an agent can sit inside one turn for
// an hour on a slow build, and a wall-clock liveness check calls that dead. So
// "alive" here means "produced a run_started or turn_finished recently", which is
// a fact the log already carries.
//
// THE GATES. Each is a way of being quiet that is not being stalled, and each has
// a distinct cost if you get it wrong:
//
//	in turn            it is working. A poke is noise at best, preemption at worst.
//	operator-paused    a human decided this. StatusPaused is excluded from
//	                   auto-resume on purpose; overriding it is not the sweeper's
//	                   call.
//	human-owned        there is no process to poke. A person is the blocker.
//	transitively       it is blocked on a child goal or an unanswered question.
//	 blocked           Poking says "get on with it" about something it cannot get
//	                   on with — repeatedly, and pokes cost tokens.
//	terminal           retired or retiring. It cannot be woken at all.
//
// AND THE ONE THAT STOPS THE BLEEDING (AC6): per-goal exponential backoff, and at
// the cap a goal_stuck event plus quarantine. Quarantine is expressed as "a goal
// with a goal_stuck event is never poked again", which is what actually stops the
// token burn — as opposed to an escalation event that coexists with continued
// poking, which would look like a fix and cost the same.
//
// THE EPOCH IS DERIVED, NOT STORED. A goal's sweep epoch is the NUMBER OF
// goal_poke EVENTS for that goal. So the backoff exponent and the poke-dedup key
// are the same number, and neither can drift from the other. Reading it as a
// global sweep counter instead — the other plausible reading of
// "(goal_event_id, sweep_epoch)" — would key pokes by wall-clock sweep rather
// than per goal, and let two sweeps in the same second double-poke.
//
// KNOWN GAP, stated rather than left for someone to discover: the gates are
// evaluated against THIS HOST's AgentState, so an elected sweeper cannot evaluate
// them for an owner living on another host. Such an owner is SKIPPED, never poked
// blind — poking would risk overriding an operator's pause on a machine this host
// cannot observe. The consequence is that a goal owned elsewhere is not swept
// until a sweeper runs where its owner lives. That is the safe direction, and
// closing it properly needs the cross-host AgentState view that agent_sessions is
// explicitly NOT allowed to be (it is an advisory projection; nothing may derive
// a wake decision from it).

// HumanOwner is the owner name that denotes the human operator rather than an
// agent.
//
// M1b REPRESENTATION, and deliberately a placeholder: the plan of record models
// human-blocked waits as open USER_QUESTION contracts in a first-class user
// inbox, which is M3. Until that exists there has to be SOME way to express "this
// goal is waiting on a person", because AC5 requires the human-owned gate, and a
// reserved owner name is the smallest thing that works. When the user inbox
// lands, this collapses into the transitive-block gate — a goal whose owner has
// an open USER_QUESTION is already not stalled by that rule — and this constant
// can go.
const HumanOwner = "user"

// Poke discipline.
const (
	// pokeBackoffBase is the first interval. Doubling from here gives
	// 15m, 30m, 1h, 2h, 4h across the five pokes below.
	pokeBackoffBase = 15 * time.Minute
	// maxGoalPokes is the cap. Five pokes spanning ~7.75 hours of backoff is
	// long enough that a genuinely slow goal is not quarantined for being slow,
	// and short enough that a permanently wedged one stops costing tokens inside
	// a working day.
	maxGoalPokes = 5
	// DefaultStallAfter is how long an owner must be quiet before its open goal
	// is a stall candidate. Comfortably longer than a long turn, because a turn
	// boundary is the liveness signal and a threshold below the length of a real
	// turn would poke working agents.
	DefaultStallAfter = 30 * time.Minute
)

// pokeBackoff is the wait before poke number `epoch` (0-based).
// note records why a candidate was skipped.
func (r *SweepResult) note(goal uuid.UUID, why string) {
	if r.SkipReasons == nil {
		r.SkipReasons = map[uuid.UUID]string{}
	}
	r.SkipReasons[goal] = why
}

func pokeBackoff(epoch int) time.Duration {
	if epoch < 0 {
		epoch = 0
	}
	return time.Duration(1<<epoch) * pokeBackoffBase
}

// StalledCandidate is one open goal plus everything the gates need, computed by
// the reader in one query.
//
// Assembled by SQL rather than by N Go round trips per goal because a sweep runs
// on a timer over every open goal in a project — and because the alternative
// invites a subtle version of the same bug five times over.
type StalledCandidate struct {
	GoalEventID uuid.UUID
	WorkflowID  uuid.UUID
	Owner       string
	GoalType    string
	OpenedAt    time.Time
	// LastOwnerActivity is the most recent turn-boundary event for this owner.
	// ZERO means this owner has never produced one, which is a real state (an
	// agent that never started) and not an error — the goal's own age is used
	// instead.
	LastOwnerActivity time.Time
	// Pokes is how many goal_poke events exist for this goal. It IS the epoch.
	Pokes int
	// LastPokeAt is when the most recent poke happened. Zero if none.
	LastPokeAt time.Time
	// Quarantined is true when a goal_stuck event exists for this goal.
	Quarantined bool
	// OtherOpenContracts counts this owner's OTHER open contracts — a delegated
	// child goal, an unanswered question, an unacked notification. Non-zero
	// means blocked rather than stalled.
	OtherOpenContracts int
}

// SweepReader produces the candidates.
type SweepReader interface {
	OpenGoals(ctx context.Context, projectID uuid.UUID) ([]StalledCandidate, error)
}

// SweeperDeps follows the repo's deps-struct convention.
type SweeperDeps struct {
	Goals    SweepReader
	Local    LocalAgents
	Emitter  EventEmitter
	Injector Injector

	ProjectID uuid.UUID
	Host      string
	// StallAfter overrides DefaultStallAfter.
	StallAfter time.Duration
	Now        func() time.Time
	Logger     *slog.Logger
}

// SweepResult is what one pass did.
type SweepResult struct {
	Considered  int
	Poked       int
	Quarantined int
	// Skipped counts candidates a gate held back, so a sweep that pokes nothing
	// can be told from one that saw nothing.
	Skipped int
	// SkipReasons maps a goal to the gate that held it back.
	//
	// EXPOSED SO A TEST CAN ASSERT WHICH GATE FIRED, which is not a convenience:
	// without it every does-NOT-poke assertion is satisfied by ANY earlier gate,
	// so the whole gate suite could stay green while three gates went untested
	// after a reordering of gateFor. That is not hypothetical — one such test
	// (the human-owned gate) was found asserting the not-on-this-host gate by
	// accident, and hand-fixing it left the systemic weakness in place.
	SkipReasons map[uuid.UUID]string
}

// Sweep runs one pass over the project's open goals.
func Sweep(ctx context.Context, d SweeperDeps) (SweepResult, error) {
	var res SweepResult
	switch {
	case d.Goals == nil:
		return res, fmt.Errorf("store: sweeper needs a goal reader")
	case d.Local == nil:
		return res, fmt.Errorf("store: sweeper needs a view of local agents; without it the in-turn and operator-paused gates cannot be evaluated, and those gates are the only thing stopping it poking a working or deliberately-paused agent")
	case d.Emitter == nil:
		return res, fmt.Errorf("store: sweeper needs an event emitter")
	case d.Injector == nil:
		return res, fmt.Errorf("store: sweeper needs an injector to deliver a poke")
	case d.Host == "":
		return res, fmt.Errorf("store: sweeper needs a host identity")
	case d.ProjectID == uuid.Nil:
		return res, fmt.Errorf("store: sweeper needs a project id")
	}
	now := d.Now
	if now == nil {
		now = time.Now
	}
	stallAfter := d.StallAfter
	if stallAfter <= 0 {
		stallAfter = DefaultStallAfter
	}
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Local state FIRST, and a failure stops the pass. Every gate below reads
	// it, so degrading to "no local agents" would make all of them unevaluable
	// while leaving the poke path intact.
	locals, err := d.Local.Snapshot(ctx)
	if err != nil {
		return res, fmt.Errorf("store: sweeper cannot read local agent state, so it is refusing to evaluate its own gates: %w", err)
	}
	localByName := make(map[string]LocalAgent, len(locals))
	for _, l := range locals {
		localByName[l.Name] = l
	}

	candidates, err := d.Goals.OpenGoals(ctx, d.ProjectID)
	if err != nil {
		return res, fmt.Errorf("store: reading open goals: %w", err)
	}

	for _, c := range candidates {
		res.Considered++
		skip, why := gateFor(c, localByName, now(), stallAfter)
		if skip {
			res.Skipped++
			res.note(c.GoalEventID, why)
			log.Debug("not poking", "goal", c.GoalEventID, "owner", c.Owner, "reason", why)
			continue
		}

		// AC6's cap, checked BEFORE the poke rather than after it: at the cap the
		// goal is quarantined and nothing further is delivered, ever.
		if c.Pokes >= maxGoalPokes {
			// A DERIVED id: a goal is quarantined once, ever. Without it, N
			// concurrently sweeping hosts each write a goal_stuck for one goal —
			// harmless to behaviour, since quarantine is existence-based, but a
			// duplicated side effect on the audit trail in the one place this
			// file's own rule was not applied.
			if _, err := d.Emitter.Emit(ctx, EmitRequest{
				TypeName:           "goal_stuck",
				TypeVersion:        1,
				EventID:            DerivedEventID(kindGoalStuck, c.GoalEventID.String()),
				WorkflowInstanceID: c.WorkflowID,
				Payload: map[string]any{
					"goal_event_id": c.GoalEventID.String(),
					"owner":         c.Owner,
					"pokes":         c.Pokes,
					"reason":        fmt.Sprintf("no progress after %d pokes; quarantined and no longer poked", c.Pokes),
					"host":          d.Host,
				},
			}); err != nil && !IsUniqueViolation(err) {
				return res, fmt.Errorf("store: quarantining goal %s: %w", c.GoalEventID, err)
			}
			res.Quarantined++
			log.Warn("goal reached the poke cap; quarantined and will not be poked again",
				"goal", c.GoalEventID, "owner", c.Owner, "pokes", c.Pokes)
			continue
		}

		// RECORDED BEFORE DELIVERED, with a DERIVED (goal, epoch) id and NO
		// CLAIM. The id is what makes a split-brain second sweeper's poke collide
		// — Appendix B item 3's conditional insert, expressed as the primary key
		// of `events` rather than as a row in `event_claims`.
		//
		// The claim that used to be here was a permanent-loss defect, and the
		// mechanism is worth restating because it is subtle: the epoch is derived
		// from the COUNT of goal_poke events, so a claim taken before an append
		// that then FAILED left the epoch unchanged and the claim held — and the
		// next sweep computed the same key, lost it to its own corpse, and
		// skipped. Forever. The goal was never poked again AND never quarantined,
		// reported under `Skipped` where it is indistinguishable from the five
		// legitimate gates. Verified with a probe in code review.
		idle := now().Sub(activitySince(c))
		pokeErr := func() error {
			_, err := d.Emitter.Emit(ctx, EmitRequest{
				TypeName:           "goal_poke",
				TypeVersion:        1,
				EventID:            DerivedEventID(kindGoalPoke, c.GoalEventID.String(), strconv.Itoa(c.Pokes)),
				WorkflowInstanceID: c.WorkflowID,
				Payload: map[string]any{
					"goal_event_id": c.GoalEventID.String(),
					"owner":         c.Owner,
					"epoch":         c.Pokes,
					"reason":        fmt.Sprintf("no turn boundary for %s while this goal is open", idle.Round(time.Minute)),
					"host":          d.Host,
					"idle_seconds":  int(idle.Seconds()),
				},
			})
			return err
		}()
		if pokeErr != nil {
			if IsUniqueViolation(pokeErr) {
				// Another sweeper already poked this (goal, epoch). Skip — and
				// note this is now decided by the DATABASE rather than by a
				// claim, so there is no window in which the marker exists and
				// the record does not.
				res.Skipped++
				log.Debug("another sweeper already poked this epoch",
					"goal", c.GoalEventID, "epoch", c.Pokes)
				continue
			}
			// A real failure. NOTHING was written, so the epoch is unchanged and
			// the next sweep retries this exact poke — which is the whole point
			// of not holding a claim across the append.
			return res, fmt.Errorf("store: recording poke %d for goal %s: %w", c.Pokes, c.GoalEventID, pokeErr)
		}
		res.Poked++

		if err := d.Injector.Inject(ctx, c.Owner, pokeBody(c)); err != nil {
			// The goal_poke event STAYS. The poke was attempted and the backoff
			// must advance regardless, or a persistently unreachable owner is
			// poked at the base interval forever — the runaway AC6 exists to
			// prevent.
			return res, fmt.Errorf("store: delivering poke %d to %q for goal %s (the poke is recorded, so the backoff still advances): %w",
				c.Pokes, c.Owner, c.GoalEventID, err)
		}
		log.Info("poked a stalled goal's owner",
			"goal", c.GoalEventID, "owner", c.Owner, "epoch", c.Pokes, "idle", idle)
	}
	return res, nil
}

// activitySince is the timestamp the stall is measured from.
//
// The owner's last turn boundary when there is one; otherwise the goal's own
// opening. The fallback is not arbitrary: an owner that has NEVER produced a turn
// boundary has no activity to compare against, and treating that as "infinitely
// stale" would poke a goal opened one second ago, while treating it as "just
// active" would make such a goal immortal.
func activitySince(c StalledCandidate) time.Time {
	if c.LastOwnerActivity.IsZero() {
		return c.OpenedAt
	}
	return c.LastOwnerActivity
}

// gateFor decides whether this candidate is poked, and says why not.
//
// The reason string is returned rather than logged in place so the caller logs
// it once, and — more usefully — so a test can assert WHICH gate held rather
// than merely that nothing happened.
func gateFor(c StalledCandidate, locals map[string]LocalAgent, now time.Time, stallAfter time.Duration) (skip bool, why string) {
	if c.Quarantined {
		return true, "quarantined: a goal_stuck event already exists for this goal"
	}
	if c.Owner == "" {
		return true, "the goal names no owner, so there is nobody to poke"
	}
	if c.Owner == HumanOwner {
		return true, "human-owned wait: a person is the blocker and there is no process to poke"
	}
	if c.OtherOpenContracts > 0 {
		return true, fmt.Sprintf("transitively blocked: the owner has %d other open contract(s)", c.OtherOpenContracts)
	}

	local, known := locals[c.Owner]
	if !known {
		// See the KNOWN GAP note in the file header. Skipping is the safe
		// direction: this host cannot evaluate the in-turn or operator-paused
		// gates for an owner it cannot see, and poking blind could override a
		// pause set on a machine it cannot observe.
		return true, "the owner is not on this host, so its in-turn and paused state cannot be observed"
	}
	// THE TRI-STATE GATE. An UNOBSERVED turn state is not an idle one.
	//
	// Turn state exists only in the supervisor's in-memory phase machine, so a
	// sweeper running outside that process cannot see it. Treating the resulting
	// `false` as "idle" would poke working agents — and it would do so silently,
	// on every sweep, with the log showing nothing but a lot of pokes. Skipping
	// is the safe direction and makes the limitation visible in the reason
	// string rather than latent in the behaviour.
	switch local.Turn {
	case TurnUnknown:
		return true, "the owner's turn state is not observable from this process, and an unobserved turn state is not an idle one"
	case TurnInTurn:
		return true, "the owner is mid-turn"
	case TurnIdle:
		// Observed idle: keep going.
	}
	if local.Status == state.StatusPaused {
		return true, "the owner is operator-paused, which is deliberately excluded from auto-resume"
	}
	if state.IsTerminal(local.Status) {
		return true, "the owner is " + local.Status + " and cannot be woken"
	}

	if idle := now.Sub(activitySince(c)); idle < stallAfter {
		return true, fmt.Sprintf("the owner was active %s ago, inside the %s stall threshold", idle.Round(time.Second), stallAfter)
	}
	// Backoff. Checked last so the reason strings above take precedence in a log
	// line — "paused" is more useful than "poked recently".
	if !c.LastPokeAt.IsZero() {
		if wait := pokeBackoff(c.Pokes - 1); now.Sub(c.LastPokeAt) < wait {
			return true, fmt.Sprintf("poked %s ago, inside the %s backoff for epoch %d",
				now.Sub(c.LastPokeAt).Round(time.Second), wait, c.Pokes)
		}
	}
	return false, ""
}

// pokeBody is the nudge. Bounded and event-naming for the same reasons as a
// notification body — see notifyBodyMaxRunes.
func pokeBody(c StalledCandidate) string {
	return truncateRunes(fmt.Sprintf(
		"Your %s goal has been open with no turn boundary from you. Re-read it with reread_my_goal or get_workflow_log: goal event %s",
		c.GoalType, c.GoalEventID), notifyBodyMaxRunes)
}
