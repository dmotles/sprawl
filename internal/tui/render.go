package tui

import (
	"strings"

	"github.com/charmbracelet/glamour"
)

// MarkdownRenderer wraps glamour to render markdown for the TUI viewport.
type MarkdownRenderer struct {
	width int
	tr    *glamour.TermRenderer
}

// NewMarkdownRenderer creates a renderer with the given width.
func NewMarkdownRenderer(width int) *MarkdownRenderer {
	if width < 10 {
		width = 80
	}
	r := &MarkdownRenderer{width: width}
	r.buildRenderer()
	return r
}

// SetWidth updates the word wrap width and rebuilds the renderer if changed.
func (r *MarkdownRenderer) SetWidth(width int) {
	if width < 10 {
		width = 80
	}
	if r.width == width {
		return
	}
	r.width = width
	r.buildRenderer()
}

// Render converts markdown text to styled terminal output.
func (r *MarkdownRenderer) Render(markdown string) string {
	if strings.TrimSpace(markdown) == "" {
		return ""
	}
	out, err := r.tr.Render(markdown)
	if err != nil {
		// Fallback: return the raw markdown on render failure.
		return markdown
	}
	// Glamour adds trailing newlines; trim to avoid extra blank lines
	// between messages in the viewport.
	return strings.TrimRight(out, "\n")
}

func (r *MarkdownRenderer) buildRenderer() {
	tr, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(r.width),
	)
	if err != nil {
		// Fallback: create a minimal renderer.
		tr, _ = glamour.NewTermRenderer(glamour.WithWordWrap(r.width))
	}
	r.tr = tr
}
