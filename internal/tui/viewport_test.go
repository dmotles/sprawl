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
	view := m.View()
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
	view := m.View()
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

	view := m.View()
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

	m.AppendToolCall("read_file", true)
	view := m.View()
	if !strings.Contains(view, "read_file") {
		t.Errorf("View() should contain approved tool call 'read_file', got:\n%s", view)
	}

	m.AppendToolCall("write_file", false)
	view = m.View()
	if !strings.Contains(view, "write_file") {
		t.Errorf("View() should contain unapproved tool call 'write_file', got:\n%s", view)
	}
}

func TestViewportModel_AppendStatus(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendStatus("connecting...")
	view := m.View()
	if !strings.Contains(view, "connecting...") {
		t.Errorf("View() should contain status text 'connecting...', got:\n%s", view)
	}
}

func TestViewportModel_AppendError(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendError("connection failed")
	view := m.View()
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
	m.AppendToolCall("read_file", true)
	m.AppendStatus("processing...")
	m.AppendError("timeout occurred")

	view := m.View()

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
			t.Errorf("expected %q (at index %d) to appear after previous text (at index %d) in view:\n%s",
				text, idx, lastIdx, view)
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

	view := m.View()
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
