package tui

// QUM-451: view-render cache invariance tests.
//
// These tests are written against an *anticipated* implementation.
// The Test Writer (TDD red phase) makes the following assumptions
// about field/method names the implementer is expected to provide on
// AppModel:
//
//   m.cache              — a struct field with bordered, ready-to-join
//                          per-panel render strings.
//   m.cache.tree         — bordered tree panel render
//   m.cache.viewport     — bordered viewport panel render
//   m.cache.activity     — bordered activity panel render (may be "")
//   m.cache.input        — bordered input panel render
//   m.cache.status       — status bar render
//
//   m.viewUncached()     — same package helper that produces the
//                          uncached equivalent of m.View(); used as
//                          the byte-equivalence oracle. Returns the
//                          same string content View() would produce.
//
// If the implementer chooses different names they should update these
// tests to match — the *behavioural* assertions are the contract, the
// names are just the access path.

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// resizedApp returns an AppModel that has received a WindowSizeMsg
// (so ready=true and resizePanels has fired) at the given dimensions.
func resizedApp(t *testing.T, w, h int) AppModel {
	t.Helper()
	m := NewAppModel("colour212", "testrepo", "v0.1.0", "v0.1.0", nil, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return updated.(AppModel)
}

func TestViewCache_OutputUnchanged_AfterInputKeystroke_TreeAndViewportBytesIdentical(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.activePanel = PanelInput
	app.updateFocus()
	_ = app.View()

	treeBefore := app.cache.tree
	vpBefore := app.cache.viewport
	statusBefore := app.cache.status
	activityBefore := app.cache.activity

	next, _ := app.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	app = next.(AppModel)
	_ = app.View()

	if app.cache.tree != treeBefore {
		t.Errorf("tree cache changed after input keystroke; only the input panel should be dirty")
	}
	if app.cache.viewport != vpBefore {
		t.Errorf("viewport cache changed after input keystroke; only the input panel should be dirty")
	}
	if app.cache.status != statusBefore {
		t.Errorf("status cache changed after input keystroke; only the input panel should be dirty")
	}
	if app.cache.activity != activityBefore {
		t.Errorf("activity cache changed after input keystroke; only the input panel should be dirty")
	}
}

func TestViewCache_InvalidatesOnTreeChange(t *testing.T) {
	app := resizedApp(t, 200, 60)
	_ = app.View()
	treeBefore := app.cache.tree

	app.childNodes = []TreeNode{
		{Name: "new-agent", Type: "engineer", Status: "active"},
	}
	app.rebuildTree()
	_ = app.View()

	if app.cache.tree == treeBefore {
		t.Errorf("tree cache did not invalidate after rebuildTree(); cache still equals pre-mutation render")
	}
}

func TestViewCache_InvalidatesOnViewportAppend(t *testing.T) {
	app := resizedApp(t, 200, 60)
	_ = app.View()
	vpBefore := app.cache.viewport

	app.rootVP().AppendStatus("hi")
	_ = app.View()

	if app.cache.viewport == vpBefore {
		t.Errorf("viewport cache did not invalidate after AppendStatus(); cache still equals pre-append render")
	}
}

func TestViewCache_InvalidatesOnActivePanelCycle(t *testing.T) {
	app := resizedApp(t, 200, 60)
	// Start at tree (default activePanel is PanelTree).
	_ = app.View()
	treeBefore := app.cache.tree
	vpBefore := app.cache.viewport

	// Tab → viewport active.
	next, _ := app.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	app = next.(AppModel)
	_ = app.View()

	if app.cache.tree == treeBefore {
		t.Errorf("tree cache did not invalidate after active panel cycle (border style changed from active to inactive)")
	}
	if app.cache.viewport == vpBefore {
		t.Errorf("viewport cache did not invalidate after active panel cycle (border style changed from inactive to active)")
	}
}

func TestViewCache_InvalidatesOnWindowResize(t *testing.T) {
	app := resizedApp(t, 200, 60)
	_ = app.View()
	treeBefore := app.cache.tree
	vpBefore := app.cache.viewport
	inputBefore := app.cache.input
	statusBefore := app.cache.status

	next, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	app = next.(AppModel)
	_ = app.View()

	if app.cache.tree == treeBefore {
		t.Errorf("tree cache did not invalidate after window resize")
	}
	if app.cache.viewport == vpBefore {
		t.Errorf("viewport cache did not invalidate after window resize")
	}
	if app.cache.input == inputBefore {
		t.Errorf("input cache did not invalidate after window resize")
	}
	if app.cache.status == statusBefore {
		t.Errorf("status cache did not invalidate after window resize")
	}
}

func TestViewCache_OutputEqualsUncached_AcrossKeystrokes(t *testing.T) {
	app := resizedApp(t, 200, 60)
	app.activePanel = PanelInput
	app.updateFocus()

	for i := 0; i < 30; i++ {
		ch := rune('a' + (i % 26))
		next, _ := app.Update(tea.KeyPressMsg{Code: ch, Text: string(ch)})
		app = next.(AppModel)
		got := app.View().Content
		want := app.viewUncached().Content
		if got != want {
			t.Fatalf("iteration %d: cached View().Content != viewUncached().Content\n--- cached ---\n%s\n--- uncached ---\n%s", i, got, want)
		}
	}
}
