package tui

// QUM-671 S1 — unit coverage for Item implementations. Focus areas: width
// stability, Finished() lifecycle, Expandable toggle, and the streaming
// cursor on in-flight assistant text.

import (
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
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

// QUM-796 #1 — the top-level (depth==0) tool-call render drops the
// `┌ │ └` box chrome entirely. No box-drawing characters may appear in any
// lifecycle state (pending, success, failure).
func TestToolCallItem_BoxChromeStripped(t *testing.T) {
	ctx := newTestCtx()
	for _, width := range []int{80, 40, 20} {
		// pending
		pend := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "ls -la"})
		assertNoBoxChrome(t, "pending", width, pend.Render(width))
		// success
		ok := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "ls -la"})
		ok.MarkResult("done\nmore", false)
		assertNoBoxChrome(t, "success", width, ok.Render(width))
		// failure
		fail := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "ls -la"})
		fail.MarkResult("boom", true)
		assertNoBoxChrome(t, "failure", width, fail.Render(width))
	}
}

func assertNoBoxChrome(t *testing.T, label string, width int, out string) {
	t.Helper()
	for _, ch := range []string{"┌", "└", "│"} {
		if strings.Contains(out, ch) {
			t.Errorf("%s w=%d: render leaked box-drawing char %q: %q", label, width, ch, out)
		}
	}
}

// QUM-796 #2 — the header renders inline as `<glyph> <ToolName>(<preview>)`.
func TestToolCallItem_HeaderInlineFormat(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "tmux list-sessions"})
	item.MarkResult("ok", false)
	header := stripANSI(strings.SplitN(item.Render(80), "\n", 2)[0])
	if !strings.HasPrefix(header, "✓ Bash(") {
		t.Errorf("header should start with `✓ Bash(`, got %q", header)
	}
	if !strings.Contains(header, "tmux list-sessions)") {
		t.Errorf("header should contain `tmux list-sessions)`, got %q", header)
	}
}

// QUM-796 #2 — newlines inside the command preview collapse to spaces so the
// header stays a single line.
func TestToolCallItem_HeaderNewlinesCollapsed(t *testing.T) {
	ctx := newTestCtx()
	// HeaderArg is fed a raw newline directly (bypassing FormatToolHeader's
	// ` ; ` collapse) to pin renderBox's own defensive newline→space collapse.
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Grep", ToolID: "t", HeaderArg: "line1\nline2"})
	item.MarkResult("ok", false)
	header := stripANSI(strings.SplitN(item.Render(80), "\n", 2)[0])
	if strings.Contains(header, "\n") {
		t.Errorf("header line must not contain a newline: %q", header)
	}
	if !strings.Contains(header, "line1 line2") {
		t.Errorf("header should collapse newline to space (`line1 line2`), got %q", header)
	}
}

// QUM-796 #2 — the command preview is truncated with `…` to fit one row.
func TestToolCallItem_HeaderTruncatedToWidth(t *testing.T) {
	ctx := newTestCtx()
	long := strings.Repeat("abcdefghij ", 20)
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: long})
	item.MarkResult("ok", false)
	for _, width := range []int{20, 40, 80} {
		header := stripANSI(strings.SplitN(item.Render(width), "\n", 2)[0])
		if w := ansi.StringWidth(header); w > width {
			t.Errorf("w=%d: header width %d exceeds terminal width: %q", width, w, header)
		}
		if !strings.Contains(header, "…") {
			t.Errorf("w=%d: truncated header should contain ellipsis: %q", width, header)
		}
	}
}

// QUM-796 #2 — the `description="..."` (and any other) param suffix is no
// longer rendered in the collapsed header.
func TestToolCallItem_DropsParamSuffix(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{
		Name:         "Bash",
		ToolID:       "t",
		HeaderArg:    "make test",
		HeaderParams: []KVPair{{Key: "description", Value: `"run tests"`}, {Key: "timeout", Value: "120000"}},
	})
	item.MarkResult("ok", false)
	out := stripANSI(item.Render(80))
	if strings.Contains(out, "description") {
		t.Errorf("header must not render description param: %q", out)
	}
	if strings.Contains(out, "timeout=") {
		t.Errorf("header must not render timeout param: %q", out)
	}
}

// QUM-796 #2 — a tool with no main arg renders just `<glyph> <Name>` with no
// empty `()`.
func TestToolCallItem_NoParensWhenNoMainArg(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "TodoWrite", ToolID: "t"})
	item.MarkResult("ok", false)
	header := stripANSI(strings.SplitN(item.Render(80), "\n", 2)[0])
	if strings.Contains(header, "(") || strings.Contains(header, ")") {
		t.Errorf("header should have no parens when main arg empty: %q", header)
	}
	if !strings.Contains(header, "TodoWrite") {
		t.Errorf("header should still show the tool name: %q", header)
	}
}

// QUM-796 #5 — multi-line output shows the first 3 lines indented under the
// header (no `│` gutter) then a `⎿  + K more lines` elision trailer.
func TestToolCallItem_ResultPreviewElision(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "seq 6"})
	item.MarkResult("l1\nl2\nl3\nl4\nl5\nl6", false)
	out := item.Render(80)
	plain := stripANSI(out)
	if !strings.Contains(plain, "⎿") {
		t.Errorf("elided result should contain ⎿ trailer glyph: %q", plain)
	}
	if !strings.Contains(plain, "+ 3 more lines") {
		t.Errorf("elided result should report `+ 3 more lines`: %q", plain)
	}
	// Body lines indent two spaces under the header (positive check).
	if !strings.Contains(plain, "\n  l1") {
		t.Errorf("result body lines should indent two spaces (`\\n  l1`): %q", plain)
	}
	// Body lines beyond the first 3 must not appear in the collapsed render.
	if strings.Contains(plain, "l4") {
		t.Errorf("collapsed render leaked the 4th output line: %q", plain)
	}
	// Result/body lines indent with two spaces, not a `│ ` gutter.
	for _, ln := range strings.Split(plain, "\n")[1:] {
		if strings.HasPrefix(ln, "│") {
			t.Errorf("output line should not use `│` gutter: %q", ln)
		}
	}
}

// QUM-796 #3/#5 — expanding lifts the cap and shows full output with no
// elision trailer; the QUM-732 expand behavior is preserved.
func TestToolCallItem_ResultPreviewExpandedShowsAll(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "seq 6"})
	item.MarkResult("l1\nl2\nl3\nl4\nl5\nl6", false)
	item.SetExpanded(true)
	plain := stripANSI(item.Render(80))
	if !strings.Contains(plain, "l6") {
		t.Errorf("expanded render should show all output lines: %q", plain)
	}
	if strings.Contains(plain, "more lines") {
		t.Errorf("expanded render should have no elision trailer: %q", plain)
	}
}

// QUM-796 #2 — since the command preview is no longer strconv.Quote'd, the
// renderer must neutralize control chars (tab, CR, raw ESC) so they cannot
// break single-line layout or leak SGR styling past truncation.
func TestToolCallItem_HeaderStripsControlChars(t *testing.T) {
	ctx := newTestCtx()
	item := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "a\tb\rc\x1b[31md"})
	item.MarkResult("ok", false)
	header := stripANSI(strings.SplitN(item.Render(80), "\n", 2)[0])
	for _, r := range header {
		if unicode.IsControl(r) {
			t.Errorf("header leaked control char %q in %q", r, header)
		}
	}
}

// QUM-796 #3 — the ✓/✗ glyphs keep their exact styled rendering: ✗ under
// ErrorText (red), ✓ under AccentText. Pins color, not just the rune, so a
// correct-glyph-wrong-color regression is caught.
func TestToolCallItem_GlyphColorsPreserved(t *testing.T) {
	ctx := newTestCtx()
	ok := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "ls"})
	ok.MarkResult("done", false)
	if want := ctx.theme.AccentText.Render("✓"); !strings.Contains(ok.Render(80), want) {
		t.Errorf("success render should contain accent-styled ✓ (%q): %q", want, ok.Render(80))
	}
	fail := NewToolCallItem(ctx, ToolCallSpec{Name: "Bash", ToolID: "t", HeaderArg: "ls"})
	fail.MarkResult("boom", true)
	if want := ctx.theme.ErrorText.Render("✗"); !strings.Contains(fail.Render(80), want) {
		t.Errorf("failure render should contain error-styled ✗ (%q): %q", want, fail.Render(80))
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
	item := NewAutoTriggerItem(ctx)
	out := item.Render(80)
	if !strings.Contains(out, "↻ auto-continued") {
		t.Errorf("auto-trigger render missing marker: %q", out)
	}
	if !item.Finished() {
		t.Fatalf("AutoTriggerItem.Finished() = false, want true")
	}
	// The marker must be styled, not raw (QUM-855: no flat unstyled dump).
	if out == stripANSI(out) {
		t.Errorf("expected styled (ANSI-wrapped) marker, got raw text: %q", out)
	}
}

// QUM-855/QUM-857: the auto-trigger marker collapses to a single styled
// indicator line. QUM-855 suppressed a stuffed sidechain-result body at the
// render layer; QUM-857 removed the body-carrying state entirely, so the marker
// is now structurally body-free. Guard that Render stays a single styled marker
// line with no markdown artifacts.
func TestAutoTriggerItem_SuppressesMarkdownBody(t *testing.T) {
	ctx := newTestCtx()
	item := NewAutoTriggerItem(ctx)
	out := stripANSI(item.Render(80))

	if !strings.Contains(out, "↻ auto-continued") {
		t.Errorf("missing auto-continued marker: %q", out)
	}
	if strings.Contains(out, "\n") {
		t.Errorf("render must be a single line, got multi-line: %q", out)
	}
	// No markdown/heading/code-fence artifacts may appear — the marker is a
	// plain cue, and no body can be threaded into it any longer.
	for _, body := range []string{"##", "**", "`"} {
		if strings.Contains(out, body) {
			t.Errorf("unexpected markdown artifact %q in indicator line: %q", body, out)
		}
	}
}

// QUM-855/QUM-857: RawMarkdown surfaces only the fixed marker — there is no
// body to yank from the transcript.
func TestAutoTriggerItem_RawMarkdownMarkerOnly(t *testing.T) {
	ctx := newTestCtx()
	item := NewAutoTriggerItem(ctx)
	if got := item.RawMarkdown(); got != "↻ auto-continued" {
		t.Errorf("RawMarkdown() = %q, want %q", got, "↻ auto-continued")
	}
}

func TestAutoTriggerItem_WidthZeroNoOps(t *testing.T) {
	ctx := newTestCtx()
	item := NewAutoTriggerItem(ctx)
	if got := item.Render(0); got != "" {
		t.Errorf("Render(0) = %q, want empty", got)
	}
}

// (stripANSI lives in testutil_test.go.)
