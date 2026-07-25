package tui

// QUM-928 — sidechain frames must not reach the chat stream.
//
// A sidechain (Claude in-process Agent-tool spawn: Explore, Plan, oracle, …)
// emits its own assistant text/thinking/tool_use frames and its own
// tool_result frames, all interleaved into the controlling agent's stream in
// raw arrival order. Measured across 660 wire logs: 11,817 sidechain
// `assistant` frames and 8,655 sidechain `user` frames. With 2-3 concurrent
// sidechains that is an unreadable mess, so all of it is suppressed from the
// chat stream.
//
// The ONLY discriminator is a non-empty `parent_tool_use_id`: `isSidechain` is
// never set on the stream-json wire and `session_id` is identical to the main
// thread. The parent `Agent` tool_use itself carries no `parent_tool_use_id`
// and therefore survives, which is what makes this a single-predicate change.
//
// Suppression is RENDER-ONLY — it happens at the TUI mapping layer, so the
// wire log, the runtime event path, and the model's own context are untouched.

import (
	"strings"
	"testing"
)

// Real sidechain frame shapes, taken verbatim from the wire corpus.
const (
	sidechainAssistantToolUse = `{"type":"assistant","parent_tool_use_id":"A1","message":{"role":"assistant","content":[{"type":"tool_use","id":"R1","name":"Read","input":{"file_path":"/tmp/sidechain-only.go"}}]}}`
	sidechainAssistantText    = `{"type":"assistant","parent_tool_use_id":"A1","message":{"role":"assistant","content":[{"type":"text","text":"SIDECHAIN_INNER_PROSE"}]}}`
	sidechainAssistantThink   = `{"type":"assistant","parent_tool_use_id":"A1","message":{"role":"assistant","content":[{"type":"thinking","thinking":""}]}}`
	sidechainUserToolResult   = `{"type":"user","parent_tool_use_id":"A1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"R1","content":"SIDECHAIN_INNER_RESULT"}]}}`
	mainThreadAssistantText   = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"MAIN_THREAD_PROSE"}]}}`
	mainThreadToolUse         = `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"M1","name":"Bash","input":{"command":"echo main"}}]}}`
	mainThreadUserToolResult  = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"M1","content":"main output"}]}}`
	// The async Agent launch ack: a MAIN-THREAD user frame (no
	// parent_tool_use_id) whose tool_use_id is the Agent row's id.
	agentLaunchAck = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a1","content":"Async agent launched successfully. (This tool result is internal metadata)\nagentId: af64dff5df2a22785"}]}}`
)

// TestMapProtocolMessage_SidechainFramesSuppressed is the core predicate:
// every frame carrying a non-empty parent_tool_use_id maps to nil.
func TestMapProtocolMessage_SidechainFramesSuppressed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"assistant tool_use", sidechainAssistantToolUse},
		{"assistant text", sidechainAssistantText},
		{"assistant thinking", sidechainAssistantThink},
		{"user tool_result", sidechainUserToolResult},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapProtocolMessage(protoMsgFromRaw(t, tc.raw)); got != nil {
				t.Errorf("sidechain frame mapped to %T (%+v), want nil (suppressed)", got, got)
			}
		})
	}
}

// TestMapProtocolMessage_MainThreadFramesStillRender is the other half of the
// predicate — nothing belonging to the controlling agent may be suppressed.
func TestMapProtocolMessage_MainThreadFramesStillRender(t *testing.T) {
	t.Run("assistant text", func(t *testing.T) {
		got := MapProtocolMessage(protoMsgFromRaw(t, mainThreadAssistantText))
		content, ok := got.(AssistantContentMsg)
		if !ok {
			t.Fatalf("got %T, want AssistantContentMsg", got)
		}
		if len(content.Msgs) != 1 {
			t.Fatalf("Msgs len = %d, want 1", len(content.Msgs))
		}
		txt, ok := content.Msgs[0].(AssistantTextMsg)
		if !ok {
			t.Fatalf("Msgs[0] = %T, want AssistantTextMsg", content.Msgs[0])
		}
		if txt.Text != "MAIN_THREAD_PROSE" {
			t.Errorf("Text = %q, want %q", txt.Text, "MAIN_THREAD_PROSE")
		}
	})

	t.Run("assistant tool_use", func(t *testing.T) {
		got := MapProtocolMessage(protoMsgFromRaw(t, mainThreadToolUse))
		content, ok := got.(AssistantContentMsg)
		if !ok {
			t.Fatalf("got %T, want AssistantContentMsg", got)
		}
		tc, ok := content.Msgs[0].(ToolCallMsg)
		if !ok {
			t.Fatalf("Msgs[0] = %T, want ToolCallMsg", content.Msgs[0])
		}
		if tc.ToolID != "M1" || tc.ToolName != "Bash" {
			t.Errorf("ToolCallMsg = {%q,%q}, want {M1,Bash}", tc.ToolID, tc.ToolName)
		}
		if tc.ParentToolUseID != "" {
			t.Errorf("ParentToolUseID = %q, want empty (main thread)", tc.ParentToolUseID)
		}
	})

	t.Run("user tool_result", func(t *testing.T) {
		got := MapProtocolMessage(protoMsgFromRaw(t, mainThreadUserToolResult))
		tr, ok := got.(ToolResultMsg)
		if !ok {
			t.Fatalf("got %T, want ToolResultMsg", got)
		}
		if tr.ToolID != "M1" {
			t.Errorf("ToolID = %q, want M1", tr.ToolID)
		}
	})
}

// TestMapProtocolMessage_ExplicitNullParentRenders pins the pointer-nil path:
// `"parent_tool_use_id":null` is main-thread, not sidechain.
func TestMapProtocolMessage_ExplicitNullParentRenders(t *testing.T) {
	raw := `{"type":"assistant","parent_tool_use_id":null,"message":{"role":"assistant","content":[{"type":"text","text":"MAIN"}]}}`
	if got := MapProtocolMessage(protoMsgFromRaw(t, raw)); got == nil {
		t.Errorf("explicit-null parent_tool_use_id was suppressed, want rendered")
	}
}

// TestMapProtocolMessage_AgentLaunchAckRenders pins the frame that CLOSES the
// Agent row under QUM-928: the ack is a main-thread tool_result and must map.
func TestMapProtocolMessage_AgentLaunchAckRenders(t *testing.T) {
	got := MapProtocolMessage(protoMsgFromRaw(t, agentLaunchAck))
	tr, ok := got.(ToolResultMsg)
	if !ok {
		t.Fatalf("got %T, want ToolResultMsg (the ack must close the Agent row)", got)
	}
	if tr.ToolID != "a1" {
		t.Errorf("ToolID = %q, want a1", tr.ToolID)
	}
	if tr.IsError {
		t.Errorf("IsError = true, want false (successful launch)")
	}
}

// TestMapProtocolMessage_SidechainUsageSuppressed pins that a suppressed
// sidechain frame does not leak its `usage` block as a SessionUsageMsg. The
// status-bar gauge measures the MAIN thread's context window; a sidechain runs
// in a different context, so folding its usage in would inflate the gauge.
func TestMapProtocolMessage_SidechainUsageSuppressed(t *testing.T) {
	raw := `{"type":"assistant","parent_tool_use_id":"A1","message":{"role":"assistant","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":50000,"output_tokens":10}}}`
	if got := MapProtocolMessage(protoMsgFromRaw(t, raw)); got != nil {
		t.Errorf("sidechain usage frame mapped to %T, want nil (gauge must not inflate)", got)
	}
}

// TestMapProtocolMessage_NonSidechainParentFramesUnaffected guards the trap
// that a "tidy this into one early-return at the switch head" refactor would
// spring — and, unlike a bare tool_progress assertion, it can actually fail.
//
// parent_tool_use_id is NOT exclusively a sidechain marker. tool_progress
// reuses it as an MCP heartbeat backlink, where tool_use_id is
// "<id>-heartbeat-0" and parent_tool_use_id is the tool's OWN id (measured
// 1,011 such frames across 660 wire logs). Hoisting the predicate to the
// MapProtocolMessage switch head would therefore suppress MCP heartbeats along
// with sidechains.
//
// Asserting tool_progress maps to nil proves nothing, because it is unmapped
// today and returns nil under the correct implementation AND under the
// switch-head cheat. So this instead uses system/init — a frame type that
// carries parent_tool_use_id here and DOES map. Under the correct
// (per-frame-type) implementation it still maps; under the switch-head cheat it
// would be suppressed and this fails.
func TestMapProtocolMessage_NonSidechainParentFramesUnaffected(t *testing.T) {
	t.Run("system/init with a parent id still maps", func(t *testing.T) {
		raw := `{"type":"system","subtype":"init","parent_tool_use_id":"toolu_01R4U","model":"claude-opus-4"}`
		got := MapProtocolMessage(protoMsgFromRaw(t, raw))
		sm, ok := got.(SessionModelMsg)
		if !ok {
			t.Fatalf("got %T, want SessionModelMsg — the sidechain predicate must be "+
				"scoped to assistant/user frames, never hoisted to the switch head "+
				"(that would also suppress MCP heartbeats)", got)
		}
		if sm.Model != "claude-opus-4" {
			t.Errorf("Model = %q, want claude-opus-4", sm.Model)
		}
	})

	// Canary, honestly labelled: tool_progress is currently unmapped, so this
	// documents the heartbeat shape for future readers rather than guarding
	// anything. If tool_progress ever becomes mapped, the subtest above is the
	// one carrying the real guarantee.
	t.Run("tool_progress heartbeat is unmapped (canary, not a guard)", func(t *testing.T) {
		raw := `{"type":"tool_progress","tool_use_id":"toolu_01R4U-heartbeat-0","tool_name":"mcp__sprawl__ask_user_question","parent_tool_use_id":"toolu_01R4U","elapsed_time_seconds":30}`
		if got := MapProtocolMessage(protoMsgFromRaw(t, raw)); got != nil {
			t.Errorf("tool_progress mapped to %T, want nil", got)
		}
	})
}

// TestSidechainHatch_RendersChildFrames pins the debug escape hatch. Default
// is suppressed; SPRAWL_SHOW_SIDECHAIN=1 restores full frames.
//
// NOTE: sidechainVisible is read ONCE at package init, so t.Setenv does NOT
// work here — the test flips the package var directly and restores it.
func TestSidechainHatch_RendersChildFrames(t *testing.T) {
	withSidechainVisible(t, true)

	got := MapProtocolMessage(protoMsgFromRaw(t, sidechainAssistantToolUse))
	content, ok := got.(AssistantContentMsg)
	if !ok {
		t.Fatalf("with the hatch on, got %T, want AssistantContentMsg", got)
	}
	tc, ok := content.Msgs[0].(ToolCallMsg)
	if !ok {
		t.Fatalf("Msgs[0] = %T, want ToolCallMsg", content.Msgs[0])
	}
	if tc.ParentToolUseID != "A1" {
		t.Errorf("ParentToolUseID = %q, want A1 (attribution preserved under the hatch)", tc.ParentToolUseID)
	}
}

func TestSidechainHatch_DefaultIsSuppressed(t *testing.T) {
	if sidechainVisible {
		t.Errorf("sidechainVisible = true by default; suppression must be the default")
	}
}

// TestMapProtocolMessage_TaskNotificationRendersNothingLive pins the deletion
// of the LIVE ↻ auto-continued marker.
//
// Two independently-measured facts force this. (1) QUM-914 routed any
// task_notification carrying a tool_use_id to TaskCompletedMsg, on the premise
// that such a notification is a sidechain completion. Across 660 wire logs ALL
// 4,014 task_notification frames carry one, and only 8% originate from Agent —
// 79% are FOREGROUND Bash. So the premise is false and that branch swallowed
// every notification. (2) More decisively: QUM-929 DELETED sprawl's
// [auto-continue] injection, so sprawl no longer auto-continues anything. A
// marker reading "↻ auto-continued" on a foreground Bash completion would
// assert something that never happened — strictly worse than the dead code it
// replaced, since dead code renders nothing while this would render a
// falsehood. And the wire offers no field to scope it to background tasks, so
// a narrower variant is unimplementable.
//
// The REPLAY marker is deliberately kept — see
// TestReplayAutoTriggerMarkerStillLives.
func TestMapProtocolMessage_TaskNotificationRendersNothingLive(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"foreground Bash with summary", `{"type":"system","subtype":"task_notification","task_id":"bo7k03pr2","tool_use_id":"toolu_01PALF5VqFiwhceDfW3pdCNG","status":"completed","summary":"Build and install sprawl binary"}`},
		{"empty summary", `{"type":"system","subtype":"task_notification","tool_use_id":"toolu_01PALF","status":"completed","summary":""}`},
		{"no tool_use_id", `{"type":"system","subtype":"task_notification","status":"completed","summary":"something finished"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MapProtocolMessage(protoMsgFromRaw(t, tc.raw)); got != nil {
				t.Errorf("task_notification mapped to %T, want nil — sprawl no longer "+
					"auto-continues (QUM-929), so there is no live event to mark", got)
			}
		})
	}
}

// TestReplayAutoTriggerMarkerStillLives pins the other side of that deletion:
// historical wire logs DO contain real [auto-continue] injections, so the
// replay classifier and the marker item must stay. Verified against real data
// by QA: 53 markers across 11 logs, 0 mis-renders. A reader will reasonably
// ask why replay renders a marker the live path doesn't — because historical
// sessions contain real injections and new ones cannot.
func TestReplayAutoTriggerMarkerStillLives(t *testing.T) {
	cl := newTestChatList()
	cl.SetSize(80)
	cl.Reset([]MessageEntry{
		{Type: MessageUser, Content: "do a thing"},
		{Type: MessageAutoTrigger, Content: "background task finished"},
		{Type: MessageAssistant, Content: "resuming", Complete: true},
	})
	found := false
	for i := range cl.items {
		if _, ok := cl.items[i].item.(*AutoTriggerItem); ok {
			found = true
		}
	}
	if !found {
		t.Errorf("replayed MessageAutoTrigger produced no AutoTriggerItem; the "+
			"historical-log marker path must survive the live-path deletion (items: %d)", cl.Len())
	}
}

// TestNoSidechainInnerContentLeaks is a belt-and-braces sweep: no suppressed
// frame's distinctive payload may appear in any mapped output.
func TestNoSidechainInnerContentLeaks(t *testing.T) {
	for _, raw := range []string{
		sidechainAssistantToolUse, sidechainAssistantText,
		sidechainAssistantThink, sidechainUserToolResult,
	} {
		got := MapProtocolMessage(protoMsgFromRaw(t, raw))
		rendered := fmtMsg(got)
		for _, marker := range []string{"SIDECHAIN_INNER_PROSE", "SIDECHAIN_INNER_RESULT", "sidechain-only.go"} {
			if strings.Contains(rendered, marker) {
				t.Errorf("suppressed frame leaked %q into %T: %s", marker, got, rendered)
			}
		}
	}
}

// applyMapped routes one mapped tea.Msg into a ChatList the way AppModel's
// reducers do. Deliberately minimal — it covers exactly the msg kinds this
// scenario produces, so an unexpected kind fails loudly instead of being
// silently swallowed (which would let a suppression bug look like a pass).
func applyMapped(t *testing.T, cl *ChatList, m any) {
	t.Helper()
	switch v := m.(type) {
	case nil:
		return // suppressed
	case AssistantContentMsg:
		for _, inner := range v.Msgs {
			applyMapped(t, cl, inner)
		}
	case AssistantTextMsg:
		cl.AppendAssistantChunk(v.Text)
	case ThinkingMsg:
		cl.AppendThinking()
	case ToolCallMsg:
		cl.AppendToolCallWithHeader(v.ToolName, v.ToolID, v.Approved,
			v.Input, v.FullInput, v.HeaderArg, v.HeaderParams, v.ParentToolUseID)
	case ToolResultMsg:
		_ = cl.MarkToolResult(v.ToolID, v.Content, v.IsError)
	case SessionUsageMsg:
		return // status-bar only, not a chat item
	default:
		t.Fatalf("unexpected mapped msg %T (%+v) — the applier must be explicit", m, m)
	}
}

// TestThreeConcurrentSidechains_OnlyAgentRowsRender is the headline AC, end to
// end through the real mapping layer into a real ChatList: three concurrent
// sidechains produce exactly three Agent rows and NOTHING else, while the
// controlling agent's own interleaved activity renders untouched.
//
// This composes the two halves the other tests cover separately (mapping →
// nil, and ChatList lifecycle), which is what makes it able to catch several
// wrong implementations at once: suppressing at the switch head, keying on the
// tool name instead of parent_tool_use_id, per-block instead of per-frame
// suppression, and any Idle()/pendingTools miscount.
//
// Frame order mirrors real arrival: the sidechains' internals interleave
// between the controlling agent's own frames, not after them.
func TestThreeConcurrentSidechains_OnlyAgentRowsRender(t *testing.T) {
	frames := []string{
		// weave launches 3 Explore agents in ONE assistant message.
		`{"type":"assistant","message":{"role":"assistant","content":[
			{"type":"tool_use","id":"a1","name":"Agent","input":{"description":"Explore alpha"}},
			{"type":"tool_use","id":"a2","name":"Agent","input":{"description":"Explore bravo"}},
			{"type":"tool_use","id":"a3","name":"Agent","input":{"description":"Explore charlie"}}]}}`,
		// Three immediate launch acks (main-thread user frames).
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a1","content":"Async agent launched successfully."}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a2","content":"Async agent launched successfully."}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a3","content":"Async agent launched successfully."}]}}`,
		// --- sidechain internals from all three, interleaved. All suppressed. ---
		`{"type":"assistant","parent_tool_use_id":"a1","message":{"role":"assistant","content":[{"type":"thinking","thinking":""}]}}`,
		`{"type":"assistant","parent_tool_use_id":"a2","message":{"role":"assistant","content":[{"type":"tool_use","id":"c2","name":"Grep","input":{"pattern":"SIDECHAIN_INNER_PROSE"}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Read","input":{"file_path":"/tmp/sidechain-only.go"}}]}}`,
		`{"type":"user","parent_tool_use_id":"a2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c2","content":"SIDECHAIN_INNER_RESULT"}]}}`,
		`{"type":"assistant","parent_tool_use_id":"a3","message":{"role":"assistant","content":[{"type":"text","text":"SIDECHAIN_INNER_PROSE"}]}}`,
		`{"type":"user","parent_tool_use_id":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"SIDECHAIN_INNER_RESULT"}]}}`,
		// --- weave's OWN activity, in the middle of the sidechain traffic ---
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"MAIN_THREAD_PROSE"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"m1","name":"Bash","input":{"command":"echo main"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"m1","content":"main output"}]}}`,
		// More sidechain noise after weave's own work.
		`{"type":"assistant","parent_tool_use_id":"a3","message":{"role":"assistant","content":[{"type":"tool_use","id":"c3","name":"Read","input":{"file_path":"/tmp/sidechain-only.go"}}]}}`,
	}

	cl := newTestChatList()
	cl.SetSize(120)
	for i, raw := range frames {
		applyMapped(t, cl, MapProtocolMessage(protoMsgFromRaw(t, raw)))
		if got := cl.OrphanCount(); got != 0 {
			t.Fatalf("after frame %d: OrphanCount() = %d, want 0", i, got)
		}
	}

	// Exactly three Agent rows, all closed by their acks, plus weave's own Bash.
	var agentIDs, toolIDs []string
	for _, it := range cl.Items() {
		if tc, ok := it.(*ToolCallItem); ok {
			toolIDs = append(toolIDs, tc.ToolID())
			if tc.Name() == "Agent" {
				agentIDs = append(agentIDs, tc.ToolID())
			}
			if tc.Depth() != 0 || tc.ParentToolID() != "" {
				t.Errorf("tool row %s is nested (depth=%d parent=%q); no sidechain row may render",
					tc.ToolID(), tc.Depth(), tc.ParentToolID())
			}
			if !tc.Finished() {
				t.Errorf("tool row %s left pending; every row here has its result", tc.ToolID())
			}
		}
	}
	if len(agentIDs) != 3 {
		t.Errorf("Agent rows = %v (%d), want exactly 3", agentIDs, len(agentIDs))
	}
	if len(toolIDs) != 4 {
		t.Errorf("tool rows = %v (%d), want 4 (3 Agent + weave's own Bash)", toolIDs, len(toolIDs))
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		if findToolCallItem(cl, id) != nil {
			t.Errorf("sidechain tool row %s rendered; it must be suppressed", id)
		}
	}

	// Nothing a sidechain produced may appear in the rendered output, and
	// everything weave produced must.
	out := stripANSI(cl.Render(120))
	for _, marker := range []string{"SIDECHAIN_INNER_PROSE", "SIDECHAIN_INNER_RESULT", "sidechain-only.go", "Grep"} {
		if strings.Contains(out, marker) {
			t.Errorf("rendered output leaked sidechain content %q", marker)
		}
	}
	for _, marker := range []string{"MAIN_THREAD_PROSE", "Bash", "Agent"} {
		if !strings.Contains(out, marker) {
			t.Errorf("rendered output is missing the controlling agent's own %q", marker)
		}
	}

	// Spinner accounting: no tool row is left pending. This is the assertion
	// that a suppressed sidechain tool_use cannot strand the spinner — its
	// tool_use never incremented pendingTools and its tool_result never arrives
	// to decrement it, so the two must cancel exactly.
	if cl.HasPendingToolCall() {
		t.Errorf("HasPendingToolCall() = true with every rendered row resolved; " +
			"a suppressed sidechain frame leaked into pendingTools")
	}
	// Idle() is still false here, correctly: streamingAssistant is turn-scoped
	// and this frame sequence has no terminal (no SessionResultMsg), so the turn
	// is genuinely still open. Not a stuck spinner — the turn end clears it.
	if cl.Idle() {
		t.Errorf("Idle() = true before the turn terminal; premature idle")
	}
	cl.FinalizeAssistantMessage()
	if !cl.Idle() {
		t.Errorf("Idle() = false after the turn finalized; spinner would be stuck")
	}
	// weave's own text block was settled by its own following Bash append, so a
	// fully-resolved transcript carries no streaming cursor at all.
	if got := cursorCount(cl, 120); got != 0 {
		t.Errorf("cursor count = %d in a fully settled transcript, want 0", got)
	}
	if got := cl.OrphanCount(); got != 0 {
		t.Errorf("OrphanCount() = %d at end of turn, want 0", got)
	}
}
