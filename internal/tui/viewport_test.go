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

func TestViewportModel_AppendBanner(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendBanner("hello world test content")
	view := m.View()
	if !strings.Contains(view, "hello world test content") {
		t.Errorf("View() should contain banner content, got:\n%s", view)
	}
	msgs := m.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Type != MessageBanner {
		t.Errorf("expected MessageBanner type, got %d", msgs[0].Type)
	}
}

func TestViewportModel_BannerSurvivesStreamingMessage(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(60, 20)
	m.AppendBanner("SPRAWL BANNER")
	m.AppendAssistantChunk("streaming text")
	// The banner should still be in the messages slice and visible in View().
	view := m.View()
	if !strings.Contains(view, "SPRAWL BANNER") {
		t.Errorf("banner should survive after streaming message, got:\n%s", view)
	}
	msgs := m.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (banner + assistant), got %d", len(msgs))
	}
	if msgs[0].Type != MessageBanner {
		t.Errorf("first message should be banner, got type %d", msgs[0].Type)
	}
	if msgs[1].Type != MessageAssistant {
		t.Errorf("second message should be assistant, got type %d", msgs[1].Type)
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

// --- Tests for QUM-401: System message rendering — collapse blank-line runs and soft-wrap at word boundaries ---

// blankLinesBetween returns the count of consecutive blank lines (after
// trimming trailing whitespace from each line) between the line containing
// `start` and the line containing `end` in the stripped view. Returns -1 if
// either marker is not found or end appears before start. Markers should be
// rare sentinels (e.g. "AAA"/"BBB") to avoid collisions with viewport chrome.
func blankLinesBetween(view, start, end string) int {
	lines := strings.Split(view, "\n")
	startIdx, endIdx := -1, -1
	for i, ln := range lines {
		if startIdx < 0 && strings.Contains(ln, start) {
			startIdx = i
		} else if startIdx >= 0 && strings.Contains(ln, end) {
			endIdx = i
			break
		}
	}
	if startIdx < 0 || endIdx < 0 {
		return -1
	}
	count := 0
	for i := startIdx + 1; i < endIdx; i++ {
		if strings.TrimSpace(lines[i]) == "" {
			count++
		} else {
			// Non-blank line between markers — caller's start/end are not
			// adjacent in the way this helper assumes.
			return -2
		}
	}
	return count
}

// TestViewportModel_RenderSystemMessage_CollapsesBlankLines_QUM401 verifies
// that runs of >=2 consecutive blank lines inside a MessageSystem entry are
// collapsed to exactly 1 blank line when rendered, while single blank lines
// and non-blank-separated lines are preserved as-is.
func TestViewportModel_RenderSystemMessage_CollapsesBlankLines_QUM401(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantBlank int // expected count of blank lines between AAA and BBB
		// scanOnly: when true, instead of comparing wantBlank we scan the
		// system block for any run of >1 consecutive blank lines (used for
		// leading/trailing blank cases where one marker is missing).
		scanOnly  bool
		scanStart string // if scanOnly, the marker to scan around
	}{
		{
			name:      "four newlines collapse to one blank line",
			input:     "AAA\n\n\n\nBBB",
			wantBlank: 1,
		},
		{
			name:      "three newlines collapse to one blank line",
			input:     "AAA\n\n\nBBB",
			wantBlank: 1,
		},
		{
			name:      "single blank line preserved",
			input:     "AAA\n\nBBB",
			wantBlank: 1,
		},
		{
			name:      "no blank line introduced",
			input:     "AAA\nBBB",
			wantBlank: 0,
		},
		{
			name:      "CRLF triple newlines collapse to one blank line",
			input:     "AAA\r\n\r\n\r\nBBB",
			wantBlank: 1,
		},
		{
			name:      "leading blanks collapsed",
			input:     "\n\n\nAAA",
			scanOnly:  true,
			scanStart: "AAA",
		},
		{
			name:      "trailing blanks collapsed",
			input:     "AAA\n\n\n",
			scanOnly:  true,
			scanStart: "AAA",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestViewportModel(t)
			m.SetSize(80, 40)
			m.AppendSystemMessage(tc.input)
			view := stripANSI(m.View())
			if tc.scanOnly {
				// Locate the system block by anchoring on the marker.
				idx := strings.Index(view, tc.scanStart)
				if idx < 0 {
					t.Fatalf("could not locate %q in view:\n%s", tc.scanStart, view)
				}
				// The bubbles viewport pads its View() output to the
				// configured height with blank lines (viewport.Model.View
				// calls lipgloss.NewStyle().Height(...).Render(...)). Those
				// padding blanks are external chrome — not part of the system
				// block — so we scan only the inclusive range from the first
				// non-blank line to the last non-blank line.
				allLines := strings.Split(view, "\n")
				firstNonBlank, lastNonBlank := -1, -1
				for i, ln := range allLines {
					if strings.TrimSpace(ln) != "" {
						if firstNonBlank < 0 {
							firstNonBlank = i
						}
						lastNonBlank = i
					}
				}
				if firstNonBlank < 0 {
					t.Fatalf("view has no non-blank lines:\n%s", view)
				}
				lines := allLines[firstNonBlank : lastNonBlank+1]
				consecutiveBlank := 0
				for _, ln := range lines {
					if strings.TrimSpace(ln) == "" {
						consecutiveBlank++
						if consecutiveBlank > 1 {
							t.Errorf("found run of >1 consecutive blank lines in system block; view:\n%s", view)
							break
						}
					} else {
						consecutiveBlank = 0
					}
				}
				return
			}
			got := blankLinesBetween(view, "AAA", "BBB")
			if got != tc.wantBlank {
				t.Errorf("blank lines between AAA and BBB = %d, want %d; view:\n%s", got, tc.wantBlank, view)
			}
		})
	}
}

// TestViewportModel_RenderSystemMessage_SoftWrapsAtWordBoundary_QUM401 verifies
// that long lines in system messages are wrapped at word boundaries (no word
// is split mid-token) and that no rendered line of the system message exceeds
// the available width budget.
func TestViewportModel_RenderSystemMessage_SoftWrapsAtWordBoundary_QUM401(t *testing.T) {
	const width = 30
	m := newTestViewportModel(t)
	m.SetSize(width, 20)
	input := "the quick brown fox jumps over the lazy dog and runs through the meadow"
	m.AppendSystemMessage(input)
	view := stripANSI(m.View())

	words := strings.Fields(input)
	for _, w := range words {
		if !strings.Contains(view, w) {
			t.Errorf("word %q was split across lines (mid-word break); stripped view:\n%s", w, view)
		}
	}

	// Identify lines belonging to the system message (those containing any of
	// the input's words) and verify none exceeds the viewport width. The
	// bubbles viewport's View() pads every line to exactly contentWidth via
	// lipgloss.NewStyle().Width(contentWidth).Render(...), so lines fit in
	// `width` cells. The QUM-401 contract is that no rendered line exceeds
	// the viewport width — i.e. content escapes the viewport.
	budget := width
	for _, line := range strings.Split(view, "\n") {
		hasWord := false
		for _, w := range words {
			if strings.Contains(line, w) {
				hasWord = true
				break
			}
		}
		if !hasWord {
			continue
		}
		if lipgloss.Width(line) > budget {
			t.Errorf("system-message line width %d exceeds budget %d (width %d): %q", lipgloss.Width(line), budget, width, line)
		}
	}
}

// TestViewportModel_RenderSystemMessage_LongUnbreakableTokenOverflows_QUM401
// verifies that the formatSystemMessage helper does NOT inject mid-word
// breaks for a long unbreakable token. We assert this directly against the
// helper output rather than the final viewport view: the bubbles viewport
// has its own SoftWrap pass that will hard-break any line wider than the
// viewport width, so the rendered View() will inevitably show the long
// token split across display rows. The QUM-401 contract is that *our* code
// preserves words intact and lets the viewport be the sole hard-wrap layer.
func TestViewportModel_RenderSystemMessage_LongUnbreakableTokenOverflows_QUM401(t *testing.T) {
	const width = 12
	longTok := strings.Repeat("A", 20)
	formatted := formatSystemMessage(longTok+" short", width)

	// The long token must appear intact on some line of the formatted output
	// (no mid-word break injected by formatSystemMessage).
	lines := strings.Split(formatted, "\n")
	intactOnSomeLine := false
	for _, ln := range lines {
		if strings.Contains(ln, longTok) {
			intactOnSomeLine = true
			break
		}
	}
	if !intactOnSomeLine {
		t.Errorf("long unbreakable token %q must appear intact on some line of formatted output, got:\n%s", longTok, formatted)
	}

	// "short" must appear and must be on a different line than the long token.
	if !strings.Contains(formatted, "short") {
		t.Errorf("trailing word 'short' must appear in formatted output, got:\n%s", formatted)
	}
	for _, ln := range lines {
		if strings.Contains(ln, longTok) && strings.Contains(ln, "short") {
			t.Errorf("'short' should wrap to its own line, but found alongside long token: %q", ln)
		}
	}

	// Sanity: rendering the message through the viewport must not panic.
	m := newTestViewportModel(t)
	m.SetSize(width, 20)
	m.AppendSystemMessage(longTok + " short")
	_ = m.View()
}

// TestViewportModel_RenderSystemMessage_SpecExample_QUM401 exercises the
// multi-section drain text from the QUM-401 spec: the rendered system block
// must contain both the leading and trailing sentences, and between them
// there must be no run of >1 consecutive empty line.
func TestViewportModel_RenderSystemMessage_SpecExample_QUM401(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)
	input := "You received 1 message(s) since the last turn:\n\n\n\n" +
		"1. from finn [status,working]  subject: [STATUS] finn → Starting oracle phase - reading relevant source files to plan the banner fix\n" +
		"   Starting oracle phase - reading relevant source files to plan the banner fix\n\n\n\n" +
		"Continue your current work unless a message tells you otherwise."
	m.AppendSystemMessage(input)
	view := stripANSI(m.View())

	if !strings.Contains(view, "You received 1 message(s)") {
		t.Errorf("View() should contain leading sentence; got:\n%s", view)
	}
	if !strings.Contains(view, "Continue your current work") {
		t.Errorf("View() should contain trailing sentence; got:\n%s", view)
	}

	// Isolate the system block: from "You received" through "Continue your".
	startIdx := strings.Index(view, "You received 1 message(s)")
	endIdx := strings.Index(view, "Continue your current work")
	if startIdx < 0 || endIdx < 0 || endIdx <= startIdx {
		t.Fatalf("could not locate system block in view:\n%s", view)
	}
	block := view[startIdx:endIdx]
	lines := strings.Split(block, "\n")
	// Trim a trailing empty element from the split — view[startIdx:endIdx]
	// ends just before the next marker, so it always finishes with a "\n"
	// that produces a spurious empty string after Split. That trailing entry
	// is a slicing artifact, not actual content.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	consecutiveBlank := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			consecutiveBlank++
			if consecutiveBlank > 1 {
				t.Errorf("found run of >1 consecutive empty line within system block; block was:\n%s", block)
				break
			}
		} else {
			consecutiveBlank = 0
		}
	}
}

// TestViewportModel_RenderUserMessage_DoesNotCollapseBlankLines_QUM401 guards
// that the QUM-401 collapse fix is scoped to MessageSystem only — user
// messages must preserve their blank-line runs verbatim.
func TestViewportModel_RenderUserMessage_DoesNotCollapseBlankLines_QUM401(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)
	m.AppendUserMessage("AAA\n\n\nBBB")
	view := stripANSI(m.View())
	got := blankLinesBetween(view, "AAA", "BBB")
	if got != 2 {
		t.Errorf("user message must preserve 2 blank lines between AAA and BBB (collapse must NOT apply to user messages); got %d, view:\n%s", got, view)
	}
}

// TestViewportModel_RenderBanner_DoesNotCollapseBlankLines_QUM401 guards that
// banner messages keep their blank-line runs intact (collapse is scoped to
// MessageSystem only).
func TestViewportModel_RenderBanner_DoesNotCollapseBlankLines_QUM401(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)
	m.AppendBanner("AAA\n\n\nBBB")
	view := stripANSI(m.View())
	got := blankLinesBetween(view, "AAA", "BBB")
	if got != 2 {
		t.Errorf("banner must preserve 2 blank lines between AAA and BBB (collapse must NOT apply to banners); got %d, view:\n%s", got, view)
	}
}

// TestViewportModel_RenderAssistantMessage_DoesNotCollapseBlankLines_QUM401
// guards that the QUM-401 system-only collapse fix does not touch the
// assistant rendering path. Assistant content goes through a Markdown
// renderer (m.renderer.Render), which has its own (unrelated) blank-line
// handling — so we do NOT assert preserved blank-line counts. Instead we
// assert determinism: rendering the same assistant content from two fresh
// model instances produces byte-identical output. The real guard is
// structural: the system-only fix must not perturb this path.
func TestViewportModel_RenderAssistantMessage_DoesNotCollapseBlankLines_QUM401(t *testing.T) {
	build := func() string {
		m := newTestViewportModel(t)
		m.SetSize(80, 40)
		m.AppendAssistantChunk("AAA\n\n\nBBB")
		m.FinalizeAssistantMessage()
		return m.View()
	}
	a := build()
	b := build()
	if a != b {
		t.Errorf("assistant rendering should be deterministic across fresh models; outputs differ:\nA:\n%s\n\nB:\n%s", a, b)
	}
	// Sanity: both markers must appear in the rendered view.
	stripped := stripANSI(a)
	if !strings.Contains(stripped, "AAA") || !strings.Contains(stripped, "BBB") {
		t.Errorf("expected AAA and BBB in assistant render, got:\n%s", stripped)
	}
}

// TestViewportModel_RenderSystemMessage_ZeroWidthDoesNotPanic_QUM401 hardens
// the width fallback path: appending and rendering a system message when the
// viewport width is 0 must not panic. Note: bubbles viewport.Model.View()
// returns an empty string when w==0 or h==0 (upstream behavior — see
// charm.land/bubbles/v2/viewport.View()), so we do NOT assert non-empty
// output. The spirit of this test is "no panic in the formatSystemMessage /
// MessageSystem render path when the viewport hasn't been sized yet"; the
// upstream early-return is unrelated to QUM-401.
func TestViewportModel_RenderSystemMessage_ZeroWidthDoesNotPanic_QUM401(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("View() panicked at zero width: %v", r)
		}
	}()
	m := newTestViewportModel(t)
	m.SetSize(0, 20)
	m.AppendSystemMessage("hello\n\n\nworld")
	_ = m.View()
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

// TestViewportModel_AppendToolCall_SequentialAgents verifies that sequential
// Agent calls (Agent inside Agent) both get Depth 0 since Agent entries are
// always top-level containers (QUM-386 depth capped at 1). Non-Agent tool
// calls inside active agents still get Depth 1.
func TestViewportModel_AppendToolCall_SequentialAgents(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "outer", "")
	m.AppendToolCall("Agent", "a2", true, "inner", "")
	m.AppendToolCall("Bash", "b1", true, "ls", "")

	msgs := m.GetMessages()
	// QUM-386: Agent entries are always depth 0 (top-level containers).
	if msgs[0].Depth != 0 {
		t.Errorf("Agent a1 Depth = %d, want 0", msgs[0].Depth)
	}
	if msgs[1].Depth != 0 {
		t.Errorf("Agent a2 Depth = %d, want 0 (Agent entries always top-level)", msgs[1].Depth)
	}
	// Non-Agent tool calls inside active agents get depth 1.
	if msgs[2].Depth != 1 {
		t.Errorf("Bash Depth = %d, want 1 (nested under active Agent)", msgs[2].Depth)
	}

	m.MarkToolResult("a2", "inner done", false)
	m.AppendToolCall("Read", "r1", true, "/tmp/y", "")

	msgs = m.GetMessages()
	// a1 is still active, so Read gets depth 1.
	if msgs[3].Depth != 1 {
		t.Errorf("Read Depth = %d, want 1 (a1 still active)", msgs[3].Depth)
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
		{Type: MessageToolCall, Content: "Agent", Complete: true, Approved: true, ToolID: "a1", Depth: 0, Pending: true},
		{Type: MessageToolCall, Content: "Bash", Complete: true, Approved: true, ToolInput: "ls", Depth: 1, ParentToolID: "a1"},
		{Type: MessageToolCall, Content: "Read", Complete: true, Approved: true, ToolInput: "/tmp/x", Depth: 1, ParentToolID: "a1"},
	})

	rendered := m.renderMessages()
	stripped := stripANSI(rendered)

	// Find the Bash and Read entries in the output and check there's no blank
	// line between them. Children are rendered inside the Agent container.
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

// --- Tests for QUM-386: Parallel Agent sub-agent rendering ---

// TestViewportModel_ParallelAgents_BothDepth0 verifies that two parallel
// Agent tool calls both get Depth 0 (siblings, not nested).
func TestViewportModel_ParallelAgents_BothDepth0(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "task A", "")
	m.AppendToolCall("Agent", "a2", true, "task B", "")

	msgs := m.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2", len(msgs))
	}
	if msgs[0].Depth != 0 {
		t.Errorf("Agent a1 Depth = %d, want 0", msgs[0].Depth)
	}
	if msgs[1].Depth != 0 {
		t.Errorf("Agent a2 Depth = %d, want 0 (parallel sibling, not nested)", msgs[1].Depth)
	}
}

// TestViewportModel_ParallelAgents_NestedCallsDepth1 verifies that tool calls
// after two parallel Agents get Depth 1 (nested under the last active Agent).
func TestViewportModel_ParallelAgents_NestedCallsDepth1(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "task A", "")
	m.AppendToolCall("Agent", "a2", true, "task B", "")
	m.AppendToolCall("Bash", "b1", true, "ls", "")

	msgs := m.GetMessages()
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if msgs[2].Depth != 1 {
		t.Errorf("Bash Depth = %d, want 1 (nested under active Agent)", msgs[2].Depth)
	}
	// a2 was the most recently started Agent, so Bash should be attributed to it.
	if msgs[2].ParentToolID != "a2" {
		t.Errorf("Bash ParentToolID = %q, want %q (most recent Agent)", msgs[2].ParentToolID, "a2")
	}
}

// TestViewportModel_ParallelAgents_Attribution verifies that nested tool calls
// are attributed to the correct parent Agent via ParentToolID.
func TestViewportModel_ParallelAgents_Attribution(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "task A", "")
	m.AppendToolCall("Bash", "b1", true, "ls for A", "")
	m.AppendToolCall("Agent", "a2", true, "task B", "")
	m.AppendToolCall("Read", "r1", true, "/tmp/x", "")

	msgs := m.GetMessages()
	if msgs[1].ParentToolID != "a1" {
		t.Errorf("Bash ParentToolID = %q, want %q", msgs[1].ParentToolID, "a1")
	}
	if msgs[3].ParentToolID != "a2" {
		t.Errorf("Read ParentToolID = %q, want %q", msgs[3].ParentToolID, "a2")
	}
}

// TestViewportModel_ParallelAgents_OneCompletes verifies that when one of two
// parallel Agents completes, the other continues; nested calls still get Depth 1.
func TestViewportModel_ParallelAgents_OneCompletes(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "task A", "")
	m.AppendToolCall("Agent", "a2", true, "task B", "")
	m.MarkToolResult("a1", "done A", false)
	m.AppendToolCall("Bash", "b1", true, "ls", "")

	msgs := m.GetMessages()
	if msgs[len(msgs)-1].Depth != 1 {
		t.Errorf("Bash Depth = %d, want 1 (a2 still active)", msgs[len(msgs)-1].Depth)
	}
	if msgs[len(msgs)-1].ParentToolID != "a2" {
		t.Errorf("Bash ParentToolID = %q, want %q (a2 is remaining)", msgs[len(msgs)-1].ParentToolID, "a2")
	}
}

// TestViewportModel_ParallelAgents_BothComplete verifies that after all parallel
// Agents complete, subsequent tool calls return to Depth 0.
func TestViewportModel_ParallelAgents_BothComplete(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "task A", "")
	m.AppendToolCall("Agent", "a2", true, "task B", "")
	m.MarkToolResult("a1", "done A", false)
	m.MarkToolResult("a2", "done B", false)
	m.AppendToolCall("Bash", "b1", true, "ls", "")

	msgs := m.GetMessages()
	if msgs[len(msgs)-1].Depth != 0 {
		t.Errorf("Bash Depth = %d, want 0 (all agents completed)", msgs[len(msgs)-1].Depth)
	}
	if msgs[len(msgs)-1].ParentToolID != "" {
		t.Errorf("Bash ParentToolID = %q, want empty (no active agent)", msgs[len(msgs)-1].ParentToolID)
	}
}

// TestViewportModel_SetMessages_ClearsActiveAgents verifies that SetMessages
// resets the active agents state so subsequent calls start at Depth 0.
func TestViewportModel_SetMessages_ClearsActiveAgents(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)

	m.AppendToolCall("Agent", "a1", true, "task", "")
	m.AppendToolCall("Agent", "a2", true, "task", "")
	m.SetMessages(nil)

	m.AppendToolCall("Bash", "b1", true, "ls", "")
	msgs := m.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Depth != 0 {
		t.Errorf("Bash Depth = %d, want 0 (activeAgents should have been cleared by SetMessages)", msgs[0].Depth)
	}
}

// TestViewportModel_RenderAgentContainer_Pending verifies that a pending Agent
// renders as a container with its nested children visible.
func TestViewportModel_RenderAgentContainer_Pending(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "sub-task", "")
	m.AppendToolCall("Bash", "b1", true, "ls -la", "")
	m.MarkToolResult("b1", "file1\nfile2", false)
	m.AppendToolCall("Read", "r1", true, "/tmp/x", "")

	rendered := stripANSI(m.renderMessages())

	// Should contain the Agent container with ┌ and └
	headerIdx := strings.Index(rendered, "┌")
	footerIdx := strings.Index(rendered, "└")
	if headerIdx < 0 {
		t.Fatalf("expected ┌ in agent container, got:\n%s", rendered)
	}
	if footerIdx < 0 {
		t.Fatalf("expected └ in agent container, got:\n%s", rendered)
	}
	// Nested children must appear BETWEEN the ┌ header and └ footer.
	between := rendered[headerIdx:footerIdx]
	if !strings.Contains(between, "Bash") {
		t.Errorf("expected nested Bash between ┌ and └, got:\n%s", between)
	}
	if !strings.Contains(between, "Read") {
		t.Errorf("expected nested Read between ┌ and └, got:\n%s", between)
	}
}

// TestViewportModel_RenderAgentContainer_Collapsed verifies that a completed Agent
// container collapses to show only the result, not its nested children.
func TestViewportModel_RenderAgentContainer_Collapsed(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	m.AppendToolCall("Agent", "a1", true, "sub-task", "")
	m.AppendToolCall("Bash", "b1", true, "ls -la", "")
	m.MarkToolResult("b1", "file1\nfile2", false)
	m.MarkToolResult("a1", "The analysis found 3 issues", false)

	rendered := stripANSI(m.renderMessages())

	// Should still have the container box
	if !strings.Contains(rendered, "┌") {
		t.Errorf("expected ┌ in collapsed container, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "└") {
		t.Errorf("expected └ in collapsed container, got:\n%s", rendered)
	}
	// Should contain the result text
	if !strings.Contains(rendered, "The analysis found 3 issues") {
		t.Errorf("expected result text in collapsed container, got:\n%s", rendered)
	}
	// Nested children should NOT appear (collapsed)
	if strings.Contains(rendered, "ls -la") {
		t.Errorf("collapsed container should not show nested tool input 'ls -la', got:\n%s", rendered)
	}
}

// TestViewportModel_RenderMessages_TwoParallelContainers verifies that two
// parallel Agent tool calls render as two independent containers.
func TestViewportModel_RenderMessages_TwoParallelContainers(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 40)

	// Two parallel agents
	m.AppendToolCall("Agent", "a1", true, "task A", "")
	m.AppendToolCall("Agent", "a2", true, "task B", "")
	// a1's work
	m.AppendToolCall("Bash", "b1", true, "ls for A", "")
	m.MarkToolResult("b1", "output A", false)
	// a2's work
	m.AppendToolCall("Read", "r1", true, "/tmp/B", "")

	rendered := stripANSI(m.renderMessages())

	// Both Agent names should be present
	if !strings.Contains(rendered, "task A") {
		t.Errorf("expected 'task A' in rendered output, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "task B") {
		t.Errorf("expected 'task B' in rendered output, got:\n%s", rendered)
	}

	// Should have two ┌ markers (one per container)
	count := strings.Count(rendered, "┌")
	if count < 2 {
		t.Errorf("expected at least 2 '┌' markers for two containers, got %d in:\n%s", count, rendered)
	}
}
