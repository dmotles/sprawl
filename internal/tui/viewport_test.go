package tui

import (
	"strings"
	"testing"
)

func newTestViewportModel(t *testing.T) ViewportModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewViewportModel(&theme)
}

func TestViewportModel_InitialContent(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	view := m.View()
	if len(strings.TrimSpace(view)) == 0 {
		t.Error("View() should not be empty initially")
	}
}

func TestViewportModel_SetContent(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.SetContent("hello world test content")
	view := m.View()
	if !strings.Contains(view, "hello world test content") {
		t.Errorf("View() should contain set content, got:\n%s", view)
	}
}

func TestViewportModel_SetSize(t *testing.T) {
	m := newTestViewportModel(t)
	// Should not panic.
	m.SetSize(80, 30)
}

func TestViewportModel_AppendUserMessage(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendUserMessage("hello")
	view := stripANSI(m.View())
	if !strings.Contains(view, "You:") {
		t.Errorf("View() should contain 'You:' label, got:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Errorf("View() should contain user message text 'hello', got:\n%s", view)
	}
}

func TestViewportModel_AppendAssistantChunk_Streaming(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendAssistantChunk("Hello ")
	m.AppendAssistantChunk("world")
	view := stripANSI(m.View())
	if !strings.Contains(view, "Hello world") {
		t.Errorf("View() should contain concatenated streaming chunks 'Hello world', got:\n%s", view)
	}
}

func TestViewportModel_FinalizeAssistantMessage(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	m.AppendAssistantChunk("first")
	m.FinalizeAssistantMessage()

	m.AppendAssistantChunk("second")
	m.FinalizeAssistantMessage()

	view := stripANSI(m.View())
	if !strings.Contains(view, "first") {
		t.Errorf("View() should contain first finalized message 'first', got:\n%s", view)
	}
	if !strings.Contains(view, "second") {
		t.Errorf("View() should contain second finalized message 'second', got:\n%s", view)
	}
}

func TestViewportModel_AppendToolCall(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	m.AppendToolCall("read_file", true, "")
	view := stripANSI(m.View())
	if !strings.Contains(view, "read_file") {
		t.Errorf("View() should contain approved tool call 'read_file', got:\n%s", view)
	}

	m.AppendToolCall("write_file", false, "")
	view = stripANSI(m.View())
	if !strings.Contains(view, "write_file") {
		t.Errorf("View() should contain unapproved tool call 'write_file', got:\n%s", view)
	}
}

func TestViewportModel_AppendToolCall_WithInput(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)

	m.AppendToolCall("Bash", true, "ls -la /tmp")
	view := stripANSI(m.View())
	if !strings.Contains(view, "Bash") {
		t.Errorf("View() should contain tool name 'Bash', got:\n%s", view)
	}
	if !strings.Contains(view, "ls -la /tmp") {
		t.Errorf("View() should contain tool input 'ls -la /tmp', got:\n%s", view)
	}
}

func TestViewportModel_AppendStatus(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendStatus("connecting...")
	view := stripANSI(m.View())
	if !strings.Contains(view, "connecting...") {
		t.Errorf("View() should contain status text 'connecting...', got:\n%s", view)
	}
}

func TestViewportModel_AppendError(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendError("connection failed")
	view := stripANSI(m.View())
	if !strings.Contains(view, "connection failed") {
		t.Errorf("View() should contain error text 'connection failed', got:\n%s", view)
	}
}

func TestViewportModel_MixedMessages(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 40) // taller to fit all messages

	m.AppendUserMessage("what is sprawl?")
	m.AppendAssistantChunk("Sprawl is a tool")
	m.FinalizeAssistantMessage()
	m.AppendToolCall("read_file", true, "")
	m.AppendStatus("processing...")
	m.AppendError("timeout occurred")

	view := stripANSI(m.View())

	expected := []string{
		"what is sprawl?",
		"Sprawl is a tool",
		"read_file",
		"processing...",
		"timeout occurred",
	}
	for _, text := range expected {
		if !strings.Contains(view, text) {
			t.Errorf("View() should contain %q in mixed messages, got:\n%s", text, view)
		}
	}

	// Verify ordering: each expected text should appear after the previous one.
	lastIdx := -1
	for _, text := range expected {
		idx := strings.Index(view, text)
		if idx <= lastIdx {
			t.Errorf("expected %q (at index %d) to appear after previous text (at index %d)",
				text, idx, lastIdx)
		}
		lastIdx = idx
	}
}

func TestViewportModel_SetSizeRerendersMessages(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendUserMessage("persistent message")

	// Resize to different dimensions.
	m.SetSize(80, 30)

	view := stripANSI(m.View())
	if !strings.Contains(view, "persistent message") {
		t.Errorf("View() should still contain message after SetSize, got:\n%s", view)
	}
}

func TestViewportModel_FinalizeNoOp(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	// Finalize with no messages at all -- should not panic.
	m.FinalizeAssistantMessage()

	// Finalize after an already-finalized message -- should not panic.
	m.AppendAssistantChunk("done")
	m.FinalizeAssistantMessage()
	m.FinalizeAssistantMessage()
}

func TestViewportModel_StreamingCursor(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	m.AppendAssistantChunk("thinking")
	view := m.View()
	if !strings.Contains(view, StreamingCursor) {
		t.Errorf("View() should contain streaming cursor %q while streaming, got:\n%s", StreamingCursor, view)
	}

	m.FinalizeAssistantMessage()
	view = m.View()
	if strings.Contains(view, StreamingCursor) {
		t.Errorf("View() should not contain streaming cursor %q after finalize, got:\n%s", StreamingCursor, view)
	}
}

func TestViewportModel_AutoScrollDefault(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	if !m.IsAutoScroll() {
		t.Error("new viewport should have auto-scroll enabled by default")
	}
}

func TestViewportModel_NewContentIndicator(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 5) // small height to force overflow

	// Add enough content to overflow the viewport.
	for i := 0; i < 20; i++ {
		m.AppendUserMessage("line of content that overflows the viewport")
	}

	// Simulate auto-scroll being disabled (user scrolled up).
	m.SetAutoScroll(false)

	// Append new content while scrolled up.
	m.AppendUserMessage("new content while scrolled up")

	view := m.View()
	if !strings.Contains(view, NewContentIndicator) {
		t.Errorf("View() should contain new-content indicator %q when auto-scroll is off and new content exists, got:\n%s",
			NewContentIndicator, view)
	}
}

// --- Tests for QUM-200 5c: Viewport GetMessages/SetMessages ---

func TestViewportModel_GetMessages(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	// Initially empty.
	msgs := m.GetMessages()
	if len(msgs) != 0 {
		t.Errorf("GetMessages() on new viewport = %d entries, want 0", len(msgs))
	}

	// Add some messages.
	m.AppendUserMessage("hello")
	m.AppendAssistantChunk("world")
	m.FinalizeAssistantMessage()

	msgs = m.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("GetMessages() = %d entries, want 2", len(msgs))
	}
	if msgs[0].Type != MessageUser || msgs[0].Content != "hello" {
		t.Errorf("msgs[0] = {Type:%v, Content:%q}, want {Type:MessageUser, Content:hello}", msgs[0].Type, msgs[0].Content)
	}
	if msgs[1].Type != MessageAssistant || msgs[1].Content != "world" {
		t.Errorf("msgs[1] = {Type:%v, Content:%q}, want {Type:MessageAssistant, Content:world}", msgs[1].Type, msgs[1].Content)
	}
}

func TestViewportModel_SetMessages(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	// Add initial content.
	m.AppendUserMessage("original message")

	// Replace with new messages.
	newMsgs := []MessageEntry{
		{Type: MessageUser, Content: "restored question", Complete: true},
		{Type: MessageAssistant, Content: "restored answer", Complete: true},
	}
	m.SetMessages(newMsgs)

	// GetMessages should return the new messages.
	got := m.GetMessages()
	if len(got) != 2 {
		t.Fatalf("GetMessages() after SetMessages = %d entries, want 2", len(got))
	}
	if got[0].Content != "restored question" {
		t.Errorf("got[0].Content = %q, want %q", got[0].Content, "restored question")
	}
	if got[1].Content != "restored answer" {
		t.Errorf("got[1].Content = %q, want %q", got[1].Content, "restored answer")
	}

	// View should render the new messages (strip ANSI since glamour adds escape codes).
	view := stripAnsi(m.View())
	if !strings.Contains(view, "restored question") {
		t.Errorf("View() should contain 'restored question' after SetMessages, got:\n%s", view)
	}
	if !strings.Contains(view, "restored answer") {
		t.Errorf("View() should contain 'restored answer' after SetMessages, got:\n%s", view)
	}
	if strings.Contains(view, "original message") {
		t.Errorf("View() should not contain 'original message' after SetMessages, got:\n%s", view)
	}
}
