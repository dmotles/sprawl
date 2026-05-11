package tui

import (
	"sort"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// newTestQuestionModel constructs a fresh QuestionModel with a default theme
// for unit testing.
func newTestQuestionModel(t *testing.T) QuestionModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewQuestionModel(&theme)
}

// mkPending builds a *supervisor.PendingQuestion with the supplied questions.
// requestID is reused as the From agent for convenience.
func mkPending(requestID, from string, qs ...supervisor.Question) *supervisor.PendingQuestion {
	return &supervisor.PendingQuestion{
		Req: supervisor.QuestionRequest{
			RequestID: requestID,
			From:      from,
			Questions: qs,
		},
		Seq: 1,
	}
}

// applyKeys threads a sequence of tea.KeyPressMsgs through the model.
// Returns the final model and the last cmd produced (cmds before the last
// are dropped — tests interested in mid-sequence cmds should call Update
// directly).
func applyKeys(t *testing.T, m QuestionModel, keys ...tea.KeyPressMsg) (QuestionModel, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = m.Update(k)
	}
	return m, cmd
}

// expectAnsweredMsg asserts cmd resolves to a QuestionAnsweredMsg and returns it.
func expectAnsweredMsg(t *testing.T, cmd tea.Cmd) QuestionAnsweredMsg {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected non-nil cmd carrying QuestionAnsweredMsg")
	}
	msg := cmd()
	answered, ok := msg.(QuestionAnsweredMsg)
	if !ok {
		t.Fatalf("expected QuestionAnsweredMsg, got %T (%+v)", msg, msg)
	}
	return answered
}

func TestQuestionModel_NotVisibleByDefault(t *testing.T) {
	m := newTestQuestionModel(t)
	if m.Visible() {
		t.Error("fresh QuestionModel.Visible() = true, want false")
	}
	if m.HasPending() {
		t.Error("fresh QuestionModel.HasPending() = true, want false")
	}
}

func TestQuestionModel_InstallShowsAndHasPending(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "pick", Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}},
	})
	m = m.Install(pq)
	m = m.Show()
	if !m.HasPending() {
		t.Error("HasPending() = false after Install, want true")
	}
	if !m.Visible() {
		t.Error("Visible() = false after Install+Show, want true")
	}
}

func TestQuestionModel_SingleSelect_EnterAdvances(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{
			ID: "q1", Prompt: "first", MultiSelect: false,
			Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}},
		},
		supervisor.Question{
			ID: "q2", Prompt: "second", MultiSelect: false,
			Options: []supervisor.QOption{{Label: "X"}, {Label: "Y"}},
		},
	)
	m = m.Install(pq).Show()

	// Move down then Enter to pick B on q1, advancing to q2.
	m, _ = applyKeys(t, m,
		tea.KeyPressMsg{Code: tea.KeyDown},
		tea.KeyPressMsg{Code: tea.KeyEnter},
	)
	if !m.HasPending() {
		t.Fatal("HasPending() should remain true after advancing to q2")
	}

	// Move down then Enter to pick Y on q2 — this is the last question,
	// so it must emit QuestionAnsweredMsg.
	var cmd tea.Cmd
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	answered := expectAnsweredMsg(t, cmd)
	if answered.RequestID != "r1" {
		t.Errorf("RequestID = %q, want r1", answered.RequestID)
	}
	if answered.Response.Outcome != supervisor.OutcomeAnswered {
		t.Errorf("Outcome = %q, want %q", answered.Response.Outcome, supervisor.OutcomeAnswered)
	}
	if got := len(answered.Response.Answers); got != 2 {
		t.Fatalf("len(Answers) = %d, want 2", got)
	}
	if got := answered.Response.Answers[0].QuestionID; got != "q1" {
		t.Errorf("Answers[0].QuestionID = %q, want q1", got)
	}
	if got := answered.Response.Answers[0].Selected; len(got) != 1 || got[0] != "B" {
		t.Errorf("Answers[0].Selected = %v, want [B]", got)
	}
	if got := answered.Response.Answers[1].Selected; len(got) != 1 || got[0] != "Y" {
		t.Errorf("Answers[1].Selected = %v, want [Y]", got)
	}
}

func TestQuestionModel_MultiSelect_SpaceToggles_EnterCommits(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "multi", MultiSelect: true,
		Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
	})
	m = m.Install(pq).Show()

	// Space selects A, ↓ moves to B, Space selects B, Enter commits.
	var cmd tea.Cmd
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	answered := expectAnsweredMsg(t, cmd)
	if got := len(answered.Response.Answers); got != 1 {
		t.Fatalf("len(Answers) = %d, want 1", got)
	}
	got := append([]string(nil), answered.Response.Answers[0].Selected...)
	sort.Strings(got)
	want := []string{"A", "B"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Selected = %v, want set {A,B}", answered.Response.Answers[0].Selected)
	}
}

func TestQuestionModel_OKey_EntersTextMode_EnterCommits(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}},
	})
	m = m.Install(pq).Show()

	// 'o' enters text mode; type "hello"; Enter commits.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'o'})
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	answered := expectAnsweredMsg(t, cmd)
	if got := len(answered.Response.Answers); got != 1 {
		t.Fatalf("len(Answers) = %d, want 1", got)
	}
	if got := answered.Response.Answers[0].CustomText; got != "hello" {
		t.Errorf("CustomText = %q, want %q", got, "hello")
	}
	if got := answered.Response.Answers[0].Selected; len(got) != 0 {
		t.Errorf("Selected = %v, want empty when custom text supplied", got)
	}
}

func TestQuestionModel_TextMode_EscReturnsToSelect_NoAdvance(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}},
	})
	m = m.Install(pq).Show()

	// Enter text mode, type some chars, then Esc — should NOT advance
	// and the text draft should be abandoned.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'o'})
	for _, r := range "abc" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !m.HasPending() {
		t.Fatal("Esc in text mode must not clear the active request")
	}

	// Pressing Enter now should select the cursor option (A) and commit,
	// with CustomText empty (Esc abandoned the typed text).
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answered := expectAnsweredMsg(t, cmd)
	if got := answered.Response.Answers[0].Selected; len(got) != 1 || got[0] != "A" {
		t.Errorf("Selected = %v, want [A]", got)
	}
	if got := answered.Response.Answers[0].CustomText; got != "" {
		t.Errorf("CustomText = %q, want empty (Esc should abandon draft)", got)
	}
}

func TestQuestionModel_DKey_PerQuestionDeclineAdvances(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "?", Options: []supervisor.QOption{{Label: "B"}}},
	)
	m = m.Install(pq).Show()

	// 'd' declines q1 and advances to q2. Enter on q2 picks B (cursor at 0).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	answered := expectAnsweredMsg(t, cmd)
	if answered.Response.Outcome != supervisor.OutcomeAnswered {
		t.Errorf("Outcome = %q, want %q (one question was answered)",
			answered.Response.Outcome, supervisor.OutcomeAnswered)
	}
	if got := len(answered.Response.Answers); got != 2 {
		t.Fatalf("len(Answers) = %d, want 2", got)
	}
	if !answered.Response.Answers[0].Declined {
		t.Error("Answers[0].Declined = false, want true (q1 was declined)")
	}
	if answered.Response.Answers[1].Declined {
		t.Error("Answers[1].Declined = true, want false (q2 was answered)")
	}
	if got := answered.Response.Answers[1].Selected; len(got) != 1 || got[0] != "B" {
		t.Errorf("Answers[1].Selected = %v, want [B]", got)
	}
}

func TestQuestionModel_DShiftKey_DeclineAllSubmits(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "?", Options: []supervisor.QOption{{Label: "B"}}},
	)
	m = m.Install(pq).Show()

	// 'D' from q1 declines the whole batch and submits.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'D'})
	answered := expectAnsweredMsg(t, cmd)
	if answered.Response.Outcome != supervisor.OutcomeDeclined {
		t.Errorf("Outcome = %q, want %q", answered.Response.Outcome, supervisor.OutcomeDeclined)
	}
	if got := len(answered.Response.Answers); got != 2 {
		t.Fatalf("len(Answers) = %d, want 2", got)
	}
	for i, ans := range answered.Response.Answers {
		if !ans.Declined {
			t.Errorf("Answers[%d].Declined = false, want true (D should decline all)", i)
		}
	}
}

func TestQuestionModel_AllPerQuestionDeclined_OutcomeIsDeclined(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "?", Options: []supervisor.QOption{{Label: "B"}}},
	)
	m = m.Install(pq).Show()

	m, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'd'})

	answered := expectAnsweredMsg(t, cmd)
	if answered.Response.Outcome != supervisor.OutcomeDeclined {
		t.Errorf("Outcome = %q, want %q when every question is per-question declined",
			answered.Response.Outcome, supervisor.OutcomeDeclined)
	}
}

func TestQuestionModel_Hide_PreservesDrafts(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "?", MultiSelect: true,
		Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}},
	})
	m = m.Install(pq).Show()

	// Pick A (multi-select).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})

	// Hide, then Show — visibility flips but pending + draft survive.
	m = m.Hide()
	if m.Visible() {
		t.Error("Visible() = true after Hide()")
	}
	if !m.HasPending() {
		t.Error("HasPending() = false after Hide() — drafts must be preserved")
	}
	m = m.Show()
	if !m.Visible() {
		t.Error("Visible() = false after Show()")
	}

	// Enter should commit with A still selected (proves the draft survived).
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answered := expectAnsweredMsg(t, cmd)
	if got := answered.Response.Answers[0].Selected; len(got) != 1 || got[0] != "A" {
		t.Errorf("Selected after Hide/Show round-trip = %v, want [A]", got)
	}
}

func TestQuestionModel_Reset_ClearsAll(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}},
	})
	m = m.Install(pq).Show()
	if !m.HasPending() {
		t.Fatal("setup: HasPending() should be true")
	}
	m = m.Reset()
	if m.HasPending() {
		t.Error("HasPending() = true after Reset(), want false")
	}
	if m.Visible() {
		t.Error("Visible() = true after Reset(), want false")
	}
}
