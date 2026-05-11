package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// readyAppWithSup builds an AppModel wired to sup and runs the standard
// WindowSizeMsg so the model is in the post-resize "ready" state. Mirrors
// readyApp() in app_palette_test.go.
func readyAppWithSup(t *testing.T, sup *mockSupervisor) AppModel {
	t.Helper()
	m := newTestAppModelWithSupervisor(t, sup)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	return updated.(AppModel)
}

func samplePending(id, from string) *supervisor.PendingQuestion {
	return &supervisor.PendingQuestion{
		Req: supervisor.QuestionRequest{
			RequestID: id,
			From:      from,
			Questions: []supervisor.Question{{
				ID: "q1", Prompt: "?", Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}},
			}},
		},
		Seq: 1,
	}
}

func TestAppModel_QuestionsAvailable_ShowsModalAndUpdatesStatusBar(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	if !app.showQuestion {
		t.Error("showQuestion should be true after QuestionsAvailableMsg")
	}
	if !app.questionModel.HasPending() {
		t.Error("questionModel.HasPending() should be true after install")
	}
	view := app.statusBar.View()
	if !strings.Contains(view, "weave") {
		t.Errorf("status bar should contain 'weave', got:\n%s", view)
	}
	if !strings.Contains(view, "asking") && !strings.Contains(view, "Ctrl-Q") {
		t.Errorf("status bar should advertise the pending question (expect 'asking' or 'Ctrl-Q'), got:\n%s", view)
	}
}

func TestAppModel_QuestionsAvailable_DoesNotPreempt(t *testing.T) {
	cases := []struct {
		name string
		set  func(*AppModel)
	}{
		{"showError", func(a *AppModel) { a.showError = true }},
		{"showConfirm", func(a *AppModel) { a.showConfirm = true }},
		{"showHelp", func(a *AppModel) { a.showHelp = true }},
		{"showPalette", func(a *AppModel) { a.showPalette = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sup := &mockSupervisor{}
			app := readyAppWithSup(t, sup)
			tc.set(&app)

			pq := samplePending("r1", "weave")
			updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
			app = updated.(AppModel)

			if app.showQuestion {
				t.Errorf("showQuestion should NOT auto-flip while %s is up", tc.name)
			}
			if !app.questionModel.HasPending() {
				t.Errorf("questionModel should still be installed even if modal not auto-shown (%s)", tc.name)
			}
			view := app.statusBar.View()
			if !strings.Contains(view, "weave") {
				t.Errorf("status bar should still update with agent name (%s); got:\n%s", tc.name, view)
			}
		})
	}
}

func TestAppModel_CtrlQ_ReopensWhenPending(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	// User dismisses (Hide) but the request remains pending.
	updated, _ = app.Update(DismissQuestionMsg{})
	app = updated.(AppModel)
	if app.showQuestion {
		t.Fatal("setup: DismissQuestionMsg should hide the modal")
	}

	updated, _ = app.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if !app.showQuestion {
		t.Error("Ctrl-Q should re-show the modal when a question is pending")
	}
}

func TestAppModel_CtrlQ_NoOpWhenNoPending(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	updated, _ := app.Update(tea.KeyPressMsg{Code: 'q', Mod: tea.ModCtrl})
	app = updated.(AppModel)
	if app.showQuestion {
		t.Error("Ctrl-Q must be a no-op when no question is pending")
	}
}

func TestAppModel_DismissQuestionMsg_HidesPreservesDrafts(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	updated, _ = app.Update(DismissQuestionMsg{})
	app = updated.(AppModel)
	if app.showQuestion {
		t.Error("showQuestion should be false after DismissQuestionMsg")
	}
	if !app.questionModel.HasPending() {
		t.Error("questionModel.HasPending() should remain true after dismiss")
	}
}

func TestAppModel_QuestionAnswered_CallsResolveAndResets(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	// No further questions available after this resolution.
	resp := supervisor.QuestionResponse{
		RequestID: "r1",
		Outcome:   supervisor.OutcomeAnswered,
	}
	updated, _ = app.Update(QuestionAnsweredMsg{RequestID: "r1", Response: resp})
	app = updated.(AppModel)

	sup.qmu.Lock()
	calls := append([]resolveCall(nil), sup.resolveCalls...)
	sup.qmu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("ResolveQuestion called %d times, want 1", len(calls))
	}
	if calls[0].ID != "r1" {
		t.Errorf("ResolveQuestion id = %q, want r1", calls[0].ID)
	}
	if calls[0].Resp.Outcome != supervisor.OutcomeAnswered {
		t.Errorf("ResolveQuestion outcome = %q, want %q",
			calls[0].Resp.Outcome, supervisor.OutcomeAnswered)
	}
	if app.questionModel.HasPending() {
		t.Error("HasPending() should be false after answer")
	}
	if app.showQuestion {
		t.Error("showQuestion should be false after answer")
	}
}

func TestAppModel_QuestionAnswered_AutoAdvancesIfHeadAvailable(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	// Prime mock so PeekQuestions reveals a follow-up after the answer is
	// recorded.
	next := samplePending("r2", "tower")
	sup.qmu.Lock()
	sup.peekDepth = 1
	sup.peekHead = next
	sup.qmu.Unlock()

	updated, _ = app.Update(QuestionAnsweredMsg{
		RequestID: "r1",
		Response:  supervisor.QuestionResponse{RequestID: "r1", Outcome: supervisor.OutcomeAnswered},
	})
	app = updated.(AppModel)

	if !app.questionModel.HasPending() {
		t.Error("HasPending() should be true after auto-advance to next head")
	}
	if !app.showQuestion {
		t.Error("showQuestion should be true after auto-advance")
	}
}

func TestAppModel_CancelQuestionMsg_ClearsMatchingActive(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	updated, _ = app.Update(CancelQuestionMsg{RequestID: "r1", Reason: "retired"})
	app = updated.(AppModel)
	if app.questionModel.HasPending() {
		t.Error("HasPending() should be false after matching cancel")
	}

	// Re-install, then send cancel for a non-matching id — must NOT clear.
	updated, _ = app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)
	updated, _ = app.Update(CancelQuestionMsg{RequestID: "r-other", Reason: "x"})
	app = updated.(AppModel)
	if !app.questionModel.HasPending() {
		t.Error("HasPending() should remain true when cancel target doesn't match active")
	}
}

func TestAppModel_RestartComplete_RepollsAndShowsPending(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	sup.qmu.Lock()
	sup.peekDepth = 1
	sup.peekHead = pq
	sup.qmu.Unlock()

	updated, _ := app.Update(RestartCompleteMsg{Bridge: nil, Err: nil})
	app = updated.(AppModel)

	if !app.questionModel.HasPending() {
		t.Error("HasPending() should be true after RestartCompleteMsg re-poll")
	}
	if !app.showQuestion {
		t.Error("showQuestion should be true after RestartCompleteMsg re-poll")
	}
}

func TestAppModel_SessionRestarting_PreservesQuestion(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)

	updated, _ = app.Update(SessionRestartingMsg{Reason: "handoff"})
	app = updated.(AppModel)

	if !app.questionModel.HasPending() {
		t.Error("HasPending() should be preserved across SessionRestartingMsg")
	}
}

func TestAppModel_OpenPalette_GatedByShowQuestion(t *testing.T) {
	sup := &mockSupervisor{}
	app := readyAppWithSup(t, sup)

	pq := samplePending("r1", "weave")
	updated, _ := app.Update(QuestionsAvailableMsg{Depth: 1, Head: pq})
	app = updated.(AppModel)
	if !app.showQuestion {
		t.Fatal("setup: showQuestion must be true")
	}

	updated, _ = app.Update(OpenPaletteMsg{})
	app = updated.(AppModel)
	if app.showPalette {
		t.Error("OpenPaletteMsg should be a no-op while showQuestion is up")
	}
}
