package tui

// QUM-673 S3 — View() switch: finished items render via ChatList; in-flight
// items fall back to the legacy ViewportModel path. These tests pin the
// Idle()-driven flip on the production wiring without re-asserting the
// rendered bytes (visual parity is gated by the existing viewport_test
// goldens + manual TUI checks).

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// rootCL is a small helper for these tests — returns the root agent's
// ChatList through the AgentBuffer wrapper. Fails the test if missing.
func rootCL(t *testing.T, app AppModel) *ChatList {
	t.Helper()
	buf, ok := app.agentBuffers[app.rootAgent]
	if !ok || buf == nil {
		t.Fatalf("root agent buffer missing")
	}
	return buf.cl
}

func TestAppModel_View_IdleAfterUserSubmit(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// QUM-828 render-on-consume: the bubble renders when the send ack arrives,
	// so drive the returned cmd (empty-uuid fallback renders on UserMessageSentMsg).
	next, cmd := app.Update(SubmitMsg{Text: "ping"})
	app = next.(AppModel)
	if cmd != nil {
		next, _ = app.Update(cmd())
		app = next.(AppModel)
	}

	cl := rootCL(t, app)
	if !cl.Idle() {
		t.Errorf("ChatList must be Idle after a user submit (no in-flight stream / tool)")
	}
	if cl.Len() != 1 {
		t.Errorf("cl.Len() = %d, want 1", cl.Len())
	}
}

func TestAppModel_View_StreamingNotIdle(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	next, _ := app.Update(AssistantTextMsg{Text: "streaming chunk"})
	app = next.(AppModel)

	cl := rootCL(t, app)
	if cl.Idle() {
		t.Errorf("ChatList must report not-Idle while assistant is streaming")
	}

	// Finalize → idle.
	next, _ = app.Update(SessionResultMsg{Result: "", DurationMs: 1, TotalCostUsd: 0})
	app = next.(AppModel)
	if !rootCL(t, app).Idle() {
		t.Errorf("ChatList must restore Idle after SessionResultMsg finalize")
	}
}

func TestAppModel_View_PendingToolNotIdle(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	next, _ := app.Update(ToolCallMsg{
		ToolName: "Bash", ToolID: "tu_42", Approved: true, Input: "ls",
	})
	app = next.(AppModel)

	cl := rootCL(t, app)
	if cl.Idle() {
		t.Errorf("ChatList must report not-Idle with a pending tool call")
	}

	next, _ = app.Update(ToolResultMsg{ToolID: "tu_42", Content: "out", IsError: false})
	app = next.(AppModel)
	if !rootCL(t, app).Idle() {
		t.Errorf("ChatList must restore Idle after ToolResultMsg")
	}
}

// TestAppModel_View_ChatRegion_DualAppendForAllVerbs verifies the QUM-673 S3
// dual-append shim widened from S2's user-only mirror to all non-system
// verbs. For each verb, after the live-path Update, the ChatList shadow
// must contain a matching item count to the vp's MessageEntries (for the
// types ChatList covers).
func TestAppModel_View_ChatRegion_DualAppendForAllVerbs(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// User submit. QUM-828 render-on-consume: drive the returned cmd so the
	// empty-uuid fallback renders the user bubble.
	next, cmd := app.Update(SubmitMsg{Text: "hello"})
	app = next.(AppModel)
	if cmd != nil {
		next, _ = app.Update(cmd())
		app = next.(AppModel)
	}

	// Assistant chunk (in-flight) → finalize.
	next, _ = app.Update(AssistantTextMsg{Text: "world"})
	app = next.(AppModel)
	next, _ = app.Update(SessionResultMsg{Result: "", DurationMs: 1})
	app = next.(AppModel)

	// Auto-trigger.
	next, _ = app.Update(AutoContinueMsg{})
	app = next.(AppModel)

	// Tool call + result.
	next, _ = app.Update(ToolCallMsg{ToolName: "Bash", ToolID: "tu1", Input: "ls"})
	app = next.(AppModel)
	next, _ = app.Update(ToolResultMsg{ToolID: "tu1", Content: "ok"})
	app = next.(AppModel)

	cl := rootCL(t, app)
	// Expected items: user + assistant + autotrigger + toolcall = 4.
	if cl.Len() != 4 {
		t.Errorf("cl.Len() = %d, want 4 (user+assistant+autotrigger+tool)", cl.Len())
	}
	if !cl.Idle() {
		t.Errorf("ChatList must be Idle after all turns settle")
	}
}
