package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// seedAppHistory installs the given entries on the app's history. It assumes
// the AppModel exposes a `history *History` field once QUM-410 is wired.
func seedAppHistory(t *testing.T, app *AppModel, entries []string) {
	t.Helper()
	if app.history == nil {
		app.history = NewHistory("")
		_ = app.history.Load()
	}
	for _, e := range entries {
		app.history.Append(e)
	}
}

// readyAppOnPanelInput returns an AppModel that has received a WindowSizeMsg
// and is ready for input-panel keybinds. (Post-QUM-695 the input is the
// sole keystroke recipient; the helper name is preserved for continuity.)
func readyAppOnPanelInput(t *testing.T) AppModel {
	t.Helper()
	m := newTestAppModel(t)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)
	app.updateFocus()
	return app
}

// TestInput_UpArrowNavigatesHistory: Up cycles back through history; Down
// cycles forward; final Down restores the live (pre-navigation) buffer.
func TestInput_UpArrowNavigatesHistory(t *testing.T) {
	app := readyAppOnPanelInput(t)
	seedAppHistory(t, &app, []string{"first", "second"})

	// Establish a "live" buffer that should be restored when Down passes the
	// front of history.
	app.input.SetValue("draft")

	// Up: newest entry "second"
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = updated.(AppModel)
	if got := app.input.Value(); got != "second" {
		t.Errorf("after Up #1: input = %q, want %q", got, "second")
	}

	// Up: older entry "first"
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = updated.(AppModel)
	if got := app.input.Value(); got != "first" {
		t.Errorf("after Up #2: input = %q, want %q", got, "first")
	}

	// Down: back to "second"
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = updated.(AppModel)
	if got := app.input.Value(); got != "second" {
		t.Errorf("after Down #1: input = %q, want %q", got, "second")
	}

	// Down: live buffer "draft" restored.
	updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	app = updated.(AppModel)
	if got := app.input.Value(); got != "draft" {
		t.Errorf("after Down #2 (live restore): input = %q, want %q", got, "draft")
	}
}

// TestSubmit_AppendsToHistory: a SubmitMsg appends to history and grows Len.
func TestSubmit_AppendsToHistory(t *testing.T) {
	app := readyAppOnPanelInput(t)
	if app.history == nil {
		app.history = NewHistory("")
		_ = app.history.Load()
	}
	startLen := app.history.Len()

	// Drive the submit path. Without a bridge, SubmitMsg would early-return,
	// but history is a UX concern and must be appended even in bridge=nil
	// test mode (per oracle plan). If implementer chooses to gate on bridge,
	// the test will fail meaningfully.
	updated, _ := app.Update(SubmitMsg{Text: "hello world"})
	app = updated.(AppModel)

	if app.history.Len() != startLen+1 {
		t.Fatalf("history.Len = %d, want %d", app.history.Len(), startLen+1)
	}
	if got := app.history.At(app.history.Len() - 1); got != "hello world" {
		t.Errorf("last history entry = %q, want %q", got, "hello world")
	}
}

// TestCtrlR_StateMachine: reverse-search interactions over multiple subtests.
func TestCtrlR_StateMachine(t *testing.T) {
	ctrlR := tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}

	t.Run("EmptyHistory_NoMatch", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, nil)
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		if !app.searchActive {
			t.Error("searchActive should be true after Ctrl+R")
		}
		if app.input.Value() != "" {
			t.Errorf("with empty history and no query, input value = %q, want \"\"", app.input.Value())
		}
	})

	t.Run("MatchNewestFirst", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, []string{"apple", "banana"})
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: 'a'})
		app = updated.(AppModel)
		// Newest with substring "a" is "banana".
		if got := app.input.Value(); got != "banana" {
			t.Errorf("after Ctrl+R + 'a': input = %q, want %q", got, "banana")
		}
	})

	t.Run("CycleToNextMatch", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, []string{"apple", "banana"})
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: 'a'})
		app = updated.(AppModel)
		updated, _ = app.Update(ctrlR)
		app = updated.(AppModel)
		if got := app.input.Value(); got != "apple" {
			t.Errorf("after second Ctrl+R: input = %q, want %q (cycled to older match)", got, "apple")
		}
	})

	t.Run("BackspaceShrinksQuery", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, []string{"apple", "banana"})
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: 'a'})
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: 'p'})
		app = updated.(AppModel)
		// Query is "ap" — only "apple" matches.
		if got := app.input.Value(); got != "apple" {
			t.Errorf("after typing 'ap': input = %q, want %q", got, "apple")
		}
		// Backspace shrinks to "a"; verify the query state retained the shorter form.
		updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		app = updated.(AppModel)
		if !strings.Contains(app.searchOverlay(), "a") {
			t.Errorf("after Backspace, overlay = %q, want to contain query \"a\"", app.searchOverlay())
		}
	})

	t.Run("EnterAcceptsMatch", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, []string{"apple", "banana"})
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: 'a'})
		app = updated.(AppModel)
		// Pressing Enter while searchActive accepts the match.
		updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		app = updated.(AppModel)
		if app.searchActive {
			t.Error("searchActive should be false after Enter")
		}
		if got := app.input.Value(); got != "banana" {
			t.Errorf("after Enter: input = %q, want match %q", got, "banana")
		}
	})

	t.Run("EscRestoresPreSearchValue", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, []string{"apple", "banana"})
		app.input.SetValue("draft")
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: 'a'})
		app = updated.(AppModel)
		updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		app = updated.(AppModel)
		if app.searchActive {
			t.Error("searchActive should be false after Esc")
		}
		if got := app.input.Value(); got != "draft" {
			t.Errorf("after Esc: input = %q, want pre-search %q", got, "draft")
		}
	})

	t.Run("CtrlCWhileSearchingCancelsSearchOnly", func(t *testing.T) {
		app := readyAppOnPanelInput(t)
		seedAppHistory(t, &app, []string{"apple", "banana"})
		app.input.SetValue("draft")
		updated, _ := app.Update(ctrlR)
		app = updated.(AppModel)
		updated, cmd := app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		app = updated.(AppModel)
		if app.searchActive {
			t.Error("Ctrl+C in search should clear searchActive")
		}
		if app.showConfirm {
			t.Error("Ctrl+C in search should NOT open quit-confirm dialog")
		}
		// And no Quit command should have been emitted.
		if cmd != nil {
			if msg := cmd(); msg != nil {
				if _, ok := msg.(tea.QuitMsg); ok {
					t.Error("Ctrl+C in search should not produce QuitMsg")
				}
			}
		}
	})
}

// TestInput_HistoryArrow_BlockedByEachModal is the QUM-537 regression guard:
// for each modal-flag in {showHelp, showConfirm, showError, showPalette,
// showQuestion}, when only that flag is set the input-panel history-arrow
// handler MUST NOT swallow Up/Down — the modal owns those keys. QUM-536 was
// the case where this handler missed showQuestion specifically and Up was
// asymmetrically swallowed by history.Prev. After QUM-537 the conjunction
// collapses to !m.anyModalUp(), so adding a future modal to that helper
// automatically extends this guard.
func TestInput_HistoryArrow_BlockedByEachModal(t *testing.T) {
	cases := []struct {
		name string
		set  func(*AppModel)
	}{
		{"showHelp", func(a *AppModel) { a.showHelp = true }},
		{"showConfirm", func(a *AppModel) { a.showConfirm = true }},
		{"showError", func(a *AppModel) { a.showError = true }},
		{"showPalette", func(a *AppModel) { a.showPalette = true }},
		{"showQuestion", func(a *AppModel) { a.showQuestion = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := readyAppOnPanelInput(t)
			seedAppHistory(t, &app, []string{"first", "second"})
			app.input.SetValue("draft")
			tc.set(&app)

			// Up must not be swallowed by the history-arrow handler. Note that
			// when showConfirm / showError / showPalette / showQuestion are
			// set, downstream modal routing in Update() will consume the key
			// (or no-op); none of those paths mutate input.Value(). showHelp
			// likewise swallows non-Esc keys. So the live "draft" value must
			// survive untouched — proof the history handler did not run.
			updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
			app = updated.(AppModel)
			if got := app.input.Value(); got != "draft" {
				t.Errorf("[%s] Up was swallowed by history handler: input = %q, want %q",
					tc.name, got, "draft")
			}

			updated, _ = app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
			app = updated.(AppModel)
			if got := app.input.Value(); got != "draft" {
				t.Errorf("[%s] Down was swallowed by history handler: input = %q, want %q",
					tc.name, got, "draft")
			}
		})
	}
}

// TestAppModel_anyModalUp pins the contract of the QUM-537 helper: it
// returns true when any of the five modal flags is set, false otherwise.
// Adding a new modal flag should extend this test alongside anyModalUp().
func TestAppModel_anyModalUp(t *testing.T) {
	app := readyAppOnPanelInput(t)
	if app.anyModalUp() {
		t.Fatalf("anyModalUp() with no flags set = true, want false")
	}
	cases := []struct {
		name string
		set  func(*AppModel)
	}{
		{"showHelp", func(a *AppModel) { a.showHelp = true }},
		{"showConfirm", func(a *AppModel) { a.showConfirm = true }},
		{"showError", func(a *AppModel) { a.showError = true }},
		{"showPalette", func(a *AppModel) { a.showPalette = true }},
		{"showQuestion", func(a *AppModel) { a.showQuestion = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := readyAppOnPanelInput(t)
			tc.set(&a)
			if !a.anyModalUp() {
				t.Errorf("anyModalUp() with only %s set = false, want true", tc.name)
			}
		})
	}
}

// TestMultilineUpDown_DoesNotHijackHistory: when the textarea contains
// multiple lines and the cursor isn't on the first line, Up should move the
// cursor within the textarea instead of loading history. Implementation must
// expose a helper such as input.AtFirstLine() to disambiguate.
func TestMultilineUpDown_DoesNotHijackHistory(t *testing.T) {
	app := readyAppOnPanelInput(t)
	seedAppHistory(t, &app, []string{"first", "second"})

	// Seed a multi-line value and place cursor at the end (line 2).
	app.input.SetValue("line1\nline2")

	// Up should move cursor to line 1, NOT load "second" from history.
	updated, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app = updated.(AppModel)
	if got := app.input.Value(); got != "line1\nline2" {
		t.Errorf("multi-line Up should not load history; value = %q, want unchanged", got)
	}
}
