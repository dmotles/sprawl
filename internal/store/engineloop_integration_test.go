//go:build store_pg

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// The engine's whole loop against a real Postgres (QUM-1252, M3a).
//
// One goal, from the requester's create_goal to the requester's acked
// notification, driven by ONE real Dispatcher over the production-shaped
// handler table:
//
//	goal_opened   -> GoalSpawnHandler -> spawn_requested
//	(the assigned agent reads its goal and closes it)
//	goal_closed   -> NotifyHandler    -> owner_notify + an injected message
//	turn_finished -> NotifyAckHandler -> the owner_notify contract closes
//
// Every handler here already has its own tests, and each of them wires its own
// one-entry table. This test is about the JOIN between them — the parts no
// single-handler test can see:
//
//   - the goal's workflow instance carries through from goal_opened to
//     spawn_requested, which is what lets the assigned agent find the goal at
//     all;
//   - the agent named by spawn_requested — NOT the goal's owner — is the one
//     who can close the contract;
//   - the close notifies the OWNER rather than the closer, so the result goes
//     to whoever asked for the work;
//   - nothing is left in open_contracts at the end, which is the only
//     statement equivalent to "`sprawl goals` is empty".
//
// It is deterministic, and that is why it exists alongside the
// engine-goal-roundtrip e2e row rather than inside it: the e2e row drives a
// real session and a real model, so it can prove the goal STARTS but cannot
// make a model choose to call report_result. This test drives the same events
// with the agent's own store method in place of the model's tool call.
type engineLoopEnv struct {
	*spawnEnv
	notifies *PgNotifyReader
	injector *recordingInjector
	goals    *PgGoalReader
}

func newEngineLoopEnv(t *testing.T) *engineLoopEnv {
	t.Helper()
	e := newSpawnEnv(t)
	return &engineLoopEnv{
		spawnEnv: e,
		notifies: &PgNotifyReader{Pool: e.pool, Registry: e.registry},
		injector: &recordingInjector{},
		goals:    &PgGoalReader{Pool: e.pool, Registry: e.registry},
	}
}

// dispatcher wires the handler table both production paths wire (the session
// path's spawn_requested handler is left out: launching a session needs a
// supervisor, and what it does with the request is not what this test is
// about).
func (e *engineLoopEnv) dispatcher(t *testing.T, namer NameAllocator) *Dispatcher {
	t.Helper()
	notify, err := NewNotifyHandler(NotifyHandlerDeps{
		Emitter:  e.emitter,
		Injector: e.injector,
		Lookup:   e.reader(),
		Notifies: e.notifies,
		Host:     "host-a",
		Consumer: "dispatcher",
	})
	if err != nil {
		t.Fatalf("NewNotifyHandler: %v", err)
	}
	ack, err := NewNotifyAckHandler(NotifyAckHandlerDeps{
		Emitter: e.emitter, Notifies: e.notifies, Host: "host-a",
	})
	if err != nil {
		t.Fatalf("NewNotifyAckHandler: %v", err)
	}
	goalSpawn, err := NewGoalSpawnHandler(GoalSpawnHandlerDeps{Emitter: e.emitter, Names: namer})
	if err != nil {
		t.Fatalf("NewGoalSpawnHandler: %v", err)
	}

	rework, err := NewReworkHandler(ReworkHandlerDeps{Emitter: e.emitter, Names: namer, Lookup: e.reader()})
	if err != nil {
		t.Fatalf("NewReworkHandler: %v", err)
	}

	deps := e.deps("host-a", notify)
	deps.Handlers = map[string]Handler{
		"goal_opened":      goalSpawn,
		"goal_closed":      notify,
		"turn_finished":    ack,
		"rework_requested": rework,
	}
	d, err := NewDispatcher(deps)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

func TestEngineLoopPg_AGoalRunsFromOpenToAnAckedOwnerNotification(t *testing.T) {
	e := newEngineLoopEnv(t)
	ctx := context.Background()

	d := e.dispatcher(t, &fixedNamer{name: "ghost"})

	// 1. weave opens a goal. This is all create_goal does.
	goal, err := e.ledger.OpenGoal(ctx, GoalResearch, "find out how the cursor is derived", "weave")
	if err != nil {
		t.Fatalf("OpenGoal: %v", err)
	}

	// 2. The dispatcher turns it into a spawn request. The instance carries
	// through — a fresh one here would spawn a healthy-looking agent onto an
	// empty workflow that closes nothing.
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over goal_opened: %v", err)
	}
	requests, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, goal.WorkflowID, 0, 100)
	if err != nil {
		t.Fatalf("reading the goal's instance: %v", err)
	}
	if len(requests) != 2 || requests[1].SchemaName != "spawn_requested" {
		t.Fatalf("the goal's instance holds %d events (last %q), want goal_opened then spawn_requested",
			len(requests), lastSchema(requests))
	}

	// 3. The ASSIGNED agent reads its own goal — not the owner's name, the
	// agent spawn_requested named. This is the leg that was broken: matching
	// only on the payload's `owner` answers "you have no goal" to the one agent
	// whose entire prompt is that goal.
	mine, err := e.ledger.OpenGoalsForAgent(ctx, "ghost")
	if err != nil {
		t.Fatalf("OpenGoalsForAgent(ghost): %v", err)
	}
	if len(mine) != 1 || mine[0].GoalEventID != goal.GoalEventID {
		t.Fatalf("the assigned agent sees %d goals, want the one it was spawned for (%s)", len(mine), goal.GoalEventID)
	}
	if mine[0].Owner != "weave" {
		t.Errorf("the agent's goal reports owner %q, want weave — it is who report_result's result gets announced to", mine[0].Owner)
	}

	// 4. It closes the goal. report_result's one write.
	if _, err := e.ledger.CloseGoalForAgent(ctx, "ghost", goal.GoalEventID, GoalSucceeded, "the cursor is derived by folding Advance over the instance log"); err != nil {
		t.Fatalf("the assigned agent could not close its own goal: %v", err)
	}

	// 5. The close notifies the OWNER, not the closer.
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over goal_closed: %v", err)
	}
	delivered := e.injector.all()
	if len(delivered) != 1 {
		t.Fatalf("%d notifications were delivered, want 1", len(delivered))
	}
	if delivered[0].Recipient != "weave" {
		t.Errorf("the result was announced to %q, want weave — the closer must not be the recipient", delivered[0].Recipient)
	}
	// The body names the closing event and how to read it, which is what makes
	// the notification actionable rather than a bare ping. It does NOT carry
	// the summary — that is deliberate in notify.go, and asserting it here
	// would have been asserting my own guess.
	if !strings.Contains(delivered[0].Body, "goal_closed") {
		t.Errorf("the notification body does not name what landed: %q", delivered[0].Body)
	}
	if !strings.Contains(delivered[0].Body, "get_workflow_log") {
		t.Errorf("the notification body does not tell the owner how to read the result: %q", delivered[0].Body)
	}
	open, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies: %v", err)
	}
	if len(open) != 1 {
		// The outstanding half matters: an unacked notification is how a lost
		// injection looks, so it has to be visible until the owner answers.
		t.Fatalf("%d notifications outstanding before the owner's turn, want 1", len(open))
	}

	// 6. weave takes a turn, which is the ack. NOTIFY_ACKED is wire-derived —
	// there is no agent-side ack tool — and turn_finished is the event the
	// runtime emits at that boundary.
	if _, err := e.emitter.Emit(ctx, EmitRequest{
		TypeName: "turn_finished", TypeVersion: 1,
		WorkflowInstanceID: uuid.New(),
		Payload: map[string]any{
			"agent_name": "weave", "session_id": "s1",
			"input_tokens": 10, "output_tokens": 20,
		},
	}); err != nil {
		t.Fatalf("emitting the owner's turn boundary: %v", err)
	}
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over turn_finished: %v", err)
	}
	if stillOpen, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave"); err != nil {
		t.Fatalf("OpenNotifies after the turn: %v", err)
	} else if len(stillOpen) != 0 {
		t.Errorf("%d notifications outstanding after the owner took a turn, want 0", len(stillOpen))
	}

	// 7. Nothing is left outstanding anywhere. This is the statement `sprawl
	// goals` renders, and it is the one assertion that catches a leg that
	// half-worked: a goal or a notification that opened and never closed sits
	// here forever and reads as work in progress.
	if got := e.openContractCount(t, "goal_opened"); got != 0 {
		t.Errorf("%d goal contracts still open at the end, want 0", got)
	}
	if got := e.openContractCount(t, "owner_notify"); got != 0 {
		t.Errorf("%d owner_notify contracts still open at the end, want 0", got)
	}
}

// TestEngineLoopPg_TheOwnerIsNotifiedEvenThoughTheCloserWasSomebodyElse is the
// negative control for step 5 above, aimed at the recipient rather than at the
// count: with `owner` read off the CLOSING event instead of the contract it
// closes, the notification would go to nobody at all (goal_closed carries no
// owner), and step 5's `len(delivered) != 1` would fire for a reason that says
// nothing about addressing. Here the closer is a named agent and the assertion
// is that NOTHING was addressed to it.
func TestEngineLoopPg_TheOwnerIsNotifiedEvenThoughTheCloserWasSomebodyElse(t *testing.T) {
	e := newEngineLoopEnv(t)
	ctx := context.Background()

	d := e.dispatcher(t, &fixedNamer{name: "ghost"})
	goal, err := e.ledger.OpenGoal(ctx, GoalResearch, "find out", "weave")
	if err != nil {
		t.Fatalf("OpenGoal: %v", err)
	}
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over goal_opened: %v", err)
	}
	if _, err := e.ledger.CloseGoalForAgent(ctx, "ghost", goal.GoalEventID, GoalFailed, "could not reproduce"); err != nil {
		t.Fatalf("closing: %v", err)
	}
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over goal_closed: %v", err)
	}

	for _, got := range e.injector.all() {
		if got.Recipient == "ghost" {
			t.Errorf("the agent that closed the goal was notified of its own result: %q", got.Body)
		}
	}
	ghost, err := e.notifies.OpenNotifies(ctx, e.projectID, "ghost")
	if err != nil {
		t.Fatalf("OpenNotifies(ghost): %v", err)
	}
	if len(ghost) != 0 {
		t.Errorf("%d notifications are outstanding for the closer, want 0", len(ghost))
	}
	// Control, so the two assertions above are not satisfied by a run that
	// notified nobody: the owner DID get one.
	owner, err := e.notifies.OpenNotifies(ctx, e.projectID, "weave")
	if err != nil {
		t.Fatalf("OpenNotifies(weave): %v", err)
	}
	if len(owner) != 1 {
		t.Errorf("the owner has %d outstanding notifications, want 1", len(owner))
	}
}

func lastSchema(evs []DispatchedEvent) string {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].SchemaName
}
