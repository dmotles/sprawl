package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func newTestViewportModel(t *testing.T) ViewportModel {
	t.Helper()
	theme := NewTheme("colour212")
	return NewViewportModel(&theme)
}

// TestViewportModel_SoftWrapPreventsHorizontalScroll guards the fix for the
// 2026-04-22 unreadable-viewport incident: default bubbles/v2 viewport binds
// `l`/`→` to bump xOffset, and rendering then shows only the tail half of
// every line (ansi.Cut from xOffset to xOffset+width). SoftWrap must be
// enabled so SetXOffset is a no-op and xOffset can never drift.
func TestViewportModel_SoftWrapPreventsHorizontalScroll(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	if !m.vp.SoftWrap {
		t.Fatal("viewport.SoftWrap must be true; a false value re-enables horizontal scroll which mangles rendering on stray l/right key presses")
	}
	// SetXOffset is a no-op when SoftWrap is true; a non-zero xOffset would
	// indicate the guard broke.
	m.vp.SetXOffset(30)
	if m.vp.XOffset() != 0 {
		t.Errorf("xOffset = %d, want 0 — SoftWrap must force xOffset to stay 0", m.vp.XOffset())
	}
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

	m.AppendToolCall("read_file", "", true, "", "")
	view := stripANSI(m.View())
	if !strings.Contains(view, "read_file") {
		t.Errorf("View() should contain approved tool call 'read_file', got:\n%s", view)
	}

	m.AppendToolCall("write_file", "", false, "", "")
	view = stripANSI(m.View())
	if !strings.Contains(view, "write_file") {
		t.Errorf("View() should contain unapproved tool call 'write_file', got:\n%s", view)
	}
}

func TestViewportModel_AppendToolCall_WithInput(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)

	m.AppendToolCall("Bash", "", true, "ls -la /tmp", "")
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
	m.AppendToolCall("read_file", "", true, "", "")
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

// --- Tests for QUM-281: Viewport selection & yank ---

func TestViewportModel_EnterSelectDisablesAutoScroll(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendAssistantChunk("one")
	m.FinalizeAssistantMessage()
	m.AppendAssistantChunk("two")
	m.FinalizeAssistantMessage()

	if !m.IsAutoScroll() {
		t.Fatalf("precondition: auto-scroll should be on before EnterSelect")
	}
	m.EnterSelect()
	if !m.IsSelecting() {
		t.Error("IsSelecting() should be true after EnterSelect")
	}
	if m.IsAutoScroll() {
		t.Error("EnterSelect should disable auto-scroll")
	}
}

func TestViewportModel_EnterSelectNoOpWhenEmpty(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.EnterSelect()
	if m.IsSelecting() {
		t.Error("EnterSelect on empty buffer should not enter select mode")
	}
}

func TestViewportModel_ExitSelect(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendAssistantChunk("x")
	m.FinalizeAssistantMessage()
	m.EnterSelect()
	m.ExitSelect()
	if m.IsSelecting() {
		t.Error("IsSelecting() should be false after ExitSelect")
	}
}

func TestViewportModel_SelectedRawAssistantVerbatim(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendAssistantChunk("# Title\n\nprose")
	m.FinalizeAssistantMessage()
	m.EnterSelect()
	got := m.SelectedRaw()
	if got != "# Title\n\nprose" {
		t.Errorf("SelectedRaw() = %q, want raw markdown verbatim", got)
	}
}

func TestViewportModel_SelectedRawEmptyWhenNotSelecting(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendAssistantChunk("x")
	m.FinalizeAssistantMessage()
	if m.SelectedRaw() != "" {
		t.Error("SelectedRaw() should be empty when not selecting")
	}
}

func TestViewportModel_MoveCursorExtendsSelection(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 40)
	m.AppendAssistantChunk("first")
	m.FinalizeAssistantMessage()
	m.AppendAssistantChunk("second")
	m.FinalizeAssistantMessage()
	m.AppendAssistantChunk("third")
	m.FinalizeAssistantMessage()

	m.EnterSelect() // cursor starts at last (index 2)
	m.MoveCursor(-2)
	got := m.SelectedRaw()
	// Selection should span all three messages.
	for _, want := range []string{"first", "second", "third"} {
		if !strings.Contains(got, want) {
			t.Errorf("SelectedRaw() after MoveCursor(-2) should contain %q, got %q", want, got)
		}
	}
}

// QUM-324: a long single-line tool input (e.g. compact JSON from
// mcp__sprawl__spawn) must not bleed past the viewport width.
// Rendered via renderToolCall directly so the assertion is independent of
// the bubbles viewport's scroll/crop behaviour.
func TestViewportModel_RenderToolCall_LongInputClipped(t *testing.T) {
	const width = 40
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: strings.Repeat("xyz", 200), // 600 chars, far wider than 40
	})
	for _, line := range strings.Split(sb.String(), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("rendered line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
}

// QUM-324 (weave follow-up): multi-line tool-result stdout (e.g. `make
// validate` output surfaced as a Bash tool_result) should wrap, not
// truncate — every logical line must appear under the `│ ` gutter and
// each wrapped display line must fit the viewport width.
func TestViewportModel_RenderToolCall_MultilineInputWrapped(t *testing.T) {
	const width = 40
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)

	input := "make validate\nfmt OK\nlint OK\n" + strings.Repeat("a", 120)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: input,
	})

	out := sb.String()
	// Every display line must fit.
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("rendered line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
	// Every logical input line should still be present (wrapping, not
	// dropping, is the contract for multi-line tool bodies).
	stripped := stripANSI(out)
	for _, frag := range []string{"make validate", "fmt OK", "lint OK"} {
		if !strings.Contains(stripped, frag) {
			t.Errorf("expected %q in wrapped tool-call body, got:\n%s", frag, stripped)
		}
	}
	// Every rendered body line should start with the `│ ` gutter so the
	// left edge lines up under the `┌ ... ` header.
	for _, line := range strings.Split(out, "\n") {
		plain := stripANSI(line)
		if plain == "" {
			continue
		}
		if strings.HasPrefix(plain, "┌") || strings.HasPrefix(plain, "└") {
			continue
		}
		if !strings.HasPrefix(plain, "│ ") {
			t.Errorf("tool-call body line missing `│ ` gutter: %q", plain)
		}
	}
}

// QUM-335: when the viewport's expand-tool-inputs flag is on, renderToolCall
// substitutes the truncated summary with the full multi-line body and every
// logical line still appears under the `│ ` gutter, wrapped to width.
func TestViewportModel_RenderToolCall_ExpandedRendersFullInput(t *testing.T) {
	const width = 40
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)
	m.SetToolInputsExpanded(true)

	short := "ls -la /tmp"
	full := "find /var/log -type f -name '*.log' -mtime -7 -size +1M\nsort\nuniq -c"

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:          MessageToolCall,
		Content:       "Bash",
		Complete:      true,
		Approved:      true,
		ToolInput:     short,
		ToolInputFull: full,
	})

	out := sb.String()
	stripped := stripANSI(out)
	if strings.Contains(stripped, short) {
		t.Errorf("expanded render should not include the truncated summary %q, got:\n%s", short, stripped)
	}
	for _, frag := range []string{"find /var/log", "sort", "uniq -c"} {
		if !strings.Contains(stripped, frag) {
			t.Errorf("expanded render missing %q, got:\n%s", frag, stripped)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("expanded render line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
	// Body lines must still wear the `│ ` gutter (QUM-324).
	for _, line := range strings.Split(out, "\n") {
		plain := stripANSI(line)
		if plain == "" {
			continue
		}
		if strings.HasPrefix(plain, "┌") || strings.HasPrefix(plain, "└") {
			continue
		}
		if !strings.HasPrefix(plain, "│ ") {
			t.Errorf("expanded body line missing `│ ` gutter: %q", plain)
		}
	}
}

// QUM-335: when the expand flag is on but the entry has no FullInput
// (legacy / unparseable input), renderToolCall must still fall back to the
// truncated summary instead of dropping the body.
func TestViewportModel_RenderToolCall_ExpandedFallsBackWhenFullEmpty(t *testing.T) {
	const width = 40
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)
	m.SetToolInputsExpanded(true)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: "ls -la",
		// ToolInputFull intentionally empty.
	})
	if !strings.Contains(stripANSI(sb.String()), "ls -la") {
		t.Errorf("expected fallback to truncated summary, got:\n%s", stripANSI(sb.String()))
	}
}

// QUM-335: AppendToolCall must store both the summary and the full input on
// the resulting MessageEntry so the global toggle can swap between them.
func TestViewportModel_AppendToolCall_StoresFullInput(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)

	m.AppendToolCall("Bash", "", true, "ls", "ls -la /tmp")
	msgs := m.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].ToolInput != "ls" {
		t.Errorf("ToolInput = %q, want %q", msgs[0].ToolInput, "ls")
	}
	if msgs[0].ToolInputFull != "ls -la /tmp" {
		t.Errorf("ToolInputFull = %q, want %q", msgs[0].ToolInputFull, "ls -la /tmp")
	}
}

// QUM-335: SetToolInputsExpanded flips the viewport's expand flag and
// triggers a re-render so existing tool-call entries flip to/from their
// full body without needing new input.
func TestViewportModel_SetToolInputsExpanded_TogglesRender(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)
	m.AppendToolCall("Bash", "", true, "short", "this is the full bash command being expanded")

	if m.ToolInputsExpanded() {
		t.Fatal("expanded flag should default to false")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "short") {
		t.Errorf("expected truncated summary 'short' before toggle, got:\n%s", view)
	}

	m.SetToolInputsExpanded(true)
	if !m.ToolInputsExpanded() {
		t.Fatal("expanded flag should be true after SetToolInputsExpanded(true)")
	}
	view = stripANSI(m.View())
	if !strings.Contains(view, "this is the full bash command being expanded") {
		t.Errorf("expected full input after toggle, got:\n%s", view)
	}
	if strings.Contains(view, "│ short") {
		t.Errorf("expected truncated summary suppressed after toggle, got:\n%s", view)
	}

	m.SetToolInputsExpanded(false)
	view = stripANSI(m.View())
	if !strings.Contains(view, "│ short") {
		t.Errorf("expected truncated summary back after toggling off, got:\n%s", view)
	}
}

// QUM-343: when the expand flag is on, renderToolCall renders the FULL tool
// result (every non-empty line) under the `│ ` gutter instead of the 3-line
// preview + `+ N more lines` trailer.
func TestViewportModel_RenderToolCall_ExpandedRendersFullResult(t *testing.T) {
	const width = 60
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)
	m.SetToolInputsExpanded(true)

	// 6 non-empty result lines — well past the 3-line preview cap.
	result := "line-one\nline-two\nline-three\nline-four\nline-five\nline-six"

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:     MessageToolCall,
		Content:  "Bash",
		Complete: true,
		Approved: true,
		Result:   result,
	})

	stripped := stripANSI(sb.String())
	for _, frag := range []string{"line-one", "line-two", "line-three", "line-four", "line-five", "line-six"} {
		if !strings.Contains(stripped, frag) {
			t.Errorf("expanded render missing result line %q, got:\n%s", frag, stripped)
		}
	}
	if strings.Contains(stripped, "more lines") {
		t.Errorf("expanded render must not include `+ N more lines` trailer, got:\n%s", stripped)
	}
	for _, line := range strings.Split(sb.String(), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("expanded result line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
	for _, line := range strings.Split(sb.String(), "\n") {
		plain := stripANSI(line)
		if plain == "" {
			continue
		}
		if strings.HasPrefix(plain, "┌") || strings.HasPrefix(plain, "└") {
			continue
		}
		if !strings.HasPrefix(plain, "│ ") {
			t.Errorf("expanded result line missing `│ ` gutter: %q", plain)
		}
	}
}

// QUM-343: when the expand flag is OFF (default), renderToolCall still
// honours the 3-line preview + `+ N more lines` trailer for tool results.
func TestViewportModel_RenderToolCall_CollapsedPreservesPreviewTrailer(t *testing.T) {
	const width = 60
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)

	result := "r1\nr2\nr3\nr4\nr5"

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:     MessageToolCall,
		Content:  "Bash",
		Complete: true,
		Approved: true,
		Result:   result,
	})

	stripped := stripANSI(sb.String())
	if !strings.Contains(stripped, "+ 2 more lines") {
		t.Errorf("collapsed render should include `+ 2 more lines` trailer, got:\n%s", stripped)
	}
	if strings.Contains(stripped, "r4") || strings.Contains(stripped, "r5") {
		t.Errorf("collapsed render should not include lines past the 3-line preview, got:\n%s", stripped)
	}
}

// QUM-343: SetToolInputsExpanded re-renders existing tool-call entries so a
// completed tool result flips between 3-line preview and full output.
func TestViewportModel_SetToolInputsExpanded_TogglesResultRender(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 30)
	m.AppendToolCall("Bash", "tool-1", true, "ls", "ls -la")
	m.MarkToolResult("tool-1", "out-1\nout-2\nout-3\nout-4\nout-5", false)

	view := stripANSI(m.View())
	if !strings.Contains(view, "+ 2 more lines") {
		t.Errorf("expected `+ 2 more lines` trailer before toggle, got:\n%s", view)
	}
	if strings.Contains(view, "out-5") {
		t.Errorf("expected line past preview cap to be hidden before toggle, got:\n%s", view)
	}

	m.SetToolInputsExpanded(true)
	view = stripANSI(m.View())
	if strings.Contains(view, "more lines") {
		t.Errorf("expected `more lines` trailer to disappear after toggle, got:\n%s", view)
	}
	for _, frag := range []string{"out-1", "out-2", "out-3", "out-4", "out-5"} {
		if !strings.Contains(view, frag) {
			t.Errorf("expected full result line %q after toggle, got:\n%s", frag, view)
		}
	}

	m.SetToolInputsExpanded(false)
	view = stripANSI(m.View())
	if !strings.Contains(view, "+ 2 more lines") {
		t.Errorf("expected `+ 2 more lines` trailer back after toggling off, got:\n%s", view)
	}
}

// QUM-338: AppendSystemMessage adds a MessageSystem entry to the buffer with
// Complete=true, mirroring AppendUserMessage but typed as a system message so
// downstream renderers (and AssembleRawMarkdown skip lists) can distinguish it
// from human-typed input.
func TestViewportModel_AppendSystemMessage_AppendsEntry(t *testing.T) {
	theme := NewTheme("")
	m := NewViewportModel(&theme)
	m.AppendSystemMessage("hello")
	if m.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", m.Len())
	}
	entries := m.GetMessages()
	if entries[0].Type != MessageSystem {
		t.Errorf("entries[0].Type = %v, want MessageSystem", entries[0].Type)
	}
	if entries[0].Content != "hello" {
		t.Errorf("entries[0].Content = %q, want %q", entries[0].Content, "hello")
	}
	if !entries[0].Complete {
		t.Errorf("entries[0].Complete = false, want true")
	}
}

// QUM-338: A rendered system message must include the mail glyph "✉" and the
// content text, distinguishing it from regular user/assistant/status entries.
func TestViewportModel_RenderMessages_SystemMessageIncludesMailGlyph(t *testing.T) {
	theme := NewTheme("")
	m := NewViewportModel(&theme)
	m.SetSize(80, 20)
	m.AppendSystemMessage("hello")
	view := stripANSI(m.View())
	if !strings.Contains(view, "✉") {
		t.Errorf("View() should contain mail glyph '✉' for system message, got:\n%s", view)
	}
	if !strings.Contains(view, "hello") {
		t.Errorf("View() should contain system message content 'hello', got:\n%s", view)
	}
}

// QUM-338: a system message must render with a distinct ANSI style from a user
// message — the SystemText style (purple) plus the ✉ glyph differs from the
// AccentText "You: " label, so raw ANSI output must differ.
func TestViewportModel_RenderMessages_SystemMessageDistinctStyleFromUser(t *testing.T) {
	themeA := NewTheme("")
	mSys := NewViewportModel(&themeA)
	mSys.SetSize(80, 20)
	mSys.AppendSystemMessage("abc")

	themeB := NewTheme("")
	mUser := NewViewportModel(&themeB)
	mUser.SetSize(80, 20)
	mUser.AppendUserMessage("abc")

	if mSys.View() == mUser.View() {
		t.Errorf("system and user messages should render to distinct ANSI output, but both produced:\n%s", mSys.View())
	}
}

// --- Tests for QUM-379: Nest sub-agent tool calls under parent Agent tool call ---

// TestViewportModel_AppendToolCall_AgentNesting_SetsDepth verifies that tool
// calls appended after an "Agent" tool call get Depth 1, and after the Agent
// result arrives, subsequent tool calls get Depth 0 again.
func TestViewportModel_AppendToolCall_AgentNesting_SetsDepth(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "agent-1", true, "sub-task", "")
	m.AppendToolCall("Bash", "bash-1", true, "ls", "")

	msgs := m.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[1].Depth != 1 {
		t.Errorf("Bash Depth = %d, want 1 (nested under Agent)", msgs[1].Depth)
	}

	m.MarkToolResult("agent-1", "done", false)
	m.AppendToolCall("Read", "read-1", true, "/tmp/x", "")

	msgs = m.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if msgs[2].Depth != 0 {
		t.Errorf("Read Depth = %d, want 0 (Agent popped)", msgs[2].Depth)
	}
}

// TestViewportModel_AppendToolCall_NestedAgentDepth2 verifies two levels of
// Agent nesting: Agent inside Agent gives Depth 2.
func TestViewportModel_AppendToolCall_NestedAgentDepth2(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "outer", "")
	m.AppendToolCall("Agent", "a2", true, "inner", "")
	m.AppendToolCall("Bash", "b1", true, "ls", "")

	msgs := m.GetMessages()
	if msgs[2].Depth != 2 {
		t.Errorf("Bash Depth = %d, want 2 (nested under two Agents)", msgs[2].Depth)
	}

	m.MarkToolResult("a2", "inner done", false)
	m.AppendToolCall("Read", "r1", true, "/tmp/y", "")

	msgs = m.GetMessages()
	if msgs[3].Depth != 1 {
		t.Errorf("Read Depth = %d, want 1 (inner Agent popped)", msgs[3].Depth)
	}

	m.MarkToolResult("a1", "outer done", false)
	m.AppendToolCall("Write", "w1", true, "/tmp/z", "")

	msgs = m.GetMessages()
	if msgs[4].Depth != 0 {
		t.Errorf("Write Depth = %d, want 0 (both Agents popped)", msgs[4].Depth)
	}
}

// TestViewportModel_RenderToolCall_NestedCompactFormat verifies that a nested
// tool call (Depth > 0) renders in a compact format: no ┌ or └ box-drawing,
// contains the │ gutter for indentation, and contains the tool name and input.
func TestViewportModel_RenderToolCall_NestedCompactFormat(t *testing.T) {
	const width = 80
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: "ls",
		Depth:     1,
	})

	out := sb.String()
	stripped := stripANSI(out)

	// Compact: should NOT have ┌ or └ box drawing.
	if strings.Contains(stripped, "┌") {
		t.Errorf("nested (Depth=1) tool call should not contain ┌, got:\n%s", stripped)
	}
	if strings.Contains(stripped, "└") {
		t.Errorf("nested (Depth=1) tool call should not contain └, got:\n%s", stripped)
	}

	// Should contain tool name and input.
	if !strings.Contains(stripped, "Bash") {
		t.Errorf("nested render missing tool name 'Bash', got:\n%s", stripped)
	}
	if !strings.Contains(stripped, "ls") {
		t.Errorf("nested render missing tool input 'ls', got:\n%s", stripped)
	}

	// Should contain │ gutter for indentation.
	if !strings.Contains(stripped, "│") {
		t.Errorf("nested render should contain │ gutter, got:\n%s", stripped)
	}

	// Every rendered line must fit within viewport width.
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("rendered line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
}

// TestViewportModel_RenderToolCall_NestedDepth2Indent verifies that Depth 2
// entries are indented more than Depth 1 entries.
func TestViewportModel_RenderToolCall_NestedDepth2Indent(t *testing.T) {
	const width = 80
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)

	var sb1 strings.Builder
	m.renderToolCall(&sb1, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: "ls",
		Depth:     1,
	})

	var sb2 strings.Builder
	m.renderToolCall(&sb2, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: "ls",
		Depth:     2,
	})

	stripped1 := stripANSI(sb1.String())
	stripped2 := stripANSI(sb2.String())

	// Depth 2 output should have more leading whitespace or gutter characters
	// than Depth 1. We compare the first non-empty line of each.
	firstLine := func(s string) string {
		for _, ln := range strings.Split(s, "\n") {
			if strings.TrimSpace(ln) != "" {
				return ln
			}
		}
		return ""
	}
	line1 := firstLine(stripped1)
	line2 := firstLine(stripped2)

	// Count leading indentation characters (spaces, │, etc.) before the tool
	// name appears.
	indent := func(line, marker string) int {
		idx := strings.Index(line, marker)
		if idx < 0 {
			return 0
		}
		return idx
	}
	i1 := indent(line1, "Bash")
	i2 := indent(line2, "Bash")
	if i2 <= i1 {
		t.Errorf("Depth 2 indent (%d) should exceed Depth 1 indent (%d);\nDepth1: %q\nDepth2: %q", i2, i1, line1, line2)
	}
}

// TestViewportModel_RenderMessages_NoBlankLineBetweenNestedEntries asserts that
// two consecutive nested tool calls (Depth > 0) do not have a double-newline
// gap between them in the rendered output.
func TestViewportModel_RenderMessages_NoBlankLineBetweenNestedEntries(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.SetMessages([]MessageEntry{
		{Type: MessageToolCall, Content: "Agent", Complete: true, Approved: true, ToolID: "a1", Depth: 0},
		{Type: MessageToolCall, Content: "Bash", Complete: true, Approved: true, ToolInput: "ls", Depth: 1},
		{Type: MessageToolCall, Content: "Read", Complete: true, Approved: true, ToolInput: "/tmp/x", Depth: 1},
	})

	rendered := m.renderMessages()
	stripped := stripANSI(rendered)

	// Find the Bash and Read entries in the output and check there's no blank
	// line between them.
	bashIdx := strings.Index(stripped, "Bash")
	readIdx := strings.Index(stripped, "Read")
	if bashIdx < 0 || readIdx < 0 {
		t.Fatalf("expected both 'Bash' and 'Read' in rendered output, got:\n%s", stripped)
	}
	between := stripped[bashIdx:readIdx]
	if strings.Contains(between, "\n\n") {
		t.Errorf("should not have double-newline between nested entries, got:\n%s", stripped)
	}
}

// TestViewportModel_RenderToolCall_NestedPendingShowsSpinner verifies that a
// nested (Depth > 0) pending tool call renders with a spinner frame.
func TestViewportModel_RenderToolCall_NestedPendingShowsSpinner(t *testing.T) {
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(80, 20)
	m.SetSpinnerFrame("⠋")

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: "ls",
		Pending:   true,
		Depth:     1,
	})

	stripped := stripANSI(sb.String())
	if !strings.Contains(stripped, "⠋") {
		t.Errorf("nested pending tool call should contain spinner frame '⠋', got:\n%s", stripped)
	}
}

// TestViewportModel_RenderToolCall_NestedFailedShowsX verifies that a nested
// (Depth > 0) failed tool call renders with the ✗ indicator.
func TestViewportModel_RenderToolCall_NestedFailedShowsX(t *testing.T) {
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(80, 20)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		Approved:  true,
		ToolInput: "ls",
		Failed:    true,
		Depth:     1,
	})

	stripped := stripANSI(sb.String())
	if !strings.Contains(stripped, "✗") {
		t.Errorf("nested failed tool call should contain '✗', got:\n%s", stripped)
	}
}

// TestViewportModel_AgentToolCallItselfHasDepth0 verifies that an "Agent" tool
// call appended at the top level gets Depth 0 (it's the parent, not nested).
func TestViewportModel_AgentToolCallItselfHasDepth0(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)

	m.AppendToolCall("Agent", "a1", true, "task", "")
	msgs := m.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Depth != 0 {
		t.Errorf("Agent tool call Depth = %d, want 0", msgs[0].Depth)
	}
}

// TestViewportModel_SetMessages_ClearsAgentStack verifies that SetMessages
// resets the agent call stack so subsequent AppendToolCall calls start at
// Depth 0 regardless of prior state.
func TestViewportModel_SetMessages_ClearsAgentStack(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)

	m.AppendToolCall("Agent", "a1", true, "task", "")
	// After appending Agent, the stack should have a1.
	// SetMessages clears everything.
	m.SetMessages(nil)

	m.AppendToolCall("Bash", "b1", true, "ls", "")
	msgs := m.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Depth != 0 {
		t.Errorf("Bash Depth = %d, want 0 (stack should have been cleared by SetMessages)", msgs[0].Depth)
	}
}

// QUM-324: a tool name containing lots of junk (or otherwise long) must not
// bleed past the viewport width in the header row either.
func TestViewportModel_RenderToolCall_LongNameHeaderClipped(t *testing.T) {
	const width = 20
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:     MessageToolCall,
		Content:  strings.Repeat("mcp__sprawl_ops__spawn__", 5),
		Complete: true,
		Approved: true,
	})
	for _, line := range strings.Split(sb.String(), "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("header line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
}
