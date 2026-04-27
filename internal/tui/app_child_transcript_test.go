package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/state"
)

// writeChildSessionFixture creates a temp sprawlRoot + homeDir, writes an
// AgentState JSON for `name`, and writes a JSONL session log at the path
// LoadChildTranscript would resolve. Returns (sprawlRoot, homeDir, sessionID).
func writeChildSessionFixture(t *testing.T, name string, jsonlLines []string) (string, string, string) {
	t.Helper()
	sprawlRoot := t.TempDir()
	homeDir := t.TempDir()
	worktree := filepath.Join(sprawlRoot, "worktree-"+name)
	sessionID := "11111111-2222-3333-4444-555555555555"

	agent := &state.AgentState{
		Name:      name,
		Type:      "engineer",
		Family:    "engineer",
		Parent:    "weave",
		Branch:    "x",
		Worktree:  worktree,
		Status:    "running",
		CreatedAt: "2026-04-25T09:00:00Z",
		SessionID: sessionID,
	}
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if jsonlLines != nil {
		path := memory.SessionLogPath(homeDir, worktree, sessionID)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		content := strings.Join(jsonlLines, "\n")
		if len(jsonlLines) > 0 {
			content += "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return sprawlRoot, homeDir, sessionID
}

func newAppForChildTranscript(t *testing.T, sprawlRoot, homeDir string) AppModel {
	t.Helper()
	m := NewAppModel("colour212", "testrepo", "v0.1.0", nil, nil, sprawlRoot, nil)
	m.SetHomeDir(homeDir)
	resized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return resized.(AppModel)
}

func findChildTranscriptMsg(msgs []tea.Msg) (ChildTranscriptMsg, bool) {
	for _, m := range msgs {
		if c, ok := m.(ChildTranscriptMsg); ok {
			return c, true
		}
	}
	return ChildTranscriptMsg{}, false
}

func TestAgentSelectedMsg_NonRoot_DispatchesChildTranscriptMsg(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-04-25T10:00:00Z","message":{"role":"user","content":"hello finn"}}`,
		`{"type":"assistant","timestamp":"2026-04-25T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"working"}]}}`,
	}
	sprawlRoot, homeDir, sessionID := writeChildSessionFixture(t, "finn", lines)
	app := newAppForChildTranscript(t, sprawlRoot, homeDir)

	_, cmd := app.Update(AgentSelectedMsg{Name: "finn"})
	if cmd == nil {
		t.Fatal("AgentSelectedMsg for non-root should return a Cmd")
	}
	msgs := collectBatchMsgs(t, cmd)
	got, ok := findChildTranscriptMsg(msgs)
	if !ok {
		t.Fatalf("expected ChildTranscriptMsg in batch, got %v", msgs)
	}
	if got.Agent != "finn" {
		t.Errorf("ChildTranscriptMsg.Agent = %q, want %q", got.Agent, "finn")
	}
	if got.SessionID != sessionID {
		t.Errorf("ChildTranscriptMsg.SessionID = %q, want %q", got.SessionID, sessionID)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2; entries=%+v", len(got.Entries), got.Entries)
	}
	if got.Entries[0].Type != MessageUser || got.Entries[0].Content != "hello finn" {
		t.Errorf("Entries[0] = %+v", got.Entries[0])
	}
	if got.Entries[1].Type != MessageAssistant || got.Entries[1].Content != "working" {
		t.Errorf("Entries[1] = %+v", got.Entries[1])
	}
}

func TestAgentSelectedMsg_NonRoot_NoSessionID_EmitsEmptyTranscriptMsg(t *testing.T) {
	sprawlRoot := t.TempDir()
	homeDir := t.TempDir()
	agent := &state.AgentState{
		Name:      "finn",
		Type:      "engineer",
		Family:    "engineer",
		Parent:    "weave",
		Worktree:  filepath.Join(sprawlRoot, "wt"),
		Status:    "spawned",
		CreatedAt: "2026-04-25T09:00:00Z",
		// SessionID intentionally empty
	}
	if err := state.SaveAgent(sprawlRoot, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	app := newAppForChildTranscript(t, sprawlRoot, homeDir)

	_, cmd := app.Update(AgentSelectedMsg{Name: "finn"})
	msgs := collectBatchMsgs(t, cmd)
	got, ok := findChildTranscriptMsg(msgs)
	if !ok {
		t.Fatalf("expected ChildTranscriptMsg even without session_id, got %v", msgs)
	}
	if len(got.Entries) != 0 {
		t.Errorf("Entries should be empty when session_id missing; got %+v", got.Entries)
	}
	if got.SessionID != "" {
		t.Errorf("SessionID should be empty; got %q", got.SessionID)
	}
}

func TestChildTranscriptMsg_PopulatesViewport(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	// Switch observed to finn so the msg is not dropped as stale.
	app.observedAgent = "finn"
	entries := []MessageEntry{
		{Type: MessageUser, Content: "hi", Complete: true},
		{Type: MessageAssistant, Content: "hello back", Complete: true},
	}
	updated, _ := app.Update(ChildTranscriptMsg{Agent: "finn", SessionID: "sid", Entries: entries})
	app = updated.(AppModel)

	got := app.viewportFor("finn").GetMessages()
	if len(got) != 2 {
		t.Fatalf("len(viewport) = %d, want 2; got %+v", len(got), got)
	}
	if got[0].Content != "hi" || got[1].Content != "hello back" {
		t.Errorf("viewport entries = %+v, want hi/hello back", got)
	}
}

func TestChildTranscriptMsg_EmptyEntries_ShowsWaitingBanner(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	app.observedAgent = "finn"
	updated, _ := app.Update(ChildTranscriptMsg{Agent: "finn", Entries: nil})
	app = updated.(AppModel)

	got := app.viewportFor("finn").GetMessages()
	found := false
	for _, e := range got {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Waiting for finn") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'Waiting for finn' status entry, got %+v", got)
	}
}

func TestChildTranscriptMsg_EmptyEntries_Idempotent(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	app.observedAgent = "finn"
	for i := 0; i < 3; i++ {
		updated, _ := app.Update(ChildTranscriptMsg{Agent: "finn", Entries: nil})
		app = updated.(AppModel)
	}
	got := app.viewportFor("finn").GetMessages()
	count := 0
	for _, e := range got {
		if e.Type == MessageStatus && strings.Contains(e.Content, "Waiting for finn") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("waiting banner repeated %d times, want exactly 1; viewport=%+v", count, got)
	}
}

func TestChildTranscriptMsg_StaleAgent_NoViewportMutation(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	// observedAgent stays as default "weave".
	before := app.viewportFor("weave").GetMessages()
	updated, _ := app.Update(ChildTranscriptMsg{
		Agent:   "finn",
		Entries: []MessageEntry{{Type: MessageUser, Content: "leaked", Complete: true}},
	})
	app = updated.(AppModel)
	// QUM-334: writing to finn's per-agent vp is correct — the guarantee is
	// that weave's vp is not polluted.
	got := app.viewportFor("weave").GetMessages()
	for _, e := range got {
		if e.Content == "leaked" {
			t.Errorf("stale ChildTranscriptMsg leaked into weave viewport; got %+v", got)
		}
	}
	if len(got) != len(before) {
		t.Errorf("weave viewport len changed (%d → %d) for stale msg", len(before), len(got))
	}
}

func TestAgentSelectedMsg_NonRoot_PopulatesAgentBuffer(t *testing.T) {
	lines := []string{
		`{"type":"user","timestamp":"2026-04-25T10:00:00Z","message":{"role":"user","content":"q"}}`,
	}
	sprawlRoot, homeDir, sessionID := writeChildSessionFixture(t, "finn", lines)
	app := newAppForChildTranscript(t, sprawlRoot, homeDir)

	updated, cmd := app.Update(AgentSelectedMsg{Name: "finn"})
	app = updated.(AppModel)
	msgs := collectBatchMsgs(t, cmd)
	tm, ok := findChildTranscriptMsg(msgs)
	if !ok {
		t.Fatalf("expected ChildTranscriptMsg; got %v", msgs)
	}
	updated, _ = app.Update(tm)
	app = updated.(AppModel)

	buf, ok := app.agentBuffers["finn"]
	if !ok {
		t.Fatalf("agentBuffers[finn] should be populated")
	}
	if buf.SessionID != sessionID {
		t.Errorf("buf.SessionID = %q, want %q", buf.SessionID, sessionID)
	}
	bufMsgs := buf.vp.GetMessages()
	if len(bufMsgs) != 1 || bufMsgs[0].Content != "q" {
		t.Errorf("buf.vp messages = %+v, want one entry 'q'", bufMsgs)
	}
}

func TestAgentSelectedMsg_BackToRoot_RestoresBufferNoTranscriptCmd(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	// Seed a weave buffer.
	app.viewportFor("weave").AppendUserMessage("weave-msg")
	app.observedAgent = "finn"
	app.viewportFor("finn").SetMessages([]MessageEntry{{Type: MessageAssistant, Content: "finn-stuff", Complete: true}})

	_, cmd := app.Update(AgentSelectedMsg{Name: "weave"})
	msgs := collectBatchMsgs(t, cmd)
	if _, ok := findChildTranscriptMsg(msgs); ok {
		t.Errorf("ChildTranscriptMsg should NOT be dispatched for root agent; got msgs=%v", msgs)
	}
}

func TestChildTranscriptMsg_ReschedulesWhileObserved(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	app.observedAgent = "finn"
	// Set tick interval to ~0 so we don't actually sleep.
	app.SetChildTranscriptTick(1)
	_, cmd := app.Update(ChildTranscriptMsg{Agent: "finn", Entries: nil})
	if cmd == nil {
		t.Fatal("ChildTranscriptMsg should reschedule a follow-up tick while observed")
	}
}

func TestChildTranscriptMsg_RootAgent_NoReschedule(t *testing.T) {
	app := newAppForChildTranscript(t, t.TempDir(), t.TempDir())
	// observedAgent default = weave.
	_, cmd := app.Update(ChildTranscriptMsg{Agent: "weave", Entries: nil})
	// For root, no reschedule expected.
	if cmd != nil {
		// It may legitimately return nil — but if non-nil, it must NOT yield
		// another ChildTranscriptMsg.
		msgs := collectBatchMsgs(t, cmd)
		if _, ok := findChildTranscriptMsg(msgs); ok {
			t.Errorf("root ChildTranscriptMsg should not reschedule; got %v", msgs)
		}
	}
}
