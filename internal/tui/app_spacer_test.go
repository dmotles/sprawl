package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// QUM-664: visual identity port — there must be exactly one blank spacer row
// between the bottom of the SPRAWL wordmark header and the first row of the
// viewport content, so the wordmark visually breathes from the chat body.
func TestAppView_WordmarkChatSpacer(t *testing.T) {
	m := newTestAppModel(t)
	const w, h = 120, 30
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	v := app.View()
	plain := stripANSI(v.Content)
	lines := strings.Split(plain, "\n")

	// Pin spacer index to the actual layout contract instead of hardcoding.
	layout := ComputeLayout(w, h, (&app).inputBoxHeight())
	headerH := layout.HeaderHeight
	if len(lines) <= headerH {
		t.Fatalf("rendered view too short to contain header + spacer + body; lines=%d:\n%s",
			len(lines), plain)
	}
	spacer := lines[headerH]
	if strings.TrimSpace(spacer) != "" {
		t.Errorf("expected blank spacer row immediately after wordmark (line index %d), got %q\n--- full view ---\n%s",
			headerH, spacer, plain)
	}
}

// QUM-664: input bar must sit flush against the status bar — zero blank
// rows between the bottom-most input row and the status bar (dmotles
// eyeball regression: a stale `+2` border reservation in inputBoxHeight
// padded the input panel taller than its rendered content, leaving dead
// rows between the textarea bar and the status line).
func TestAppView_InputFlushWithStatusBar(t *testing.T) {
	m := newTestAppModel(t)
	const w, h = 120, 30
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	v := app.View()
	plain := stripANSI(v.Content)
	lines := strings.Split(plain, "\n")
	// Drop any trailing blank caused by a final newline in the join.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 {
		t.Fatalf("rendered view too short: %d lines\n%s", len(lines), plain)
	}
	// Status bar is the last row. The row immediately above must be the
	// bottom-most input row (containing the `▌ ` gutter glyph), not a blank
	// padding row.
	statusRow := lines[len(lines)-1]
	rowAboveStatus := lines[len(lines)-2]
	if strings.TrimSpace(statusRow) == "" {
		t.Fatalf("expected status bar on last row, got blank; full view:\n%s", plain)
	}
	if !strings.Contains(rowAboveStatus, "▌") {
		t.Errorf("expected input bar (containing `▌`) flush above status bar;\n  rowAboveStatus = %q\n  statusRow      = %q\n--- full view ---\n%s",
			rowAboveStatus, statusRow, plain)
	}
}

// QUM-664: with the short-help strip removed, the rendered view height
// equals HeaderHeight + 1 (spacer) + ViewportHeight + InputHeight +
// StatusHeight — no extra row for shorthelp.
func TestAppView_NoShortHelpRow(t *testing.T) {
	m := newTestAppModel(t)
	const w, h = 120, 30
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	app, ok := updated.(AppModel)
	if !ok {
		t.Fatalf("Update returned %T, want AppModel", updated)
	}

	layout := ComputeLayout(w, h, (&app).inputBoxHeight())
	if layout.ShortHelpHeight != 0 {
		t.Errorf("layout.ShortHelpHeight = %d, want 0 (QUM-664)", layout.ShortHelpHeight)
	}

	v := app.View()
	plain := stripANSI(v.Content)
	lines := strings.Split(plain, "\n")
	// trim trailing blank from any join newline
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	const headerSpacer = 1
	want := layout.HeaderHeight + headerSpacer + layout.ViewportHeight + layout.InputHeight + layout.StatusHeight
	if len(lines) != want {
		t.Errorf("rendered view height = %d lines, want %d (= header(%d) + spacer(%d) + viewport(%d) + input(%d) + status(%d))",
			len(lines), want, layout.HeaderHeight, headerSpacer, layout.ViewportHeight, layout.InputHeight, layout.StatusHeight)
	}
}
