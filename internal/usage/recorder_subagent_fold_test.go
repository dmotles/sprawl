package usage

import (
	"testing"

	"github.com/dmotles/sprawl/internal/protocol"
	"github.com/dmotles/sprawl/internal/state"
)

// TestRecorder_SubagentFramesFoldIntoParentRecord covers QUM-368 AC §7:
// assistant frames whose inner JSON includes parent_tool_use_id (i.e.
// dispatched via Claude's Agent / Task tool inside the parent process)
// still accumulate into the parent's single record under the parent's
// agent_name. There is no per-subagent file.
func TestRecorder_SubagentFramesFoldIntoParentRecord(t *testing.T) {
	tmp := t.TempDir()
	if err := state.SaveAgent(tmp, &state.AgentState{
		Name:   "tower",
		Type:   "weave",
		Status: "active",
	}); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	rec, err := NewRecorder(tmp, "tower")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer rec.Close()

	sessionID := "sess-sub"
	parentTool := "toolu_abc"

	// Outer (parent) frame.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 10, OutputTokens: 20}, "claude-opus-4-7"))
	// Subagent-dispatched inner frame (has parent_tool_use_id).
	rec.Handle(assistantEventWithParent(t, sessionID, protocol.Usage{InputTokens: 100, OutputTokens: 200}, "claude-opus-4-7", &parentTool))
	// Another outer continuation.
	rec.Handle(assistantEvent(t, sessionID, protocol.Usage{InputTokens: 5, OutputTokens: 5}, "claude-opus-4-7"))
	rec.Handle(turnCompletedEvent(sessionID, 0.05))

	if err := rec.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	records := readNDJSONLines(t, usageLogPath(tmp, "tower", sessionID))
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1 (subagent frames must fold)", len(records))
	}
	r := records[0]
	if r.AgentName != "tower" {
		t.Errorf("AgentName = %q, want tower (subagent must record under parent name)", r.AgentName)
	}
	if r.InputTokens != 115 {
		t.Errorf("InputTokens = %d, want 115 (10+100+5)", r.InputTokens)
	}
	if r.OutputTokens != 225 {
		t.Errorf("OutputTokens = %d, want 225 (20+200+5)", r.OutputTokens)
	}
}
