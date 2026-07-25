package tui

// QUM-928 — the wire-log rehydrate path must suppress sidechain frames too.
//
// The wire log DOES contain the interleaved sidechain frames (measured: 11,817
// sidechain `assistant` + 8,655 sidechain `user` frames across 660 logs), so
// without this the mess returns on every reload/resync even though the live
// path is clean.
//
// LoadTranscript (root pane) already passed includeSidechain=false. The gap was
// LoadChildTranscript, which passed true — so a child pane's reload showed all
// the sidechain internals. Both legs must now agree, and both must key on the
// same sidechainVisible var as the live mapping so live and replay cannot drift.

import (
	"os"
	"strings"
	"testing"
)

// sidechainWireFixture is one Agent launch + its ack, a sidechain tool call and
// result, a sidechain assistant text block, and the controlling agent's own
// tool call and text. Only the main-thread items may survive.
func sidechainWireFixture(t *testing.T) string {
	t.Helper()
	return writeWireLog(t, []string{
		`{"type":"user","message":{"role":"user","content":"go explore"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"a1","name":"Agent","input":{"description":"Explore"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"a1","content":"Async agent launched successfully.\nagentId: abc123"}]}}`,
		// --- sidechain internals: every one of these must be suppressed ---
		`{"type":"assistant","parent_tool_use_id":"a1","message":{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Read","input":{"file_path":"/tmp/SIDECHAIN_ONLY_PATH.go"}}]}}`,
		`{"type":"user","parent_tool_use_id":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"SIDECHAIN_ONLY_RESULT"}]}}`,
		`{"type":"assistant","parent_tool_use_id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"SIDECHAIN_ONLY_PROSE"}]}}`,
		// --- controlling agent's own activity, interleaved in time ---
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"m1","name":"Bash","input":{"command":"echo MAINTHREAD_MARKER"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"m1","content":"MAINTHREAD_RESULT"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"MAINTHREAD_SUMMARY"}]}}`,
	})
}

// assertNoSidechainLeak checks that no suppressed payload survived anywhere in
// the rehydrated entries, and that nothing is marked as nested.
func assertNoSidechainLeak(t *testing.T, entries []MessageEntry) {
	t.Helper()
	markers := []string{"SIDECHAIN_ONLY_PATH.go", "SIDECHAIN_ONLY_RESULT", "SIDECHAIN_ONLY_PROSE"}
	for i, e := range entries {
		blob := strings.Join([]string{e.Content, e.ToolInput, e.ToolInputFull, e.Result, e.HeaderArg}, "\x00")
		for _, m := range markers {
			if strings.Contains(blob, m) {
				t.Errorf("entries[%d] (%v) leaked sidechain payload %q", i, e.Type, m)
			}
		}
		if e.Depth > 0 {
			t.Errorf("entries[%d] has Depth=%d; no nested entry may survive suppression", i, e.Depth)
		}
		if e.ParentToolID != "" {
			t.Errorf("entries[%d] has ParentToolID=%q; want empty", i, e.ParentToolID)
		}
	}
}

// assertMainThreadSurvived is the positive half — suppression must not eat the
// controlling agent's own activity. Never assert only an absence.
func assertMainThreadSurvived(t *testing.T, entries []MessageEntry) {
	t.Helper()
	var sawAgent, sawBash, sawSummary bool
	for _, e := range entries {
		switch {
		case e.Type == MessageToolCall && e.Content == "Agent" && e.ToolID == "a1":
			sawAgent = true
		case e.Type == MessageToolCall && e.Content == "Bash" && e.ToolID == "m1":
			sawBash = true
		case e.Type == MessageAssistant && strings.Contains(e.Content, "MAINTHREAD_SUMMARY"):
			sawSummary = true
		}
	}
	if !sawAgent {
		t.Error("the Agent tool call itself was suppressed; it is a main-thread frame and must survive")
	}
	if !sawBash {
		t.Error("the controlling agent's own Bash call was suppressed")
	}
	if !sawSummary {
		t.Error("the controlling agent's own assistant text was suppressed")
	}
}

func TestLoadTranscript_SidechainFramesSuppressed(t *testing.T) {
	entries, err := LoadTranscript(sidechainWireFixture(t), ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	assertMainThreadSurvived(t, entries)
	assertNoSidechainLeak(t, entries)
}

// TestLoadChildTranscript_SidechainFramesSuppressed is the leg that fails
// today: LoadChildTranscript passed includeSidechain=true, so a child pane's
// reload reproduced the full interleaved mess. QUM-928 applies to all agent
// windows, root and child alike.
func TestLoadChildTranscript_SidechainFramesSuppressed(t *testing.T) {
	entries, err := LoadChildTranscript(sidechainWireFixture(t), ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadChildTranscript: %v", err)
	}
	assertMainThreadSurvived(t, entries)
	assertNoSidechainLeak(t, entries)
}

// TestLoadTranscript_AgentAckIsCompleteOnReload pins reload parity with the live
// path: the Agent row rehydrates CLOSED (its ack is its result), not pending —
// otherwise a reloaded transcript would show a spinner that can never resolve.
func TestLoadTranscript_AgentAckIsCompleteOnReload(t *testing.T) {
	entries, err := LoadTranscript(sidechainWireFixture(t), ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Type == MessageToolCall && e.ToolID == "a1" {
			found = true
			if e.Pending {
				t.Error("rehydrated Agent row is Pending; the launch ack must close it (QUM-928)")
			}
			if !strings.Contains(e.Result, "launched successfully") {
				t.Errorf("Agent row Result = %q, want the launch ack", e.Result)
			}
		}
	}
	if !found {
		t.Fatal("no Agent tool-call entry found")
	}
}

// TestLoadChildTranscript_MatchesLoadTranscript pins that the two legs cannot
// drift: after QUM-928 they apply identical suppression.
func TestLoadChildTranscript_MatchesLoadTranscript(t *testing.T) {
	path := sidechainWireFixture(t)
	root, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	child, err := LoadChildTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadChildTranscript: %v", err)
	}
	if len(root) != len(child) {
		t.Fatalf("entry count differs: root=%d child=%d (legs must not drift)", len(root), len(child))
	}
	for i := range root {
		if root[i].Type != child[i].Type || root[i].ToolID != child[i].ToolID ||
			root[i].Content != child[i].Content {
			t.Errorf("entries[%d] differ: root=%+v child=%+v", i, root[i], child[i])
		}
	}
}

// TestReplaySidechainHatch_IncludesSidechain is the measurement-validity
// control for the replay leg: the same fixture and the same assertions must
// produce sidechain entries when the hatch is on. If this fails, the zeros
// above prove nothing (the fixture might simply carry no suppressible frames).
func TestReplaySidechainHatch_IncludesSidechain(t *testing.T) {
	withSidechainVisible(t, true)

	entries, err := LoadTranscript(sidechainWireFixture(t), ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	var sawNested, sawPayload bool
	for _, e := range entries {
		if e.Depth > 0 || e.ParentToolID != "" {
			sawNested = true
		}
		blob := strings.Join([]string{e.Content, e.ToolInput, e.ToolInputFull, e.Result}, "\x00")
		if strings.Contains(blob, "SIDECHAIN_ONLY_PATH.go") || strings.Contains(blob, "SIDECHAIN_ONLY_PROSE") {
			sawPayload = true
		}
	}
	if !sawNested {
		t.Error("hatch on: no nested entry produced — the suppression assertions above are not measuring anything")
	}
	if !sawPayload {
		t.Error("hatch on: no sidechain payload produced — fixture carries nothing to suppress")
	}
}

// TestSuppressionIsRenderOnly pins the scope boundary that the rest of this
// file's assertions could otherwise be mistaken for: suppression filters what
// is DISPLAYED, never what is RECORDED or DELIVERED.
//
// The nil-mapping tests prove frames don't reach the chat list. They say
// nothing about the wire log — and the wire log is what the model's context,
// the runtime event path, and every forensic tool are reconstructed from. If
// suppression ever became a drop at the log or event layer, sidechain results
// would stop reaching the controlling agent and it could no longer summarize
// its own sidechains' findings, which is an explicit non-goal.
func TestSuppressionIsRenderOnly(t *testing.T) {
	path := sidechainWireFixture(t)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// Every sidechain frame must still be on disk, verbatim.
	if got := strings.Count(string(raw), "parent_tool_use_id"); got != 3 {
		t.Errorf("wire log carries %d parent_tool_use_id frames, want 3 — suppression "+
			"must not remove frames from the log", got)
	}
	for _, marker := range []string{"SIDECHAIN_ONLY_PATH.go", "SIDECHAIN_ONLY_RESULT", "SIDECHAIN_ONLY_PROSE"} {
		if !strings.Contains(string(raw), marker) {
			t.Errorf("wire log lost sidechain payload %q; the model's context is "+
				"reconstructed from this log", marker)
		}
	}

	// ...while the rendered transcript contains none of them. Same fixture,
	// same run: the log is complete and the render is filtered.
	entries, err := LoadTranscript(path, ReplayMaxMessages)
	if err != nil {
		t.Fatalf("LoadTranscript: %v", err)
	}
	assertNoSidechainLeak(t, entries)
	assertMainThreadSurvived(t, entries)
}

// TestMappingDoesNotMutateFrame pins that suppression is a decision, not an
// in-place edit: a suppressed frame's Raw bytes must be untouched, since the
// same *protocol.Message is observed by the wire logger and the event bus.
func TestMappingDoesNotMutateFrame(t *testing.T) {
	msg := protoMsgFromRaw(t, sidechainAssistantToolUse)
	before := string(msg.Raw)
	beforeType, beforeSub := msg.Type, msg.Subtype

	if got := MapProtocolMessage(msg); got != nil {
		t.Fatalf("precondition: sidechain frame mapped to %T, want nil", got)
	}
	if string(msg.Raw) != before {
		t.Errorf("mapping mutated Raw:\n got %s\nwant %s", msg.Raw, before)
	}
	if msg.Type != beforeType || msg.Subtype != beforeSub {
		t.Errorf("mapping mutated Type/Subtype: got %q/%q want %q/%q",
			msg.Type, msg.Subtype, beforeType, beforeSub)
	}
}
