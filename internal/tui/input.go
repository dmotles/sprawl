package tui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// maxInputLines caps how tall the input textarea can grow.
const maxInputLines = 10

// inputPlaceholderHelp is the static one-line key-binding hint shown as the
// textarea placeholder while the input is empty (QUM-664).
const inputPlaceholderHelp = "/: commands • ?: help • tab: cycle panel • ctrl+c: clear/quit"

// inputBarGutter is the chrome prefix prepended to every visible input row —
// a "▌ " gutter rendered under theme.InputBarStyle (QUM-664).
const inputBarGutter = "▌ "

// pasteLookaheadWindow is how long after a plain Enter we wait for a follow-up
// KeyPressMsg before resolving the Enter as a real submit. If another key
// arrives within the window, the Enter is reclassified as an embedded newline
// (a stripped bracketed-paste line break). var (not const) so tests / future
// tuning can override. (QUM-455)
var pasteLookaheadWindow = 40 * time.Millisecond

// InputModel wraps a textarea for the bottom input panel.
type InputModel struct {
	ta       textarea.Model
	theme    *Theme
	width    int
	disabled bool

	// inputBg is the background color that fills the input box on every row
	// (QUM-664). Sourced from the bubbles textarea's default Focused.CursorLine
	// background so the chrome reads identically across rows.
	inputBg color.Color

	// pendingPreview is a short preview of the queued submit (QUM-340). When
	// non-empty the View() renders a dim indicator alongside the textarea
	// signalling that an Enter while the agent was busy stashed a message that
	// will auto-submit when the turn finalizes.
	pendingPreview string

	// pendingEnter / pendingEnterSeq drive the QUM-455 post-Enter lookahead
	// debounce. A plain Enter does not submit synchronously; instead it sets
	// pendingEnter=true, bumps pendingEnterSeq, and schedules a
	// pasteLookaheadMsg via tea.Tick. If another KeyPressMsg arrives before
	// the tick fires, the pending Enter is reclassified as an embedded
	// newline. If the tick fires with a still-current seq, the pending Enter
	// resolves as a real submit. seq is bumped on every state transition so
	// stale ticks (from reclassified Enters) compare unequal and are ignored.
	pendingEnter    bool
	pendingEnterSeq uint64
}

// NewInputModel creates an input model with a placeholder prompt.
func NewInputModel(theme *Theme) InputModel {
	ta := textarea.New()
	ta.Placeholder = inputPlaceholderHelp
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxInputLines
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	// QUM-664: bubbles textarea defaults paint a subtle Background on
	// Focused.CursorLine only — visible as a 1-row "input box tint" that
	// doesn't grow with the textarea when it goes multi-line. Drive the
	// chrome bg from the palette (Palette.InputBg) and apply it to
	// Focused/Blurred Base + CursorLine so every row of the input renders
	// with the same tint, matching the `▌ ` gutter that already grows
	// per-row.
	inputBg := theme.Palette.InputBg
	styles := ta.Styles()
	styles.Focused.Base = styles.Focused.Base.Background(inputBg)
	styles.Focused.CursorLine = styles.Focused.CursorLine.Background(inputBg)
	styles.Blurred.Base = styles.Blurred.Base.Background(inputBg)
	styles.Blurred.CursorLine = styles.Blurred.CursorLine.Background(inputBg)
	ta.SetStyles(styles)

	return InputModel{
		ta:      ta,
		theme:   theme,
		inputBg: inputBg,
	}
}

// Update handles key events: Enter submits (via lookahead tick), disabled
// blocks all input.
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	// Lookahead tick resolution — handled before the KeyPressMsg branch so a
	// late tick after disable still gets cleanly dropped via seq mismatch.
	if lk, ok := msg.(pasteLookaheadMsg); ok {
		if !m.pendingEnter || lk.seq != m.pendingEnterSeq {
			return m, nil
		}
		// Trailing-backslash line continuation (Claude Code / crush
		// convention, QUM-456). If the input ends with a literal `\`, drop
		// the backslash and insert a newline instead of submitting. Checked
		// against the raw value so a final backslash followed by trailing
		// whitespace doesn't trigger continuation.
		if v := m.ta.Value(); strings.HasSuffix(v, `\`) {
			m.ta.SetValue(strings.TrimSuffix(v, `\`))
			m.ta.InsertString("\n")
			m.pendingEnter = false
			m.pendingEnterSeq++
			return m, nil
		}
		text := strings.TrimSpace(m.ta.Value())
		m.pendingEnter = false
		m.pendingEnterSeq++
		if text == "" {
			m.ta.SetValue("")
			return m, nil
		}
		m.ta.SetValue("")
		return m, func() tea.Msg { return SubmitMsg{Text: text} }
	}

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

		isPlainEnter := keyMsg.Code == tea.KeyEnter && keyMsg.Mod == 0
		// QUM-571: Alt+Enter and Ctrl+J are explicit newline-insert keys —
		// they never submit and never schedule the lookahead tick. If a plain
		// Enter is pending, it resolves as embedded ("\n") before the
		// requested newline is inserted.
		isNewlineKey := (keyMsg.Code == tea.KeyEnter && keyMsg.Mod&tea.ModAlt != 0) ||
			(keyMsg.Code == 'j' && keyMsg.Mod == tea.ModCtrl)
		if isNewlineKey {
			if m.pendingEnter {
				m.ta.InsertString("\n")
				m.pendingEnter = false
				m.pendingEnterSeq++
			}
			m.ta.InsertString("\n")
			return m, nil
		}

		if m.pendingEnter {
			if isPlainEnter {
				// Two consecutive plain Enters: the prior one is reclassified
				// as embedded ("\n"), and this new Enter becomes the new
				// pending submit candidate.
				m.ta.InsertString("\n")
				m.pendingEnterSeq++ // invalidate prior tick
				m.pendingEnterSeq++ // seq for the new pending Enter
				seq := m.pendingEnterSeq
				return m, tea.Tick(pasteLookaheadWindow, func(time.Time) tea.Msg {
					return pasteLookaheadMsg{seq: seq}
				})
			}
			// Any other key: prior Enter is embedded, then fall through so
			// the new key gets forwarded to the textarea normally.
			m.ta.InsertString("\n")
			m.pendingEnter = false
			m.pendingEnterSeq++
			// Fall through to forward keyMsg to textarea below.
		} else if isPlainEnter {
			m.pendingEnter = true
			m.pendingEnterSeq++
			seq := m.pendingEnterSeq
			return m, tea.Tick(pasteLookaheadWindow, func(time.Time) tea.Msg {
				return pasteLookaheadMsg{seq: seq}
			})
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
	if m.pendingPreview != "" {
		suffix := m.theme.PlaceholderStyle.Render("  " + queuedIndicator(m.pendingPreview))
		base += suffix
	}
	// QUM-664: prepend the "▌ " gutter to every visible row of the rendered
	// input. Rendered under InputBarStyle (grey) so the bar reads as chrome
	// rather than text. The bar is two cells wide; textInputWidth() compensates
	// so wrap happens inside the bar, not past it. The gutter background
	// matches the textarea's row tint so the gutter + textarea read as one
	// continuous input box across all rows.
	gutterStyle := m.theme.InputBarStyle.Background(m.inputBg)
	bar := gutterStyle.Render("▌") + gutterStyle.Render(" ")
	lines := strings.Split(base, "\n")
	for i, ln := range lines {
		lines[i] = bar + ln
	}
	return strings.Join(lines, "\n")
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
	// QUM-664: reserve two cells for the "▌ " gutter so wrap stays inside the
	// bar. Width is computed from m.width only when it is positive — at
	// construction time before SetWidth runs, m.width is 0 and the textarea
	// should receive the unmodified 0 so its own defaults govern.
	w := m.width
	if w > 0 {
		w -= lipgloss.Width(inputBarGutter)
	}
	if m.pendingPreview != "" {
		indicatorLen := len(queuedIndicator(m.pendingPreview)) + 2 // +2 for leading spaces
		w -= indicatorLen
	}
	if m.width > 0 && w < 4 {
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

// CursorEnd moves the textarea cursor to the end of the buffer. Used by the
// Esc-reload path (QUM-576) so a reloaded draft lands with the cursor where
// the user expects to continue typing.
func (m *InputModel) CursorEnd() {
	m.ta.CursorEnd()
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
		m.ta.Placeholder = inputPlaceholderHelp
	}
}
