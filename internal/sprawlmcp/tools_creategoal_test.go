package sprawlmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// Tests for create_goal (QUM-1252, M3a slice 10b).
//
// The tool is deliberately thin: it appends `goal_opened` and returns. The
// interesting assertions are therefore not about what it computes but about
// what it REFUSES and what it TELLS the caller, because both failure modes here
// are quiet ones — a goal opened by the wrong caller, or a caller who believes
// an agent is already running.

func createGoalServer(src GoalSource, agents ...supervisor.AgentInfo) *Server {
	return New(&mockSupervisor{statusResult: agents}).WithGoals(src)
}

func createGoalArgs(goalType, text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"goal_type": goalType, "text": text})
	return b
}

// TestCreateGoal_OpensTheGoalAndOwnsItToTheCaller.
//
// `owner` is the load-bearing one. It is who OWNER_NOTIFY reaches when the goal
// closes, so an owner taken from anywhere other than the calling session is a
// result delivered to the wrong agent — and delivered successfully, which is why
// nothing downstream would report it as a fault.
func TestCreateGoal_OpensTheGoalAndOwnsItToTheCaller(t *testing.T) {
	opened := store.OpenedGoal{GoalEventID: uuid.New(), WorkflowID: uuid.New(), GoalType: store.GoalResearch}
	src := &fakeGoalSource{enabled: true, openedGoal: opened}
	srv := createGoalServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"})

	out, err := srv.toolCreateGoal(callerCtx("boss"), createGoalArgs("research", "how is the cursor derived?"))
	if err != nil {
		t.Fatalf("toolCreateGoal: %v", err)
	}
	if src.openCalls != 1 {
		t.Fatalf("OpenGoal reached the store %d time(s), want 1", src.openCalls)
	}
	if src.openedOwner != "boss" {
		t.Errorf("the goal was owned to %q, want boss — the owner is who the result is reported to", src.openedOwner)
	}
	if src.openedType != store.GoalResearch {
		t.Errorf("goal type reached the store as %q, want research", src.openedType)
	}
	if src.openedText != "how is the cursor derived?" {
		t.Errorf("text reached the store as %q", src.openedText)
	}

	var got createGoalOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.GoalEventID != opened.GoalEventID.String() {
		t.Errorf("goal_event_id came back %q, want the id the store minted (%s)", got.GoalEventID, opened.GoalEventID)
	}
	if got.WorkflowInstanceID != opened.WorkflowID.String() {
		t.Errorf("workflow_instance_id came back %q, want %s", got.WorkflowInstanceID, opened.WorkflowID)
	}
	// The note is the whole honesty of the log-driven design. create_goal
	// returning success means the GOAL IS RECORDED, not that anybody is working
	// it — the dispatcher spawns from the event afterwards. A caller that reads
	// success as "the researcher is running" will report that to a human, and
	// will be wrong for as long as it takes the dispatcher to get there.
	if !strings.Contains(got.Note, "recorded") {
		t.Errorf("the result must say the goal was RECORDED rather than implying an agent is already running; got note=%q", got.Note)
	}
	if !strings.Contains(got.Note, "dispatcher") {
		t.Errorf("the note should name what does the spawning, so a caller that sees no agent yet knows what it is waiting for; got note=%q", got.Note)
	}
}

// TestCreateGoal_ResolvesAnUnnamedCallerToTheRootAgent.
//
// The root weave session calls tools with no injected identity — it is the most
// likely caller of create_goal, so an empty identity cannot simply be refused
// the way report_result refuses it. But an owner must be a real agent name, so
// it is read from the supervisor rather than invented.
func TestCreateGoal_ResolvesAnUnnamedCallerToTheRootAgent(t *testing.T) {
	src := &fakeGoalSource{enabled: true, openedGoal: store.OpenedGoal{GoalEventID: uuid.New(), WorkflowID: uuid.New()}}
	srv := createGoalServer(src, supervisor.AgentInfo{Name: "weave", Type: "root"})

	if _, err := srv.toolCreateGoal(callerCtx(""), createGoalArgs("research", "q")); err != nil {
		t.Fatalf("toolCreateGoal from the root session: %v", err)
	}
	if src.openedOwner != "weave" {
		t.Errorf("an unnamed caller owned the goal to %q, want the real root name weave — a literal here would put a name in the log that no agent record backs", src.openedOwner)
	}
}

// TestCreateGoal_RefusalsNeverReachTheStore.
//
// Same reasoning as report_result's and ask_user's equivalents: a tool that
// validated AFTER appending would still return an error, and the goal would
// still be open — and an open goal is not something an error message retracts.
func TestCreateGoal_RefusalsNeverReachTheStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  string
		roster  []supervisor.AgentInfo
		args    json.RawMessage
		wantErr string
	}{
		{
			// An engineer opening goals would route work around its own manager.
			"ineligible caller", "ratz",
			[]supervisor.AgentInfo{{Name: "ratz", Type: "engineer"}},
			createGoalArgs("research", "q"), "restricted to weave and managers",
		},
		{
			"empty text", "boss",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			createGoalArgs("research", ""), "needs text",
		},
		{
			"empty goal type", "boss",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			createGoalArgs("", "q"), "goal_type",
		},
		{
			"malformed arguments", "boss",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			json.RawMessage(`{`), "invalid arguments",
		},
		{
			"unnamed caller and no root agent", "",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			createGoalArgs("research", "q"), "cannot tell which agent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeGoalSource{enabled: true, openedGoal: store.OpenedGoal{GoalEventID: uuid.New()}}
			_, err := createGoalServer(src, tc.roster...).toolCreateGoal(callerCtx(tc.caller), tc.args)
			if err == nil {
				t.Fatal("the call was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q; got: %v", tc.wantErr, err)
			}
			if src.openCalls != 0 {
				t.Errorf("the store was written to despite the refusal (%d call(s))", src.openCalls)
			}
		})
	}
}

// TestCreateGoal_RoutesAnUnmigratedTypeToTheLegacyPath is the routing table as
// the CALLER experiences it.
//
// The store refuses an unmigrated type, and this pins that the tool passes that
// refusal through with the alternative intact. A create_goal that answered
// "invalid goal type" and stopped would leave a model with no next move, and the
// legacy `spawn` path — which is fully functional and is the correct answer for
// every unmigrated type — would go unused because nothing pointed at it.
func TestCreateGoal_RoutesAnUnmigratedTypeToTheLegacyPath(t *testing.T) {
	src := &fakeGoalSource{
		enabled:    true,
		openErr:    store.ErrUnmigratedGoalType,
		openedGoal: store.OpenedGoal{GoalEventID: uuid.New()},
	}
	srv := createGoalServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"})

	_, err := srv.toolCreateGoal(callerCtx("boss"), createGoalArgs("change", "make it faster"))
	if err == nil {
		t.Fatal("an unmigrated goal type was accepted")
	}
	if !strings.Contains(err.Error(), "spawn") {
		t.Errorf("the refusal must name the legacy path that DOES handle this type; got: %v", err)
	}

	// Positive control: a migrated type is not refused, so the assertion above
	// is about the type and not about the tool refusing everything.
	ok := &fakeGoalSource{enabled: true, openedGoal: store.OpenedGoal{GoalEventID: uuid.New(), WorkflowID: uuid.New()}}
	if _, err := createGoalServer(ok, supervisor.AgentInfo{Name: "boss", Type: "manager"}).
		toolCreateGoal(callerCtx("boss"), createGoalArgs("bug_investigation", "it panics on startup")); err != nil {
		t.Fatalf("control: a migrated goal type must be accepted: %v", err)
	}
	if ok.openedType != store.GoalBugInvestigation {
		t.Errorf("control: goal type reached the store as %q, want bug_investigation", ok.openedType)
	}
}

// TestCreateGoal_ReportsADisabledStoreAsConfiguration. "there are no goals" and
// "this host does not record goals" are two different answers, and an agent that
// cannot see the host's config cannot tell them apart on its own.
func TestCreateGoal_ReportsADisabledStoreAsConfiguration(t *testing.T) {
	src := &fakeGoalSource{enabled: false}
	srv := createGoalServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"})

	_, err := srv.toolCreateGoal(callerCtx("boss"), createGoalArgs("research", "q"))
	if err == nil {
		t.Fatal("create_goal succeeded against a disabled store")
	}
	if !strings.Contains(err.Error(), "event_log.enabled") {
		t.Errorf("the error must name the config key; got: %v", err)
	}
	if src.openCalls != 0 {
		t.Errorf("a disabled store was written to (%d call(s))", src.openCalls)
	}
}

// TestCreateGoal_IsAdvertisedAndDispatchable closes the gap between "the
// function exists" and "an agent can call it".
//
// A tool missing from the definition list is invisible to the model; a tool
// missing from the dispatch switch returns "unknown tool" at the moment it is
// first used. Neither shows up in any test of toolCreateGoal itself.
func TestCreateGoal_IsAdvertisedAndDispatchable(t *testing.T) {
	var found map[string]any
	for _, d := range toolDefinitions() {
		if d["name"] == "create_goal" {
			found = d
			break
		}
	}
	if found == nil {
		t.Fatal("create_goal is not in the tool definitions, so no agent can see it")
	}
	schema, _ := json.Marshal(found["inputSchema"])
	for _, want := range []string{"goal_type", "text", "research", "bug_investigation"} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("the input schema does not mention %q; the enum is what stops a model inventing a goal type: %s", want, schema)
		}
	}

	src := &fakeGoalSource{enabled: true, openedGoal: store.OpenedGoal{GoalEventID: uuid.New(), WorkflowID: uuid.New()}}
	srv := createGoalServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"})
	if _, err := srv.dispatchTool(callerCtx("boss"), "create_goal", createGoalArgs("research", "q")); err != nil {
		t.Fatalf("dispatchTool(create_goal): %v", err)
	}
	if src.openCalls != 1 {
		t.Errorf("dispatching create_goal reached the store %d time(s), want 1", src.openCalls)
	}
}
