package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/google/uuid"
)

// The lifecycle emitter subscribes to an agent's runtime EventBus at the same
// point usage.Recorder does, and turns turn boundaries into log events.
//
// Turn boundaries are the LIVENESS SIGNAL (Appendix B item 4): the supervisor
// heartbeat was deleted precisely because a long quiet turn is legitimate, so
// "when did this agent last do something" is answered by these events and by
// nothing else. That makes a dropped turn_finished a liveness failure, not just
// missing telemetry.

func newCapturingLedger(t *testing.T) (*Ledger, *recordingPool, *capturingSpiller) {
	t.Helper()
	pool := newRecordingPool()
	reg := mustSeedRegistry(t)
	spill := &capturingSpiller{}
	return &Ledger{
		enabled:   true,
		registry:  reg,
		projectID: uuid.New(),
		appender:  NewAppender(AppenderDeps{Pool: pool, Registry: reg, Spill: spill}),
	}, pool, spill
}

// newLifecycleFixture wires an emitter onto a recording pool, so assertions can
// count what actually reached the append path rather than what the emitter
// believes it sent.
func newLifecycleFixture(t *testing.T) (*LifecycleEmitter, *recordingPool) {
	t.Helper()
	l, pool, _ := newCapturingLedger(t)
	e := NewLifecycleEmitter(LifecycleDeps{
		Ledger:      l,
		AgentName:   "finn",
		AgentType:   "engineer",
		SessionID:   "sess-1",
		GitSHA:      "0123456789abcdef0123456789abcdef01234567",
		DirtyDigest: "cafebabe",
	})
	return e, pool
}

func assistantUsageEvent(input, output int) sprawlrt.RuntimeEvent {
	raw := json.RawMessage(`{"type":"assistant","session_id":"sess-1","message":{"model":"claude-sonnet","usage":{"input_tokens":` +
		itoa(int64(input)) + `,"output_tokens":` + itoa(int64(output)) + `}}}`)
	return sprawlrt.RuntimeEvent{
		Type:    sprawlrt.EventProtocolMessage,
		Message: &protocol.Message{Type: "assistant", SessionID: "sess-1", Raw: raw},
	}
}

func turnCompleted(cost float64, isError bool) sprawlrt.RuntimeEvent {
	return sprawlrt.RuntimeEvent{
		Type: sprawlrt.EventTurnCompleted,
		Result: &protocol.ResultMessage{
			Type: "result", SessionID: "sess-1",
			TotalCostUsd: cost, IsError: isError,
		},
	}
}

// countAppends counts how many events reached the database.
func countAppends(pool *recordingPool) int {
	var n int
	for _, c := range pool.log() {
		if c == "insert_event" {
			n++
		}
	}
	return n
}

// TestLifecycle_RunStartedThenTurnFinishedThenRunFinished pins the three-event
// arc and its count.
//
// The count matters as much as the presence: an emitter that fired
// turn_finished on every protocol message rather than on the turn boundary would
// satisfy a presence check and flood the log.
func TestLifecycle_RunStartedThenTurnFinishedThenRunFinished(t *testing.T) {
	e, pool := newLifecycleFixture(t)
	ctx := context.Background()

	e.RunStarted(ctx)
	if got := countAppends(pool); got != 1 {
		t.Fatalf("after RunStarted, %d event(s) appended, want 1", got)
	}

	// Two protocol messages inside one turn must NOT each produce an event.
	e.Handle(assistantUsageEvent(10, 5))
	e.Handle(assistantUsageEvent(3, 2))
	if got := countAppends(pool); got != 1 {
		t.Errorf("protocol messages inside a turn produced %d event(s); only the turn BOUNDARY is an event", got-1)
	}

	e.Handle(turnCompleted(0.10, false))
	if got := countAppends(pool); got != 2 {
		t.Errorf("after one completed turn, %d event(s) appended, want 2 (run_started + turn_finished)", got)
	}

	e.Close(ctx)
	if got := countAppends(pool); got != 3 {
		t.Errorf("after Close, %d event(s) appended, want 3 (+ run_finished)", got)
	}

	// Close twice must not double-emit: the subscriber's stop function is
	// idempotent, so this is reachable.
	e.Close(ctx)
	if got := countAppends(pool); got != 3 {
		t.Errorf("a second Close emitted another run_finished (%d total)", got)
	}
}

// TestLifecycle_NilLedgerEmitsNothingAndDoesNotPanic pins the default path. The
// flag is off by default, so this is what runs on every host that has not
// enabled the store.
func TestLifecycle_NilLedgerEmitsNothingAndDoesNotPanic(t *testing.T) {
	e := NewLifecycleEmitter(LifecycleDeps{AgentName: "finn", AgentType: "engineer"})
	ctx := context.Background()
	e.RunStarted(ctx)
	e.Handle(assistantUsageEvent(1, 1))
	e.Handle(turnCompleted(0.01, false))
	e.Handle(sprawlrt.RuntimeEvent{Type: sprawlrt.EventBackendFaulted, FaultClass: "HangTimeout"})
	e.Close(ctx)
	e.Close(ctx)
}

// TestLifecycle_TurnFinishedCarriesTheTurnsTokens pins that tokens are
// per-turn and RESET between turns.
//
// Without the reset, each turn would report the run's cumulative tokens, so
// per-turn cost analysis would be monotonically increasing nonsense while
// looking entirely plausible.
func TestLifecycle_TurnFinishedCarriesTheTurnsTokens(t *testing.T) {
	e, _ := newLifecycleFixture(t)
	ctx := context.Background()
	e.RunStarted(ctx)

	e.Handle(assistantUsageEvent(100, 20))
	e.Handle(turnCompleted(0.10, false))
	first := e.LastTurnPayload()

	e.Handle(assistantUsageEvent(7, 3))
	e.Handle(turnCompleted(0.20, false))
	second := e.LastTurnPayload()

	if first["input_tokens"] != 100 || first["output_tokens"] != 20 {
		t.Errorf("first turn recorded in=%v out=%v, want 100/20", first["input_tokens"], first["output_tokens"])
	}
	if second["input_tokens"] != 7 || second["output_tokens"] != 3 {
		t.Errorf("second turn recorded in=%v out=%v, want 7/3 — the accumulator is not reset between turns, so every turn reports the run's cumulative total",
			second["input_tokens"], second["output_tokens"])
	}
}

// TestLifecycle_RunFinishedCostIsTheSessionCumulativeNotASumOfTurns pins the
// post-M0 cost semantics.
//
// QUM-1247 fixed a confirmed 4-10x cost inflation whose mechanism was summing
// per-turn numbers that were already cumulative. total_cost_usd on a result
// frame is the SESSION's running total, so a run's cost is the LAST value seen —
// never the sum. This assertion is the thing standing between M1a and
// re-introducing that exact bug in a new place.
func TestLifecycle_RunFinishedCostIsTheSessionCumulativeNotASumOfTurns(t *testing.T) {
	e, _ := newLifecycleFixture(t)
	ctx := context.Background()
	e.RunStarted(ctx)

	// Three turns whose CUMULATIVE session cost rises to 0.30. Summing them
	// would give 0.60, which is the inflation bug.
	for _, cost := range []float64{0.10, 0.20, 0.30} {
		e.Handle(assistantUsageEvent(1, 1))
		e.Handle(turnCompleted(cost, false))
	}
	e.Close(ctx)

	got := e.LastRunPayload()
	if cost, ok := got["cost_usd"].(float64); !ok || cost != 0.30 {
		t.Errorf("run cost = %v, want 0.30 (the last session-cumulative value). 0.60 would mean per-turn costs were summed, which is the QUM-1247 inflation bug",
			got["cost_usd"])
	}
	if turns, ok := got["turns"].(int); !ok || turns != 3 {
		t.Errorf("run turns = %v, want 3", got["turns"])
	}
}

// TestLifecycle_RunFinishedCarriesGitProvenance pins that the SHA and dirty
// digest reach the log. Without them a replay cannot reconstruct what the agent
// was looking at.
func TestLifecycle_RunFinishedCarriesGitProvenance(t *testing.T) {
	e, _ := newLifecycleFixture(t)
	ctx := context.Background()
	e.RunStarted(ctx)
	e.Handle(turnCompleted(0.01, false))
	e.Close(ctx)

	got := e.LastRunPayload()
	if got["git_sha"] != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("git_sha = %v", got["git_sha"])
	}
	if got["dirty_digest"] != "cafebabe" {
		t.Errorf("dirty_digest = %v", got["dirty_digest"])
	}
	if _, present := got["card_version_id"]; present {
		t.Error("card_version_id is present; it is nullable until M2 and emitters must OMIT it rather than send an empty string, or every run appears to reference a card that does not exist")
	}
}

// TestLifecycle_OutcomeReflectsHowTheRunEnded pins that the outcome is measured
// rather than assumed to be success.
//
// A run_finished that always says "success" is the plausible-value failure: it
// is indistinguishable from a real measurement, and every eval built on it would
// silently treat faults as wins.
func TestLifecycle_OutcomeReflectsHowTheRunEnded(t *testing.T) {
	cases := []struct {
		name  string
		drive func(e *LifecycleEmitter)
		want  string
	}{
		{"clean", func(e *LifecycleEmitter) {
			e.Handle(turnCompleted(0.01, false))
		}, "success"},
		{"turn reported is_error", func(e *LifecycleEmitter) {
			e.Handle(turnCompleted(0.01, true))
		}, "failure"},
		{"backend faulted", func(e *LifecycleEmitter) {
			e.Handle(sprawlrt.RuntimeEvent{Type: sprawlrt.EventBackendFaulted, FaultClass: "HangTimeout"})
		}, "faulted"},
		{"interrupted", func(e *LifecycleEmitter) {
			e.Handle(sprawlrt.RuntimeEvent{Type: sprawlrt.EventInterrupted})
		}, "interrupted"},
		{"no turns at all", func(_ *LifecycleEmitter) {}, "success"},
	}
	for _, tc := range cases {
		e, _ := newLifecycleFixture(t)
		ctx := context.Background()
		e.RunStarted(ctx)
		tc.drive(e)
		e.Close(ctx)
		if got := e.LastRunPayload()["outcome"]; got != tc.want {
			t.Errorf("%s: outcome = %v, want %q", tc.name, got, tc.want)
		}
	}
}

// TestLifecycle_FailedTurnsAreCounted pins the failed-turn tally.
//
// NOTE ON THE FIELD NAME: this is `failed_turns`, not `tool_failures`. Per-tool
// failure counting needs tool_result inspection that nothing here does, and
// shipping a field called tool_failures populated from turn-level errors would
// render a number that was never measured — the exact defect the repo's
// diagnostic rule forbids. M4's eval harness needs the tool-level data anyway
// and can add it as a new optional field.
func TestLifecycle_FailedTurnsAreCounted(t *testing.T) {
	e, _ := newLifecycleFixture(t)
	ctx := context.Background()
	e.RunStarted(ctx)
	e.Handle(turnCompleted(0.01, false))
	e.Handle(turnCompleted(0.02, true))
	e.Handle(turnCompleted(0.03, true))
	e.Close(ctx)

	got := e.LastRunPayload()
	if n, ok := got["failed_turns"].(int); !ok || n != 2 {
		t.Errorf("failed_turns = %v, want 2", got["failed_turns"])
	}
	if n, ok := got["turns"].(int); !ok || n != 3 {
		t.Errorf("turns = %v, want 3 — a failed turn is still a turn", got["turns"])
	}
	// Control: a clean run reports zero, so the count above is not just
	// "whatever number happened to be there".
	clean, _ := newLifecycleFixture(t)
	clean.RunStarted(ctx)
	clean.Handle(turnCompleted(0.01, false))
	clean.Close(ctx)
	if n, ok := clean.LastRunPayload()["failed_turns"].(int); !ok || n != 0 {
		t.Errorf("a clean run reported failed_turns = %v, want 0", clean.LastRunPayload()["failed_turns"])
	}
}

// TestLifecycle_AppendFailureDoesNotPropagate pins the no-brick guarantee at the
// emitter level.
//
// Handle runs on the agent's EventBus subscriber goroutine. If a store failure
// escaped from here, an unreachable database would take out turn processing for
// an agent that has nothing to do with the event log.
func TestLifecycle_AppendFailureDoesNotPropagate(t *testing.T) {
	l, pool, _ := newCapturingLedger(t)
	pool.beginErr = errConnRefused
	e := NewLifecycleEmitter(LifecycleDeps{Ledger: l, AgentName: "finn", AgentType: "engineer", SessionID: "s"})
	ctx := context.Background()

	// None of these may panic or block; the emitter has no way to return an
	// error and must not acquire one.
	e.RunStarted(ctx)
	e.Handle(assistantUsageEvent(1, 1))
	e.Handle(turnCompleted(0.01, false))
	e.Close(ctx)
}
