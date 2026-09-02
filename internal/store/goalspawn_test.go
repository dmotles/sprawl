package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Tests for the goal_opened -> spawn_requested handler (QUM-1252, M3a slice
// 10b commit 2).
//
// This handler is the thing that makes create_goal non-inert. Its failure modes
// are all quiet: a goal that produces no request is a contract open forever with
// nobody working it, and a request naming an agent type nothing can spawn is the
// same outcome one step later.

type fixedNamer struct {
	name     string
	err      error
	forTypes []string
}

func (f *fixedNamer) AllocateName(_ context.Context, agentType string) (string, error) {
	f.forTypes = append(f.forTypes, agentType)
	if f.err != nil {
		return "", f.err
	}
	return f.name, nil
}

// decodePayload reads an appended payload back out. EmitRequest.Payload is
// json.RawMessage, so the assertions have to go through the wire form — which is
// the right level anyway: it is what the next handler will read.
func decodePayload(t *testing.T, payload any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload does not marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("payload is not an object: %v (%s)", err, raw)
	}
	return m
}

func goalSpawnEvent(t *testing.T, payload map[string]any) DispatchedEvent {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshalling payload: %v", err)
	}
	return DispatchedEvent{
		ID:                 uuid.New(),
		WorkflowInstanceID: uuid.New(),
		SchemaName:         "goal_opened",
		SchemaVersion:      1,
		Payload:            b,
	}
}

func newGoalSpawnHandler(t *testing.T, e EventEmitter, n NameAllocator) *GoalSpawnHandler {
	t.Helper()
	h, err := NewGoalSpawnHandler(GoalSpawnHandlerDeps{Emitter: e, Names: n})
	if err != nil {
		t.Fatalf("NewGoalSpawnHandler: %v", err)
	}
	return h
}

// TestGoalSpawnHandler_RequestsAnAgentForAResearchGoal.
//
// The workflow_instance_id is the load-bearing field: it is how the spawned
// agent's reread_my_goal and report_result find the goal at all. A request that
// carried a fresh one would spawn an agent onto an empty workflow — it would
// look entirely healthy and would close nothing.
func TestGoalSpawnHandler_RequestsAnAgentForAResearchGoal(t *testing.T) {
	em := &recordingEmitter{}
	h := newGoalSpawnHandler(t, em, &fixedNamer{name: "ada"})
	ev := goalSpawnEvent(t, map[string]any{
		"goal_type": "research", "text": "how is the cursor derived?", "owner": "boss",
	})

	if err := h.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(em.events) != 1 {
		t.Fatalf("emitted %d event(s), want exactly one spawn_requested", len(em.events))
	}
	req := em.events[0]
	pay := decodePayload(t, req.Payload)
	if req.TypeName != "spawn_requested" {
		t.Errorf("emitted %q, want spawn_requested", req.TypeName)
	}
	if req.WorkflowInstanceID != ev.WorkflowInstanceID {
		t.Errorf("the request carries workflow instance %s, want the goal's %s — an agent on the wrong workflow can neither read its goal nor close it",
			req.WorkflowInstanceID, ev.WorkflowInstanceID)
	}
	if got := pay["agent_name"]; got != "ada" {
		t.Errorf("agent_name is %v, want the allocated name ada", got)
	}
	if got := pay["agent_type"]; got != "researcher" {
		t.Errorf("a research goal asked for agent_type %v, want researcher", got)
	}
	if got := pay["parent"]; got != "boss" {
		t.Errorf("parent is %v, want the goal's owner boss — the parent is who the agent reports to", got)
	}
	prompt, _ := pay["prompt"].(string)
	if !strings.Contains(prompt, "how is the cursor derived?") {
		t.Errorf("the goal text is not in the prompt; it is the entire task: %q", prompt)
	}
	if !strings.Contains(prompt, ev.WorkflowInstanceID.String()) {
		t.Errorf("the prompt does not carry the workflow instance id, so the agent cannot read its own log: %q", prompt)
	}
	if !strings.Contains(prompt, "report_result") {
		t.Errorf("the prompt does not name the tool that closes the goal; without it the goal is never closed: %q", prompt)
	}
}

// TestGoalSpawnHandler_RequestsAWorktreeTheSpawnerCanActuallyCreate.
//
// family and branch are not decoration: agentops.PrepareSpawn REFUSES a spawn
// with an invalid family, and REFUSES one with an empty branch when subagent is
// false. A request missing either is a well-formed event that every consumer
// rejects — the goal would look requested and never start.
//
// The branch is derived from the goal event id rather than from the goal text so
// two goals opened against the same agent name cannot collide, and it is prefixed
// `goal/` so an engine-created branch is identifiable without consulting the log.
func TestGoalSpawnHandler_RequestsAWorktreeTheSpawnerCanActuallyCreate(t *testing.T) {
	em := &recordingEmitter{}
	h := newGoalSpawnHandler(t, em, &fixedNamer{name: "ada"})
	ev := goalSpawnEvent(t, map[string]any{
		"goal_type": "research", "text": "q", "owner": "boss",
	})

	if err := h.Handle(context.Background(), ev); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	pay := decodePayload(t, em.events[0].Payload)
	if got := pay["family"]; got != "engineering" {
		t.Errorf("family is %v, want engineering — an invalid family is refused by the spawner", got)
	}
	branch, _ := pay["branch"].(string)
	if !strings.HasPrefix(branch, "goal/ada-") {
		t.Errorf("branch is %q, want a goal/<agent>-<id> branch; the spawner refuses an empty branch", branch)
	}
	if !strings.HasPrefix(ev.ID.String(), strings.TrimPrefix(branch, "goal/ada-")) {
		t.Errorf("branch %q does not derive from the goal event id %s, so two goals for one agent name could collide",
			branch, ev.ID)
	}
	if sub, ok := pay["subagent"].(bool); ok && sub {
		t.Error("the request asks for a sub-agent; an engine-driven goal needs its own worktree, and a sub-agent must have no branch")
	}
}

// TestGoalSpawnHandler_MapsBugInvestigationToItsOwnAgentType is the routing
// table's second row, and a control on the first: without it, a handler that
// hard-coded "researcher" would pass every assertion above.
func TestGoalSpawnHandler_MapsBugInvestigationToItsOwnAgentType(t *testing.T) {
	em := &recordingEmitter{}
	namer := &fixedNamer{name: "finn"}
	h := newGoalSpawnHandler(t, em, namer)

	if err := h.Handle(context.Background(), goalSpawnEvent(t, map[string]any{
		"goal_type": "bug_investigation", "text": "it panics on startup", "owner": "boss",
	})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := decodePayload(t, em.events[0].Payload)["agent_type"]; got != "engineer" {
		t.Errorf("a bug_investigation goal asked for agent_type %v, want engineer", got)
	}
	// The name must be allocated FOR that type: the pools are partitioned, and a
	// researcher name handed to an engineer is a name collision waiting to happen.
	if len(namer.forTypes) != 1 || namer.forTypes[0] != "engineer" {
		t.Errorf("the name was allocated for %v, want [engineer]", namer.forTypes)
	}
}

// TestGoalSpawnHandler_RefusesAGoalTypeItCannotDrive.
//
// A goal type with no mapping must fail the handler rather than emit a request
// for some default agent. The dispatcher retries a failed handler, so the goal
// stays visibly unstarted; a defaulted spawn would look like success and put the
// wrong agent on the work.
func TestGoalSpawnHandler_RefusesAGoalTypeItCannotDrive(t *testing.T) {
	em := &recordingEmitter{}
	h := newGoalSpawnHandler(t, em, &fixedNamer{name: "ada"})

	err := h.Handle(context.Background(), goalSpawnEvent(t, map[string]any{
		"goal_type": "change", "text": "make it faster", "owner": "boss",
	}))
	if err == nil {
		t.Fatal("a goal type with no agent mapping was accepted")
	}
	if !strings.Contains(err.Error(), "change") {
		t.Errorf("the error should name the goal type it cannot drive; got: %v", err)
	}
	if len(em.events) != 0 {
		t.Errorf("a spawn was requested anyway (%d event(s))", len(em.events))
	}
}

// TestGoalSpawnHandler_NoNameNoRequest. Allocation failure must abort BEFORE the
// append: spawn_requested requires agent_name, and the reconciler matches
// intents to local agents by name, so an unnamed request is unreconcilable in a
// log that cannot be edited.
func TestGoalSpawnHandler_NoNameNoRequest(t *testing.T) {
	em := &recordingEmitter{}
	h := newGoalSpawnHandler(t, em, &fixedNamer{err: errors.New("state dir is unreadable")})

	err := h.Handle(context.Background(), goalSpawnEvent(t, map[string]any{
		"goal_type": "research", "text": "q", "owner": "boss",
	}))
	if err == nil {
		t.Fatal("a goal was handled without allocating a name")
	}
	if len(em.events) != 0 {
		t.Errorf("spawn_requested was appended without an agent name (%d event(s))", len(em.events))
	}

	// Positive control: the same handler with a working allocator does append,
	// so the assertion above is about the allocation failure and not about the
	// handler never emitting.
	ok := &recordingEmitter{}
	if err := newGoalSpawnHandler(t, ok, &fixedNamer{name: "ada"}).
		Handle(context.Background(), goalSpawnEvent(t, map[string]any{
			"goal_type": "research", "text": "q", "owner": "boss",
		})); err != nil {
		t.Fatalf("control: %v", err)
	}
	if len(ok.events) != 1 {
		t.Fatalf("control: emitted %d event(s), want 1", len(ok.events))
	}
}

// TestGoalSpawnHandler_RefusesAnOwnerlessGoal. `owner` becomes the spawned
// agent's parent, and an agent with no parent has nobody to report to — its
// result would go to whatever the notify handler's host-level fallback names.
func TestGoalSpawnHandler_RefusesAnOwnerlessGoal(t *testing.T) {
	em := &recordingEmitter{}
	h := newGoalSpawnHandler(t, em, &fixedNamer{name: "ada"})

	err := h.Handle(context.Background(), goalSpawnEvent(t, map[string]any{
		"goal_type": "research", "text": "q",
	}))
	if err == nil {
		t.Fatal("a goal with no owner was accepted")
	}
	if len(em.events) != 0 {
		t.Errorf("a parentless spawn was requested (%d event(s))", len(em.events))
	}
}

// TestGoalSpawnHandler_RejectsAnUnreadablePayload keeps a malformed event from
// being silently treated as an empty goal.
func TestGoalSpawnHandler_RejectsAnUnreadablePayload(t *testing.T) {
	em := &recordingEmitter{}
	h := newGoalSpawnHandler(t, em, &fixedNamer{name: "ada"})

	ev := DispatchedEvent{ID: uuid.New(), WorkflowInstanceID: uuid.New(), Payload: []byte(`{`)}
	if err := h.Handle(context.Background(), ev); err == nil {
		t.Fatal("an unreadable goal_opened payload was accepted")
	}
	if len(em.events) != 0 {
		t.Errorf("a spawn was requested from an unreadable payload (%d event(s))", len(em.events))
	}
}

// TestNewGoalSpawnHandler_RequiresItsDeps. Both are structural: without an
// emitter the handler is a no-op that reports success, and without an allocator
// every request would be unnamed.
func TestNewGoalSpawnHandler_RequiresItsDeps(t *testing.T) {
	if _, err := NewGoalSpawnHandler(GoalSpawnHandlerDeps{Names: &fixedNamer{}}); err == nil {
		t.Error("a handler with no emitter was accepted")
	}
	if _, err := NewGoalSpawnHandler(GoalSpawnHandlerDeps{Emitter: &recordingEmitter{}}); err == nil {
		t.Error("a handler with no name allocator was accepted")
	}
	// Control: both present is accepted, so the two assertions above are about
	// the missing dep rather than the constructor refusing everything.
	if _, err := NewGoalSpawnHandler(GoalSpawnHandlerDeps{Emitter: &recordingEmitter{}, Names: &fixedNamer{}}); err != nil {
		t.Errorf("control: a fully wired handler was refused: %v", err)
	}
}

// TestEveryMigratedGoalTypeCanBeDriven is the coupling between the two tables.
//
// create_goal accepts MigratedGoalTypes; this handler drives goalAgentTypes. If
// the first grows without the second, create_goal opens a goal that no agent is
// ever requested for — a well-formed contract nothing can close, which surfaces
// hours later as work that simply never happened. Nothing else in the tree
// relates the two lists, so this test is the relation.
func TestEveryMigratedGoalTypeCanBeDriven(t *testing.T) {
	for _, gt := range MigratedGoalTypes {
		if _, ok := goalAgentTypes[gt]; !ok {
			t.Errorf("create_goal accepts goal type %q but no agent type is mapped to it, so opening one would spawn nobody", gt)
		}
	}
	// Positive control on the other direction: a type nobody has migrated must
	// NOT be driveable, or the assertion above would pass against a table that
	// mapped everything.
	if _, ok := goalAgentTypes[GoalType("change")]; ok {
		t.Error(`goalAgentTypes drives "change", which create_goal refuses — the two tables disagree in the other direction`)
	}
}
