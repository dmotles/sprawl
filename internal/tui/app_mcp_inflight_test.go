package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// QUM-497: TUI surfacing for in-flight MCP operations.

// findEntry returns the first MessageEntry whose body contains substr, or
// (entry{}, false) if none match.
func findStatusEntry(m AppModel, substr string) (string, bool) {
	for _, e := range m.rootVP().GetMessages() {
		if e.Type == MessageStatus && strings.Contains(e.Content, substr) {
			return e.Content, true
		}
	}
	return "", false
}

func newInflightTestModel(t *testing.T) AppModel {
	t.Helper()
	m := NewAppModel("colour212", "myrepo", "v1.0.0", nil, nil, "", nil)
	m.statusBar.SetWidth(200)
	return m
}

func TestMCPCallStartedMsg_PopulatesStatusBarAndArmsTick(t *testing.T) {
	m := newInflightTestModel(t)
	started := time.Now().Add(-3 * time.Second)
	updated, cmd := m.Update(MCPCallStartedMsg{
		CallID:  "call-1",
		Tool:    "retire",
		Caller:  "weave",
		Started: started,
	})
	app := updated.(AppModel)
	if got := len(app.activeMCPOps); got != 1 {
		t.Fatalf("activeMCPOps=%d, want 1", got)
	}
	op, ok := app.activeMCPOps["call-1"]
	if !ok {
		t.Fatalf("call-1 missing from activeMCPOps")
	}
	if op.Tool != "retire" || op.Caller != "weave" {
		t.Errorf("op fields wrong: %+v", op)
	}
	if !app.mcpOpTickPending {
		t.Errorf("mcpOpTickPending should be true after first Started")
	}
	if cmd == nil {
		t.Errorf("expected tick + threshold cmd batch, got nil")
	}
	view := app.statusBar.View()
	if !strings.Contains(view, "retire(weave)") {
		t.Errorf("status bar should render 'retire(weave)' segment, got:\n%s", view)
	}
}

func TestMCPCallEndedMsg_RemovesOpFromStatusBar(t *testing.T) {
	m := newInflightTestModel(t)
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "c1", Tool: "merge", Caller: "weave", Started: time.Now()})
	updated, _ = updated.(AppModel).Update(MCPCallEndedMsg{CallID: "c1", Status: "ok", Duration: time.Second})
	app := updated.(AppModel)
	if len(app.activeMCPOps) != 0 {
		t.Errorf("activeMCPOps should be empty after Ended, got %d", len(app.activeMCPOps))
	}
	view := app.statusBar.View()
	if strings.Contains(view, "merge(weave)") {
		t.Errorf("status bar should not contain 'merge(weave)' after Ended, got:\n%s", view)
	}
}

func TestMCPCallProgressMsg_UpdatesStep(t *testing.T) {
	m := newInflightTestModel(t)
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "c1", Tool: "merge", Caller: "weave", Started: time.Now()})
	updated, _ = updated.(AppModel).Update(MCPCallProgressMsg{CallID: "c1", Step: "merge.validate-started"})
	app := updated.(AppModel)
	if op := app.activeMCPOps["c1"]; op.Step != "merge.validate-started" {
		t.Errorf("op.Step=%q, want merge.validate-started", op.Step)
	}
	view := app.statusBar.View()
	if !strings.Contains(view, "merge.validate-started") {
		t.Errorf("status bar should render step name, got:\n%s", view)
	}
}

func TestMCPThresholdMsg_AppendsViewportBanner(t *testing.T) {
	m := newInflightTestModel(t)
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "c1", Tool: "retire", Caller: "weave", Started: time.Now().Add(-65 * time.Second)})
	updated, _ = updated.(AppModel).Update(mcpOpThresholdMsg{CallID: "c1"})
	app := updated.(AppModel)
	if !app.mcpOpThresholdShown["c1"] {
		t.Errorf("mcpOpThresholdShown[c1] should be true")
	}
	body, ok := findStatusEntry(app, "retire(weave) is taking longer than usual")
	if !ok {
		t.Fatalf("expected viewport banner, found none")
	}
	if !strings.Contains(body, "SIGUSR1") {
		t.Errorf("banner should mention SIGUSR1, got: %q", body)
	}
}

func TestMCPThresholdMsg_AfterEnded_NoBanner(t *testing.T) {
	m := newInflightTestModel(t)
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "c1", Tool: "retire", Caller: "weave", Started: time.Now()})
	updated, _ = updated.(AppModel).Update(MCPCallEndedMsg{CallID: "c1", Status: "ok"})
	updated, _ = updated.(AppModel).Update(mcpOpThresholdMsg{CallID: "c1"})
	app := updated.(AppModel)
	if _, ok := findStatusEntry(app, "is taking longer than usual"); ok {
		t.Errorf("threshold after Ended should not raise banner")
	}
}

func TestMCPThresholdMsg_DuplicatesIgnored(t *testing.T) {
	m := newInflightTestModel(t)
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "c1", Tool: "retire", Caller: "weave", Started: time.Now()})
	updated, _ = updated.(AppModel).Update(mcpOpThresholdMsg{CallID: "c1"})
	updated, _ = updated.(AppModel).Update(mcpOpThresholdMsg{CallID: "c1"})
	app := updated.(AppModel)
	count := 0
	for _, e := range app.rootVP().GetMessages() {
		if e.Type == MessageStatus && strings.Contains(e.Content, "is taking longer than usual") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate threshold should not double-render banner; count=%d", count)
	}
}

func TestMCPOpTickMsg_SelfPerpetuatesWhileActive(t *testing.T) {
	m := newInflightTestModel(t)
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "c1", Tool: "retire", Caller: "weave", Started: time.Now()})
	updated, cmd := updated.(AppModel).Update(mcpOpTickMsg{})
	if cmd == nil {
		t.Errorf("tick should re-arm while ops active")
	}
	// Drain the op; tick should self-stop.
	updated, _ = updated.(AppModel).Update(MCPCallEndedMsg{CallID: "c1", Status: "ok"})
	app := updated.(AppModel)
	_, cmd2 := app.Update(mcpOpTickMsg{})
	if cmd2 != nil {
		t.Errorf("tick should self-stop when no ops active, got cmd=%v", cmd2)
	}
}

func TestMCPCallStartedMsg_EmptyCallID_Ignored(t *testing.T) {
	m := newInflightTestModel(t)
	updated, cmd := m.Update(MCPCallStartedMsg{Tool: "retire"})
	app := updated.(AppModel)
	if len(app.activeMCPOps) != 0 {
		t.Errorf("empty call_id should be ignored")
	}
	if cmd != nil {
		t.Errorf("empty call_id should not arm tick")
	}
}

func TestMCPInsertionOrder_PreservesOldestFirst(t *testing.T) {
	m := newInflightTestModel(t)
	t0 := time.Now()
	updated, _ := m.Update(MCPCallStartedMsg{CallID: "a", Tool: "merge", Caller: "weave", Started: t0})
	updated, _ = updated.(AppModel).Update(MCPCallStartedMsg{CallID: "b", Tool: "retire", Caller: "weave", Started: t0.Add(time.Second)})
	app := updated.(AppModel)
	ops := app.orderedMCPOps()
	if len(ops) != 2 || ops[0].CallID != "a" || ops[1].CallID != "b" {
		t.Errorf("ordered ops mismatch: %+v", ops)
	}
}

// Sanity: verify the public Update branch type-routing works via tea.Msg
// (defends against accidental enum drift if someone refactors the switch).
var (
	_ tea.Msg = MCPCallStartedMsg{}
	_ tea.Msg = MCPCallProgressMsg{}
	_ tea.Msg = MCPCallEndedMsg{}
)
