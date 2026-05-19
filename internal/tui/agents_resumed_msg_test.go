package tui

import (
	"strings"
	"testing"
)

// QUM-372: when runEnter finishes its best-effort startup scan and resumes
// (or fails to resume) suspended child agents, the TUI must render a short
// viewport banner in the root viewport summarizing the counts.

func TestAppModel_AgentsResumedMsg_AppendsBanner_NoFailures(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 3, Failed: 0})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "[startup] resumed 3 agents") {
		t.Errorf("viewport should contain startup banner; got:\n%s", view)
	}
	if strings.Contains(view, "failed") {
		t.Errorf("viewport should not mention failures when Failed==0; got:\n%s", view)
	}
}

func TestAppModel_AgentsResumedMsg_AppendsBanner_WithFailures(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 3, Failed: 1})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if !strings.Contains(view, "[startup] resumed 3 agents (1 failed)") {
		t.Errorf("viewport should contain startup banner with failure count; got:\n%s", view)
	}
}

func TestAppModel_AgentsResumedMsg_ZeroCounts_NoBanner(t *testing.T) {
	app := newTestAppModel(t)

	updated, _ := app.Update(AgentsResumedMsg{Resumed: 0, Failed: 0})
	app = updated.(AppModel)

	view := stripAnsi(app.viewportFor("weave").View())
	if strings.Contains(view, "[startup]") {
		t.Errorf("viewport should NOT contain a startup banner with zero counts; got:\n%s", view)
	}
}
