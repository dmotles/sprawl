// Package tui — toast subsystem (QUM-649).
//
// Toasts are short-lived, right-anchored overlays drawn above the chat region
// and below any modal. They replace ad-hoc status-bar transient labels for
// content that should be visible momentarily without competing with the
// chat content or the modal stack.
//
// Dismiss policy is governed by a DismissContract:
//   - DismissTimer: auto-dismisses after a duration (Spawn returns a tea.Cmd
//     that fires a toastTimerMsg with the toast's ID).
//   - DismissCondition: dismissed when ClearCondition(id) is invoked.
//   - DismissUserOnly: only Ctrl+T (DismissAll) removes the toast.
//
// The Overlay rendering preserves the visible width of every base line and
// never writes to the bottom two rows (reserved for the input/status bar
// area).

package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
)

// ToastStyle selects the toast's accent color.
type ToastStyle int

const (
	// ToastInfo is the default neutral/cyan style.
	ToastInfo ToastStyle = iota
	// ToastWarning is the amber attention style.
	ToastWarning
	// ToastError is the red failure style.
	ToastError
)

// DismissKind enumerates the supported toast dismiss policies.
type DismissKind int

const (
	// DismissTimer auto-dismisses after a duration.
	DismissTimer DismissKind = iota
	// DismissCondition is keyed by a string ID and dismissed via
	// ClearCondition.
	DismissCondition
	// DismissUserOnly persists until the user dismisses it (Ctrl+T clears
	// all).
	DismissUserOnly
)

// DismissContract describes how/when a toast should be removed.
type DismissContract struct {
	Kind      DismissKind
	Timer     time.Duration
	Condition string
}

// TimerDismiss returns a DismissContract that auto-dismisses after d.
func TimerDismiss(d time.Duration) DismissContract {
	return DismissContract{Kind: DismissTimer, Timer: d}
}

// ConditionDismiss returns a DismissContract keyed by id; the toast is removed
// when ClearCondition(id) is invoked.
func ConditionDismiss(id string) DismissContract {
	return DismissContract{Kind: DismissCondition, Condition: id}
}

// UserOnlyDismiss returns a DismissContract that requires explicit user
// dismissal (Ctrl+T DismissAll).
func UserOnlyDismiss() DismissContract {
	return DismissContract{Kind: DismissUserOnly}
}

// Toast is a single notification entry. ID is optional on Spawn — the model
// auto-assigns a unique ID when empty so callers can dispatch without
// bookkeeping.
type Toast struct {
	ID        string
	Text      string
	Style     ToastStyle
	DismissOn DismissContract
}

// ToastModel owns the live toast stack and renders the right-anchored
// overlay. Embed in AppModel; route ToastSpawnMsg / ToastDismissMsg /
// ToastConditionClearedMsg / toastTimerMsg through the reducer wrappers in
// app.go.
type ToastModel struct {
	theme        *Theme
	width        int
	height       int
	headerHeight int
	toasts       []Toast
	seq          uint64
}

// NewToastModel constructs an empty ToastModel bound to the given theme.
func NewToastModel(theme *Theme) ToastModel {
	return ToastModel{theme: theme}
}

// SetSize records the current terminal dimensions. Width is used by the
// Overlay renderer; height bounds the number of visible toasts (the bottom
// two rows are always preserved for the input/status bar).
func (m *ToastModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// SetHeaderHeight records the height (in rows) of the SPRAWL header strip
// that the chat region is rendered below. Toasts anchor at headerHeight+1
// (one row below the header). When n is 0 (no header), toasts anchor at
// row 0. Negative values are clamped to 0.
func (m *ToastModel) SetHeaderHeight(n int) {
	if n < 0 {
		n = 0
	}
	m.headerHeight = n
}

// Spawn appends t to the stack. If t.ID is empty an auto-ID is assigned. If
// t.DismissOn.Kind == DismissTimer, the returned tea.Cmd fires a
// toastTimerMsg after the configured duration.
func (m *ToastModel) Spawn(t Toast) tea.Cmd {
	if t.ID == "" {
		m.seq++
		t.ID = fmt.Sprintf("toast-%d", m.seq)
	}
	m.toasts = append(m.toasts, t)
	if t.DismissOn.Kind == DismissTimer {
		id := t.ID
		d := t.DismissOn.Timer
		return tea.Tick(d, func(time.Time) tea.Msg {
			return toastTimerMsg{ID: id}
		})
	}
	return nil
}

// Dismiss removes the toast with the matching ID. Idempotent — unknown IDs
// are silently ignored.
func (m *ToastModel) Dismiss(id string) {
	for i, t := range m.toasts {
		if t.ID == id {
			m.toasts = append(m.toasts[:i], m.toasts[i+1:]...)
			return
		}
	}
}

// DismissAll clears every toast.
func (m *ToastModel) DismissAll() {
	m.toasts = nil
}

// ClearCondition removes every toast whose DismissOn is keyed by condID.
func (m *ToastModel) ClearCondition(condID string) {
	filtered := m.toasts[:0]
	for _, t := range m.toasts {
		if t.DismissOn.Kind == DismissCondition && t.DismissOn.Condition == condID {
			continue
		}
		filtered = append(filtered, t)
	}
	// Allocate a fresh backing array so the trimmed entries don't pin memory.
	out := make([]Toast, len(filtered))
	copy(out, filtered)
	m.toasts = out
}

// Toasts returns a copy of the current toast stack (preserved insertion
// order).
func (m ToastModel) Toasts() []Toast {
	if len(m.toasts) == 0 {
		return nil
	}
	out := make([]Toast, len(m.toasts))
	copy(out, m.toasts)
	return out
}

// Empty reports whether the toast stack is empty.
func (m ToastModel) Empty() bool { return len(m.toasts) == 0 }

// Overlay returns base with the toast stack composited on top, anchored
// horizontally centered immediately below the header (anchor row =
// headerHeight + 1, or row 0 when no header is set). Toasts stack
// vertically in insertion order; each toast is a 3-line bordered box.
// Visible line widths are preserved (cell-by-cell) and the bottom two
// rows of base are never altered. Toasts that would land within the
// bottom-two-row reserve are skipped.
func (m ToastModel) Overlay(base string) string {
	if len(m.toasts) == 0 {
		return base
	}
	lines := strings.Split(base, "\n")
	const bottomReserve = 2
	maxRow := len(lines) - bottomReserve
	anchor := m.headerHeight
	if anchor > 0 {
		anchor++ // one blank row of breathing room below the header
	}
	if anchor < 0 {
		anchor = 0
	}
	for _, t := range m.toasts {
		boxed := m.renderToast(t)
		boxLines := strings.Split(boxed, "\n")
		if anchor+len(boxLines) > maxRow {
			break
		}
		for i, bl := range boxLines {
			row := anchor + i
			if row < 0 || row >= len(lines) {
				continue
			}
			lines[row] = compositeCentered(lines[row], bl)
		}
		anchor += len(boxLines)
	}
	return strings.Join(lines, "\n")
}

// compositeCentered overlays `box` horizontally centered onto `line`,
// preserving the visible width of `line` and the left/right base segments
// that fall outside the box footprint.
func compositeCentered(line, box string) string {
	lineW := ansi.StringWidth(line)
	boxW := ansi.StringWidth(box)
	if boxW >= lineW {
		return ansi.Truncate(box, lineW, "")
	}
	leftW := (lineW - boxW) / 2
	rightW := lineW - boxW - leftW

	leftPart := ansi.Truncate(line, leftW, "")
	if lw := ansi.StringWidth(leftPart); lw < leftW {
		leftPart += strings.Repeat(" ", leftW-lw)
	}
	rightPart := ansi.TruncateLeft(line, leftW+boxW, "")
	if rw := ansi.StringWidth(rightPart); rw < rightW {
		rightPart += strings.Repeat(" ", rightW-rw)
	} else if rw > rightW {
		rightPart = ansi.Truncate(rightPart, rightW, "")
	}
	return leftPart + box + rightPart
}

// renderToast formats a single toast as a 3-line bordered box. The border
// and foreground both track the per-style palette color: ToastInfo →
// Primary (accent), ToastWarning → Warning, ToastError → Error.
func (m ToastModel) renderToast(t Toast) string {
	if m.theme == nil {
		return lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			Padding(0, 1).
			Render(t.Text)
	}
	var c color.Color
	switch t.Style {
	case ToastWarning:
		c = m.theme.Palette.Warning
	case ToastError:
		c = m.theme.Palette.Error
	default: // ToastInfo
		c = m.theme.Palette.Primary
	}
	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if c != nil {
		style = style.BorderForeground(c).Foreground(c)
	}
	return style.Render(t.Text)
}
