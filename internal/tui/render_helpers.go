package tui

// QUM-684 — pre-S3 refactor: lift shared width-budgeting and box-drawing
// helpers out of viewport.go and items.go into pure package-level functions.
// Zero behavioral change: callers in both files must produce byte-identical
// output. See docs/designs/tui-structural-rewrite-plan.md §3 S3 prep.

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// sanitizeHeaderArg neutralizes control characters (newlines, tabs, carriage
// returns, raw ESC, etc.) in a tool-call header preview, mapping each to a
// space. The preview is no longer strconv.Quote'd (QUM-796 #2), so this is
// the defense that keeps it a single styled line: a stray ESC would otherwise
// leak SGR styling past the truncation cell, and a CR/tab would corrupt the
// column layout.
func sanitizeHeaderArg(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// toolCallTrailerPrefix is the cell width of the `"⎿  "` prefix on the
// elision trailer line of a collapsed tool-call result (QUM-796 #5).
const toolCallTrailerPrefix = 3

// toolIndicator selects the styled indicator glyph for a tool-call row.
// The two historical call sites use different empty-spinner fallback glyphs:
// the viewport path passes its live spinner frame (first-frame fallback "⠋"),
// while the items path always uses a static glyph (pendingToolGlyph = "⠿").
// The `defaultGlyph` parameter preserves both behaviors without forking the
// switch. When pending, the rendered string is spinnerFrame if non-empty,
// otherwise defaultGlyph — both rendered under AccentText. Failed rows render
// "✗" under ErrorText; complete rows render "✓" under AccentText.
func toolIndicator(theme *Theme, pending, failed bool, spinnerFrame, defaultGlyph string) string {
	switch {
	case pending:
		frame := spinnerFrame
		if frame == "" {
			frame = defaultGlyph
		}
		return theme.AccentText.Render(frame)
	case failed:
		return theme.ErrorText.Render("✗")
	default:
		return theme.AccentText.Render("✓")
	}
}

// renderResultPreviewLines emits the `│ ` gutter lines for the result preview
// block. Returns a leading-newline-then-body string so callers can append it
// to their string builder and preserve the existing emission pattern (every
// line is newline-prefixed). When expanded, the line cap is lifted; otherwise
// at most 3 non-empty lines are shown with a `+ N more lines` trailer.
// Failures render under ErrorText so they stand out (QUM-336). Returns "" if
// the result has no non-empty lines.
func renderResultPreviewLines(theme *Theme, result string, isError, expanded bool, width int) string {
	previewStyle := theme.NormalText
	if isError {
		previewStyle = theme.ErrorText
	}
	maxLines := 3
	if expanded {
		maxLines = -1
	}
	previewLines, more := previewResultLines(result, maxLines, width-toolCallInputPrefix)
	if len(previewLines) == 0 && more == 0 {
		return ""
	}
	var sb strings.Builder
	for _, ln := range previewLines {
		sb.WriteString("\n")
		sb.WriteString(previewStyle.Render("  " + ln))
	}
	if more > 0 {
		// QUM-796 #5 — elision trailer uses the ⎿ corner glyph (no `│`
		// gutter) in dim grey; "⎿  " is 3 cells wide.
		trailer := fmt.Sprintf("+ %d more lines", more)
		if width > toolCallTrailerPrefix {
			trailer = ansi.Truncate(trailer, width-toolCallTrailerPrefix, "…")
		}
		sb.WriteString("\n")
		sb.WriteString(theme.ToolTrailerText.Render("⎿  " + trailer))
	}
	return sb.String()
}

// renderUserPromptBlock applies the QUM-664 chevron prefix to a user-message
// body and word-wraps it to the given width (QUM-797). The very first visible
// line of the whole block gets "› "; every continuation line — whether it is
// a wrap of the first source line or any line of a later source line — gets
// two spaces of hang indent, so wrapped text stays aligned under the chevron.
// The whole block renders under theme.UserPromptText (bright), or
// theme.UserPromptPendingText (dim/faint) when pending is true (QUM-832); the
// chevron and body render as separate spans so the chevron's SGR sequence is
// independently identifiable in tests.
//
// Wrapping happens per source line (split on "\n" first) so explicit newlines
// are preserved as independent wrap units. width-2 reserves room for the
// 2-cell prefix; when that budget is <=0 (very narrow terminals) the body
// falls back to unwrapped to avoid a degenerate wrap.
func renderUserPromptBlock(theme *Theme, content string, width int, pending bool) string {
	style := theme.UserPromptText
	if pending {
		// QUM-832: a pending (queued, not-yet-echoed) bubble renders dim. The
		// only delta is the SGR (Faint vs Bold) so the bubble brightens to the
		// exact committed rendering on settle.
		style = theme.UserPromptPendingText
	}
	wrapBudget := width - 2
	var visible []string
	for _, src := range strings.Split(content, "\n") {
		if wrapBudget <= 0 {
			visible = append(visible, src)
			continue
		}
		visible = append(visible, strings.Split(ansi.Wrap(src, wrapBudget, ""), "\n")...)
	}
	out := make([]string, len(visible))
	for i, ln := range visible {
		if i == 0 {
			out[i] = style.Render("›") + " " + style.Render(ln)
		} else {
			out[i] = "  " + style.Render(ln)
		}
	}
	return strings.Join(out, "\n")
}

// renderToolInputBody emits the ` │ ` gutter lines for the expanded tool
// input body. Returns a leading-newline-then-body string so callers can
// append it directly. Returns "" if the body is empty.
func renderToolInputBody(theme *Theme, body string, width int) string {
	if body == "" {
		return ""
	}
	var sb strings.Builder
	for _, ln := range wrapToolInput(body, width-toolCallInputPrefix) {
		sb.WriteString("\n")
		sb.WriteString(theme.NormalText.Render("  " + ln))
	}
	return sb.String()
}
