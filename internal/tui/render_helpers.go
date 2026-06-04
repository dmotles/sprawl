package tui

// QUM-684 — pre-S3 refactor: lift shared width-budgeting and box-drawing
// helpers out of viewport.go and items.go into pure package-level functions.
// Zero behavioral change: callers in both files must produce byte-identical
// output. See docs/designs/tui-structural-rewrite-plan.md §3 S3 prep.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

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
		sb.WriteString(previewStyle.Render("│ " + ln))
	}
	if more > 0 {
		trailer := fmt.Sprintf("+ %d more lines", more)
		if width > toolCallInputPrefix {
			trailer = ansi.Truncate(trailer, width-toolCallInputPrefix, "…")
		}
		sb.WriteString("\n")
		sb.WriteString(theme.NormalText.Render("│ " + trailer))
	}
	return sb.String()
}

// renderUserPromptBlock applies the QUM-664 chevron prefix to a
// user-message body: the first content line gets "› ", continuation lines
// get two spaces of hang indent, and the whole block renders under
// theme.UserPromptText. The chevron and body render as separate spans so
// the chevron's SGR sequence is independently identifiable in tests.
func renderUserPromptBlock(theme *Theme, content string) string {
	style := theme.UserPromptText
	lines := strings.Split(content, "\n")
	out := make([]string, len(lines))
	for i, ln := range lines {
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
		sb.WriteString(theme.NormalText.Render("│ " + ln))
	}
	return sb.String()
}
