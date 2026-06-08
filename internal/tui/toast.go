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
	theme  *Theme
	width  int
	height int
	toasts []Toast
	seq    uint64
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

// Overlay returns base with the toast stack composited on top, anchored to
// the top-right. Visible line widths are preserved (cell-by-cell) and the
// bottom two rows of base are never altered.
func (m ToastModel) Overlay(base string) string {
	if len(m.toasts) == 0 {
		return base
	}
	lines := strings.Split(base, "\n")
	maxToasts := len(lines) - 2
	if maxToasts < 0 {
		maxToasts = 0
	}
	n := len(m.toasts)
	if n > maxToasts {
		n = maxToasts
	}
	for i := 0; i < n; i++ {
		boxed := m.renderToast(m.toasts[i])
		boxW := ansi.StringWidth(boxed)
		line := lines[i]
		lineW := ansi.StringWidth(line)
		if boxW >= lineW {
			lines[i] = ansi.Truncate(boxed, lineW, "")
			continue
		}
		leftW := lineW - boxW
		leftPart := ansi.Truncate(line, leftW, "")
		leftPartW := ansi.StringWidth(leftPart)
		if leftPartW < leftW {
			leftPart += strings.Repeat(" ", leftW-leftPartW)
		}
		lines[i] = leftPart + boxed
	}
	return strings.Join(lines, "\n")
}

// renderToast formats a single toast as a styled, padded single-line box.
func (m ToastModel) renderToast(t Toast) string {
	box := " " + t.Text + " "
	if m.theme == nil {
		return box
	}
	var fg color.Color
	switch t.Style {
	case ToastWarning:
		fg = m.theme.Palette.Warning
	case ToastError:
		fg = m.theme.Palette.Error
	default:
		fg = m.theme.Palette.Info
	}
	if fg == nil {
		return box
	}
	return lipgloss.NewStyle().Foreground(fg).Render(box)
}
