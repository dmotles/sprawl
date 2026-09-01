package cmd

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dmotles/sprawl/internal/store"
)

// fakeInbox stands in for *store.Ledger. The enabled/degraded flags are separate
// fields rather than derived, because the three states this command must tell
// apart — off, unreachable, and empty — are exactly what a single bool would
// collapse.
type fakeInbox struct {
	enabled    bool
	degraded   error
	questions  []store.UserQuestion
	listErr    error
	answerErr  error
	answered   uuid.UUID
	answerText string
	answeredBy string
	answers    int
}

func (f *fakeInbox) Enabled() bool        { return f.enabled }
func (f *fakeInbox) DegradedError() error { return f.degraded }

func (f *fakeInbox) OpenUserQuestions(context.Context) ([]store.UserQuestion, error) {
	return f.questions, f.listErr
}

func (f *fakeInbox) AnswerUserQuestion(_ context.Context, id uuid.UUID, answer, by string) (uuid.UUID, error) {
	f.answers++
	f.answered, f.answerText, f.answeredBy = id, answer, by
	return uuid.New(), f.answerErr
}

func newTestInboxDeps(t *testing.T, fake *fakeInbox) (*inboxDeps, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	return &inboxDeps{
		SprawlRoot: t.TempDir(),
		OpenInbox:  func(context.Context, string) (userInbox, error) { return fake, nil },
		Whoami:     func() string { return "dmotles" },
		Stdout:     out,
		Stderr:     &bytes.Buffer{},
	}, out
}

func aQuestion(asker, q string) store.UserQuestion {
	return store.UserQuestion{
		EventID:    uuid.New(),
		WorkflowID: uuid.New(),
		Asker:      asker,
		Question:   q,
		AskedAt:    time.Now().Add(-2 * time.Hour),
	}
}

func TestInboxList_ShowsWhoIsWaitingAndOnWhat(t *testing.T) {
	q := aQuestion("finn", "ship or wait?")
	q.Context = "the branch is green"
	deps, out := newTestInboxDeps(t, &fakeInbox{enabled: true, questions: []store.UserQuestion{q}})

	if err := runInboxList(context.Background(), deps); err != nil {
		t.Fatalf("runInboxList: %v", err)
	}
	got := out.String()
	for _, want := range []string{"finn", "ship or wait?", "the branch is green", q.EventID.String()} {
		if !strings.Contains(got, want) {
			t.Errorf("the listing omits %q, so it cannot be acted on:\n%s", want, got)
		}
	}
	// The id is what `sprawl inbox answer` takes, so the listing must say how to
	// use it. A list of questions with no visible next step is a dead end.
	if !strings.Contains(got, "sprawl inbox answer") {
		t.Errorf("the listing never tells the reader how to answer:\n%s", got)
	}
}

// TestInboxList_EmptyIsNotTheSameAsOff — the three states this command must
// never conflate. An operator who reads "nothing is waiting" on a host where the
// store is switched off has been told their agents are unblocked when nobody is
// even recording the questions.
func TestInboxList_EmptyIsNotTheSameAsOff(t *testing.T) {
	empty, out := newTestInboxDeps(t, &fakeInbox{enabled: true})
	if err := runInboxList(context.Background(), empty); err != nil {
		t.Fatalf("an empty inbox is not an error: %v", err)
	}
	if !strings.Contains(out.String(), "no questions") {
		t.Errorf("an empty inbox should say so plainly; got: %s", out.String())
	}

	off, _ := newTestInboxDeps(t, &fakeInbox{enabled: false})
	err := runInboxList(context.Background(), off)
	if err == nil {
		t.Fatal("a disabled event log reported an inbox")
	}
	if !strings.Contains(err.Error(), "event_log.enabled") {
		t.Errorf("the refusal must name the config key; got: %v", err)
	}

	down, _ := newTestInboxDeps(t, &fakeInbox{enabled: true, degraded: errors.New("connection refused")})
	err = runInboxList(context.Background(), down)
	if err == nil {
		t.Fatal("a degraded store reported an inbox")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("the refusal should say the store is unreachable; got: %v", err)
	}
	// The three refusals must be distinguishable from each other, not merely
	// present: that is the whole point of separating them.
	if strings.Contains(err.Error(), "event_log.enabled") {
		t.Errorf("the degraded refusal reads like the disabled one: %v", err)
	}
}

func TestInboxList_RequiresSprawlRoot(t *testing.T) {
	deps, _ := newTestInboxDeps(t, &fakeInbox{enabled: true})
	deps.SprawlRoot = ""
	err := runInboxList(context.Background(), deps)
	if err == nil {
		t.Fatal("the inbox was read with no SPRAWL_ROOT, so it answered about the wrong project")
	}
	if !strings.Contains(err.Error(), "SPRAWL_ROOT") {
		t.Errorf("error should name SPRAWL_ROOT; got: %v", err)
	}
}

func TestInboxAnswer_RecordsTheAnswerAndWhoGaveIt(t *testing.T) {
	q := aQuestion("finn", "ship or wait?")
	fake := &fakeInbox{enabled: true, questions: []store.UserQuestion{q}}
	deps, out := newTestInboxDeps(t, fake)

	if err := runInboxAnswer(context.Background(), deps, q.EventID.String(), "ship it"); err != nil {
		t.Fatalf("runInboxAnswer: %v", err)
	}
	if fake.answers != 1 {
		t.Fatalf("the store took %d answers, want 1", fake.answers)
	}
	if fake.answered != q.EventID {
		t.Errorf("answered %s, want the question named on the command line (%s)", fake.answered, q.EventID)
	}
	if fake.answerText != "ship it" {
		t.Errorf("the answer reached the store as %q", fake.answerText)
	}
	// Attribution comes from the machine, not from a flag: an answer nobody can
	// trace is the state the store layer already refuses.
	if fake.answeredBy != "dmotles" {
		t.Errorf("the answer was attributed to %q, want the invoking user", fake.answeredBy)
	}
	if !strings.Contains(out.String(), "finn") {
		t.Errorf("the confirmation should name the agent now unblocked; got: %s", out.String())
	}
}

// TestInboxAnswer_RefusalsNeverReachTheStore. Same reasoning as the tools: a
// command that validated after the append would still print an error and the
// answer would still be in the log, which is monotone.
func TestInboxAnswer_RefusalsNeverReachTheStore(t *testing.T) {
	q := aQuestion("finn", "ship or wait?")
	for _, tc := range []struct {
		name    string
		id      string
		answer  string
		off     bool
		wantErr string
	}{
		{"malformed id", "not-a-uuid", "ship it", false, "not a uuid"},
		{"empty answer", q.EventID.String(), "", false, "empty"},
		{"event log off", q.EventID.String(), "ship it", true, "event_log.enabled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeInbox{enabled: !tc.off, questions: []store.UserQuestion{q}}
			deps, _ := newTestInboxDeps(t, fake)
			err := runInboxAnswer(context.Background(), deps, tc.id, tc.answer)
			if err == nil {
				t.Fatal("the answer was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q; got: %v", tc.wantErr, err)
			}
			if fake.answers != 0 {
				t.Errorf("the store recorded %d answer(s) despite the refusal", fake.answers)
			}
		})
	}

	// Paired control: a well-formed answer DOES reach the store. Without it,
	// every case above is satisfied by a command that refuses everything.
	fake := &fakeInbox{enabled: true, questions: []store.UserQuestion{q}}
	deps, _ := newTestInboxDeps(t, fake)
	if err := runInboxAnswer(context.Background(), deps, q.EventID.String(), "ship it"); err != nil {
		t.Fatalf("a well-formed answer was refused: %v", err)
	}
	if fake.answers != 1 {
		t.Errorf("a valid answer reached the store %d times, want 1", fake.answers)
	}
}

// TestInboxAnswer_SurfacesTheStoresRefusal — "already answered" is decided in
// the store against the open set, so this command's job is to not swallow it and
// to say what to do next.
func TestInboxAnswer_SurfacesTheStoresRefusal(t *testing.T) {
	q := aQuestion("finn", "ship or wait?")
	fake := &fakeInbox{
		enabled:   true,
		questions: []store.UserQuestion{q},
		answerErr: errors.New("store: " + q.EventID.String() + " is not an open user question — it may already be answered; re-read `sprawl inbox`"),
	}
	deps, _ := newTestInboxDeps(t, fake)
	err := runInboxAnswer(context.Background(), deps, q.EventID.String(), "ship it")
	if err == nil {
		t.Fatal("the command reported success over a store refusal")
	}
	// Asserting on the STORE's own distinctive wording, not a paraphrase: this
	// case is the race in which the question was open when we listed it and gone
	// by the time we appended, so the store's message is the only accurate
	// account of what happened and must survive the wrap.
	if !strings.Contains(err.Error(), "not an open user question") {
		t.Errorf("the store's reason was lost; got: %v", err)
	}
}

// TestInboxHelpSaysWhatItIsNot. `inbox` is already the word this codebase uses
// for the agent maildir, and the two have nothing to do with each other. A
// reader who conflates them will go looking for agent messages here.
func TestInboxHelpSaysWhatItIsNot(t *testing.T) {
	long := inboxCmd.Long
	if !strings.Contains(long, "ask_user") {
		t.Errorf("the help should name the tool that fills this inbox; got: %s", long)
	}
	if !strings.Contains(long, "messages") {
		t.Errorf("the help must distinguish this from agent messages; got: %s", long)
	}
}

// TestInboxHelpCitesOnlyRealCommands. Caught by hand first: the help originally
// pointed at `sprawl messages`, which does not exist — there is no CLI surface
// for agent mail at all. A help text that sends someone to a command that is not
// there is worse than one that says nothing, and no amount of prose review
// reliably catches it, so it is checked mechanically.
func TestInboxHelpCitesOnlyRealCommands(t *testing.T) {
	cited := regexp.MustCompile("`sprawl ([a-z-]+)").FindAllStringSubmatch(
		inboxCmd.Long+"\n"+inboxAnswerCmd.Long, -1)
	if len(cited) == 0 {
		t.Fatal("no `sprawl <cmd>` citations found — the extraction is broken, not the help")
	}
	for _, m := range cited {
		name := m[1]
		var found bool
		for _, c := range rootCmd.Commands() {
			if c.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the help sends the reader to `sprawl %s`, which is not a command", name)
		}
	}
}
