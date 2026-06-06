package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-336: ToolCallMsg followed by ToolResultMsg routes both updates to the
// root agent's viewport: the call entry transitions Pending=true → Pending=false,
// and the rendered viewport carries the result preview text.
func TestAppModel_ToolResultMsg_RoutesToRootViewport(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)

	// Tool call → entry appears with Pending=true.
	updated, _ = app.Update(ToolCallMsg{
		ToolName: "Bash",
		ToolID:   "t-42",
		Approved: true,
		Input:    "ls",
	})
	app = updated.(AppModel)

	// QUM-693: banners never enter ChatList; filter dropped.
	items := app.rootVP().ChatList().Items()
	if len(items) != 1 {
		t.Fatalf("root viewport items = %+v, want one item", items)
	}
	tool, ok := items[0].(*ToolCallItem)
	if !ok {
		t.Fatalf("items[0] = %T, want *ToolCallItem", items[0])
	}
	if !tool.Pending() {
		t.Errorf("Pending = false, want true after ToolCallMsg")
	}

	// Tool result for the same toolID → Pending flips, Result populated.
	updated, _ = app.Update(ToolResultMsg{
		ToolID:  "t-42",
		Content: "fileA\nfileB",
		IsError: false,
	})
	app = updated.(AppModel)

	items = app.rootVP().ChatList().Items()
	tool, ok = items[0].(*ToolCallItem)
	if !ok {
		t.Fatalf("items[0] = %T, want *ToolCallItem", items[0])
	}
	if tool.Pending() {
		t.Errorf("Pending = true, want false after ToolResultMsg")
	}
	if tool.Failed() {
		t.Errorf("Failed = true, want false on success")
	}
	if tool.Result() != "fileA\nfileB" {
		t.Errorf("Result = %q, want %q", tool.Result(), "fileA\nfileB")
	}

	view := stripANSI(app.rootVP().View())
	if !strings.Contains(view, "fileA") {
		t.Errorf("rendered viewport missing result preview line, got:\n%s", view)
	}
	if !strings.Contains(view, "✓") {
		t.Errorf("rendered viewport missing success ✓, got:\n%s", view)
	}
}

// QUM-336: a ToolResultMsg whose ToolID doesn't match any pending entry is a
// safe no-op (no panic, no state change).
func TestAppModel_ToolResultMsg_NoMatchingEntry_NoOp(t *testing.T) {
	m := newTestAppModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app := updated.(AppModel)

	updated, _ = app.Update(ToolResultMsg{ToolID: "nope", Content: "x"})
	app = updated.(AppModel)

	// QUM-693: ChatList should be empty — the orphan tool result must not
	// have appended anything (banners never enter ChatList either).
	if got := app.rootVP().ChatList().Items(); len(got) != 0 {
		t.Errorf("unexpected items after orphan tool result: %+v", got)
	}
}
