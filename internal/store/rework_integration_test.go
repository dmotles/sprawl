//go:build store_pg

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The rework path (QUM-1252, M3a, AC3).
//
// A close is final and the log is monotone, so an owner who finds a defect in a
// result cannot reopen the goal — it emits a rework_requested that OPENS a new
// contract, linked to the rejected goal by the follows_event_id column. The
// dispatcher then drives that request into the next spawn exactly as it drives
// goal_opened, and the reworking agent closes the rework's own contract with its
// own goal_closed.
//
// Integration rather than unit because two of the three things asserted here are
// properties only Postgres can report: the follows_event_id link survives the
// round trip through a real column, and the new contract lands in the
// open_contracts projection the append transaction maintains.

// seqNamer hands out distinct names in order, so a test can tell the fresh
// agent from the one whose result was rejected. fixedNamer cannot: it returns
// the same name every time, which is exactly the value the discard_and_redo
// assertion has to rule out.
type seqNamer struct {
	names []string
	n     int
}

func (s *seqNamer) AllocateName(_ context.Context, _ string) (string, error) {
	if s.n >= len(s.names) {
		return "", fmt.Errorf("seqNamer: asked for name %d, only %d were provided", s.n+1, len(s.names))
	}
	name := s.names[s.n]
	s.n++
	return name, nil
}

func (e *engineLoopEnv) reworkPayload(t *testing.T, ev DispatchedEvent) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(ev.Payload, &out); err != nil {
		t.Fatalf("unmarshalling %s payload: %v", ev.SchemaName, err)
	}
	return out
}

func TestReworkPg_ARejectedResultOpensANewContractLinkedToTheOldOne(t *testing.T) {
	e := newEngineLoopEnv(t)
	ctx := context.Background()

	d := e.dispatcher(t, &seqNamer{names: []string{"ghost", "spectre"}})
	goal, err := e.ledger.OpenGoal(ctx, GoalResearch, "find out how the cursor is derived", "weave")
	if err != nil {
		t.Fatalf("OpenGoal: %v", err)
	}
	if _, err := d.Step(ctx); err != nil { // goal_opened -> spawn_requested
		t.Fatalf("Step over goal_opened: %v", err)
	}
	if _, err := e.ledger.CloseGoalForAgent(ctx, "ghost", goal.GoalEventID, GoalSucceeded, "it is folded"); err != nil {
		t.Fatalf("closing the goal: %v", err)
	}

	// The owner rejects the result. This is the whole of RequestRework: it
	// appends, and the dispatcher drives the rest.
	rework, err := e.ledger.RequestRework(ctx, "weave", goal.GoalEventID, "the answer does not mention the claims consumer", DiscardAndRedo)
	if err != nil {
		t.Fatalf("RequestRework: %v", err)
	}

	// 1. The link is the COLUMN, not a payload field — a chain the database can
	// enforce. Read back through the reader the dispatcher itself uses.
	got, err := e.reader().ByID(ctx, rework.ReworkEventID)
	if err != nil {
		t.Fatalf("reading the rework back: %v", err)
	}
	if got.FollowsEventID == nil || *got.FollowsEventID != goal.GoalEventID {
		t.Fatalf("the rework follows %v, want the rejected goal %s", got.FollowsEventID, goal.GoalEventID)
	}
	if got.WorkflowInstanceID != goal.WorkflowID {
		t.Errorf("the rework landed on instance %s, want the goal's %s — a fresh instance would hide the rework from the goal's own log",
			got.WorkflowInstanceID, goal.WorkflowID)
	}

	// 2. It opened a contract of its own. Without this the rework is telemetry:
	// nothing would report the work as outstanding, and nothing would demand a
	// close.
	if n := e.openContractCount(t, "rework_requested"); n != 1 {
		t.Fatalf("%d rework contracts open, want 1", n)
	}

	// 3. The dispatcher drives it into a spawn, on the same instance, under a
	// FRESH name — discard_and_redo means a new agent, not the one that already
	// produced the rejected result.
	if _, err := d.Step(ctx); err != nil { // goal_closed -> notify
		t.Fatalf("Step over goal_closed: %v", err)
	}
	if _, err := d.Step(ctx); err != nil { // rework_requested -> spawn_requested
		t.Fatalf("Step over rework_requested: %v", err)
	}
	events, err := e.goals.EventsByWorkflowInstance(ctx, e.projectID, goal.WorkflowID, 0, 100)
	if err != nil {
		t.Fatalf("reading the instance: %v", err)
	}
	var spawns []DispatchedEvent
	for _, ev := range events {
		if ev.SchemaName == "spawn_requested" {
			spawns = append(spawns, ev)
		}
	}
	if len(spawns) != 2 {
		t.Fatalf("the instance holds %d spawn requests, want 2 (the original and the rework's); last event %q", len(spawns), lastSchema(events))
	}
	second := e.reworkPayload(t, spawns[1])
	if second["agent_name"] == "ghost" {
		t.Errorf("the rework was handed back to %q, the agent whose result was rejected — discard_and_redo means a fresh agent", second["agent_name"])
	}
	prompt, _ := second["prompt"].(string)
	if !strings.Contains(prompt, "the answer does not mention the claims consumer") {
		t.Errorf("the rework prompt does not carry the reason the first attempt was rejected: %q", prompt)
	}
	if !strings.Contains(prompt, "find out how the cursor is derived") {
		t.Errorf("the rework prompt does not carry the original goal's text, so the fresh agent has no task: %q", prompt)
	}
	if !strings.Contains(prompt, rework.ReworkEventID.String()) {
		t.Errorf("the rework prompt does not name the contract the agent must close: %q", prompt)
	}
}

// The reworking agent has to be able to SEE and CLOSE its contract. A rework
// that spawns an agent which then reads "you have no goal" is the same
// end-to-end defect the assigned-agent predicate fixed for goal_opened, one
// schema over — and it fails the same silent way: report_result refuses, and the
// contract stays open forever.
func TestReworkPg_TheReworkingAgentSeesAndClosesTheReworkContract(t *testing.T) {
	e := newEngineLoopEnv(t)
	ctx := context.Background()

	d := e.dispatcher(t, &seqNamer{names: []string{"ghost", "spectre"}})
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
	rework, err := e.ledger.RequestRework(ctx, "weave", goal.GoalEventID, "you did not try the sandbox", DiscardAndRedo)
	if err != nil {
		t.Fatalf("RequestRework: %v", err)
	}
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over rework_requested: %v", err)
	}

	mine, err := e.ledger.OpenGoalsForAgent(ctx, "spectre")
	if err != nil {
		t.Fatalf("OpenGoalsForAgent(spectre): %v", err)
	}
	if len(mine) != 1 || mine[0].GoalEventID != rework.ReworkEventID {
		t.Fatalf("the reworking agent sees %d goals, want the rework contract %s", len(mine), rework.ReworkEventID)
	}
	if mine[0].Owner != "weave" {
		t.Errorf("the rework reports owner %q, want weave — who the redone result is announced to", mine[0].Owner)
	}
	if mine[0].GoalType != string(GoalResearch) {
		t.Errorf("the rework reports goal_type %q, want %q carried over from the goal it follows", mine[0].GoalType, GoalResearch)
	}

	if _, err := e.ledger.CloseGoalForAgent(ctx, "spectre", rework.ReworkEventID, GoalSucceeded, "reproduced in the sandbox"); err != nil {
		t.Fatalf("the reworking agent could not close its own contract: %v", err)
	}
	if n := e.openContractCount(t, "rework_requested"); n != 0 {
		t.Errorf("%d rework contracts still open after the redone result landed, want 0", n)
	}

	// The redone close notifies the owner, same as any other. Without this leg
	// the owner asks for rework and is never told it landed.
	if _, err := d.Step(ctx); err != nil {
		t.Fatalf("Step over the rework's goal_closed: %v", err)
	}
	var toOwner int
	for _, got := range e.injector.all() {
		if got.Recipient == "weave" {
			toOwner++
		}
	}
	if toOwner != 2 {
		t.Errorf("the owner got %d notifications, want 2 (the rejected result and the redone one)", toOwner)
	}
}

// re_engage_original is defined by the seed and by internal/engine, and the
// dispatcher does NOT implement it. Asserted rather than left undefined: an
// unhandled policy that fell through to "allocate a fresh name" would silently
// do discard_and_redo while the log said otherwise.
func TestReworkPg_ReEngageOriginalIsRefusedRatherThanSilentlyRedone(t *testing.T) {
	e := newEngineLoopEnv(t)
	ctx := context.Background()

	goal, err := e.ledger.OpenGoal(ctx, GoalResearch, "find out", "weave")
	if err != nil {
		t.Fatalf("OpenGoal: %v", err)
	}
	if _, err := e.ledger.RequestRework(ctx, "weave", goal.GoalEventID, "missed a case", ReEngageOriginal); err == nil {
		t.Fatal("RequestRework accepted re_engage_original, which nothing downstream implements")
	} else if !strings.Contains(err.Error(), "re_engage_original") {
		t.Errorf("the refusal does not name the policy it refused: %v", err)
	}
	// Control: the same call with the implemented policy is accepted, so the
	// assertion above is not passing because RequestRework refuses everything.
	if _, err := e.ledger.RequestRework(ctx, "weave", goal.GoalEventID, "missed a case", DiscardAndRedo); err != nil {
		t.Fatalf("RequestRework(discard_and_redo): %v", err)
	}
}
