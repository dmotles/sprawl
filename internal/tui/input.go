package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// maxInputLines caps how tall the input textarea can grow.
const maxInputLines = 10

// nowFunc is the clock used by the time-based paste classifier (QUM-432).
// Overridable in tests.
var nowFunc = time.Now

// pasteBurstWindow / pasteQuietWindow tune the QUM-432 stripped-bracketed-paste
// classifier. An Enter arriving within pasteBurstWindow of the prior printable
// keypress is treated as an embedded newline; once paste-mode is active it
// remains active for pasteQuietWindow after the most recent paste activity.
// vars (not consts) so tests / future tuning can override.
var (
	pasteBurstWindow = 10 * time.Millisecond
	pasteQuietWindow = 50 * time.Millisecond
)

// InputModel wraps a textarea for the bottom input panel.
type InputModel struct {
	ta       textarea.Model
	theme    *Theme
	width    int
	disabled bool

	// pendingPreview is a short preview of the queued submit (QUM-340). When
	// non-empty the View() renders a dim indicator alongside the textarea
	// signalling that an Enter while the agent was busy stashed a message that
	// will auto-submit when the turn finalizes.
	pendingPreview string

	// QUM-432 paste-classifier state. lastKeyAt is the timestamp of the most
	// recent printable KeyPressMsg seen; pasteUntil is a deadline during which
	// any Enter is treated as an embedded newline rather than a submit. Both
	// zero outside of paste bursts. See package-level pasteBurstWindow /
	// pasteQuietWindow for the tuning constants.
	lastKeyAt  time.Time
	pasteUntil time.Time
}

// NewInputModel creates an input model with a placeholder prompt.
func NewInputModel(theme *Theme) InputModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message..."
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxInputLines
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")
	return InputModel{
		ta:    ta,
		theme: theme,
	}
}

// Update handles key events: Enter submits, disabled blocks all input.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if m.disabled {
			return m, nil
		}
		// Intercept `/` as the very first character of an empty input: open
		// the command palette rather than inserting the literal slash. `/`
		// mid-text falls through and is inserted by textarea normally.
		if keyMsg.Code == '/' && m.ta.Value() == "" {
			return m, func() tea.Msg { return OpenPaletteMsg{} }
		}
		now := nowFunc()
		// Plain Enter (no shift) submits the message — unless the paste
		// classifier (QUM-432) determines this Enter is an embedded newline
		// from a stripped-bracketed-paste burst.
		if keyMsg.Code == tea.KeyEnter && keyMsg.Mod&tea.ModShift == 0 {
			embedded := (!m.pasteUntil.IsZero() && now.Before(m.pasteUntil)) ||
				(!m.lastKeyAt.IsZero() && now.Sub(m.lastKeyAt) < pasteBurstWindow)
			if embedded {
				m.ta.InsertString("\n")
				m.pasteUntil = now.Add(pasteQuietWindow)
				m.lastKeyAt = now
				return m, nil
			}
			text := strings.TrimSpace(m.ta.Value())
			if text != "" {
				m.ta.SetValue("")
				m.lastKeyAt = time.Time{}
				m.pasteUntil = time.Time{}
				return m, func() tea.Msg { return SubmitMsg{Text: text} }
			}
			return m, nil
		}
		// Track timing for printable keys so the next Enter can be classified.
		// keyMsg.Text is non-empty for character-producing keys (letters,
		// digits, punctuation, space) and empty for control keys (arrows,
		// Esc, etc.) — exactly the signal we want.
		if keyMsg.Text != "" {
			m.lastKeyAt = now
			if !m.pasteUntil.IsZero() && now.Before(m.pasteUntil) {
				m.pasteUntil = now.Add(pasteQuietWindow)
			}
		}
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// View renders the input field. When a pending submit is queued (QUM-340),
// a dim "⏸ queued: <preview>" suffix is appended on the first line; the
// textarea's width is reduced via SetPendingPreview so the two co-exist
// without wrapping.
func (m InputModel) View() string {
	base := m.ta.View()
	if m.pendingPreview == "" {
		return base
	}
	suffix := m.theme.PlaceholderStyle.Render("  " + queuedIndicator(m.pendingPreview))
	return base + suffix
}

// queuedPreviewMaxLen caps the indicator text so a long queued message
// doesn't push past the input bar.
const queuedPreviewMaxLen = 40

// queuedIndicator builds the muted "⏸ queued: <preview>" string.
func queuedIndicator(text string) string {
	preview := text
	if len(preview) > queuedPreviewMaxLen {
		preview = preview[:queuedPreviewMaxLen] + "…"
	}
	return "⏸ queued: " + preview
}

// SetWidth updates the input width. When a pending preview is set, the
// textarea receives the remaining width after the indicator's space so the
// two render side-by-side without wrapping (QUM-340).
func (m *InputModel) SetWidth(w int) {
	m.width = w
	m.ta.SetWidth(m.textInputWidth())
}

// textInputWidth returns the width budget the textarea should receive,
// shrinking by the indicator's footprint when a queued preview is active.
func (m *InputModel) textInputWidth() int {
	if m.pendingPreview == "" {
		return m.width
	}
	indicatorLen := len(queuedIndicator(m.pendingPreview)) + 2 // +2 for leading spaces
	w := m.width - indicatorLen
	if w < 4 {
		w = 4
	}
	return w
}

// SetPendingPreview sets the queued-submit indicator text. Empty string clears
// it. Re-applies width so the textarea re-allocates room for the suffix.
func (m *InputModel) SetPendingPreview(text string) {
	m.pendingPreview = text
	m.ta.SetWidth(m.textInputWidth())
}

// PendingPreview returns the current queued-submit indicator text.
func (m *InputModel) PendingPreview() string { return m.pendingPreview }

// Focus activates the input for typing.
func (m *InputModel) Focus() tea.Cmd {
	return m.ta.Focus()
}

// Blur deactivates the input.
func (m *InputModel) Blur() {
	m.ta.Blur()
}

// Height returns the current height of the textarea in rows.
func (m *InputModel) Height() int {
	return m.ta.Height()
}

// Value returns the current textarea contents.
func (m *InputModel) Value() string {
	return m.ta.Value()
}

// SetValue replaces the textarea contents.
func (m *InputModel) SetValue(s string) {
	m.ta.SetValue(s)
}

// AtFirstLine reports whether the textarea cursor sits on the first logical
// line. Used to disambiguate Up keys between in-buffer cursor movement and
// history navigation (QUM-410).
func (m *InputModel) AtFirstLine() bool {
	return m.ta.Line() == 0
}

// AtLastLine reports whether the textarea cursor sits on the last logical
// line. Mirror of AtFirstLine for Down. (QUM-410)
func (m *InputModel) AtLastLine() bool {
	lc := m.ta.LineCount()
	if lc == 0 {
		return true
	}
	return m.ta.Line() >= lc-1
}

// SetDisabled enables or disables the input.
func (m *InputModel) SetDisabled(disabled bool) {
	m.disabled = disabled
	if disabled {
		m.ta.Placeholder = "Thinking..."
	} else {
		m.ta.Placeholder = "Type a message..."
	}
}
