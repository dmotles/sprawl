package sprawlmcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func askQArgs(recipient string, questions []string, goal, followUp string) json.RawMessage {
	m := map[string]any{"recipient": recipient, "questions": questions}
	if goal != "" {
		m["goal_event_id"] = goal
	}
	if followUp != "" {
		m["follow_up_of"] = followUp
	}
	b, _ := json.Marshal(m)
	return b
}

// TestAskQuestions_RecordsTheAskAgainstTheCaller. The load-bearing assertion is
// askedQAsker: a tool that let the caller name its own asker would let one agent
// file a question in another's name, and the answer would be delivered to the
// wrong place with nothing in the log saying so.
func TestAskQuestions_RecordsTheAskAgainstTheCaller(t *testing.T) {
	id := uuid.New()
	src := &fakeGoalSource{enabled: true, askQID: id}

	out, err := goalServer(src).toolAskQuestions(callerCtx("weave"), askQArgs("finn", []string{"ship or wait?", "which host?"}, "", ""))
	if err != nil {
		t.Fatalf("toolAskQuestions: %v", err)
	}
	if src.askQCalls != 1 {
		t.Fatalf("AskQuestions reached the store %d times, want 1", src.askQCalls)
	}
	if src.askedQAsker != "weave" {
		t.Errorf("the store recorded the asker as %q, want weave — it comes from the session", src.askedQAsker)
	}
	if src.askedQRecipient != "finn" {
		t.Errorf("the store recorded the recipient as %q", src.askedQRecipient)
	}
	// Order is the contract: answers come back positionally, so a tool that
	// reordered or deduplicated the list would silently mismatch every answer.
	if len(src.askedQuestions) != 2 || src.askedQuestions[0] != "ship or wait?" || src.askedQuestions[1] != "which host?" {
		t.Errorf("the questions reached the store as %q, want both in the order asked", src.askedQuestions)
	}
	if src.askedQGoal != nil || src.askedQFollowUp != nil {
		t.Errorf("no goal or follow-up was named, but the store got goal=%v follow_up=%v", src.askedQGoal, src.askedQFollowUp)
	}

	var got askQuestionsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.AskEventID != id.String() {
		t.Errorf("ask_event_id came back %q, want the id the store minted (%s)", got.AskEventID, id)
	}
	if got.Count != 2 {
		t.Errorf("question_count came back %d, want 2", got.Count)
	}
	// The note is what stops an agent sitting on a contract nothing will poke
	// it about, what names the peer it is waiting on, and — added after a
	// mutation of this sentence survived — that answers come back BY POSITION,
	// which is the one thing about the reply that is not self-evident when it
	// arrives.
	for _, want := range []string{"Do not block", "finn", "positionally"} {
		if !strings.Contains(got.Note, want) {
			t.Errorf("the result note should mention %q; got note=%q", want, got.Note)
		}
	}
}

// TestAskQuestions_PassesTheOptionalIDsThroughAndRejectsMalformedOnes. Both ids
// change where the contract lands or which thread it joins, so dropping a
// malformed one would report success while filing the ask somewhere nobody is
// watching.
func TestAskQuestions_PassesTheOptionalIDsThroughAndRejectsMalformedOnes(t *testing.T) {
	goal, earlier := uuid.New(), uuid.New()
	src := &fakeGoalSource{enabled: true, askQID: uuid.New()}
	if _, err := goalServer(src).toolAskQuestions(callerCtx("weave"),
		askQArgs("finn", []string{"q"}, goal.String(), earlier.String())); err != nil {
		t.Fatalf("toolAskQuestions with both ids: %v", err)
	}
	if src.askedQGoal == nil || *src.askedQGoal != goal {
		t.Errorf("goal_event_id reached the store as %v, want %s", src.askedQGoal, goal)
	}
	if src.askedQFollowUp == nil || *src.askedQFollowUp != earlier {
		t.Errorf("follow_up_of reached the store as %v, want %s", src.askedQFollowUp, earlier)
	}

	for _, tc := range []struct{ name, goal, followUp, want string }{
		{"malformed goal", "not-a-uuid", "", "goal_event_id"},
		{"malformed follow-up", "", "not-a-uuid", "follow_up_of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := &fakeGoalSource{enabled: true, askQID: uuid.New()}
			_, err := goalServer(bad).toolAskQuestions(callerCtx("weave"),
				askQArgs("finn", []string{"q"}, tc.goal, tc.followUp))
			if err == nil {
				t.Fatal("a malformed id was accepted")
			}
			// Named specifically: with two uuid arguments, "not a uuid" alone
			// leaves the agent to guess which of them it got wrong.
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "not a uuid") {
				t.Errorf("the error should name %s and say it is not a uuid; got: %v", tc.want, err)
			}
			if bad.askQCalls != 0 {
				t.Errorf("the store was written to despite the refusal (%d calls)", bad.askQCalls)
			}
		})
	}
}

// TestAskQuestions_RefusalsNeverReachTheStore.
func TestAskQuestions_RefusalsNeverReachTheStore(t *testing.T) {
	for _, tc := range []struct {
		name, caller string
		args         json.RawMessage
		enabled      bool
		wantErr      string
	}{
		{"unnamed caller", "", askQArgs("finn", []string{"q"}, "", ""), true, "cannot tell which agent is asking"},
		{"malformed arguments", "weave", json.RawMessage(`{`), true, "invalid arguments"},
		{"event log off", "weave", askQArgs("finn", []string{"q"}, "", ""), false, "event_log.enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeGoalSource{enabled: tc.enabled, askQID: uuid.New()}
			_, err := goalServer(src).toolAskQuestions(callerCtx(tc.caller), tc.args)
			if err == nil {
				t.Fatal("ask_questions accepted the call")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q; got: %v", tc.wantErr, err)
			}
			if src.askQCalls != 0 {
				t.Errorf("the store recorded an ask %d time(s) despite the refusal", src.askQCalls)
			}
		})
	}

	// Paired control: a well-formed ask from a named caller DOES reach the
	// store. Without it every case above is satisfied by a tool that refuses
	// everything.
	src := &fakeGoalSource{enabled: true, askQID: uuid.New()}
	if _, err := goalServer(src).toolAskQuestions(callerCtx("weave"), askQArgs("finn", []string{"q"}, "", "")); err != nil {
		t.Fatalf("a well-formed ask was refused: %v", err)
	}
	if src.askQCalls != 1 {
		t.Errorf("a valid ask reached the store %d times, want exactly 1", src.askQCalls)
	}
}

// TestAskQuestions_SurfacesTheStoresRefusal — emptiness, a self-addressed ask
// and goal ownership are all decided in the store, so the tool's job is to not
// swallow the reason. Deliberately re-checked here rather than duplicated in the
// tool: two gates meant to be identical eventually differ.
func TestAskQuestions_SurfacesTheStoresRefusal(t *testing.T) {
	src := &fakeGoalSource{enabled: true, askQErr: storeRefusal("store: \"weave\" cannot ask itself a question")}
	_, err := goalServer(src).toolAskQuestions(callerCtx("weave"), askQArgs("weave", []string{"q"}, "", ""))
	if err == nil {
		t.Fatal("ask_questions reported success over a store refusal")
	}
	if !strings.Contains(err.Error(), "cannot ask itself") {
		t.Errorf("the store's reason was lost; got: %v", err)
	}
}

func answerArgs(ask string, answers []string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"ask_event_id": ask, "answers": answers})
	return b
}

// TestAnswerQuestions_RecordsTheAnswerAgainstTheCaller. answeredBy is what makes
// "only the recipient may answer" checkable in the store: a tool that let the
// caller name the answerer would let any agent answer in the recipient's name.
func TestAnswerQuestions_RecordsTheAnswerAgainstTheCaller(t *testing.T) {
	ask, closeID := uuid.New(), uuid.New()
	src := &fakeGoalSource{enabled: true, answerCloseID: closeID}

	out, err := goalServer(src).toolAnswerQuestions(callerCtx("finn"), answerArgs(ask.String(), []string{"ship it", "us-east"}))
	if err != nil {
		t.Fatalf("toolAnswerQuestions: %v", err)
	}
	if src.answerCalls != 1 {
		t.Fatalf("AnswerQuestions reached the store %d times, want 1", src.answerCalls)
	}
	if src.answeredBy != "finn" {
		t.Errorf("the store recorded the answerer as %q, want finn — it comes from the session", src.answeredBy)
	}
	if src.answeredAsk != ask {
		t.Errorf("the store was asked to close %s, want the ask named in the arguments (%s)", src.answeredAsk, ask)
	}
	if len(src.answers) != 2 || src.answers[0] != "ship it" || src.answers[1] != "us-east" {
		t.Errorf("the answers reached the store as %q, want both in order", src.answers)
	}

	var got answerQuestionsOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.CloseEvent != closeID.String() {
		t.Errorf("close_event_id came back %q, want %s", got.CloseEvent, closeID)
	}
	if !got.Irreversible {
		t.Error("the result must say the close is irreversible; the log is monotone")
	}
	if !strings.Contains(got.Note, "follow_up_of") {
		t.Errorf("the note must say how to correct an answer; got: %q", got.Note)
	}
}

// TestAnswerQuestions_RefusalsNeverReachTheStore. Same reasoning as
// report_result's equivalent: validating after the append would still return an
// error, and the close would still be final.
func TestAnswerQuestions_RefusalsNeverReachTheStore(t *testing.T) {
	ask := uuid.New()
	for _, tc := range []struct {
		name, caller string
		args         json.RawMessage
		enabled      bool
		wantErr      string
	}{
		{"unnamed caller", "", answerArgs(ask.String(), []string{"yes"}), true, "cannot tell which agent is answering"},
		{"malformed ask id", "finn", answerArgs("not-a-uuid", []string{"yes"}), true, "not a uuid"},
		{"malformed arguments", "finn", json.RawMessage(`{`), true, "invalid arguments"},
		{"event log off", "finn", answerArgs(ask.String(), []string{"yes"}), false, "event_log.enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeGoalSource{enabled: tc.enabled, answerCloseID: uuid.New()}
			_, err := goalServer(src).toolAnswerQuestions(callerCtx(tc.caller), tc.args)
			if err == nil {
				t.Fatal("answer_questions accepted the call")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q; got: %v", tc.wantErr, err)
			}
			if src.answerCalls != 0 {
				t.Errorf("the store closed a contract %d time(s) despite the refusal", src.answerCalls)
			}
		})
	}

	// Paired control.
	src := &fakeGoalSource{enabled: true, answerCloseID: uuid.New()}
	if _, err := goalServer(src).toolAnswerQuestions(callerCtx("finn"), answerArgs(ask.String(), []string{"yes"})); err != nil {
		t.Fatalf("a well-formed answer was refused: %v", err)
	}
	if src.answerCalls != 1 {
		t.Errorf("a valid answer reached the store %d times, want exactly 1", src.answerCalls)
	}
}

// TestAnswerQuestions_SurfacesTheStoresRefusal — arity and "is this addressed to
// you?" are both decided against the open set in the store.
func TestAnswerQuestions_SurfacesTheStoresRefusal(t *testing.T) {
	src := &fakeGoalSource{enabled: true, answerErr: storeRefusal("store: asks 2 question(s) but 1 answer(s) were given")}
	_, err := goalServer(src).toolAnswerQuestions(callerCtx("finn"), answerArgs(uuid.New().String(), []string{"yes"}))
	if err == nil {
		t.Fatal("answer_questions reported success over a store refusal")
	}
	if !strings.Contains(err.Error(), "asks 2 question(s)") {
		t.Errorf("the store's reason was lost; got: %v", err)
	}
}

// TestQuestionTools_AreAdvertisedAndDistinctFromMessages. Both tools are
// unreachable to a model that is not told they exist, and the description is the
// only place the ask-vs-send_message choice is stated — an agent that reaches
// for send_message when it is blocked gets no contract and no sweeper.
func TestQuestionTools_AreAdvertisedAndDistinctFromMessages(t *testing.T) {
	desc := map[string]string{}
	for _, def := range baseToolDefinitions() {
		switch def["name"] {
		case "ask_questions", "answer_questions":
			desc[def["name"].(string)], _ = def["description"].(string)
		}
	}
	if desc["ask_questions"] == "" || desc["answer_questions"] == "" {
		t.Fatalf("both tools must be advertised; got ask_questions=%d chars, answer_questions=%d chars",
			len(desc["ask_questions"]), len(desc["answer_questions"]))
	}
	if !strings.Contains(desc["ask_questions"], "send_message") {
		t.Error("ask_questions' description must name the tool it is NOT, or the model will pick by coin flip")
	}
	for _, want := range []string{"positionally", "Do NOT block"} {
		if !strings.Contains(desc["ask_questions"], want) {
			t.Errorf("ask_questions' description should mention %q; got: %s", want, desc["ask_questions"])
		}
	}
	for _, want := range []string{"IRREVERSIBLE", "addressed to YOU", "position"} {
		if !strings.Contains(desc["answer_questions"], want) {
			t.Errorf("answer_questions' description should mention %q; got: %s", want, desc["answer_questions"])
		}
	}
}
