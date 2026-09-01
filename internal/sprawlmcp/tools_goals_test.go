package sprawlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	backendpkg "github.com/dmotles/sprawl/internal/backend"
	"github.com/dmotles/sprawl/internal/store"
)

// fakeGoalSource is a hand-rolled GoalSource. It records what it was asked so
// the tests can assert the CALLER's identity reached the store — a tool that
// passed the wrong owner would still return a well-formed, entirely wrong goal.
type fakeGoalSource struct {
	enabled  bool
	gotAgent string
	goals    []store.AgentGoal
	goalsErr error

	gotWorkflow uuid.UUID
	gotAfterSeq int64
	gotLimit    int
	events      []store.DispatchedEvent
	eventsErr   error

	closedAgent   string
	closedGoal    uuid.UUID
	closedOutcome store.GoalOutcome
	closedSummary string
	closeCalls    int
	closeID       uuid.UUID
	closeErr      error
}

func (f *fakeGoalSource) CloseGoalForAgent(_ context.Context, agent string, goal uuid.UUID, outcome store.GoalOutcome, summary string) (uuid.UUID, error) {
	f.closeCalls++
	f.closedAgent, f.closedGoal, f.closedOutcome, f.closedSummary = agent, goal, outcome, summary
	return f.closeID, f.closeErr
}

func (f *fakeGoalSource) Enabled() bool { return f.enabled }

func (f *fakeGoalSource) OpenGoalsForAgent(_ context.Context, agent string) ([]store.AgentGoal, error) {
	f.gotAgent = agent
	return f.goals, f.goalsErr
}

func (f *fakeGoalSource) EventsByWorkflowInstance(_ context.Context, wf uuid.UUID, afterSeq int64, limit int) ([]store.DispatchedEvent, error) {
	f.gotWorkflow, f.gotAfterSeq, f.gotLimit = wf, afterSeq, limit
	return f.events, f.eventsErr
}

func callerCtx(name string) context.Context {
	return backendpkg.WithCallerIdentity(context.Background(), name)
}

func goalServer(src GoalSource) *Server {
	return New(nil).WithGoals(src)
}

// TestRereadMyGoal_ReturnsTheCallersOwnGoals. The load-bearing assertion is
// gotAgent: the owner is taken from the session, never from arguments, so an
// agent cannot ask for somebody else's goal and the tool cannot guess.
func TestRereadMyGoal_ReturnsTheCallersOwnGoals(t *testing.T) {
	goal, wf := uuid.New(), uuid.New()
	src := &fakeGoalSource{enabled: true, goals: []store.AgentGoal{{
		GoalEventID: goal, WorkflowID: wf, GoalType: "research",
		OpenedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}}}

	out, err := goalServer(src).toolRereadMyGoal(callerCtx("finn"))
	if err != nil {
		t.Fatalf("toolRereadMyGoal: %v", err)
	}
	if src.gotAgent != "finn" {
		t.Errorf("the store was asked for %q's goals, want finn's — the owner comes from the session", src.gotAgent)
	}

	var got rereadMyGoalResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("the result is not JSON: %v\n%s", err, out)
	}
	if got.Count != 1 || len(got.Goals) != 1 {
		t.Fatalf("got count=%d and %d goals, want 1 and 1", got.Count, len(got.Goals))
	}
	if got.Goals[0].GoalEventID != goal.String() {
		t.Errorf("goal_event_id came back %q, want %q — it is the id a close must reference", got.Goals[0].GoalEventID, goal)
	}
	if got.Goals[0].WorkflowID != wf.String() {
		t.Errorf("workflow_instance_id came back %q, want %q — it is what get_workflow_log takes", got.Goals[0].WorkflowID, wf)
	}
	if got.Note != "" {
		t.Errorf("a single unambiguous goal should carry no note; got %q", got.Note)
	}
}

// TestRereadMyGoal_NoGoalsAndManyGoalsAreBothSaidOutLoud.
//
// Paired on purpose. An empty list and a two-element list are the two results
// an agent will MISREAD if the tool merely returns them: empty reads as "the
// store is broken" and two reads as "here is your goal". Each half is the
// other's control — a note hard-coded either way fails the opposite case.
func TestRereadMyGoal_NoGoalsAndManyGoalsAreBothSaidOutLoud(t *testing.T) {
	empty, err := goalServer(&fakeGoalSource{enabled: true}).toolRereadMyGoal(callerCtx("finn"))
	if err != nil {
		t.Fatalf("toolRereadMyGoal(no goals): %v", err)
	}
	var none rereadMyGoalResult
	if err := json.Unmarshal([]byte(empty), &none); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if none.Count != 0 || !strings.Contains(none.Note, "no open goals") {
		t.Errorf("an agent with no open goals got count=%d note=%q", none.Count, none.Note)
	}
	if none.Goals == nil {
		t.Error("goals came back as JSON null rather than [], which a model reads as an error")
	}

	src := &fakeGoalSource{enabled: true, goals: []store.AgentGoal{
		{GoalEventID: uuid.New(), WorkflowID: uuid.New(), GoalType: "research"},
		{GoalEventID: uuid.New(), WorkflowID: uuid.New(), GoalType: "bug"},
	}}
	many, err := goalServer(src).toolRereadMyGoal(callerCtx("finn"))
	if err != nil {
		t.Fatalf("toolRereadMyGoal(two goals): %v", err)
	}
	var amb rereadMyGoalResult
	if err := json.Unmarshal([]byte(many), &amb); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if amb.Count != 2 {
		t.Errorf("got count=%d, want 2 — the store returns all of them rather than tie-breaking", amb.Count)
	}
	if !strings.Contains(amb.Note, "more than one") {
		t.Errorf("an ambiguous result carried note %q; the ambiguity has to reach the agent", amb.Note)
	}
}

func TestRereadMyGoal_RefusesWhenTheCallerIsUnknown(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	_, err := goalServer(src).toolRereadMyGoal(context.Background())
	if err == nil {
		t.Fatal("toolRereadMyGoal answered a call with no caller identity")
	}
	if src.gotAgent != "" {
		t.Errorf("the store was queried for %q despite an unknown caller", src.gotAgent)
	}
}

// TestGoalTools_RefuseWhenTheEventLogIsOff covers both tools and both ways the
// store can be off: never attached, and attached but disabled. A `s.goals !=
// nil` gate passes the second case, because a typed-nil *store.Ledger in an
// interface is not a nil interface.
func TestGoalTools_RefuseWhenTheEventLogIsOff(t *testing.T) {
	args := json.RawMessage(fmt.Sprintf(`{"workflow_instance_id":%q}`, uuid.New()))

	for _, tc := range []struct {
		name string
		src  GoalSource
	}{
		{"never attached", nil},
		{"attached but disabled", &fakeGoalSource{enabled: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := New(nil)
			if tc.src != nil {
				s.WithGoals(tc.src)
			}
			if _, err := s.toolRereadMyGoal(callerCtx("finn")); err == nil {
				t.Error("reread_my_goal answered with the event log off")
			} else if !strings.Contains(err.Error(), "event_log.enabled") {
				t.Errorf("the error should name the config key so the agent can tell this from an empty result; got: %v", err)
			}
			if _, err := s.toolGetWorkflowLog(callerCtx("finn"), args); err == nil {
				t.Error("get_workflow_log answered with the event log off")
			}
		})
	}

	// Control, aimed the other way: with the store enabled, the same calls
	// succeed. Without this the gate could be refusing unconditionally.
	s := goalServer(&fakeGoalSource{enabled: true})
	if _, err := s.toolRereadMyGoal(callerCtx("finn")); err != nil {
		t.Errorf("reread_my_goal refused an ENABLED store: %v", err)
	}
	if _, err := s.toolGetWorkflowLog(callerCtx("finn"), args); err != nil {
		t.Errorf("get_workflow_log refused an ENABLED store: %v", err)
	}
}

// TestGetWorkflowLog_PagesAndReportsTruncation.
//
// `truncated` is the assertion that matters. An agent that reads one full page
// and stops concludes the goal has no further history — the one wrong answer
// this tool can give that is indistinguishable from a right one.
func TestGetWorkflowLog_PagesAndReportsTruncation(t *testing.T) {
	wf := uuid.New()
	mk := func(seq int64, name string) store.DispatchedEvent {
		return store.DispatchedEvent{
			Seq: seq, ID: uuid.New(), SchemaName: name, SchemaVersion: 1,
			At: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), Payload: json.RawMessage(`{"k":"v"}`),
		}
	}
	src := &fakeGoalSource{enabled: true, events: []store.DispatchedEvent{mk(7, "goal_opened"), mk(9, "goal_poke")}}

	out, err := goalServer(src).toolGetWorkflowLog(callerCtx("finn"),
		json.RawMessage(fmt.Sprintf(`{"workflow_instance_id":%q,"after_seq":4,"limit":2}`, wf)))
	if err != nil {
		t.Fatalf("toolGetWorkflowLog: %v", err)
	}
	if src.gotWorkflow != wf || src.gotAfterSeq != 4 || src.gotLimit != 2 {
		t.Errorf("the store was asked for (%s, after %d, limit %d), want (%s, after 4, limit 2)",
			src.gotWorkflow, src.gotAfterSeq, src.gotLimit, wf)
	}

	var got workflowLogResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Count != 2 || len(got.Events) != 2 {
		t.Fatalf("got count=%d with %d events, want 2 and 2", got.Count, len(got.Events))
	}
	if got.Events[0].Type != "goal_opened@1" {
		t.Errorf("type rendered as %q, want goal_opened@1 — the version is what tells an agent which shape it is reading", got.Events[0].Type)
	}
	if got.NextSeq != 9 {
		t.Errorf("next_seq came back %d, want 9 (the LAST event's seq); anything else re-reads or skips", got.NextSeq)
	}
	if !got.Truncated {
		t.Error("a page that came back exactly full reported truncated=false, so an agent stops reading here")
	}

	// The other half: a short page is NOT truncated. Without it, `truncated`
	// could be hard-coded true and this test would still pass.
	src.events = src.events[:1]
	out, err = goalServer(src).toolGetWorkflowLog(callerCtx("finn"),
		json.RawMessage(fmt.Sprintf(`{"workflow_instance_id":%q,"limit":2}`, wf)))
	if err != nil {
		t.Fatalf("toolGetWorkflowLog(short page): %v", err)
	}
	var short workflowLogResult
	if err := json.Unmarshal([]byte(out), &short); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if short.Truncated {
		t.Error("a partial page reported truncated=true, which sends the agent round a pointless extra read forever")
	}
}

// TestGetWorkflowLog_ClampsTheLimit. The default and the ceiling are what stop
// an unbounded goal history from being pulled into one turn.
func TestGetWorkflowLog_ClampsTheLimit(t *testing.T) {
	wf := uuid.New()
	for _, tc := range []struct {
		name string
		arg  string
		want int
	}{
		{"absent", "", workflowLogDefaultLimit},
		{"zero", `,"limit":0`, workflowLogDefaultLimit},
		{"negative", `,"limit":-5`, workflowLogDefaultLimit},
		{"over the ceiling", `,"limit":100000`, workflowLogMaxLimit},
		{"honoured when sane", `,"limit":7`, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeGoalSource{enabled: true}
			args := json.RawMessage(fmt.Sprintf(`{"workflow_instance_id":%q%s}`, wf, tc.arg))
			if _, err := goalServer(src).toolGetWorkflowLog(callerCtx("finn"), args); err != nil {
				t.Fatalf("toolGetWorkflowLog: %v", err)
			}
			if src.gotLimit != tc.want {
				t.Errorf("limit reached the store as %d, want %d", src.gotLimit, tc.want)
			}
		})
	}
}

func TestGetWorkflowLog_RefusesAMalformedWorkflowID(t *testing.T) {
	src := &fakeGoalSource{enabled: true}
	_, err := goalServer(src).toolGetWorkflowLog(callerCtx("finn"), json.RawMessage(`{"workflow_instance_id":"not-a-uuid"}`))
	if err == nil {
		t.Fatal("get_workflow_log accepted a non-uuid workflow_instance_id")
	}
	if src.gotWorkflow != uuid.Nil {
		t.Error("the store was queried despite an unparseable id")
	}
}

// TestGoalTools_DispatchWiring — a handler nothing routes to is dead code, and
// every unit test above would still pass.
func TestGoalTools_DispatchWiring(t *testing.T) {
	s := goalServer(&fakeGoalSource{enabled: true})
	for _, name := range []string{"reread_my_goal", "get_workflow_log", "report_result"} {
		_, err := s.dispatchTool(callerCtx("finn"), name, json.RawMessage(fmt.Sprintf(`{"workflow_instance_id":%q}`, uuid.New())))
		var ute *unknownToolError
		if err != nil && errors.As(err, &ute) {
			t.Errorf("dispatchTool has no case for %q", name)
		}
	}
}

// TestGoalTools_Registered — a tool claude is never told about is unreachable
// however well it is wired.
func TestGoalTools_Registered(t *testing.T) {
	have := map[string]bool{}
	for _, def := range baseToolDefinitions() {
		have[def["name"].(string)] = true
	}
	for _, name := range []string{"reread_my_goal", "get_workflow_log", "report_result"} {
		if !have[name] {
			t.Errorf("%q is missing from baseToolDefinitions()", name)
		}
	}
}
