package tui

import (
	"strings"
	"testing"
)

// QUM-372 / QUM-675 S5: when runEnter finishes its best-effort startup scan
// and resumes (or fails to resume) suspended child agents, the TUI must
// surface a short summary line via the status-bar transient label — NOT as
// a viewport banner. Pre-S5 this was an AppendStatus call; S5 reroutes it
// per tower's display-policy spec on QUM-675 ("[startup] resumed N agents"
// row).

func TestAppModel_AgentsResumedMsg_TransientLabel_NoFailures(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 3, Failed: 0})
	app = updated.(AppModel)

	bar := stripAnsi(app.statusBar.View())
	if !strings.Contains(bar, "[startup] resumed 3 agents") {
		t.Errorf("status bar should contain startup label; got:\n%s", bar)
	}
	if strings.Contains(bar, "failed") {
		t.Errorf("status bar should not mention failures when Failed==0; got:\n%s", bar)
	}
	// Status/Banner/Error never enter ChatList post QUM-693 — viewport-bleed
	// negative assertion is structurally vacuous and was deleted.
}

func TestAppModel_AgentsResumedMsg_TransientLabel_WithFailures(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 3, Failed: 1})
	app = updated.(AppModel)

	bar := stripAnsi(app.statusBar.View())
	if !strings.Contains(bar, "[startup] resumed 3 agents (1 failed)") {
		t.Errorf("status bar should contain startup label with failure count; got:\n%s", bar)
	}
}

func TestAppModel_AgentsResumedMsg_ZeroCounts_NoLabel(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 0, Failed: 0})
	app = updated.(AppModel)

	bar := stripAnsi(app.statusBar.View())
	if strings.Contains(bar, "[startup]") {
		t.Errorf("status bar should NOT contain a startup label with zero counts; got:\n%s", bar)
	}
}
