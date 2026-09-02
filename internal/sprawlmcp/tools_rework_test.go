package sprawlmcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// Tests for request_rework (QUM-1252, M3a, AC3).
//
// Like create_goal the tool is thin, so the assertions are about what it
// REFUSES and what it TELLS the caller. Both failure modes are quiet: rework
// requested by an agent that is not the one waiting for the redone result, or a
// caller who believes a fresh agent is already running when the tool returns.

func reworkArgs(goalEventID, reason string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"goal_event_id": goalEventID, "reason": reason})
	return b
}

func TestRequestRework_RejectsTheResultAndOwnsTheReworkToTheCaller(t *testing.T) {
	rejected := uuid.New()
	src := &fakeGoalSource{enabled: true, rework: store.RequestedRework{ReworkEventID: uuid.New(), WorkflowID: uuid.New()}}
	srv := New(&mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "boss", Type: "manager"}}}).WithGoals(src)

	out, err := srv.toolRequestRework(callerCtx("boss"), reworkArgs(rejected.String(), "the answer does not name the consumer"))
	if err != nil {
		t.Fatalf("toolRequestRework: %v", err)
	}
	if src.reworkCalls != 1 {
		t.Fatalf("RequestRework reached the store %d time(s), want 1", src.reworkCalls)
	}
	if src.reworkOwner != "boss" {
		t.Errorf("the rework was owned to %q, want boss — the owner is who the redone result is reported to", src.reworkOwner)
	}
	if src.reworkRejected != rejected {
		t.Errorf("the store was asked to rework %s, want %s", src.reworkRejected, rejected)
	}
	if src.reworkReason != "the answer does not name the consumer" {
		t.Errorf("reason reached the store as %q", src.reworkReason)
	}
	if src.reworkPolicy != store.DiscardAndRedo {
		t.Errorf("re-engagement reached the store as %q, want %q — nothing downstream can wake the original agent", src.reworkPolicy, store.DiscardAndRedo)
	}

	var got requestReworkOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshalling the result: %v", err)
	}
	if got.ReworkEventID != src.rework.ReworkEventID.String() {
		t.Errorf("the result names rework contract %q, want %q", got.ReworkEventID, src.rework.ReworkEventID)
	}
	// The note is the only place a caller learns that "recorded" is not
	// "underway" — the same distinction create_goal's note carries, and the same
	// one a caller will otherwise report to a human as a running agent.
	if !strings.Contains(got.Note, "recorded") {
		t.Errorf("the note does not say the rework is only recorded: %q", got.Note)
	}
}

func TestRequestRework_RefusesAnUnparseableGoalEventID(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	srv := New(&mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "boss", Type: "manager"}}}).WithGoals(src)

	_, err := srv.toolRequestRework(callerCtx("boss"), reworkArgs("not-a-uuid", "wrong"))
	if err == nil {
		t.Fatal("request_rework accepted a goal_event_id that is not an id")
	}
	if src.reworkCalls != 0 {
		t.Errorf("the store was reached %d time(s) despite the refusal, want 0", src.reworkCalls)
	}
}

func TestRequestRework_RefusesAnEmptyReason(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	srv := New(&mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "boss", Type: "manager"}}}).WithGoals(src)

	_, err := srv.toolRequestRework(callerCtx("boss"), reworkArgs(uuid.New().String(), ""))
	if err == nil {
		t.Fatal("request_rework accepted an empty reason, which is all the next agent gets told about the miss")
	}
	if src.reworkCalls != 0 {
		t.Errorf("the store was reached %d time(s) despite the refusal, want 0", src.reworkCalls)
	}
}

// Restricted to the same callers create_goal is, and for the same reason: this
// spawns an agent by way of the log, and it must be its own refusal so a caller
// can tell the two apart programmatically.
func TestRequestRework_RestrictedToManagersAndRoot(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	srv := New(&mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "grunt", Type: "engineer"}}}).WithGoals(src)

	_, err := srv.toolRequestRework(callerCtx("grunt"), reworkArgs(uuid.New().String(), "wrong"))
	if err == nil {
		t.Fatal("an engineer was allowed to request rework")
	}
	if !strings.Contains(err.Error(), "request_rework") {
		t.Errorf("the refusal does not name the tool it refused: %v", err)
	}
	if src.reworkCalls != 0 {
		t.Errorf("the store was reached %d time(s) despite the refusal, want 0", src.reworkCalls)
	}
}

// A store error reaches the caller rather than being reported as a recorded
// rework. The write is the only durable step, so a swallowed error here is a
// rejection nobody acts on and nobody can see.
func TestRequestRework_SurfacesAStoreFailure(t *testing.T) {
	src := &fakeGoalSource{enabled: true, reworkErr: errors.New("no open contract")}
	srv := New(&mockSupervisor{statusResult: []supervisor.AgentInfo{{Name: "boss", Type: "manager"}}}).WithGoals(src)

	if _, err := srv.toolRequestRework(callerCtx("boss"), reworkArgs(uuid.New().String(), "wrong")); err == nil {
		t.Fatal("a failed append was reported as a recorded rework")
	}
}

// The catalog entry is load-bearing: a tool the model cannot see is a tool
// nobody calls, and the dispatch switch alone gives no such signal.
func TestRequestRework_IsInTheToolCatalog(t *testing.T) {
	var found map[string]any
	for _, tool := range toolDefinitions() {
		if tool["name"] == "request_rework" {
			found = tool
			break
		}
	}
	if found == nil {
		t.Fatal("request_rework is not in the tool catalog, so no model will ever call it")
	}
	schema, _ := found["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"goal_event_id", "reason"} {
		if _, ok := props[want]; !ok {
			t.Errorf("the request_rework schema has no %q property", want)
		}
	}
}
