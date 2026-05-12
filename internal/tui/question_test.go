package tui

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/supervisor"
)

// containsEllipsis reports whether s contains the unicode ellipsis glyph or
// the three-dot ASCII fallback. Either is acceptable as a truncation marker.
func containsEllipsis(s string) bool {
	return strings.Contains(s, "…") || strings.Contains(s, "...")
}

// tabStripLine returns the rendered tab strip for the model. The spec
// originally described this as "line index 1 of View()", but View() centers
// the box via lipgloss.Place so the strip is not at a fixed line index in
// the full output. Instead we call renderTabStrip() directly (same package),
// which is what View() embeds verbatim into the box body. The `out` arg is
// retained as a contextual hint when the lookup fails.
func tabStripLine(t *testing.T, m QuestionModel, out string) string {
	t.Helper()
	strip := m.renderTabStrip()
	if strip == "" {
		t.Fatalf("renderTabStrip() returned empty; modal output was:\n%s", out)
	}
	// The strip must be a single line — overflow handling truncates rather
	// than wrapping. If a newline sneaks in, the contract is broken.
	if strings.Contains(strip, "\n") {
		t.Fatalf("renderTabStrip() must be a single line (no \\n); got:\n%q", strip)
	}
	return strip
}

// makeNQuestions builds n single-option questions with IDs q1..qN and
// prompts p1?..pN? for tab-strip overflow tests.
func makeNQuestions(n int) []supervisor.Question {
	qs := make([]supervisor.Question, n)
	for i := 0; i < n; i++ {
		qs[i] = supervisor.Question{
			ID:      fmt.Sprintf("q%d", i+1),
			Prompt:  fmt.Sprintf("p%d?", i+1),
			Options: []supervisor.QOption{{Label: "A"}},
		}
	}
	return qs
}

// containsAnyMarker reports whether s contains any of the supplied candidate
// substrings. Used by tab-strip tests where the implementer is free to choose
// the answered-marker glyph.
func containsAnyMarker(s string, candidates ...string) bool {
	for _, c := range candidates {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

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

// TestQuestionModel_CursorNavigation_DownDownUpUp drives the documented
// QUM-536 acceptance sequence (Down, Down, Up, Up) through the modal and
// asserts the cursor index after every step, plus equivalence with the
// j/k vim aliases and the clamp behaviour at both boundaries. The bug is
// at the AppModel routing layer (TestAppModel_KeyUp_RoutesToQuestion below),
// so this test is expected to pass on main — it stands as a regression
// guard against any future change to the modal-level handler.
func TestQuestionModel_CursorNavigation_DownDownUpUp(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", supervisor.Question{
		ID: "q1", Prompt: "?",
		Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}},
	})
	m = m.Install(pq).Show()

	if m.cursors[m.qIdx] != 0 {
		t.Fatalf("cursor at start = %d, want 0", m.cursors[m.qIdx])
	}

	steps := []struct {
		key  tea.KeyPressMsg
		want int
	}{
		{tea.KeyPressMsg{Code: tea.KeyDown}, 1},
		{tea.KeyPressMsg{Code: tea.KeyDown}, 2},
		{tea.KeyPressMsg{Code: tea.KeyUp}, 1},
		{tea.KeyPressMsg{Code: tea.KeyUp}, 0},
		// Clamp at top.
		{tea.KeyPressMsg{Code: tea.KeyUp}, 0},
		// vim aliases march the cursor symmetrically.
		{tea.KeyPressMsg{Code: 'j'}, 1},
		{tea.KeyPressMsg{Code: 'j'}, 2},
		{tea.KeyPressMsg{Code: 'j'}, 3},
		// Clamp at bottom.
		{tea.KeyPressMsg{Code: 'j'}, 3},
		{tea.KeyPressMsg{Code: 'k'}, 2},
	}
	for i, step := range steps {
		var cmd tea.Cmd
		m, cmd = m.Update(step.key)
		if cmd != nil {
			t.Errorf("step %d (%v): expected nil cmd, got %v", i, step.key, cmd)
		}
		if got := m.cursors[m.qIdx]; got != step.want {
			t.Errorf("step %d (%v): cursor = %d, want %d", i, step.key, got, step.want)
		}
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

// TestQuestionModel_RightLeft_NavigatesQIdx — QUM-538: KeyRight advances qIdx
// forward, KeyLeft moves it back. Asserts navigation by substring-matching the
// distinct Prompt rendered in View() at each step.
func TestQuestionModel_RightLeft_NavigatesQIdx(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "first?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "second?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q3", Prompt: "third?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	if !strings.Contains(m.View(), "first?") {
		t.Fatalf("initial View() should render q1 prompt; got:\n%s", m.View())
	}

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd != nil {
		t.Errorf("KeyRight step 1: expected nil cmd, got %v", cmd)
	}
	if !strings.Contains(m.View(), "second?") {
		t.Errorf("after 1x KeyRight: View() missing q2 prompt; got:\n%s", m.View())
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if cmd != nil {
		t.Errorf("KeyRight step 2: expected nil cmd, got %v", cmd)
	}
	if !strings.Contains(m.View(), "third?") {
		t.Errorf("after 2x KeyRight: View() missing q3 prompt; got:\n%s", m.View())
	}

	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Errorf("KeyLeft step 1: expected nil cmd, got %v", cmd)
	}
	if !strings.Contains(m.View(), "second?") {
		t.Errorf("after 1x KeyLeft: View() missing q2 prompt; got:\n%s", m.View())
	}
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Errorf("KeyLeft step 2: expected nil cmd, got %v", cmd)
	}
	if !strings.Contains(m.View(), "first?") {
		t.Errorf("after 2x KeyLeft: View() missing q1 prompt; got:\n%s", m.View())
	}
}

// TestQuestionModel_RightLeft_ClampsAtBoundaries — QUM-538: KeyLeft at qIdx=0
// is a no-op; KeyRight past the last question clamps. Verified by View().
func TestQuestionModel_RightLeft_ClampsAtBoundaries(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "first?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "second?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if !strings.Contains(m.View(), "first?") {
		t.Errorf("KeyLeft at qIdx=0 should stay on q1; View():\n%s", m.View())
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !strings.Contains(m.View(), "second?") {
		t.Errorf("KeyRight past last should clamp at q2; View():\n%s", m.View())
	}
	if strings.Contains(m.View(), "first?") {
		t.Errorf("after clamping at q2, q1 prompt should not be in main body; View():\n%s", m.View())
	}
}

// TestQuestionModel_Navigation_PreservesOptionCursorDraft — QUM-538: the
// per-question option-cursor must survive a navigate-away + navigate-back
// round-trip. q1's cursor is parked at index 2 (C), we navigate away to q2 and
// back, then Enter — committed answer must be [C].
func TestQuestionModel_Navigation_PreservesOptionCursorDraft(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "q1?", Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}}},
		supervisor.Question{ID: "q2", Prompt: "q2?", Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}}},
		supervisor.Question{ID: "q3", Prompt: "q3?", Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}, {Label: "D"}}},
	)
	m = m.Install(pq).Show()

	// Park q1 cursor on C.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})

	// Navigate q1 → q2 → q1.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})

	// Commit q1 — cursor draft survived → answer = [C].
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// We should now be on q2 (advance by enter). q2 cursor at 0 → press down
	// once to land on B, Enter.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	// q3: cursor at 0 → A; Enter submits.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answered := expectAnsweredMsg(t, cmd)
	if got := len(answered.Response.Answers); got != 3 {
		t.Fatalf("len(Answers) = %d, want 3", got)
	}
	if got := answered.Response.Answers[0].Selected; len(got) != 1 || got[0] != "C" {
		t.Errorf("Answers[0].Selected = %v, want [C] (cursor draft must survive nav)", got)
	}
	if got := answered.Response.Answers[1].Selected; len(got) != 1 || got[0] != "B" {
		t.Errorf("Answers[1].Selected = %v, want [B]", got)
	}
	if got := answered.Response.Answers[2].Selected; len(got) != 1 || got[0] != "A" {
		t.Errorf("Answers[2].Selected = %v, want [A]", got)
	}
}

// TestQuestionModel_Navigation_PreservesMultiSelectDraft — QUM-538: a
// MultiSelect draft (set of picks) must survive a navigate-away + back.
func TestQuestionModel_Navigation_PreservesMultiSelectDraft(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{
			ID: "q1", Prompt: "q1?", MultiSelect: true,
			Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		},
		supervisor.Question{
			ID: "q2", Prompt: "q2?", MultiSelect: true,
			Options: []supervisor.QOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		},
	)
	m = m.Install(pq).Show()

	// q1: Space (pick A), Down, Space (pick B).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})

	// Round-trip nav.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})

	// Commit q1.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// q2: commit empty pick set.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	answered := expectAnsweredMsg(t, cmd)
	got := append([]string(nil), answered.Response.Answers[0].Selected...)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Errorf("Answers[0].Selected = %v, want set {A,B} (multi draft must survive nav)", answered.Response.Answers[0].Selected)
	}
	if got := len(answered.Response.Answers[1].Selected); got != 0 {
		t.Errorf("Answers[1].Selected len = %d, want 0", got)
	}
}

// TestQuestionModel_Navigation_PreservesTextDraft — QUM-538: a partially-typed
// custom-text buffer and the text mode itself must survive a nav round-trip.
func TestQuestionModel_Navigation_PreservesTextDraft(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "q1?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "q2?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()

	// q1: enter text mode, type "abc".
	m, _ = m.Update(tea.KeyPressMsg{Code: 'o'})
	for _, r := range "abc" {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	// Nav round-trip — note KeyRight in text mode might be ambiguous, but the
	// spec says Right/Left navigate questions outside text mode. Going via
	// Esc would discard draft; instead we test that nav from text mode works
	// (implementer may need to special-case). To keep this test focused on
	// draft preservation, we accept either: (a) text mode survives nav, or
	// (b) nav is ignored in text mode entirely. We assert (a) here — the
	// strongest interpretation of the spec.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})

	// Commit q1 — if text-mode + buffer survived, CustomText == "abc".
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// q2: commit default (cursor over A).
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answered := expectAnsweredMsg(t, cmd)
	if got := answered.Response.Answers[0].CustomText; got != "abc" {
		t.Errorf("Answers[0].CustomText = %q, want %q (text draft must survive nav)", got, "abc")
	}
}

// TestQuestionModel_EnterJumpsToNextUnanswered — QUM-538: Enter commits the
// current question and advances to the next *unanswered* question (not
// strictly qIdx+1). When all are answered, submits.
func TestQuestionModel_EnterJumpsToNextUnanswered(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "first?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "second?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q3", Prompt: "third?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	// Navigate to q3 without committing.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})

	// Enter on q3 → commits q3=A; next-unanswered hunt jumps to q1 (qIdx=0).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.View(), "first?") {
		t.Errorf("after Enter on q3 (q1+q2 unanswered): View should show q1 prompt; got:\n%s", m.View())
	}

	// 'd' declines q1 → counts as answered → jump to next unanswered (q2).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'd'})
	if !strings.Contains(m.View(), "second?") {
		t.Errorf("after 'd' on q1 (only q2 unanswered): View should show q2 prompt; got:\n%s", m.View())
	}

	// Enter on q2 → all answered → submit.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answered := expectAnsweredMsg(t, cmd)
	if answered.Response.Outcome != supervisor.OutcomeAnswered {
		t.Errorf("Outcome = %q, want %q", answered.Response.Outcome, supervisor.OutcomeAnswered)
	}
	if got := len(answered.Response.Answers); got != 3 {
		t.Fatalf("len(Answers) = %d, want 3", got)
	}
	if !answered.Response.Answers[0].Declined {
		t.Error("Answers[0].Declined = false, want true (q1 was declined)")
	}
	if got := answered.Response.Answers[1].Selected; len(got) != 1 || got[0] != "A" {
		t.Errorf("Answers[1].Selected = %v, want [A]", got)
	}
	if got := answered.Response.Answers[2].Selected; len(got) != 1 || got[0] != "A" {
		t.Errorf("Answers[2].Selected = %v, want [A]", got)
	}
}

// TestQuestionModel_TabStrip_RendersAnsweredMarkers — QUM-538: View() must
// render a tab strip indicating answered vs. unanswered questions. We accept
// any one of several candidate marker glyphs (`[*]`, `●`, `✓`) — the
// implementer picks. The strip must also contain the question numbers.
func TestQuestionModel_TabStrip_RendersAnsweredMarkers(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "q1?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "q2?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	out := m.View()
	if !strings.Contains(out, "1") || !strings.Contains(out, "2") {
		t.Errorf("View() must mention question numbers 1 and 2; got:\n%s", out)
	}
	// Before any commit, no answered-marker should be present.
	if containsAnyMarker(out, "[*]", "●", "✓", "[x]") {
		t.Errorf("pre-commit View() must NOT contain an answered marker; got:\n%s", out)
	}

	// Commit q1 — advance hunts to q2 (the only remaining unanswered).
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	out = m.View()
	// Look for any answered-marker sentinel.
	if !containsAnyMarker(out, "[*]", "●", "✓", "[x]") {
		t.Errorf("after committing q1, View() must contain an 'answered' marker glyph (one of [*], ●, ✓, [x]); got:\n%s", out)
	}
}

// TestQuestionModel_View_HelpLine_DocumentsArrowNav — QUM-538: the help line
// must document ←/→ as navigate-question keys.
func TestQuestionModel_View_HelpLine_DocumentsArrowNav(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "q1?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "q2?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	out := m.View()
	if !strings.Contains(out, "←") || !strings.Contains(out, "→") {
		t.Errorf("View() help line must contain ← and → glyphs; got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "navigate") {
		t.Errorf("View() help line must contain 'navigate' (case-insensitive); got:\n%s", out)
	}
}

// TestQuestionModel_LeftRight_IgnoredInTextMode — QUM-538: while in custom-text
// mode, KeyLeft/KeyRight must be consumed by the textinput (cursor movement)
// and must NOT bounce qIdx between questions mid-typing.
func TestQuestionModel_LeftRight_IgnoredInTextMode(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave",
		supervisor.Question{ID: "q1", Prompt: "first?", Options: []supervisor.QOption{{Label: "A"}}},
		supervisor.Question{ID: "q2", Prompt: "second?", Options: []supervisor.QOption{{Label: "A"}}},
	)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	// q1: enter text mode, type 'x'.
	m, _ = m.Update(tea.KeyPressMsg{Code: 'o'})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

	// Left/Right while in text mode must not change qIdx — View still on q1.
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if !strings.Contains(m.View(), "first?") {
		t.Errorf("Left/Right in text mode must not change qIdx; expected q1 prompt; got:\n%s", m.View())
	}
	if strings.Contains(m.View(), "second?") {
		t.Errorf("Left/Right in text mode must not navigate to q2; View:\n%s", m.View())
	}

	// Enter → commits q1 with CustomText containing "x".
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Enter on q2 → submits.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	answered := expectAnsweredMsg(t, cmd)
	if got := answered.Response.Answers[0].CustomText; !strings.Contains(got, "x") {
		t.Errorf("Answers[0].CustomText = %q, want to contain %q", got, "x")
	}
}

// TestQuestionModel_TabStrip_OverflowKeepsCurrentVisible — QUM-541: with 25
// questions and qIdx=12, the rendered tab strip must fit within the inner
// modal width budget (60 cols), keep the active tab "13" visible, show an
// ellipsis marker, and trim at least one end (the "25" tab when truncated
// on the right, or "1[" near the start when truncated on the left).
func TestQuestionModel_TabStrip_OverflowKeepsCurrentVisible(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", makeNQuestions(25)...)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	for i := 0; i < 12; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}

	out := m.View()
	strip := tabStripLine(t, m, out)

	if w := lipgloss.Width(strip); w > 60 {
		t.Errorf("tab strip width = %d, want <= 60; strip:\n%s", w, strip)
	}
	if !strings.Contains(strip, "13") {
		t.Errorf("tab strip must contain active tab label %q; strip:\n%s", "13", strip)
	}
	if !containsEllipsis(strip) {
		t.Errorf("tab strip must contain ellipsis marker when truncated; strip:\n%s", strip)
	}
	// Windowing must trim at least one end. With qIdx=12 in a 25-question
	// strip, both edges should be trimmed; assert at least one is gone.
	hasRightEdge := strings.Contains(strip, "25")
	hasLeftEdge := strings.Contains(strip, " 1[") || strings.HasPrefix(strings.TrimLeft(strip, " "), "1[")
	if hasRightEdge && hasLeftEdge {
		t.Errorf("tab strip with qIdx=12 should trim at least one end (both 25 and leading 1[ present); strip:\n%s", strip)
	}
}

// TestQuestionModel_TabStrip_OverflowAtLeftBoundary — QUM-541: with qIdx=0 and
// 25 questions, the strip fits, contains "1", contains an ellipsis on the
// truncated right side, and does NOT contain "25". Strip must not start with
// an ellipsis (no leading truncation when active tab is at the left edge).
func TestQuestionModel_TabStrip_OverflowAtLeftBoundary(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", makeNQuestions(25)...)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	out := m.View()
	strip := tabStripLine(t, m, out)

	if w := lipgloss.Width(strip); w > 60 {
		t.Errorf("tab strip width = %d, want <= 60; strip:\n%s", w, strip)
	}
	if !strings.Contains(strip, "1") {
		t.Errorf("tab strip must contain tab label %q at left edge; strip:\n%s", "1", strip)
	}
	if !containsEllipsis(strip) {
		t.Errorf("tab strip must contain ellipsis marker on right when truncated; strip:\n%s", strip)
	}
	if strings.Contains(strip, "25") {
		t.Errorf("tab strip with qIdx=0 should not show right-edge tab %q; strip:\n%s", "25", strip)
	}
	trimmed := strings.TrimLeft(strip, " ")
	if strings.HasPrefix(trimmed, "…") || strings.HasPrefix(trimmed, "...") {
		t.Errorf("tab strip at left boundary must not begin with ellipsis; strip:\n%s", strip)
	}
}

// TestQuestionModel_TabStrip_OverflowAtRightBoundary — QUM-541: with qIdx=24
// (last) the strip fits, contains "25", contains ellipsis on the truncated
// left side, does NOT contain a leading "1[" segment, and does not end with
// a trailing ellipsis after the "25" segment.
func TestQuestionModel_TabStrip_OverflowAtRightBoundary(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", makeNQuestions(25)...)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	for i := 0; i < 24; i++ {
		m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	}

	out := m.View()
	strip := tabStripLine(t, m, out)

	if w := lipgloss.Width(strip); w > 60 {
		t.Errorf("tab strip width = %d, want <= 60; strip:\n%s", w, strip)
	}
	if !strings.Contains(strip, "25") {
		t.Errorf("tab strip must contain active tab label %q; strip:\n%s", "25", strip)
	}
	if !containsEllipsis(strip) {
		t.Errorf("tab strip must contain ellipsis marker when left-truncated; strip:\n%s", strip)
	}
	// At the right boundary, the leading "1[" segment must have been trimmed.
	head := strings.TrimLeft(strip, " ")
	if strings.HasPrefix(head, "1[") {
		t.Errorf("tab strip at right boundary must not start with %q; strip:\n%s", "1[", strip)
	}
	// And the strip must not end with an ellipsis after the active tab.
	tail := strings.TrimRight(strip, " ")
	if strings.HasSuffix(tail, "…") || strings.HasSuffix(tail, "...") {
		t.Errorf("tab strip at right boundary must not end with ellipsis after %q; strip:\n%s", "25", strip)
	}
}

// TestQuestionModel_TabStrip_NoOverflowWhenFits — QUM-541: with only 3
// questions the strip fits trivially; no ellipsis truncation marker should
// appear. Regression guard so the common case isn't decorated.
func TestQuestionModel_TabStrip_NoOverflowWhenFits(t *testing.T) {
	m := newTestQuestionModel(t)
	pq := mkPending("r1", "weave", makeNQuestions(3)...)
	m = m.Install(pq).Show()
	m.SetSize(80, 24)

	out := m.View()
	strip := tabStripLine(t, m, out)

	if containsEllipsis(strip) {
		t.Errorf("tab strip with 3 questions must not contain an ellipsis marker; strip:\n%s", strip)
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
