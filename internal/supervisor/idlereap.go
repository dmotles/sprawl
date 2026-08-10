// Package supervisor — QUM-1186 lane 3: the observed idle reaper.
//
// Deleting report_status deleted the tree's only self-service teardown
// trigger: Real.ReportStatus was the sole caller of StopAfterTurn, which is
// what reclaims an agent's ~280MB of subprocess RSS. This file is the
// replacement, and it is deliberately built the opposite way round — an agent
// no longer ASSERTS that it is finished, the supervisor OBSERVES that it is
// idle.
//
// Every term of the predicate is an observation of a process, a file, or an
// in-memory queue. None of them is something an agent said about itself.
//
// D1a, the rule that shapes the whole file: an UNAVAILABLE observation is not
// a negative one. Reaping tears down a live subprocess, so a term that could
// not be resolved must BLOCK the reap. That is why idleObs is a tri-state and
// not a bool — a bool that is false because nobody could measure it is
// indistinguishable from one that is false because the agent is genuinely
// quiet, and only one of those two justifies a destructive action.
package supervisor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
)

// idleObs is the tri-state result of ONE reap precondition.
type idleObs int

const (
	// obsUnavailable means the observation could not be made. It NEVER counts
	// as evidence of idleness.
	obsUnavailable idleObs = iota
	// obsIdle means the term was observed and is consistent with reaping.
	obsIdle
	// obsBusy means the term was observed and blocks the reap.
	obsBusy
)

func (o idleObs) String() string {
	switch o {
	case obsIdle:
		return "idle"
	case obsBusy:
		return "busy"
	default:
		return "unavailable"
	}
}

// idleRuntimeProbe is the narrow observation surface the predicate consumes.
// *AgentRuntime implements it in production; the two "Observed" methods exist
// precisely because the plain InTurn()/UnifiedRuntime() accessors collapse
// "could not measure" into the same value as "measured, and it is fine".
type idleRuntimeProbe interface {
	InTurnObserved() (inTurn bool, observed bool)
	LastActivityAt() time.Time
	InFlightSystemObserved() (n int, observed bool)
}

// questionPendingProbe answers whether an agent has an outstanding
// ask_user_question. Satisfied by *questionQueue.
type questionPendingProbe interface {
	hasPendingFrom(name string) bool
}

var (
	_ idleRuntimeProbe     = (*AgentRuntime)(nil)
	_ questionPendingProbe = (*questionQueue)(nil)
)

// idleInputs is everything assessIdle looks at. Passed as a struct so a new
// term cannot be added by silently widening a positional signature.
type idleInputs struct {
	Name       string
	RootName   string
	SprawlRoot string
	Probe      idleRuntimeProbe
	Questions  questionPendingProbe
	Now        time.Time
	Threshold  time.Duration
}

// idleAssessment records EVERY term the decision looked at, not just the
// verdict, so a log line and a failing test can both name which observation
// blocked the reap — and can tell "busy" from "could not tell".
type idleAssessment struct {
	InTurn    idleObs
	Pending   idleObs
	InFlight  idleObs
	Question  idleObs
	Quiescent idleObs
	NotRoot   idleObs

	// Reap is true iff every term above is obsIdle.
	Reap bool
	// Blocker names the first term that was not obsIdle, distinguishing a
	// busy observation from an unavailable one. Empty when Reap is true.
	Blocker string
}

// assessIdle evaluates the six reap preconditions. It performs no teardown and
// takes no locks of its own — it is a pure function of its inputs plus two
// cheap reads (the queue directory, the question queue).
func assessIdle(in idleInputs) idleAssessment {
	// Threshold 0 means the reaper is switched off. Checked first and
	// independently of the ticker's own start-time check: without it, every
	// age would exceed a zero threshold and a future caller that skipped the
	// start-time check would reap the entire fleet.
	if in.Threshold <= 0 {
		return idleAssessment{Blocker: "disabled"}
	}

	a := idleAssessment{}

	// Root. Not "unavailable" — an empty or root name is a definite refusal.
	// weave owns the operator's console; reaping it takes the fleet's UI with
	// it.
	if in.Name == "" || in.Name == in.RootName {
		a.NotRoot = obsBusy
	} else {
		a.NotRoot = obsIdle
	}

	// InTurn. The unavailable arm is the D1a case: a nil handle, or a handle
	// that is not a turnProbe, yields "false" from the plain accessor, and
	// that false is not evidence of anything.
	switch inTurn, observed := probeInTurn(in.Probe); {
	case !observed:
		a.InTurn = obsUnavailable
	case inTurn:
		a.InTurn = obsBusy
	default:
		a.InTurn = obsIdle
	}

	// Durable queue. A MISSING pending/ dir is a real "no mail" answer
	// (agentloop.listDir returns no error for it); an I/O error is not.
	if pending, err := agentloop.ListPending(in.SprawlRoot, in.Name); err != nil {
		a.Pending = obsUnavailable
	} else if len(pending) > 0 {
		a.Pending = obsBusy
	} else {
		a.Pending = obsIdle
	}

	// In-flight system entries: mail already handed to the runtime but not yet
	// consumed. No UnifiedRuntime to ask means unavailable, not clean.
	switch n, observed := probeInFlight(in.Probe); {
	case !observed:
		a.InFlight = obsUnavailable
	case n > 0:
		a.InFlight = obsBusy
	default:
		a.InFlight = obsIdle
	}

	// Outstanding ask_user_question. Reaping here would deliver the operator's
	// answer to a process that no longer exists.
	switch {
	case in.Questions == nil:
		a.Question = obsUnavailable
	case in.Questions.hasPendingFrom(in.Name):
		a.Question = obsBusy
	default:
		a.Question = obsIdle
	}

	// Quiescence. The zero time is BOTH "never observed" and "infinitely old",
	// and only the second reading would permit a reap — so it must be treated
	// as unavailable, explicitly.
	switch last := probeLastActivity(in.Probe); {
	case last.IsZero():
		a.Quiescent = obsUnavailable
	case in.Now.Sub(last) > in.Threshold:
		a.Quiescent = obsIdle
	default:
		a.Quiescent = obsBusy
	}

	// One loop, one rule: reap iff every term is obsIdle. Written as a table
	// rather than a chain of &&s so adding a term cannot forget to include it
	// in the verdict.
	terms := []struct {
		name string
		obs  idleObs
	}{
		{"not_root", a.NotRoot},
		{"in_turn", a.InTurn},
		{"pending_queue", a.Pending},
		{"in_flight_system", a.InFlight},
		{"question", a.Question},
		{"quiescent", a.Quiescent},
	}
	for _, term := range terms {
		if term.obs == obsIdle {
			continue
		}
		if term.obs == obsUnavailable {
			a.Blocker = term.name + "_unobservable"
		} else {
			a.Blocker = term.name
		}
		return a
	}
	a.Reap = true
	return a
}

// probeInTurn / probeInFlight / probeLastActivity fold a nil probe into the
// unavailable answer, so "no runtime at all" flows through the same D1a arm as
// "a runtime that cannot be measured".
func probeInTurn(p idleRuntimeProbe) (bool, bool) {
	if p == nil {
		return false, false
	}
	return p.InTurnObserved()
}

func probeInFlight(p idleRuntimeProbe) (int, bool) {
	if p == nil {
		return 0, false
	}
	return p.InFlightSystemObserved()
}

func probeLastActivity(p idleRuntimeProbe) time.Time {
	if p == nil {
		return time.Time{}
	}
	return p.LastActivityAt()
}

// idleInputsFor assembles the predicate's inputs for one runtime.
func (r *Real) idleInputsFor(rt *AgentRuntime) idleInputs {
	in := idleInputs{
		RootName:   r.callerName,
		SprawlRoot: r.sprawlRoot,
		Questions:  r.questions,
		Now:        time.Now(),
		Threshold:  r.idleReclaimAfter.get(),
	}
	if rt != nil {
		in.Name = rt.Name()
		in.Probe = rt
	}
	return in
}

// maybeReclaimIdle is the reaper's per-agent action: assess, and if the agent
// is observably idle, hand its subprocess back.
//
// Three phases, and the gate boundaries are the design:
//
//	A (gate held)     final assessment — linearised against SendMessage's
//	                  liveness decision + Enqueue, so a send that has already
//	                  been decided cannot be reaped out from under.
//	B (gate RELEASED) StopAfterTurn. Held here it would stall every send to
//	                  this agent for the full runaway budget, and StopAfterTurn
//	                  can legitimately wait out a whole turn.
//	C (gate held)     backstop — re-read the queue. Anything enqueued during B
//	                  is invisible to A, and nothing else would pick it up: the
//	                  poke was dropped because the runtime went away, and child
//	                  handles have no redrain ticker.
//
// The gate alone closes the before-A and after-C cases; only the backstop
// closes the during-B case. Both are required. Do not simplify this to one.
func (r *Real) maybeReclaimIdle(sweepCtx context.Context, rt *AgentRuntime) {
	if rt == nil {
		return
	}
	name := rt.Name()
	if name == "" {
		return
	}
	gate := r.reclaimGate(name)

	// Phase A.
	gate.Lock()
	inputs := r.idleInputsFor(rt)
	assessment := assessIdle(inputs)
	gate.Unlock()
	if !assessment.Reap {
		// Debug, not Info: this fires once per agent per sweep forever, and an
		// Info-level line at that cadence is a log nobody can read. The level
		// is the only concession — the CONTENT is identical to the reap line,
		// because "why was this agent NOT reaped" is the question people will
		// actually be asking once the reaper ships disabled by default, and a
		// record that only explains reaps cannot answer it.
		logAssessment(slog.LevelDebug, "idle reclaim: agent not reclaimed", name, assessment, inputs)
		return
	}
	// Declining here rather than mid-teardown is what keeps Shutdown from
	// waiting out a full stop budget for a reap it never needed to start.
	if sweepCtx != nil && sweepCtx.Err() != nil {
		return
	}

	// Phase B. StopAfterTurnIf, never Stop and never the unguarded
	// StopAfterTurn. Two distinct protections, and both are needed:
	//
	//   - subscribe-before-check DEFERS the teardown past a turn that began
	//     after phase A;
	//   - the guard ABANDONS it. Every StopAfterTurn arm stops, including the
	//     runaway timer, so deferral alone still kills an agent that acquired
	//     real work during the wait — one turn later, silently. A stop that can
	//     only be deferred is not enough when the agent never consented to it.
	//
	// The guard holds the reclaim gate ACROSS the re-check and the stop, so
	// "still idle" and "stopped" are one step against Real.SendMessage rather
	// than two. Releasing before the stop would only re-check, which is a
	// narrower window, not a closed one.
	guard := func() (bool, func()) {
		gate.Lock()
		in := r.idleInputsFor(rt)
		a := assessIdle(in)
		if !a.Reap {
			logAssessment(slog.LevelDebug, "idle reclaim: teardown abandoned, agent became active", name, a, in)
			gate.Unlock()
			return false, nil
		}
		logAssessment(slog.LevelInfo, "idle reclaim: reaping agent", name, a, r.idleInputsFor(rt))
		return true, gate.Unlock
	}
	ctx, cancel := context.WithTimeout(context.Background(), idleReclaimStopBudget)
	defer cancel()
	stopped, stopErr := rt.StopAfterTurnIf(ctx, idleReclaimStopBudget, stopReasonIdleReclaim, guard)
	if !stopped && stopErr == nil {
		// Abandoned: the agent became active while we waited. Nothing was torn
		// down and nothing needs waking — the next sweep reconsiders. A ticker
		// that runs forever can afford to lose a race; it cannot afford to win
		// one wrongly.
		return
	}
	// ctx is deliberately NOT derived from sweepCtx. A teardown already under
	// way must run to completion, and the backstop below must be able to wake
	// an agent whose mail arrived mid-reap even if the supervisor is shutting
	// down — otherwise the entry sits in pending/ behind a StatusIdle agent
	// that boot does not resume.

	// Phase C.
	gate.Lock()
	defer gate.Unlock()
	if stopErr != nil {
		// A failed StopWithReason leaves the handle attached and the process
		// alive — there is nothing to revive, and Wake would return
		// ErrWakeNotNeeded. So this is a WARN, not a re-wake: an earlier draft
		// re-woke here, and the test critic's measurement showed the branch was
		// unobservable in every direction. An unobservable arm that reads as
		// live logic is the stranded-conditional shape this slice keeps paying
		// for, so it is gone rather than left in with a comment.
		slog.Default().Warn("idle reclaim: teardown failed; agent left as-is for the next sweep",
			slog.String("agent", name), slog.Any("err", stopErr))
	}
	pending, listErr := agentloop.ListPending(r.sprawlRoot, name)
	// An unreadable queue re-wakes on purpose: unknown is not empty, the same
	// D1a rule the predicate follows.
	if listErr == nil && len(pending) == 0 {
		return
	}
	if _, wErr := r.Wake(ctx, name, agentpkg.WakeReasonSendMessage, ""); wErr != nil {
		slog.Default().Warn("idle reclaim: agent had queued mail after teardown but could not be re-woken",
			slog.String("agent", name),
			slog.Any("err", wErr),
			slog.Any("list_err", listErr),
		)
	}
}

// logAssessment emits the full six-term decision record. Every term is logged,
// not just the deciding one, and each renders as idle/busy/UNAVAILABLE rather
// than as a bool — flattening "unavailable" to "false" here would mislead the
// next reader in exactly the direction D1a exists to prevent, and this line is
// the only artifact a reap leaves behind.
func logAssessment(level slog.Level, msg, name string, a idleAssessment, in idleInputs) {
	slog.Default().Log(context.Background(), level, msg,
		slog.String("agent", name),
		slog.Bool("reap", a.Reap),
		slog.String("blocker", a.Blocker),
		slog.String("in_turn", a.InTurn.String()),
		slog.String("pending_queue", a.Pending.String()),
		slog.String("in_flight_system", a.InFlight.String()),
		slog.String("question", a.Question.String()),
		slog.String("quiescent", a.Quiescent.String()),
		slog.String("not_root", a.NotRoot.String()),
		slog.Time("last_activity_at", probeLastActivity(in.Probe)),
		slog.Duration("threshold", in.Threshold),
	)
}

// idleReclaimStopBudget bounds the teardown, including StopAfterTurn's wait
// for a turn to end. It is the same order as runtimeStopTimeout; a reap that
// cannot complete promptly is abandoned rather than pinning the sweep.
const idleReclaimStopBudget = 30 * time.Second

// reclaimGate returns the per-agent mutex serialising a reap against a send.
// Per-agent, not global: StopAfterTurn can block for the full runaway budget,
// so one global gate would stall every send in the fleet behind one reap.
func (r *Real) reclaimGate(name string) *sync.Mutex {
	r.reclaimMu.Lock()
	defer r.reclaimMu.Unlock()
	if r.reclaimGates == nil {
		r.reclaimGates = make(map[string]*sync.Mutex)
	}
	g, ok := r.reclaimGates[name]
	if !ok {
		g = &sync.Mutex{}
		r.reclaimGates[name] = g
	}
	return g
}
