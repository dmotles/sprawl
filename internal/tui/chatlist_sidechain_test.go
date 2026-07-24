package tui

// QUM-914 — Breakage A (live TUI sidechain rendering) RED-phase tests.
//
// The Agent tool is now ASYNC: its tool_result is a "launched" ack, not a
// completion. Real completion arrives on the task_* channel
// (task_notification, keyed on tool_use_id). These tests pin the ChatList
// contract that:
//   - an async Agent launch ack does NOT close the group / flip Idle();
//   - MarkSidechainComplete (driven by task_notification) finishes the group;
//   - ≥2 concurrent sidechains nest under the correct parent via
//     parent_tool_use_id (no orphan-to-top-level, no last-agent
//     misattribution);
//   - a failed launch (is_error ack) still finishes the row (nothing else is
//     coming).
import "testing"

// findToolCallItem returns the first ToolCallItem with the given tool id, or
// nil if none is present.
func findToolCallItem(cl *ChatList, id string) *ToolCallItem {
	for _, it := range cl.Items() {
		if t, ok := it.(*ToolCallItem); ok && t.ToolID() == id {
			return t
		}
	}
	return nil
}

func TestChatList_AsyncAgentAck_KeepsGroupPending(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "Explore", "Explore", nil, "")
	if cl.Idle() {
		t.Fatal("Idle() true immediately after Agent spawn; want pending")
	}
	// The async launch ack must NOT finish the group.
	cl.MarkToolResult("a1", "Async agent launched successfully.", false)
	if cl.Idle() {
		t.Error("Idle() true after async launch ack; the Agent group must stay in-progress (QUM-914)")
	}
	if !cl.HasPendingToolCall() {
		t.Error("HasPendingToolCall() false after async ack; want still pending")
	}
	if it := findToolCallItem(cl, "a1"); it == nil || it.Finished() {
		t.Error("Agent item was finished by the async launch ack; want still pending")
	}
}

func TestChatList_MarkSidechainComplete_FinishesGroup(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "Explore", "Explore", nil, "")
	cl.MarkToolResult("a1", "Async agent launched successfully.", false)

	if ok := cl.MarkSidechainComplete("a1"); !ok {
		t.Fatal("MarkSidechainComplete(a1) returned false; want true (matching group)")
	}
	if !cl.Idle() {
		t.Error("Idle() false after the sidechain completed")
	}
	if it := findToolCallItem(cl, "a1"); it == nil || !it.Finished() {
		t.Error("Agent item not finished after MarkSidechainComplete")
	}
	if cl.MarkSidechainComplete("nope") {
		t.Error("MarkSidechainComplete for an unknown id returned true; want false")
	}
}

func TestChatList_ConcurrentSidechains_NestByParent(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)

	// Two concurrent sidechains, each launched then acked.
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "", "", nil, "")
	cl.MarkToolResult("a1", "Async agent launched successfully.", false)
	cl.AppendToolCallWithHeader("Agent", "a2", true, "Explore", "", "", nil, "")
	cl.MarkToolResult("a2", "Async agent launched successfully.", false)

	// Child of a1 arrives AFTER a2 was spawned. The dead lastActiveAgent
	// single-slot fallback would misattribute it to a2; explicit
	// parent_tool_use_id must win.
	cl.AppendToolCallWithHeader("Bash", "c1", true, "ls", "ls", "ls", nil, "a1")
	cl.AppendToolCallWithHeader("Bash", "c2", true, "pwd", "pwd", "pwd", nil, "a2")

	if c1 := findToolCallItem(cl, "c1"); c1 == nil || c1.Depth() != 1 || c1.ParentToolID() != "a1" {
		t.Errorf("c1 nesting wrong: %+v; want depth=1 parent=a1", c1)
	}
	if c2 := findToolCallItem(cl, "c2"); c2 == nil || c2.Depth() != 1 || c2.ParentToolID() != "a2" {
		t.Errorf("c2 nesting wrong: %+v; want depth=1 parent=a2", c2)
	}

	// A parent-less main-thread tool call must NOT be nested under any active
	// sidechain — it stays top-level (no last-agent misattribution).
	cl.AppendToolCallWithHeader("Bash", "m1", true, "echo", "echo", "echo", nil, "")
	if m1 := findToolCallItem(cl, "m1"); m1 == nil || m1.Depth() != 0 || m1.ParentToolID() != "" {
		t.Errorf("m1 misattributed: depth=%d parent=%q; want depth=0 parent=\"\"", m1.Depth(), m1.ParentToolID())
	}

	// Completing a1 must leave a2 still in flight.
	cl.MarkSidechainComplete("a1")
	if cl.Idle() {
		t.Error("Idle() true while a2 is still running")
	}
	if a1 := findToolCallItem(cl, "a1"); a1 == nil || !a1.Finished() {
		t.Error("a1 not finished after MarkSidechainComplete(a1)")
	}
	if a2 := findToolCallItem(cl, "a2"); a2 == nil || a2.Finished() {
		t.Error("a2 finished prematurely; only a1 completed")
	}
}

func TestChatList_FailedAgentLaunch_FinishesRow(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "", "", nil, "")

	// A failed launch arrives as an is_error ack; no task_started/notification
	// will follow, so the row must finish now rather than spin forever.
	cl.MarkToolResult("a1", "launch failed", true)
	if !cl.Idle() {
		t.Error("Idle() false after a failed Agent-launch ack; row stuck pending")
	}
	it := findToolCallItem(cl, "a1")
	if it == nil || !it.Finished() {
		t.Error("failed-launch Agent row not finished")
	}
	if it != nil && !it.Failed() {
		t.Error("failed-launch Agent row not marked failed")
	}
}
