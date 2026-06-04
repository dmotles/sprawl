package tui

// QUM-674 S4 — invariants folded in from S3 residue + the View() flip.
//
//   L1: AgentBuffer.MarkToolResult must reconcile vp/cl divergence so cl
//       never gets stuck in not-Idle when vp's match drains the counter.
//   L2: ChatList.Reset must always end with no in-flight assistant — even
//       if the incoming transcript has an assistant entry with Complete=false
//       (Reset is called from preload / restart / resync / waiting-banner /
//       child transcript and snapshots are never resumed mid-stream).
//   L3: ChatList.AppendSystemNotification with no envelope tag must NOT
//       surface a raw-text item (which would diverge from vp's
//       AppendSystemMessage fallback that creates a contract-violator entry
//       routed via vp.View()).
//   View flip: chatRegionContent must use cl.Render even while a stream or
//       tool call is in flight — no more "in-flight → vp fallback" branch.

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// L1 ------------------------------------------------------------------------

// TestAgentBuffer_MarkToolResult_ClOnlyMatch covers the case where the cl
// pending tool exists but vp has lost track of it. The wrapper must still
// report matched=true so the spinner counter drains and cl ends up Idle.
func TestAgentBuffer_MarkToolResult_ClOnlyMatch(t *testing.T) {
	theme := NewTheme("colour212")
	buf := &AgentBuffer{vp: NewViewportModel(&theme), cl: NewChatList(&theme)}
	buf.cl.SetSize(80)

	// Append the tool to cl only — vp never sees it. This simulates a
	// divergence where the legacy store missed the event.
	buf.cl.AppendToolCall(ToolCallSpec{Name: "Bash", ToolID: "tu1", Input: "ls"})
	if buf.cl.Idle() {
		t.Fatalf("cl precondition: pending tool must keep cl not-Idle")
	}

	matched := buf.MarkToolResult("tu1", "ok", false)
	if !matched {
		t.Errorf("MarkToolResult must report true when cl matches even if vp doesn't")
	}
	if !buf.cl.Idle() {
		t.Errorf("cl must be Idle after MarkToolResult resolves its pending tool")
	}
}

// TestAgentBuffer_MarkToolResult_VpOnlyMatch covers the inverse divergence:
// vp matched, cl missed. Wrapper still reports true (load-bearing) and cl is
// left in a defensible state (no orphan pending).
func TestAgentBuffer_MarkToolResult_VpOnlyMatch(t *testing.T) {
	theme := NewTheme("colour212")
	buf := &AgentBuffer{vp: NewViewportModel(&theme), cl: NewChatList(&theme)}
	buf.vp.SetSize(80, 24)

	buf.vp.AppendToolCall("Bash", "tu1", true, "ls", "ls")
	matched := buf.MarkToolResult("tu1", "ok", false)
	if !matched {
		t.Errorf("MarkToolResult must report true when vp matches even if cl doesn't")
	}
	if !buf.cl.Idle() {
		t.Errorf("cl must remain Idle when vp matches; got pendingTools=%d", buf.cl.pendingTools)
	}
}

// L2 ------------------------------------------------------------------------

// TestChatList_Reset_StreamingAssistantEntryFinalizes asserts the conservative
// L2 fix: Reset must always finalize the trailing assistant so cl never gets
// stuck in streamingAssistant=true after a transcript snapshot.
func TestChatList_Reset_StreamingAssistantEntryFinalizes(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)

	cl.Reset([]MessageEntry{
		{Type: MessageUser, Content: "hi", Complete: true},
		{Type: MessageAssistant, Content: "partial reply", Complete: false},
	})

	if !cl.Idle() {
		t.Errorf("Reset with Complete=false trailing assistant must leave cl Idle (always-finalize)")
	}
}

// L3 ------------------------------------------------------------------------

// TestChatList_AppendSystemNotification_NoEnvelopeIsDropped asserts L3 fix:
// AppendSystemNotification with no envelope must NOT surface a raw-text item
// in cl. The legacy vp side still creates a MessageSystem entry (contract
// violator) which triggers HasContractViolators → chatRegionContent falls
// back to vp.View(), so the user still sees the text. cl staying empty is
// the contract.
func TestChatList_AppendSystemNotification_NoEnvelopeIsDropped(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendSystemNotification("plain banner with no envelope tag")
	if got := cl.Len(); got != 0 {
		t.Errorf("cl.Len() = %d after raw-text AppendSystemNotification, want 0 (L3 alignment)", got)
	}
}

// View flip ----------------------------------------------------------------

// TestAppModel_ChatRegionContent_UsesChatListDuringStream asserts that the
// S4 View() flip drops the in-flight fallback: chatRegionContent must call
// cl.Render even while an assistant stream is in flight.
func TestAppModel_ChatRegionContent_UsesChatListDuringStream(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	// Land a user message + start streaming → cl has 2 items; not Idle.
	next, _ := app.Update(SubmitMsg{Text: "hi"})
	app = next.(AppModel)
	next, _ = app.Update(AssistantTextMsg{Text: "streaming chunk"})
	app = next.(AppModel)

	cl := app.agentBuffers[app.rootAgent].cl
	if cl.Idle() {
		t.Fatalf("precondition: cl must not be Idle while streaming")
	}

	// Drive chatRegionContent — it should push cl.Render into vp via
	// SetContentExternal even though cl is not Idle. (Glamour splits the
	// streamed text into per-word styled spans, so we look for one word.)
	out := app.chatRegionContent(76)
	if !strings.Contains(out, "streaming") {
		t.Errorf("chatRegionContent during stream missing cl-rendered text; got:\n%s", out)
	}
	// Also confirm cl.Render produces the streaming text.
	cr := cl.Render(76)
	if !strings.Contains(cr, "streaming") {
		t.Errorf("cl.Render during stream missing chunk text; got:\n%s", cr)
	}
}

// TestAppModel_ChatRegionContent_UsesChatListDuringPendingTool same as above
// for the pending-tool branch.
func TestAppModel_ChatRegionContent_UsesChatListDuringPendingTool(t *testing.T) {
	mock := newFakeSessionBackend()
	m := newTestAppModelWithBridge(t, mock)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app := resized.(AppModel)

	next, _ := app.Update(SubmitMsg{Text: "hi"})
	app = next.(AppModel)
	next, _ = app.Update(ToolCallMsg{
		ToolName: "Bash", ToolID: "tu_77", Input: "ls", HeaderArg: "ls",
	})
	app = next.(AppModel)

	cl := app.agentBuffers[app.rootAgent].cl
	if cl.Idle() {
		t.Fatalf("precondition: cl must not be Idle while a tool is pending")
	}
	out := app.chatRegionContent(76)
	if !strings.Contains(out, "Bash") {
		t.Errorf("chatRegionContent with pending tool missing cl-rendered tool header; got:\n%s", out)
	}
}
