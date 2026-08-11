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
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	agentpkg "github.com/dmotles/sprawl/internal/agent"
	"github.com/dmotles/sprawl/internal/agentloop"
	runtimepkg "github.com/dmotles/sprawl/internal/runtime"
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
	// WorkOutstandingObserved is the QUM-1197 item-2 term: the agent's
	// outstanding CLI-managed background work. Returns the tasks (with the time
	// each was first seen, for the refusal record's age) and whether the set
	// could be observed at all.
	WorkOutstanding() (tasks []runtimepkg.OutstandingTask, basis runtimepkg.WorkBasis)
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
	Work      idleObs
	Pending   idleObs
	InFlight  idleObs
	Question  idleObs
	Quiescent idleObs
	NotRoot   idleObs

	// WorkBasis records WHY the work verdict is what it is (a real frame, or the
	// absence of one on a session that demonstrably spoke). The QUM-1197 ruling
	// of 2026-08-11 relaxed the strict rule and made this provenance the price:
	// a fleet whose reaps all read by_absence is what a CLI vocabulary change
	// would look like, and it must be one grep away rather than silent.
	WorkBasis runtimepkg.WorkBasis
	// WorkTasks is the outstanding set the DECISION saw, kept so the record
	// describes that set rather than a fresher one re-probed at log time.
	WorkTasks []runtimepkg.OutstandingTask

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

	// Work outstanding (QUM-1197 item 2). The term this whole issue exists for:
	// every other term can read `idle` HONESTLY while the agent has work it means
	// to return to. An agent that backgrounds a tool call or spawns a sidechain
	// and ends its turn was reaped, the work died with it, and the wake
	// notification it was waiting for never arrived.
	//
	// The unavailable arm is not a formality — it is the load-bearing half. A set
	// never observed is not an empty set: if the CLI renames the subtype or stops
	// emitting, a set-valued term would silently read "no work" and the whole
	// protection would evaporate with no error anywhere.
	//
	// LIMIT, stated so it is not discovered later: this covers CLI-MANAGED
	// background work only. A process a Bash call `nohup`ed away, or a long
	// MCP-side call, carries no task_id and is invisible here. And this protects
	// the REAP DECISION only — an operator can still see an agent rendered idle
	// with live sidechains until QUM-1213 lands.
	tasks, basis := probeWorkOutstanding(in.Probe)
	a.WorkBasis = basis
	switch {
	case basis == runtimepkg.WorkUnobservable:
		a.Work = obsUnavailable
	case len(tasks) > 0:
		a.Work = obsBusy
		a.WorkTasks = tasks
	default:
		a.Work = obsIdle
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

	// In-flight system entries: SPRAWL MAIL already handed to the runtime but not
	// yet consumed. No UnifiedRuntime to ask means unavailable, not clean.
	//
	// Read that first sentence carefully, because this term's NAME is what misled
	// a whole verification pass on QUM-1197: "in_flight_system" sounds like it
	// covers work the agent has in flight. It does not — it counts sprawl's own
	// undelivered messages (InFlightSystemEntryIDs). The agent's own background
	// work is `work_outstanding` above, and nothing measured it until item 2.
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
		{"work_outstanding", a.Work},
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

func probeWorkOutstanding(p idleRuntimeProbe) ([]runtimepkg.OutstandingTask, runtimepkg.WorkBasis) {
	if p == nil {
		return nil, runtimepkg.WorkUnobservable
	}
	return p.WorkOutstanding()
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
//
// Two bounds this comment previously left implicit, because claiming the
// boundaries are exhaustively reasoned while omitting them is its own kind of
// false record (review F7/F8):
//
//   - Shutdown can block for up to idleReclaimStopBudget + runtimeStopTimeout
//     on a reap that had already STARTED. The sweepCtx check below declines
//     reaps that have not started; a started one deliberately runs to
//     completion rather than leaving a half-torn-down agent.
//   - Phase B does not hold the gate while WAITING, but the guard re-takes it
//     and holds it ACROSS the stop — and that stop drains in-flight MCP
//     handlers. An agent's own in-flight send_message TO ITSELF (permitted for
//     now=false) blocks on this same per-agent gate, so that handler stalls
//     until the drain timeout rather than deadlocking. Bounded, but it is the
//     one place the gate is held across a wait on work that can itself be
//     waiting for the gate.
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
		// QUM-1197: the level comes from refusalLevel, not from a constant here.
		// The old rule was "Debug, not Info, because this fires once per agent
		// per sweep forever" — true about the cadence, and wrong about the
		// consequence: no slog level is configured outside cmd/hubd/main.go and
		// slog's default is Info, so Debug did not make the record quiet, it
		// made it ABSENT. A reap left a record and a refusal left nothing, with
		// no knob to change it, and a five-run investigation on QUM-1197 could
		// not answer "was this agent even assessed?". The CONTENT was always
		// identical to the reap line; now the first refusal per reason reaches a
		// shipped binary too, and only the repeats are demoted.
		if assessment.Blocker == "disabled" {
			// Six "unavailable"s would read as "nothing could be measured"
			// when in fact nothing was ASKED. Review N3. Note real.go only
			// constructs the reaper when the knob is > 0, so this arm is reached
			// via a direct call or a mid-run knob change, not by a default
			// install's sweep.
			slog.Default().Log(context.Background(), r.refusalLevel(name, "disabled"),
				"idle reclaim: disabled, no agent is reclaimed",
				slog.String("agent", name))
			return
		}
		logAssessment(r.refusalLevel(name, "assess:"+assessment.Blocker),
			"idle reclaim: agent not reclaimed", name, assessment, inputs)
		return
	}
	// The agent is reapable, so whatever it used to refuse for is over: forget
	// it, or a later refusal for the same reason is demoted to Debug — i.e.
	// invisible — for the rest of the process's life.
	r.forgetRefusal(name)
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
			// Namespaced "abandon:" so a later phase-A refusal on the same term
			// is not demoted because an abandonment recorded that term first.
			// Only that direction is live: forgetRefusal already ran on this
			// pass (the reap branch above), so no phase-A key is outstanding
			// here — the reverse direction cannot occur.
			//
			// BOUND, stated because it is not deduped: since forgetRefusal runs
			// on every reapable pass, EVERY abandoned teardown logs at Info. An
			// agent that is idle at phase A and starts a turn during the wait
			// therefore costs one Info line per sweep for as long as that holds.
			// Each abandonment is a real event, so that is chatter rather than a
			// flood — but it is not covered by the one-line-per-agent bound
			// above, and a reader must not assume it is.
			logAssessment(r.refusalLevel(name, "abandon:"+a.Blocker),
				"idle reclaim: teardown abandoned, agent became active", name, a, in)
			gate.Unlock()
			return false, nil
		}
		logAssessment(slog.LevelInfo, "idle reclaim: reaping agent", name, a, in)
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
		// RETURN, do not fall through. Review F3: without this the backstop
		// runs against a handle that is still attached and healthy, Wake
		// returns ErrWakeNotNeeded, and we log "had queued mail after teardown
		// but could not be re-woken" — a warning asserting the mail is
		// stranded when the agent is alive and its own drain will deliver it.
		// A false alarm in the log is the same defect class as a false claim
		// in an error string.
		return
	}
	pending, listErr := agentloop.ListPending(r.sprawlRoot, name)
	// An unreadable queue re-wakes on purpose: unknown is not empty, the same
	// D1a rule the predicate follows.
	if listErr == nil && len(pending) == 0 {
		return
	}
	// Fresh budget, NOT the stop ctx: StopAfterTurnIf can return precisely
	// BECAUSE that ctx fired, and handing an already-cancelled ctx to the wake
	// would make this backstop's stated guarantee depend on Wake happening to
	// ignore its ctx two layers down. Review F6.
	wakeCtx, wakeCancel := context.WithTimeout(context.Background(), idleReclaimStopBudget)
	defer wakeCancel()
	if _, wErr := r.Wake(wakeCtx, name, agentpkg.WakeReasonSendMessage, ""); wErr != nil {
		slog.Default().Warn("idle reclaim: agent had queued mail after teardown but could not be re-woken",
			slog.String("agent", name),
			slog.Any("err", wErr),
			slog.Any("list_err", listErr),
		)
	}
}

// logAssessment emits the full six-term decision record. Every term is logged,
// not just the deciding one, and each renders as idle/busy/unavailable rather
// than as a bool — flattening "unavailable" to "false" here would mislead the
// next reader in exactly the direction D1a exists to prevent, and this line is
// the only artifact a reap or a refusal leaves behind.
//
// The level is a PARAMETER, not a per-message constant: the reap is always
// Info, while a refusal's level is the caller's QUM-1197 dedup decision
// (refusalLevel).
func logAssessment(level slog.Level, msg, name string, a idleAssessment, in idleInputs) {
	slog.Default().Log(context.Background(), level, msg,
		slog.String("agent", name),
		slog.Bool("reap", a.Reap),
		slog.String("blocker", a.Blocker),
		slog.String("in_turn", a.InTurn.String()),
		slog.String("work_outstanding", renderWorkTerm(a.Work, a.WorkBasis)),
		slog.Int("work_outstanding_n", len(a.WorkTasks)),
		slog.String("work_outstanding_tasks", renderOutstanding(a.WorkTasks, in.Now)),
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

// reclaimEntry is the per-agent reaper state. Two locks, deliberately not one,
// and the load-bearing reason is not latency: at the phase-B abandon site the
// gate is ALREADY HELD BY THIS GOROUTINE when the refusal level is computed, so
// a design that reused gate for the dedup state would self-deadlock on a
// non-reentrant sync.Mutex. (It is also true that gate is held across a whole
// teardown, up to idleReclaimStopBudget, and a log-level decision should not
// queue behind that — but that is the weaker reason.) Do not collapse them.
//
// Full lock order, including the map lock a reader would worry about:
// gate → reclaimMu → refusalMu. reclaimMu is taken and RELEASED inside
// reclaimEntryFor, and no path takes a gate while holding reclaimMu, so the
// inverse edge does not exist.
type reclaimEntry struct {
	// gate serialises a reap against a send. Per-agent, not global:
	// StopAfterTurn can block for the full runaway budget, so one global gate
	// would stall every send in the fleet behind one reap.
	gate sync.Mutex

	// refusalMu guards lastRefusal ONLY. Lock order: gate → refusalMu.
	refusalMu sync.Mutex
	// lastRefusal is the key of the refusal last logged at Info for this agent.
	// Empty means "no refusal outstanding", so the next one is news again.
	lastRefusal string
}

// reclaimEntryFor returns the agent's entry, creating it on first use.
func (r *Real) reclaimEntryFor(name string) *reclaimEntry {
	r.reclaimMu.Lock()
	defer r.reclaimMu.Unlock()
	if r.reclaimGates == nil {
		r.reclaimGates = make(map[string]*reclaimEntry)
	}
	e, ok := r.reclaimGates[name]
	if !ok {
		e = &reclaimEntry{}
		r.reclaimGates[name] = e
	}
	return e
}

// reclaimGate returns the per-agent mutex serialising a reap against a send.
func (r *Real) reclaimGate(name string) *sync.Mutex { return &r.reclaimEntryFor(name).gate }

// refusalLevel is the QUM-1197 level policy for ONE refusal record, and the
// reason the record is observable at all: slog's default level is Info and
// nothing in this repo configures a level outside cmd/hubd/main.go, so a Debug
// record is not a quieter record — it is no record.
//
// The first refusal under a given key is Info; identical repeats are demoted to
// Debug. So a steady-state idle fleet costs ONE Info line per agent rather than
// one per agent per sweep, which is what made Debug look like the only option.
//
// The key is the DECIDING term (the blocker), namespaced by call site by the
// caller. Not the full six-term tuple: the non-deciding terms oscillate —
// quiescent flips busy→idle the moment activity ages past the threshold while
// in_turn keeps blocking — so a tuple key would change with no change of reason
// and restore the flood. The accepted bound is that an agent whose BLOCKER
// genuinely alternates costs one Info per sweep; that is an agent changing
// state, and each line is real news.
//
// It returns the level rather than logging, so refusalMu is never held across a
// log call. Note the narrower claim: the phase-B site logs with the reclaim gate
// held (as the reap line already did), so this is not a promise that no
// supervisor mutex is held during handler I/O.
//
// refusalMu is a GUARD, not a measured race: idleReaper.runOnce sweeps serially
// from one goroutine today, so nothing in production drives two refusals for one
// agent concurrently. The lock is what keeps that from becoming a race if a
// future caller parallelises the sweep, and TestRefusalLevel_ConcurrentForOneAgent
// is what makes its absence detectable under -race rather than merely intended.
func (r *Real) refusalLevel(name, key string) slog.Level {
	e := r.reclaimEntryFor(name)
	e.refusalMu.Lock()
	defer e.refusalMu.Unlock()
	if e.lastRefusal == key {
		return slog.LevelDebug
	}
	e.lastRefusal = key
	return slog.LevelInfo
}

// forgetRefusal clears the dedup memory for an agent that has stopped refusing,
// so its NEXT refusal is reported at Info even if the reason is the same one as
// before. Without this the record silently goes dark again for any agent that
// cycles between reapable and not.
func (r *Real) forgetRefusal(name string) {
	e := r.reclaimEntryFor(name)
	e.refusalMu.Lock()
	defer e.refusalMu.Unlock()
	e.lastRefusal = ""
}

// renderOutstanding formats the outstanding set for the refusal record, with a
// per-task AGE.
//
// The age is the operator-facing half of a settled decision: stale tasks are
// deliberately NOT auto-expired, because any cap short enough to clear a
// two-hour wedge (a real one was measured: five tasks pinned for 2h1m by a
// pgrep that matched its own command line) would also clear a legitimate
// `make validate` on a loaded box. So a wedged task must instead be a NAMED,
// visible condition — and without the age, a task stuck for hours and one
// started a second ago read identically.
//
// Bounded at renderMaxOutstanding entries so one pathological agent cannot make
// this line unreadable; the count is reported separately, so truncation is
// visible rather than silent.
func renderOutstanding(tasks []runtimepkg.OutstandingTask, now time.Time) string {
	if len(tasks) == 0 {
		return ""
	}
	var b strings.Builder
	for i, task := range tasks {
		if i == renderMaxOutstanding {
			fmt.Fprintf(&b, ",+%d more", len(tasks)-i)
			break
		}
		if i > 0 {
			b.WriteByte(',')
		}
		// No zero-FirstSeen arm: noteBackgroundTasks stamps FirstSeen on every
		// task it admits, so a zero value cannot reach here. An unreachable
		// branch that reads as live logic is its own defect.
		fmt.Fprintf(&b, "%s:%s:age=%s", task.TaskType, task.TaskID,
			now.Sub(task.FirstSeen).Round(time.Second))
	}
	return b.String()
}

// renderMaxOutstanding bounds renderOutstanding's detail.
const renderMaxOutstanding = 8

// renderWorkTerm renders the work term with its PROVENANCE, e.g.
// "idle(observed_empty)" versus "idle(by_absence)". The distinction is the whole
// price of the QUM-1197 (c) ruling: absence may now conclude "nothing
// outstanding", and the one failure shape that trades away — the CLI renaming or
// dropping the subtype — shows up as a fleet-wide by_absence rather than as
// nothing at all. On the reap line as well as the refusal line, by design: the
// reap is the decision the provenance is about.
func renderWorkTerm(obs idleObs, basis runtimepkg.WorkBasis) string {
	if obs != obsIdle {
		return obs.String()
	}
	if basis == runtimepkg.WorkByAbsence {
		return "idle(by_absence)"
	}
	return "idle(observed_empty)"
}
