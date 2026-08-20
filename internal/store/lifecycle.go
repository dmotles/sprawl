package store

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/usage"
)

// LifecycleEmitter turns an agent's runtime EventBus traffic into log events.
//
// It subscribes at the same point usage.Recorder does
// (internal/supervisor/runtime_launcher.go) and reuses usage.TurnAccumulator for
// token parsing, so there is exactly one implementation of "what did this turn
// cost" in the tree.
//
// TURN BOUNDARIES ARE THE LIVENESS SIGNAL (Appendix B item 4). The supervisor
// heartbeat was deleted in QUM-730/QUM-1071 precisely because a long quiet turn
// is legitimate, so "when did this agent last do something" is answered by
// turn_finished and by nothing else. A dropped turn_finished is therefore a
// liveness failure, not merely missing telemetry.
//
// CONCURRENCY: Handle, RunStarted and Close are all called from the SINGLE
// subscriber goroutine that ranges over the bus channel — Close after the range
// loop ends (see runLedgerSubscriber). There is deliberately no mutex, matching
// usage.Recorder's shape; adding one would imply a concurrency this type does
// not have and does not want.
type LifecycleEmitter struct {
	ledger *Ledger
	deps   LifecycleDeps
	log    *slog.Logger

	accum usage.TurnAccumulator

	turns       int
	failedTurns int
	// lastSessionCost is the most recent session-CUMULATIVE total_cost_usd.
	//
	// A run's cost is this value, NOT a sum of per-turn numbers. QUM-1247 fixed
	// a confirmed 4-10x cost inflation whose mechanism was exactly that sum, and
	// nothing about the wire format stops anyone doing it again.
	lastSessionCost float64
	outcome         string
	closed          bool

	// lastTurnPayload / lastRunPayload are kept for assertions. They are the
	// payload as EMITTED, so a test reads what the log received rather than
	// re-deriving it from the same inputs the emitter used.
	lastTurnPayload map[string]any
	lastRunPayload  map[string]any
}

// LifecycleDeps follows the repo's deps-struct convention.
type LifecycleDeps struct {
	// Ledger may be nil — that is the DEFAULT (the feature flag is off), and
	// every method must be a no-op in that case.
	Ledger      *Ledger
	AgentName   string
	AgentType   string
	AgentFamily string
	Parent      string
	Branch      string
	SessionID   string
	Resumed     bool
	// GitSHA and DirtyDigest are the run's provenance, resolved once at launch.
	GitSHA      string
	DirtyDigest string
	Now         func() time.Time
	Logger      *slog.Logger
}

// Outcome values recorded on run_finished.
const (
	outcomeSuccess     = "success"
	outcomeFailure     = "failure"
	outcomeFaulted     = "faulted"
	outcomeInterrupted = "interrupted"
)

func NewLifecycleEmitter(d LifecycleDeps) *LifecycleEmitter {
	log := d.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &LifecycleEmitter{ledger: d.Ledger, deps: d, log: log, outcome: outcomeSuccess}
}

// RunStarted records that the agent's backend session came up.
//
// Explicit rather than lazily fired on the first observed event: a run that
// starts and produces no traffic at all is exactly the case an operator most
// wants to see in the log, and a lazy emit would omit it.
func (e *LifecycleEmitter) RunStarted(ctx context.Context) {
	payload := map[string]any{
		"agent_name": e.deps.AgentName,
		"agent_type": e.deps.AgentType,
		"session_id": e.deps.SessionID,
	}
	addIfSet(payload, "family", e.deps.AgentFamily)
	addIfSet(payload, "git_sha", e.deps.GitSHA)
	if e.deps.Resumed {
		payload["resumed"] = true
	}
	e.emit(ctx, "run_started", payload)
}

// Handle processes one RuntimeEvent. It never returns an error and never
// panics: it runs on the agent's own subscriber goroutine, so a store failure
// escaping from here would take out turn processing for an agent that has
// nothing to do with the event log.
func (e *LifecycleEmitter) Handle(ev sprawlrt.RuntimeEvent) {
	ctx := context.Background()
	switch ev.Type {
	case sprawlrt.EventProtocolMessage:
		e.absorbUsage(ev)

	case sprawlrt.EventTurnCompleted:
		e.turns++
		if ev.Result != nil {
			e.lastSessionCost = ev.Result.TotalCostUsd
			if ev.Result.IsError {
				e.failedTurns++
				e.outcome = outcomeFailure
			}
		}
		e.emitTurnFinished(ctx, ev)
		e.accum.Reset()

	case sprawlrt.EventTurnFailed:
		// EventTurnFailed IS a terminal turn boundary — internal/runtime groups
		// it with EventTurnCompleted, and a mid-turn terminal error publishes
		// exactly one EventTurnFailed and zero EventTurnCompleted.
		//
		// It therefore emits turn_finished, for the reason stated at the top of
		// this file: turn boundaries are the liveness signal, so a boundary that
		// records nothing makes an agent look quieter than it is. This branch
		// originally incremented the counters and emitted nothing, which meant an
		// agent failing every turn (an ErrHangTimeout loop, say) produced
		// run_started, then N invisible failures, then run_finished — in the log,
		// indistinguishable from an agent that started and did nothing, which is
		// precisely the state this signal exists to distinguish.
		e.turns++
		e.failedTurns++
		e.outcome = outcomeFailure
		e.emitTurnFinished(ctx, ev)
		e.accum.Reset()

	case sprawlrt.EventInterrupted:
		// An interrupted turn writes no turn_finished: it did not finish. The
		// in-flight token accumulation is discarded for the same reason.
		e.outcome = outcomeInterrupted
		e.accum.Reset()

	case sprawlrt.EventBackendFaulted:
		e.outcome = outcomeFaulted
		e.accum.Reset()
	}
}

func (e *LifecycleEmitter) absorbUsage(ev sprawlrt.RuntimeEvent) {
	if ev.Message == nil || ev.Message.Type != "assistant" || len(ev.Message.Raw) == 0 {
		return
	}
	var am protocol.AssistantMessage
	if err := json.Unmarshal(ev.Message.Raw, &am); err != nil {
		return
	}
	u, model, err := am.ParseUsage()
	if err != nil || u == nil {
		return
	}
	e.accum.Absorb(*u, model)
}

func (e *LifecycleEmitter) emitTurnFinished(ctx context.Context, ev sprawlrt.RuntimeEvent) {
	u := e.accum.Usage()
	payload := map[string]any{
		"session_id":    e.sessionID(ev),
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
	}
	addIfSet(payload, "agent_name", e.deps.AgentName)
	// Per-turn cost is deliberately ABSENT. total_cost_usd on a result frame is
	// the session's running total, so a per-turn figure would have to be a
	// delta — and computing deltas here would duplicate the one piece of
	// arithmetic QUM-1247 got wrong. The run's total is recorded once, on
	// run_finished, from the last cumulative value.
	// A failed turn carries no Result at all (the turn died before one arrived),
	// so the outcome is keyed on the EVENT TYPE first and only then on the
	// result's is_error. Keying solely on ev.Result would label every
	// EventTurnFailed a success, which is the plausible-value failure: a
	// turn_finished row saying "success" for a turn that faulted is worse than
	// no row, because a reader cannot tell it from a real one.
	switch {
	case ev.Type == sprawlrt.EventTurnFailed:
		payload["outcome"] = outcomeFailure
	case ev.Result != nil && ev.Result.IsError:
		payload["outcome"] = outcomeFailure
	default:
		payload["outcome"] = outcomeSuccess
	}
	e.lastTurnPayload = payload
	e.emit(ctx, "turn_finished", payload)
}

// Close records run_finished. Idempotent: the subscriber's stop function is
// idempotent, so a second call is reachable and must not double-emit.
func (e *LifecycleEmitter) Close(ctx context.Context) {
	if e.closed {
		return
	}
	e.closed = true

	payload := map[string]any{
		"session_id":   e.deps.SessionID,
		"outcome":      e.outcome,
		"turns":        e.turns,
		"failed_turns": e.failedTurns,
		"cost_usd":     e.lastSessionCost,
	}
	addIfSet(payload, "agent_name", e.deps.AgentName)
	addIfSet(payload, "git_sha", e.deps.GitSHA)
	addIfSet(payload, "dirty_digest", e.deps.DirtyDigest)
	// card_version_id is OMITTED, not sent empty: it is nullable until M2
	// registers cards, and an empty string would make every run appear to
	// reference a card that does not exist.
	e.lastRunPayload = payload
	e.emit(ctx, "run_finished", payload)
}

// LastTurnPayload / LastRunPayload expose the payloads as emitted, for
// assertions. Reading what was SENT rather than re-deriving it means a test
// cannot agree with the emitter by accident.
func (e *LifecycleEmitter) LastTurnPayload() map[string]any { return e.lastTurnPayload }
func (e *LifecycleEmitter) LastRunPayload() map[string]any  { return e.lastRunPayload }

func (e *LifecycleEmitter) sessionID(ev sprawlrt.RuntimeEvent) string {
	if e.deps.SessionID != "" {
		return e.deps.SessionID
	}
	if ev.Result != nil {
		return ev.Result.SessionID
	}
	return "unknown"
}

// emit appends and swallows the error into a log line.
//
// Swallowing is correct HERE and only here: the Ledger has already decided
// whether an outage was survivable (spill) or fatal (goal open/close), and every
// type this emitter sends is spillable telemetry. What is left is a genuine
// defect — an invalid payload, an unknown schema — which must be visible to an
// operator but must not fail an agent's turn.
func (e *LifecycleEmitter) emit(ctx context.Context, typeName string, payload map[string]any) {
	if !e.ledger.Enabled() {
		return
	}
	if _, err := e.ledger.Emit(ctx, EmitRequest{
		TypeName:    typeName,
		TypeVersion: 1,
		Payload:     payload,
	}); err != nil {
		e.log.Warn("could not record lifecycle event",
			"type", typeName, "agent", e.deps.AgentName, "error", err)
	}
}

// addIfSet omits an empty value rather than writing "".
//
// An empty string in a payload is a plausible-looking value that means "we did
// not know", and a consumer cannot tell it from a real measurement. Absent is
// unambiguous.
func addIfSet(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}
