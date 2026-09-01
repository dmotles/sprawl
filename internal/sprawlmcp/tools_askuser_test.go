package sprawlmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// askUserServer wires a GoalSource onto a server whose supervisor reports the
// given roster. The roster matters: ask_user reuses ask_user_question's
// eligibility gate, which reads it.
func askUserServer(src GoalSource, agents ...supervisor.AgentInfo) *Server {
	return New(&mockSupervisor{statusResult: agents}).WithGoals(src)
}

func askUserArgs(question, context, goal string) json.RawMessage {
	m := map[string]any{"question": question}
	if context != "" {
		m["context"] = context
	}
	if goal != "" {
		m["goal_event_id"] = goal
	}
	b, _ := json.Marshal(m)
	return b
}

// TestAskUser_RecordsTheQuestionAgainstTheCaller. The load-bearing assertion is
// askedAsker: the asker is the session's identity, so the human always knows who
// to answer.
func TestAskUser_RecordsTheQuestionAgainstTheCaller(t *testing.T) {
	id := uuid.New()
	src := &fakeGoalSource{enabled: true, askID: id}
	srv := askUserServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"})

	out, err := srv.toolAskUser(callerCtx("boss"), askUserArgs("ship or wait?", "the branch is green", ""))
	if err != nil {
		t.Fatalf("toolAskUser: %v", err)
	}
	if src.askCalls != 1 {
		t.Fatalf("AskUser reached the store %d times, want 1", src.askCalls)
	}
	if src.askedAsker != "boss" {
		t.Errorf("the store recorded the asker as %q, want boss — it comes from the session", src.askedAsker)
	}
	if src.askedQuestion != "ship or wait?" || src.askedContext != "the branch is green" {
		t.Errorf("question/context reached the store as (%q, %q)", src.askedQuestion, src.askedContext)
	}
	if src.askedGoal != nil {
		t.Errorf("no goal was named, but the store was given %v", src.askedGoal)
	}

	var got askUserOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.QuestionEventID != id.String() {
		t.Errorf("question_event_id came back %q, want the id the store minted (%s)", got.QuestionEventID, id)
	}
	// The note is the only thing standing between this tool and an agent that
	// sits waiting for an answer nothing will ever deliver to it.
	if !strings.Contains(got.Note, "do not block") {
		t.Errorf("the result must tell the agent not to block; got note=%q", got.Note)
	}
}

// TestAskUser_PassesAGoalIDThroughAndRejectsAMalformedOne — the goal id is
// optional, so the two interesting cases are "absent" (covered above) and
// "present but not a uuid", which must be refused rather than dropped: silently
// dropping it would file the question on an instance nobody is watching while
// reporting success.
func TestAskUser_PassesAGoalIDThroughAndRejectsAMalformedOne(t *testing.T) {
	goal := uuid.New()
	src := &fakeGoalSource{enabled: true, askID: uuid.New()}
	srv := askUserServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"})

	if _, err := srv.toolAskUser(callerCtx("boss"), askUserArgs("q", "", goal.String())); err != nil {
		t.Fatalf("toolAskUser with a goal id: %v", err)
	}
	if src.askedGoal == nil || *src.askedGoal != goal {
		t.Errorf("the goal id reached the store as %v, want %s", src.askedGoal, goal)
	}

	bad := &fakeGoalSource{enabled: true, askID: uuid.New()}
	badSrv := askUserServer(bad, supervisor.AgentInfo{Name: "boss", Type: "manager"})
	_, err := badSrv.toolAskUser(callerCtx("boss"), askUserArgs("q", "", "not-a-uuid"))
	if err == nil {
		t.Fatal("a malformed goal_event_id was accepted")
	}
	if !strings.Contains(err.Error(), "not a uuid") {
		t.Errorf("error should say the id is not a uuid; got: %v", err)
	}
	if bad.askCalls != 0 {
		t.Errorf("the store was written to despite the refusal (%d calls)", bad.askCalls)
	}
}

// TestAskUser_RefusalsNeverReachTheStore. Same reasoning as report_result's
// equivalent: a tool that validated after appending would still return an error
// and the question would still be in the human's inbox.
func TestAskUser_RefusalsNeverReachTheStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		caller  string
		roster  []supervisor.AgentInfo
		args    json.RawMessage
		wantErr string
	}{
		{
			"ineligible caller", "ratz",
			[]supervisor.AgentInfo{{Name: "ratz", Type: "engineer"}},
			askUserArgs("q", "", ""), "restricted to weave and managers",
		},
		{
			"empty question", "boss",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			askUserArgs("", "", ""), "needs a question",
		},
		{
			"malformed arguments", "boss",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			json.RawMessage(`{`), "invalid arguments",
		},
		{
			// An unnamed caller with no root agent registered. Refused rather
			// than attributed to a literal, because a name in the log that no
			// agent record backs is worse than no question at all.
			"unnamed caller and no root agent", "",
			[]supervisor.AgentInfo{{Name: "boss", Type: "manager"}},
			askUserArgs("q", "", ""), "cannot tell which agent is asking",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeGoalSource{enabled: true, askID: uuid.New()}
			_, err := askUserServer(src, tc.roster...).toolAskUser(callerCtx(tc.caller), tc.args)
			if err == nil {
				t.Fatal("ask_user accepted the call")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q; got: %v", tc.wantErr, err)
			}
			if src.askCalls != 0 {
				t.Errorf("the store recorded a question %d time(s) despite the refusal", src.askCalls)
			}
		})
	}

	// Paired control: a well-formed call from an eligible caller DOES reach the
	// store. Without it every case above is satisfied by a tool that refuses
	// everything.
	src := &fakeGoalSource{enabled: true, askID: uuid.New()}
	if _, err := askUserServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"}).
		toolAskUser(callerCtx("boss"), askUserArgs("q", "", "")); err != nil {
		t.Fatalf("a well-formed ask_user was refused: %v", err)
	}
	if src.askCalls != 1 {
		t.Errorf("a valid call reached the store %d times, want exactly 1", src.askCalls)
	}
}

// TestAskUser_UnnamedCallerIsAttributedToTheRootAgent. An empty caller identity
// IS the root weave session, which is the tool's most likely caller — so it
// cannot be refused the way report_result refuses it. The name is read from the
// supervisor rather than invented.
func TestAskUser_UnnamedCallerIsAttributedToTheRootAgent(t *testing.T) {
	src := &fakeGoalSource{enabled: true, askID: uuid.New()}
	srv := askUserServer(src,
		supervisor.AgentInfo{Name: "boss", Type: "manager"},
		supervisor.AgentInfo{Name: "weave", Type: "root"},
	)

	if _, err := srv.toolAskUser(callerCtx(""), askUserArgs("q", "", "")); err != nil {
		t.Fatalf("toolAskUser as root: %v", err)
	}
	if src.askedAsker != "weave" {
		t.Errorf("the root's question was attributed to %q, want the registered root name (weave)", src.askedAsker)
	}
}

func TestAskUser_RefusedWhenTheEventLogIsOff(t *testing.T) {
	src := &fakeGoalSource{enabled: false}
	_, err := askUserServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"}).
		toolAskUser(callerCtx("boss"), askUserArgs("q", "", ""))
	if err == nil {
		t.Fatal("ask_user recorded a question with the event log off")
	}
	if !strings.Contains(err.Error(), "event_log.enabled") {
		t.Errorf("the error should name the config key; got: %v", err)
	}
	if src.askCalls != 0 {
		t.Error("the store was written to with the event log off")
	}
}

// TestAskUser_SurfacesTheStoresRefusal — the "is this goal yours?" check lives
// in the store, so the tool's job is to not swallow it.
func TestAskUser_SurfacesTheStoresRefusal(t *testing.T) {
	src := &fakeGoalSource{enabled: true, askErr: errStoreRefused}
	_, err := askUserServer(src, supervisor.AgentInfo{Name: "boss", Type: "manager"}).
		toolAskUser(callerCtx("boss"), askUserArgs("q", "", uuid.New().String()))
	if err == nil {
		t.Fatal("ask_user reported success over a store refusal")
	}
	if !strings.Contains(err.Error(), "not an open goal") {
		t.Errorf("the store's reason was lost; got: %v", err)
	}
}

// TestAskUser_IsDistinctFromAskUserQuestion. Two tools that interrupt the human
// in different ways are only useful if the model can tell them apart, and the
// descriptions are the only place that distinction is stated.
func TestAskUser_IsDistinctFromAskUserQuestion(t *testing.T) {
	var askUserDesc, askQuestionDesc string
	for _, def := range baseToolDefinitions() {
		switch def["name"] {
		case "ask_user":
			askUserDesc, _ = def["description"].(string)
		case "ask_user_question":
			askQuestionDesc, _ = def["description"].(string)
		}
	}
	if askUserDesc == "" || askQuestionDesc == "" {
		t.Fatalf("both tools must be advertised; got ask_user=%d chars, ask_user_question=%d chars", len(askUserDesc), len(askQuestionDesc))
	}
	if !strings.Contains(askUserDesc, "ask_user_question") {
		t.Error("ask_user's description must name the tool it is NOT, or the model will pick by coin flip")
	}
	for _, want := range []string{"sprawl inbox", "do not block", "estricted to weave and managers"} {
		if !strings.Contains(askUserDesc, want) {
			t.Errorf("ask_user's description should mention %q; got: %s", want, askUserDesc)
		}
	}
}

// errStoreRefused stands in for the store's ownership refusal.
var errStoreRefused = storeRefusal("store: 4f8c is not an open goal owned by \"boss\"")

type storeRefusal string

func (e storeRefusal) Error() string { return string(e) }
