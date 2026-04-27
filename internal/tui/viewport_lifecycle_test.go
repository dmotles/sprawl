package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// QUM-336: a fresh tool call is in-flight by default — Pending=true, Failed=false,
// Result="". The render shows the spinner frame (a stable ASCII glyph injected
// via SetSpinnerFrame) and not the success/failure glyph.
func TestViewportModel_AppendToolCall_PendingByDefault(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)
	m.SetSpinnerFrame("Z")

	m.AppendToolCall("Bash", "tool-1", true, "ls", "")

	msgs := m.GetMessages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if !msgs[0].Pending {
		t.Errorf("Pending = false, want true on freshly appended tool call")
	}
	if msgs[0].Failed {
		t.Errorf("Failed = true, want false on freshly appended tool call")
	}
	if msgs[0].Result != "" {
		t.Errorf("Result = %q, want empty", msgs[0].Result)
	}
	if msgs[0].ToolID != "tool-1" {
		t.Errorf("ToolID = %q, want %q", msgs[0].ToolID, "tool-1")
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "Z") {
		t.Errorf("pending render should include spinner frame %q, got:\n%s", "Z", view)
	}
	if strings.Contains(view, "✓") || strings.Contains(view, "✗") {
		t.Errorf("pending render should not include ✓/✗, got:\n%s", view)
	}
}

// QUM-336: MarkToolResult on a matching toolID flips Pending=false, Failed=false,
// stores the Result, and returns true.
func TestViewportModel_MarkToolResult_Success(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)
	m.AppendToolCall("Bash", "tool-1", true, "ls", "")

	matched := m.MarkToolResult("tool-1", "file_a.txt\nfile_b.txt", false)
	if !matched {
		t.Fatal("MarkToolResult returned false, want true for matching toolID")
	}
	msgs := m.GetMessages()
	if msgs[0].Pending {
		t.Errorf("Pending = true, want false after MarkToolResult")
	}
	if msgs[0].Failed {
		t.Errorf("Failed = true, want false on success")
	}
	if msgs[0].Result != "file_a.txt\nfile_b.txt" {
		t.Errorf("Result = %q, want %q", msgs[0].Result, "file_a.txt\nfile_b.txt")
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "✓") {
		t.Errorf("completed render should contain ✓, got:\n%s", view)
	}
	if !strings.Contains(view, "file_a.txt") || !strings.Contains(view, "file_b.txt") {
		t.Errorf("result preview missing expected lines, got:\n%s", view)
	}
}

// QUM-336: a failing tool result yields Failed=true and a ✗ glyph in the render.
func TestViewportModel_MarkToolResult_Failure(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)
	m.AppendToolCall("Bash", "tool-1", true, "cat /nope", "")

	matched := m.MarkToolResult("tool-1", "cat: /nope: No such file or directory", true)
	if !matched {
		t.Fatal("MarkToolResult returned false, want true")
	}
	msgs := m.GetMessages()
	if !msgs[0].Failed {
		t.Errorf("Failed = false, want true")
	}
	if msgs[0].Pending {
		t.Errorf("Pending should be false after MarkToolResult")
	}

	view := stripANSI(m.View())
	if !strings.Contains(view, "✗") {
		t.Errorf("failure render should contain ✗, got:\n%s", view)
	}
	if !strings.Contains(view, "No such file") {
		t.Errorf("failure render should include error text, got:\n%s", view)
	}
}

// QUM-336: MarkToolResult with no matching toolID is a no-op and returns false.
func TestViewportModel_MarkToolResult_NoMatch(t *testing.T) {
	m := newTestViewportModel(t)
	m.SetSize(80, 20)
	m.AppendToolCall("Bash", "tool-1", true, "ls", "")

	matched := m.MarkToolResult("tool-OTHER", "irrelevant", false)
	if matched {
		t.Errorf("MarkToolResult returned true for non-matching toolID, want false")
	}
	msgs := m.GetMessages()
	if !msgs[0].Pending {
		t.Errorf("Pending = false, want true (no-op should not have flipped state)")
	}
	if msgs[0].Result != "" {
		t.Errorf("Result = %q, want empty (no-op should not have written)", msgs[0].Result)
	}
}

// QUM-336: result preview shows up to 3 non-empty lines + "+ N more lines"
// trailer when the source has more than 3 non-empty lines.
func TestViewportModel_RenderToolCall_ResultPreview_MoreThan3Lines(t *testing.T) {
	const width = 80
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		Complete:  true,
		ToolID:    "t-1",
		ToolInput: "ls",
		Result:    "alpha\nbeta\ngamma\ndelta\nepsilon",
	})

	out := stripANSI(sb.String())
	for _, frag := range []string{"alpha", "beta", "gamma"} {
		if !strings.Contains(out, frag) {
			t.Errorf("preview missing %q, got:\n%s", frag, out)
		}
	}
	if strings.Contains(out, "delta") || strings.Contains(out, "epsilon") {
		t.Errorf("preview should cap at 3 lines, got:\n%s", out)
	}
	if !strings.Contains(out, "+ 2 more lines") {
		t.Errorf("preview should include `+ 2 more lines` trailer, got:\n%s", out)
	}
}

// QUM-336: 2-line result renders both lines and no "+ N more" trailer.
func TestViewportModel_RenderToolCall_ResultPreview_2Lines(t *testing.T) {
	const width = 80
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:    MessageToolCall,
		Content: "Bash",
		ToolID:  "t-1",
		Result:  "first\nsecond",
	})

	out := stripANSI(sb.String())
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("expected both lines in preview, got:\n%s", out)
	}
	if strings.Contains(out, "more lines") {
		t.Errorf("2-line result should NOT include `more lines` trailer, got:\n%s", out)
	}
}

// QUM-336: result preview drops empty lines when counting toward the 3-line cap
// and the "+ N more" trailer (so a result with leading/trailing blank lines
// doesn't waste preview slots).
func TestViewportModel_RenderToolCall_ResultPreview_DropsBlankLines(t *testing.T) {
	const width = 80
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:    MessageToolCall,
		Content: "Bash",
		ToolID:  "t-1",
		Result:  "\n\nalpha\n\nbeta\n\n",
	})

	out := stripANSI(sb.String())
	for _, frag := range []string{"alpha", "beta"} {
		if !strings.Contains(out, frag) {
			t.Errorf("preview missing %q, got:\n%s", frag, out)
		}
	}
	if strings.Contains(out, "more lines") {
		t.Errorf("only 2 non-empty lines; should NOT include `more lines`, got:\n%s", out)
	}
}

// QUM-336: a single result line wider than the viewport is truncated with `…`,
// and the rendered display line stays inside the viewport width.
func TestViewportModel_RenderToolCall_ResultPreview_TruncatesLongLine(t *testing.T) {
	const width = 30
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)

	long := strings.Repeat("xyz", 40) // 120 chars
	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:    MessageToolCall,
		Content: "Bash",
		ToolID:  "t-1",
		Result:  long,
	})

	out := sb.String()
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("rendered line width %d exceeds viewport width %d: %q", w, width, line)
		}
	}
	if !strings.Contains(stripANSI(out), "…") {
		t.Errorf("expected truncation marker `…` in preview, got:\n%s", stripANSI(out))
	}
}

// QUM-336: while a tool call is pending, the indicator slot shows the
// spinner frame the AppModel injected via SetSpinnerFrame, NOT a static
// glyph. This lets AppModel drive a single global spinner across all
// pending entries by pushing a frame on each spinner.TickMsg.
func TestViewportModel_RenderToolCall_PendingShowsInjectedSpinnerFrame(t *testing.T) {
	const width = 60
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 20)
	m.SetSpinnerFrame("⠋")

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		ToolID:    "t-1",
		Pending:   true,
		ToolInput: "sleep 1",
	})

	out := stripANSI(sb.String())
	if !strings.Contains(out, "⠋") {
		t.Errorf("pending tool call should render injected spinner frame, got:\n%s", out)
	}
	if strings.Contains(out, "✓") || strings.Contains(out, "✗") {
		t.Errorf("pending tool call should not render success/failure glyph, got:\n%s", out)
	}
}

// QUM-336: gutter alignment must hold even when a result preview is
// rendered — every body line (input + result + trailer) must wear the
// `│ ` gutter so nothing escapes the viewport (QUM-324 protection).
func TestViewportModel_RenderToolCall_ResultPreservesGutter(t *testing.T) {
	const width = 50
	theme := NewTheme("colour212")
	m := NewViewportModel(&theme)
	m.SetSize(width, 30)

	var sb strings.Builder
	m.renderToolCall(&sb, MessageEntry{
		Type:      MessageToolCall,
		Content:   "Bash",
		ToolID:    "t-1",
		ToolInput: "ls",
		Result:    "a\nb\nc\nd\ne",
	})

	for _, line := range strings.Split(sb.String(), "\n") {
		plain := stripANSI(line)
		if plain == "" {
			continue
		}
		if strings.HasPrefix(plain, "┌") || strings.HasPrefix(plain, "└") {
			continue
		}
		if !strings.HasPrefix(plain, "│ ") {
			t.Errorf("body line missing `│ ` gutter: %q", plain)
		}
	}
}
