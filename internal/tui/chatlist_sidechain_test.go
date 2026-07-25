package tui

// QUM-928 — the Agent tool renders as a PLAIN tool call.
//
// This deliberately REVERSES QUM-914's carve-out. QUM-914 treated the async
// Agent tool_result as "launched, not finished" and kept the row spinning until
// a task_notification arrived, driving lifecycle off the task_* channel. Rule 2
// of QUM-928 is "treat Agent() like any other tool call": the immediate ack is
// what the tool call actually did, so it closes the row. Net effect for the
// reader is `Agent("...") ✓` then silence, because every frame the sidechain
// produces is suppressed (see protocol_mapping_sidechain_test.go).
//
// Consequences pinned here:
//   - the launch ack CLOSES the row — no indefinite spinner, which also moots
//     QUM-926 (a never-arriving task_notification stranding a row);
//   - pendingTools/Idle() stay consistent with ONE lifecycle path per tool:
//     one append, one decrement, no special case;
//   - a failed launch is no longer an exception, just the same path;
//   - the controlling agent's own interleaved tool calls are unaffected.
//
// activeAgents, MarkSidechainComplete and TaskCompletedMsg are gone: with the
// ack closing the row there is no second completion signal to route, and
// keeping them would leave dead code.
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

// TestChatList_AsyncAgentAck_ClosesRow is the requirement-2 test.
func TestChatList_AsyncAgentAck_ClosesRow(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "Explore", "Explore", nil, "")
	if cl.Idle() {
		t.Fatal("Idle() true immediately after Agent spawn; want pending")
	}

	if ok := cl.MarkToolResult("a1", "Async agent launched successfully.", false); !ok {
		t.Fatal("MarkToolResult(a1) returned false; the ack must close the row (QUM-928)")
	}
	if !cl.Idle() {
		t.Error("Idle() false after the launch ack; the Agent row must close like any tool call")
	}
	if cl.HasPendingToolCall() {
		t.Error("HasPendingToolCall() true after the launch ack; want false")
	}
	it := findToolCallItem(cl, "a1")
	if it == nil || !it.Finished() {
		t.Error("Agent item not finished by the launch ack")
	}
	if it != nil && it.Failed() {
		t.Error("successful launch marked failed")
	}
}

// TestChatList_ThreeConcurrentAgentAcks_IdleAccounting covers the AC for
// spinner/idle correctness in the issue's headline scenario of 3 concurrent
// sidechains: no premature idle, no stuck spinner, acked out of order.
func TestChatList_ThreeConcurrentAgentAcks_IdleAccounting(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	for _, id := range []string{"a1", "a2", "a3"} {
		cl.AppendToolCallWithHeader("Agent", id, true, "Explore", "", "", nil, "")
	}
	if cl.Idle() {
		t.Fatal("Idle() true with 3 Agent rows pending")
	}

	// Ack out of order — nothing may depend on arrival order.
	cl.MarkToolResult("a2", "Async agent launched successfully.", false)
	if cl.Idle() {
		t.Error("premature Idle() after 1 of 3 acks")
	}
	cl.MarkToolResult("a1", "Async agent launched successfully.", false)
	if cl.Idle() {
		t.Error("premature Idle() after 2 of 3 acks")
	}
	cl.MarkToolResult("a3", "Async agent launched successfully.", false)
	if !cl.Idle() {
		t.Error("Idle() false after all 3 acks; spinner would be stuck")
	}

	// A duplicate/late ack must not double-decrement.
	cl.MarkToolResult("a1", "Async agent launched successfully.", false)
	if !cl.Idle() {
		t.Error("duplicate ack corrupted pendingTools; Idle() flipped back to false")
	}
	if cl.HasPendingToolCall() {
		t.Error("HasPendingToolCall() true after a duplicate ack")
	}
	for _, id := range []string{"a1", "a2", "a3"} {
		if it := findToolCallItem(cl, id); it == nil || !it.Finished() {
			t.Errorf("Agent row %s not finished", id)
		}
	}
}

// TestChatList_MainThreadToolInterleavedWithAgents is AC #3: the controlling
// agent's OWN tool calls, interleaved in time with running sidechains, render
// as before and stay top-level.
func TestChatList_MainThreadToolInterleavedWithAgents(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "", "", nil, "")
	cl.MarkToolResult("a1", "Async agent launched successfully.", false)

	// weave's own Bash, fired while the sidechain is still working.
	cl.AppendToolCallWithHeader("Bash", "m1", true, "echo main", "echo main", "echo main", nil, "")
	m1 := findToolCallItem(cl, "m1")
	if m1 == nil {
		t.Fatal("main-thread Bash row missing")
	}
	if m1.Depth() != 0 || m1.ParentToolID() != "" {
		t.Errorf("main-thread tool misattributed: depth=%d parent=%q; want 0 and \"\"",
			m1.Depth(), m1.ParentToolID())
	}
	if cl.Idle() {
		t.Error("Idle() true while the main-thread tool is in flight")
	}
	cl.MarkToolResult("m1", "main output", false)
	if !cl.Idle() {
		t.Error("Idle() false after the main-thread tool resolved")
	}
}

// TestChatList_FailedAgentLaunch_FinishesRow — retained from QUM-914, but it is
// no longer an "exception": a failed launch takes the same single path as a
// successful one.
func TestChatList_FailedAgentLaunch_FinishesRow(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.AppendToolCallWithHeader("Agent", "a1", true, "Explore", "", "", nil, "")

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

// TestChatList_AgentHasNoSpecialCasing pins the structural half of "plain tool
// call": an Agent row and a Bash row must be indistinguishable in lifecycle. If
// a future change reintroduces a Name == "Agent" carve-out anywhere in the
// append or result path, this diverges.
func TestChatList_AgentHasNoSpecialCasing(t *testing.T) {
	mk := func(name string) *ChatList {
		cl := newTestChatList()
		cl.SetSize(80)
		cl.AppendToolCallWithHeader(name, "x1", true, "arg", "arg", "arg", nil, "")
		return cl
	}
	agent, bash := mk("Agent"), mk("Bash")

	for _, step := range []string{"after append", "after result"} {
		if step == "after result" {
			agent.MarkToolResult("x1", "ok", false)
			bash.MarkToolResult("x1", "ok", false)
		}
		if agent.Idle() != bash.Idle() {
			t.Errorf("%s: Idle() differs — Agent=%v Bash=%v", step, agent.Idle(), bash.Idle())
		}
		if agent.HasPendingToolCall() != bash.HasPendingToolCall() {
			t.Errorf("%s: HasPendingToolCall() differs — Agent=%v Bash=%v",
				step, agent.HasPendingToolCall(), bash.HasPendingToolCall())
		}
		ai, bi := findToolCallItem(agent, "x1"), findToolCallItem(bash, "x1")
		if ai == nil || bi == nil {
			t.Fatalf("%s: missing row", step)
		}
		if ai.Finished() != bi.Finished() {
			t.Errorf("%s: Finished() differs — Agent=%v Bash=%v", step, ai.Finished(), bi.Finished())
		}
	}
}
