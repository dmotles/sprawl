package tui

// QUM-933 app-level guard for the one invariant the settle chokepoint must not
// break: settling an intermediate assistant item mid-turn must NOT clear the
// turn-scoped streamingAssistant flag.
//
// SessionResultMsg (app.go:1523) and InterruptCompletedMsg (app.go:1565) append
// msg.Result as assistant text only when !HasPendingAssistant(), because Claude
// echoes the turn's final text block in result.Result. If a mid-turn settle
// cleared the flag, that guard would open and the last block would render
// twice. The ChatList-level tests assert HasPendingAssistant() directly; this
// pins the user-visible consequence, so the invariant survives someone later
// "cleaning up" the flag.

import "testing"

func assistantItems(app AppModel) []*AssistantTextItem {
	var out []*AssistantTextItem
	for _, it := range app.rootVP().ChatList().Items() {
		if a, ok := it.(*AssistantTextItem); ok {
			out = append(out, a)
		}
	}
	return out
}

func TestAppModel_SettleMidTurn_NoDuplicateResultText(t *testing.T) {
	app := readyAppWithBridge(t, newFakeSessionBackend())
	app.turnState = TurnStreaming

	// text -> tool call (settles the text) -> tool result -> terminal result
	// echoing that same text. The echo must be suppressed.
	updated, _ := app.Update(AssistantTextMsg{Text: "the answer is 42"})
	app = updated.(AppModel)
	updated, _ = app.Update(ToolCallMsg{ToolName: "Read", ToolID: "t1", Approved: true, HeaderArg: "f"})
	app = updated.(AppModel)
	updated, _ = app.Update(ToolResultMsg{ToolID: "t1", Content: "ok"})
	app = updated.(AppModel)

	// The settle must already have landed, without clearing the turn flag.
	items := assistantItems(app)
	if len(items) != 1 {
		t.Fatalf("assistant item count = %d, want 1", len(items))
	}
	if !items[0].Finished() {
		t.Errorf("assistant item not settled by the tool-call append")
	}
	if !app.rootVP().ChatList().HasPendingAssistant() {
		t.Fatalf("mid-turn settle cleared streamingAssistant; the result echo will double-render")
	}

	updated, _ = app.Update(SessionResultMsg{Result: "the answer is 42"})
	app = updated.(AppModel)

	items = assistantItems(app)
	if len(items) != 1 {
		t.Fatalf("assistant item count = %d after SessionResultMsg, want 1 (result text double-rendered)", len(items))
	}
	if got := items[0].Text(); got != "the answer is 42" {
		t.Errorf("assistant text = %q, want %q", got, "the answer is 42")
	}
	if app.rootVP().ChatList().HasPendingAssistant() {
		t.Errorf("HasPendingAssistant() = true after the turn finalized, want false")
	}
	if !app.rootVP().ChatList().Idle() {
		t.Errorf("Idle() = false after the turn finalized, want true (stuck spinner)")
	}
}
