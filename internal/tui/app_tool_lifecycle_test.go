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

	rootMsgs := app.rootVP().GetMessages()
	if len(rootMsgs) != 1 || rootMsgs[0].Type != MessageToolCall {
		t.Fatalf("root viewport entries = %+v, want one MessageToolCall", rootMsgs)
	}
	if !rootMsgs[0].Pending {
		t.Errorf("Pending = false, want true after ToolCallMsg")
	}

	// Tool result for the same toolID → Pending flips, Result populated.
	updated, _ = app.Update(ToolResultMsg{
		ToolID:  "t-42",
		Content: "fileA\nfileB",
		IsError: false,
	})
	app = updated.(AppModel)

	rootMsgs = app.rootVP().GetMessages()
	if rootMsgs[0].Pending {
		t.Errorf("Pending = true, want false after ToolResultMsg")
	}
	if rootMsgs[0].Failed {
		t.Errorf("Failed = true, want false on success")
	}
	if rootMsgs[0].Result != "fileA\nfileB" {
		t.Errorf("Result = %q, want %q", rootMsgs[0].Result, "fileA\nfileB")
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

	if got := len(app.rootVP().GetMessages()); got != 0 {
		t.Errorf("root viewport entries = %d, want 0 (orphan tool result should not append)", got)
	}
}
