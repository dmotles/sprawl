package tui

// QUM-671 S1 — unit coverage for Item implementations. Focus areas: width
// stability, Finished() lifecycle, Expandable toggle, and the streaming
// cursor on in-flight assistant text.

import (
	"strings"
	"testing"
)

// newTestCtx returns a fresh itemRenderCtx for tests; the theme is
// deterministic and the renderer is sized to 80 (rebuilt per width by items
// that need it).
func newTestCtx() *itemRenderCtx {
	theme := NewTheme("")
	return &itemRenderCtx{theme: &theme, renderer: NewMarkdownRenderer(80)}
}

func TestUserItem_RenderAlwaysFinished(t *testing.T) {
	ctx := newTestCtx()
	item := NewUserItem(ctx, "hello\nworld")
	if !item.Finished() {
		t.Fatalf("UserItem.Finished() = false, want true")
	}
	out := item.Render(80)
	if !strings.Contains(out, "›") {
		t.Errorf("expected chevron prefix, got %q", out)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("expected both lines rendered, got %q", out)
	}
}

func TestUserItem_WidthZeroNoOps(t *testing.T) {
	ctx := newTestCtx()
	item := NewUserItem(ctx, "hello")
	if got := item.Render(0); got != "" {
		t.Errorf("Render(0) = %q, want empty", got)
	}
}

func TestAssistantTextItem_StreamingLifecycle(t *testing.T) {
	ctx := newTestCtx()
	item := NewAssistantTextItem(ctx, "hel")
	if item.Finished() {
		t.Fatalf("streaming item should not be Finished")
	}
	item.AppendChunk("lo")
	if item.Text() != "hello" {
		t.Errorf("Text() = %q, want %q", item.Text(), "hello")
	}
	out := item.Render(80)
	if !strings.HasSuffix(out, itemsStreamingCursor) {
		t.Errorf("expected streaming cursor at tail of %q", out)
	}
	item.Finalize()
	if !item.Finished() {
		t.Fatalf("Finalize() did not flip Finished()")
	}
	out = item.Render(80)
	if strings.HasSuffix(out, itemsStreamingCursor) {
		t.Errorf("finished item should not have streaming cursor; got %q", out)
	}
}

func TestAssistantTextItem_AppendAfterFinalizeNoOp(t *testing.T) {
	ctx := newTestCtx()
	item := NewAssistantTextItem(ctx, "first")
	item.Finalize()
	item.AppendChunk(" second")
	if item.Text() != "first" {
		t.Errorf("post-finalize AppendChunk mutated text: %q", item.Text())
	}
}

func TestThinkingItem_RenderCount(t *testing.T) {
	// QUM-677 S7 pivot: ThinkingItem is a transient count marker. Render
	// uses "block" (singular) for count=1 and "blocks" otherwise.
	cases := []struct {
		count int
		want  string
	}{
		{1, "(1 block)"},
		{5, "(5 blocks)"},
		{20, "(20 blocks)"},
	}
	ctx := newTestCtx()
	for _, tc := range cases {
		item := NewThinkingItem(ctx)
		for i := 1; i < tc.count; i++ {
			item.Bump()
		}
		if !item.Finished() {
			t.Errorf("count=%d: Finished()=false, want true", tc.count)
		}
		if got := item.Count(); got != tc.count {
			t.Errorf("count=%d: Count()=%d", tc.count, got)
		}
		out := item.Render(80)
		if !strings.Contains(out, "thinking") {
			t.Errorf("count=%d: render missing 'thinking': %q", tc.count, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("count=%d: render = %q, want substring %q", tc.count, out, tc.want)
		}
		if item.RawMarkdown() != "" {
			t.Errorf("count=%d: RawMarkdown should be empty, got %q", tc.count, item.RawMarkdown())
		}
	}
}

func TestToolCallItem_PendingThenResultLifecycle(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{
		Name:      "Bash",
		ToolID:    "toolu_x",
		Input:     "ls -la",
		HeaderArg: "ls -la",
	})
	if item.Finished() {
		t.Fatalf("new ToolCallItem should be in flight (Finished=false)")
	}
	if got := item.ToolID(); got != "toolu_x" {
		t.Errorf("ToolID() = %q, want %q", got, "toolu_x")
	}
	pending := item.Render(80)
	if !strings.Contains(pending, toolSpinnerFrames[0]) {
		t.Errorf("pending render missing initial spinner frame %q: %q", toolSpinnerFrames[0], pending)
	}
	if !strings.Contains(pending, "Bash") {
		t.Errorf("pending render missing tool name: %q", pending)
	}
	item.MarkResult("done", false)
	if !item.Finished() {
		t.Fatalf("MarkResult did not flip Finished()")
	}
	done := item.Render(80)
	for _, frame := range toolSpinnerFrames {
		if strings.Contains(done, frame) {
			t.Errorf("completed render still shows spinner frame %q: %q", frame, done)
		}
	}
	if !strings.Contains(done, "✓") {
		t.Errorf("completed render missing success glyph: %q", done)
	}
}

func TestToolCallItem_FailedShowsErrorGlyph(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t"})
	item.MarkResult("boom", true)
	out := item.Render(80)
	if !strings.Contains(out, "✗") {
		t.Errorf("failed tool render missing ✗: %q", out)
	}
}

func TestToolCallItem_ExpandableInputBody(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{
		Name:      "Bash",
		ToolID:    "t",
		Input:     "ls",
		InputFull: "ls -la /tmp\necho done",
		HeaderArg: "ls",
	})
	item.MarkResult("ok", false)
	collapsed := item.Render(80)
	if strings.Contains(collapsed, "echo done") {
		t.Errorf("collapsed tool render leaked InputFull: %q", collapsed)
	}
	item.SetExpanded(true)
	expanded := item.Render(80)
	if !strings.Contains(expanded, "echo done") {
		t.Errorf("expanded tool render missing InputFull: %q", expanded)
	}
}

func TestToolCallItem_NestedDepthCompactRender(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{
		Name:         "Read",
		ToolID:       "t",
		Input:        "a.go",
		Depth:        1,
		ParentToolID: "agent_root",
	})
	out := item.Render(80)
	if !strings.HasPrefix(stripANSI(out), "│ ") {
		t.Errorf("nested render expected to start with '│ ' gutter (post-ANSI strip): %q", stripANSI(out))
	}
	if strings.Contains(out, "┌") || strings.Contains(out, "└") {
		t.Errorf("nested render leaked box-drawing chars: %q", out)
	}
}

func TestToolCallItem_StartTickCmdNilWhenNotPending(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t"})
	item.MarkResult("done", false)
	if cmd := item.StartTickCmd(); cmd != nil {
		t.Errorf("StartTickCmd on finished item should be nil")
	}
}

func TestToolCallItem_StartTickCmdNonNilWhenPending(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t"})
	if cmd := item.StartTickCmd(); cmd == nil {
		t.Errorf("StartTickCmd on pending item should be non-nil")
	}
	// Idempotent: a second call while ticking returns nil.
	if cmd := item.StartTickCmd(); cmd != nil {
		t.Errorf("StartTickCmd while already ticking should return nil (no double-arm)")
	}
}

func TestToolCallItem_TickAdvancesFrame(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "ls"})
	before := item.Render(80)
	cmd := item.Update(toolTickMsg{ToolID: "t"})
	if cmd == nil {
		t.Errorf("Update(toolTickMsg) on pending item should return follow-up cmd")
	}
	after := item.Render(80)
	if before == after {
		t.Errorf("Tick should change rendered output. before=%q after=%q", before, after)
	}
}

func TestToolCallItem_TickIgnoresWrongID(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t1"})
	before := item.Render(80)
	cmd := item.Update(toolTickMsg{ToolID: "other"})
	if cmd != nil {
		t.Errorf("Update with mismatched ToolID should return nil cmd")
	}
	after := item.Render(80)
	if before != after {
		t.Errorf("mismatched tick should not change render")
	}
}

func TestToolCallItem_TickAfterMarkResultTerminates(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t"})
	_ = item.StartTickCmd()
	item.MarkResult("done", false)
	if cmd := item.Update(toolTickMsg{ToolID: "t"}); cmd != nil {
		t.Errorf("Update after MarkResult must return nil (no follow-up tick)")
	}
}

func TestToolCallItem_NestedTickAnimates(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{
		Name: "Read", ToolID: "t", Input: "x.go",
		Depth: 1, ParentToolID: "agent_root",
	})
	before := item.Render(80)
	_ = item.Update(toolTickMsg{ToolID: "t"})
	after := item.Render(80)
	if before == after {
		t.Errorf("nested-render tick should change output. before=%q after=%q", before, after)
	}
}

func TestSystemNotificationItem_RendersInterrupt(t *testing.T) {
	ctx := newTestCtx()
	item := NewSystemNotificationItem(ctx, "act now", NotificationKindMessage, true)
	out := item.Render(80)
	if !strings.Contains(out, "⚡") {
		t.Errorf("interrupt notification missing ⚡ glyph: %q", out)
	}
}

func TestSystemNotificationItem_RendersStatusChange(t *testing.T) {
	ctx := newTestCtx()
	item := NewSystemNotificationItem(ctx, "alpha → working", NotificationKindStatusChange, false)
	out := item.Render(80)
	if !strings.Contains(out, "◉") {
		t.Errorf("status_change notification missing ◉ glyph: %q", out)
	}
}

func TestAutoTriggerItem_Render(t *testing.T) {
	ctx := newTestCtx()
	item := NewAutoTriggerItem(ctx, "task notification fired")
	out := item.Render(80)
	if !strings.Contains(out, "↻ auto-continued") || !strings.Contains(out, "task notification fired") {
		t.Errorf("auto-trigger render missing marker: %q", out)
	}
	if !item.Finished() {
		t.Fatalf("AutoTriggerItem.Finished() = false, want true")
	}
}

// (stripANSI lives in testutil_test.go.)
