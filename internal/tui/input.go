package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// InputModel wraps a textinput for the bottom input panel.
type InputModel struct {
	ti       textinput.Model
	theme    *Theme
	width    int
	disabled bool

	// pendingPreview is a short preview of the queued submit (QUM-340). When
	// non-empty the View() renders a dim indicator alongside the textinput
	// signalling that an Enter while the agent was busy stashed a message that
	// will auto-submit when the turn finalizes.
	pendingPreview string
}

// NewInputModel creates an input model with a placeholder prompt.
func NewInputModel(theme *Theme) InputModel {
	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	return InputModel{
		ti:    ti,
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
		// mid-text falls through and is inserted by textinput normally.
		if keyMsg.Code == '/' && m.ti.Value() == "" {
			return m, func() tea.Msg { return OpenPaletteMsg{} }
		}
		if keyMsg.Code == tea.KeyEnter {
			text := strings.TrimSpace(m.ti.Value())
			if text != "" {
				m.ti.SetValue("")
				return m, func() tea.Msg { return SubmitMsg{Text: text} }
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

// View renders the input field. When a pending submit is queued (QUM-340),
// a dim "⏸ queued: <preview>" suffix is appended on the same line; the
// textinput's width is reduced via SetPendingPreview so the two co-exist
// without wrapping.
func (m InputModel) View() string {
	base := m.ti.View()
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
// textinput receives the remaining width after the indicator's space so the
// two render side-by-side without wrapping (QUM-340).
func (m *InputModel) SetWidth(w int) {
	m.width = w
	m.ti.SetWidth(m.textInputWidth())
}

// textInputWidth returns the width budget the textinput should receive,
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
// it. Re-applies width so the textinput re-allocates room for the suffix.
func (m *InputModel) SetPendingPreview(text string) {
	m.pendingPreview = text
	m.ti.SetWidth(m.textInputWidth())
}

// PendingPreview returns the current queued-submit indicator text.
func (m *InputModel) PendingPreview() string { return m.pendingPreview }

// Focus activates the input for typing.
func (m *InputModel) Focus() tea.Cmd {
	return m.ti.Focus()
}

// Blur deactivates the input.
func (m *InputModel) Blur() {
	m.ti.Blur()
}

// SetDisabled enables or disables the input.
func (m *InputModel) SetDisabled(disabled bool) {
	m.disabled = disabled
	if disabled {
		m.ti.Placeholder = "Thinking..."
	} else {
		m.ti.Placeholder = "Type a message..."
	}
}
