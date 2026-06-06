package tui

// QUM-676 S6 — RED-phase tests for the ViewportResyncMsg reducer's port from
// vp.SetMessages → cl.Reset. After S6 the viewport.ViewportModel no longer
// owns the resync replacement path; ChatList is the sole transcript store.
//
// Reference: docs/designs/qum-669-viewport-wedge-recovery.md §3 portable seam.
// The "✓ resynced — recovered N events" surface is already on the statusbar
// transient label (S5 done); these tests pin the data-path port and the
// wedge-exit invariant.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestAppModel_ViewportResyncMsg_PopulatesChatList asserts that after S6
// the rebuilt entries land in the ChatList (cl.Items()). QUM-693 deleted the
// legacy ViewportModel facade; this test pins ChatList as the post-resync store.
func TestAppModel_ViewportResyncMsg_PopulatesChatList(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)
	app.setTurnState(TurnStreaming)

	rebuilt := []MessageEntry{
		{Type: MessageUser, Content: "rebuilt user", Complete: true},
		{Type: MessageAssistant, Content: "rebuilt assistant", Complete: true},
	}
	resynced, _ := app.Update(ViewportResyncMsg{Entries: rebuilt, MissingCount: 7, Err: nil})
	next := resynced.(AppModel)

	// New surface: ChatList must contain the rebuilt entries as items.
	buf := next.agentBuffers[next.rootAgent]
	if buf == nil || buf.cl == nil {
		t.Fatalf("rootBuf().cl is nil — ChatList wiring missing post-resync")
	}
	items := buf.cl.Items()
	if len(items) != 2 {
		t.Fatalf("ChatList should hold the 2 rebuilt items after resync; got %d", len(items))
	}
	if _, ok := items[0].(*UserItem); !ok {
		t.Errorf("items[0] = %T, want *UserItem", items[0])
	}
	if _, ok := items[1].(*AssistantTextItem); !ok {
		t.Errorf("items[1] = %T, want *AssistantTextItem", items[1])
	}
}

// TestAppModel_ViewportResyncMsg_ForceFinalizesTrailingStreamingAssistant
// asserts the wedge-exit invariant from chatlist-invariants §8 and
// qum-669-viewport-wedge-recovery.md §2.7: after a resync the ChatList must
// be Idle (no streaming assistant, no pending tools) even if the rebuilt
// transcript contains a non-Complete trailing assistant entry.
func TestAppModel_ViewportResyncMsg_ForceFinalizesTrailingStreamingAssistant(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)
	app.setTurnState(TurnStreaming)

	rebuilt := []MessageEntry{
		{Type: MessageUser, Content: "u", Complete: true},
		// Trailing assistant with Complete=false — the wedge case.
		{Type: MessageAssistant, Content: "in flight", Complete: false},
	}
	resynced, _ := app.Update(ViewportResyncMsg{Entries: rebuilt, MissingCount: 3, Err: nil})
	next := resynced.(AppModel)

	buf := next.agentBuffers[next.rootAgent]
	if buf == nil || buf.cl == nil {
		t.Fatalf("rootBuf().cl is nil after resync")
	}
	if !buf.cl.Idle() {
		t.Errorf("ChatList must be Idle after resync (wedge-exit invariant) — streamingAssistant should be force-finalized by cl.Reset")
	}
}

// TestAppModel_ViewportResyncMsg_ClearsPendingTools asserts pendingTools
// counter is cleared after resync (no orphaned pending counters that would
// keep cl in not-Idle).
func TestAppModel_ViewportResyncMsg_ClearsPendingTools(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)

	// Seed a pending tool call so pendingTools > 0.
	app.rootBuf().AppendToolCallWithHeader("Bash", "tu_pre", true, "ls", "ls", "ls", nil, "")
	if app.rootBuf().cl.Idle() {
		t.Fatalf("precondition: cl should not be Idle with a pending tool call")
	}

	// Resync with a transcript that has only finished entries.
	rebuilt := []MessageEntry{
		{Type: MessageUser, Content: "u", Complete: true},
	}
	resynced, _ := app.Update(ViewportResyncMsg{Entries: rebuilt, MissingCount: 1, Err: nil})
	next := resynced.(AppModel)

	if !next.rootBuf().cl.Idle() {
		t.Errorf("After resync, cl must be Idle (pendingTools cleared by Reset)")
	}
}

// TestAppModel_ViewportResyncMsg_BannerOnStatusBarOnly asserts the
// "✓ resynced — recovered N events" banner appears on the statusbar
// transient label, not as a ChatList item or vp MessageStatus entry.
func TestAppModel_ViewportResyncMsg_BannerOnStatusBarOnly(t *testing.T) {
	fake := newFakeSessionBackend()
	fake.SetContinuous(true)
	m := NewAppModel("colour212", "testrepo", "v0.1.0", fake, nil, "", nil)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := updated.(AppModel)

	rebuilt := []MessageEntry{{Type: MessageUser, Content: "u", Complete: true}}
	resynced, _ := app.Update(ViewportResyncMsg{Entries: rebuilt, MissingCount: 4, Err: nil})
	next := resynced.(AppModel)

	view := stripAnsi(next.statusBar.View())
	if !strings.Contains(strings.ToLower(view), "resynced") {
		t.Errorf("status bar should contain the resync banner; got:\n%s", view)
	}

	// Negative: no ChatList item should carry the banner text.
	buf := next.agentBuffers[next.rootAgent]
	if buf != nil && buf.cl != nil {
		for _, it := range buf.cl.Items() {
			if strings.Contains(strings.ToLower(it.RawMarkdown()), "resynced") {
				t.Errorf("ChatList must not carry the resync banner as an item; got %T with raw %q", it, it.RawMarkdown())
			}
		}
	}
}
