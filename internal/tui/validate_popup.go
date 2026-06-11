package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// validatePopupTailCap caps the on-screen line buffer (last N lines of validate
// output kept for display). 200 per QUM-588 spec.
const validatePopupTailCap = 200

// ValidatePopupState enumerates the high-level UI state of the validate
// popup. The popup is owned by AppModel and reacts to ValidateEventMsg
// dispatched from the supervisor's validateEmitter via cmd/enter.go.
//
// Lifecycle for a merge:
//   - hidden → queued (on merge.queued)        // when contention is detected
//   - hidden|queued → runningHidden (on merge.validate-started)
//   - runningHidden → runningVisible (after validatePopupAfter, via timer)
//   - runningVisible ↔ minimized (on toggle key)
//   - any running state → failed (on merge.validate-ended exit!=0)
//   - any running state → hidden (on merge.validate-ended exit=0)
type ValidatePopupState int

const (
	PopupHidden ValidatePopupState = iota
	PopupQueued
	PopupRunningHidden
	PopupRunningVisible
	PopupMinimized
	PopupFailed
)

// ValidatePopupModel is the live validate-output popup (QUM-588). It is a
// Bubble Tea sub-model owned by AppModel; AppModel forwards ValidateEventMsg
// and validatePopupTickMsg / validatePopupTimerMsg here, and queries Visible()
// / View() / PillText() during rendering.
//
// The model is intentionally small: no virtualization, no scrolling beyond
// the tail cap, no mouse support (keybind toggles only). Spec called for
// minimize/restore; everything else is YAGNI for the v1 surface.
type ValidatePopupModel struct {
	theme         *Theme
	width, height int

	state ValidatePopupState

	// auto-open threshold; 0 = use default 10s.
	popupAfter time.Duration

	// cmd is the validate command string captured from merge.validate-started.
	cmd string
	// logPath is the on-disk validate log path captured from validate-started/ended.
	logPath string
	// behind is the in-flight agent name captured from merge.queued.
	behind string
	// elapsed-clock anchors.
	queuedAt    time.Time
	validateAt  time.Time
	endedExit   string // "0", "nonzero", or "" while running
	endedErrMsg string // populated on failure (truncated)

	// tail is a ring of the most-recent validate output lines.
	tail []string

	// now is injected for testability; production callers leave nil and the
	// model uses time.Now.
	now func() time.Time
}

// NewValidatePopupModel constructs an empty, hidden popup bound to theme.
// popupAfter is the auto-open threshold (use 0 to default to 10s).
func NewValidatePopupModel(theme *Theme, popupAfter time.Duration) ValidatePopupModel {
	return ValidatePopupModel{theme: theme, popupAfter: popupAfter, state: PopupHidden}
}

// SetSize updates the centering dimensions for layout.
func (m *ValidatePopupModel) SetSize(w, h int) {
	m.width = w
	m.height = h
}

// SetNow installs a test-time clock. Pass nil to clear (production uses time.Now).
func (m *ValidatePopupModel) SetNow(fn func() time.Time) { m.now = fn }

func (m ValidatePopupModel) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Visible reports whether the popup should occupy the screen overlay. Status-
// bar pill rendering uses Pill() separately and is non-overlapping.
func (m ValidatePopupModel) Visible() bool {
	return m.state == PopupQueued || m.state == PopupRunningVisible || m.state == PopupFailed
}

// MinimizedActive reports whether a status-bar pill should be rendered.
func (m ValidatePopupModel) MinimizedActive() bool {
	return m.state == PopupMinimized
}

// State exposes the internal state for tests/assertions.
func (m ValidatePopupModel) State() ValidatePopupState { return m.state }

// Tail returns a shallow copy of the buffered output lines (oldest first).
func (m ValidatePopupModel) Tail() []string {
	out := make([]string, len(m.tail))
	copy(out, m.tail)
	return out
}

// LogPath returns the captured validate log path.
func (m ValidatePopupModel) LogPath() string { return m.logPath }

// Cmd returns the validate command string.
func (m ValidatePopupModel) Cmd() string { return m.cmd }

// Behind returns the agent name we're/were queued behind (empty when uncontended).
func (m ValidatePopupModel) Behind() string { return m.behind }

// validatePopupTimerMsg is dispatched once per merge.validate-started after
// the configured popup-after threshold; if the popup is still in
// runningHidden it transitions to runningVisible.
type validatePopupTimerMsg struct{ at time.Time }

// validatePopupTickMsg is the 1Hz self-perpetuating tick that re-renders the
// elapsed-time labels. Self-stops when popup returns to hidden.
type validatePopupTickMsg struct{}

// Handle routes a ValidateEventMsg through the popup state machine. Returns
// optional tea.Cmds (timer + tick) the caller (AppModel) should run.
func (m *ValidatePopupModel) Handle(msg ValidateEventMsg) []tea.Cmd {
	now := m.clock()
	var cmds []tea.Cmd
	switch msg.Step {
	case "merge.queued":
		m.behind = msg.KV["behind"]
		m.queuedAt = now
		if m.state == PopupHidden {
			m.state = PopupQueued
			cmds = append(cmds, m.tickCmd())
		}
	case "merge.starting":
		// Lock acquired. If we'd been in PopupQueued, we transition straight
		// into runningHidden — but merge.validate-started will follow when
		// validate actually starts. Treat this as a no-op for state but keep
		// the behind/queuedAt context for the eventual queued-wait readout.
	case "merge.validate-started":
		m.cmd = msg.KV["cmd"]
		m.logPath = msg.KV["log_path"]
		m.validateAt = now
		m.endedExit = ""
		m.endedErrMsg = ""
		m.tail = m.tail[:0]
		// Move into runningHidden and arm the auto-open timer.
		m.state = PopupRunningHidden
		cmds = append(cmds, m.timerCmd())
		cmds = append(cmds, m.tickCmd())
	case "merge.validate-line":
		if line := msg.KV["line"]; line != "" {
			m.appendLine(line)
		}
	case "merge.validate-ended":
		m.endedExit = msg.KV["exit"]
		m.endedErrMsg = msg.KV["error"]
		if path := msg.KV["log_path"]; path != "" {
			m.logPath = path
		}
		if m.endedExit == "0" {
			m.state = PopupHidden
		} else {
			// Failure auto-restores the popup regardless of prior state.
			m.state = PopupFailed
		}
	}
	return cmds
}

// HandleTimer transitions runningHidden → runningVisible when the auto-open
// threshold has elapsed.
func (m *ValidatePopupModel) HandleTimer(_ validatePopupTimerMsg) {
	if m.state == PopupRunningHidden {
		m.state = PopupRunningVisible
	}
}

// ToggleMinimize swaps between visible and minimized states when applicable.
// Returns true if the keypress was consumed.
func (m *ValidatePopupModel) ToggleMinimize() bool {
	switch m.state {
	case PopupRunningVisible:
		m.state = PopupMinimized
		return true
	case PopupMinimized:
		m.state = PopupRunningVisible
		return true
	case PopupFailed:
		// Failure is sticky — minimize is intentionally disabled so the
		// operator can't lose track of the failure footer.
		return false
	}
	return false
}

// Dismiss clears the PopupFailed state and returns the popup to hidden so the
// operator can resume work after a post-merge validate failure (QUM-609).
// Returns true if the dismiss was consumed; false (no-op) in any other state.
func (m *ValidatePopupModel) Dismiss() bool {
	if m.state != PopupFailed {
		return false
	}
	m.Reset()
	return true
}

// Reset returns the model to hidden state (used by tests / session restart).
func (m *ValidatePopupModel) Reset() {
	m.state = PopupHidden
	m.behind = ""
	m.cmd = ""
	m.logPath = ""
	m.endedExit = ""
	m.endedErrMsg = ""
	m.tail = m.tail[:0]
}

func (m *ValidatePopupModel) appendLine(line string) {
	if len(m.tail) >= validatePopupTailCap {
		// Shift left; drop the oldest line.
		copy(m.tail, m.tail[1:])
		m.tail[len(m.tail)-1] = line
		return
	}
	m.tail = append(m.tail, line)
}

func (m ValidatePopupModel) timerCmd() tea.Cmd {
	d := m.popupAfter
	if d <= 0 {
		d = 10 * time.Second
	}
	return tea.Tick(d, func(t time.Time) tea.Msg { return validatePopupTimerMsg{at: t} })
}

func (m ValidatePopupModel) tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return validatePopupTickMsg{} })
}

// HandleTick re-renders if still running; returns the next tickCmd or nil.
func (m *ValidatePopupModel) HandleTick(_ validatePopupTickMsg) tea.Cmd {
	if m.state == PopupHidden {
		return nil
	}
	return m.tickCmd()
}

// Pill renders the status-bar pill text used while the popup is minimized.
// Empty string when no pill should be shown.
func (m ValidatePopupModel) Pill() string {
	if !m.MinimizedActive() {
		return ""
	}
	elapsed := m.clock().Sub(m.validateAt).Round(time.Second)
	return fmt.Sprintf("validate: %s, running", elapsed)
}

// View renders the popup overlay. Returns empty string when not Visible().
func (m ValidatePopupModel) View() string {
	if !m.Visible() {
		return ""
	}
	style := lipgloss.NewStyle().
		Padding(0, 1).
		BorderStyle(lipgloss.RoundedBorder())
	if m.theme != nil {
		style = style.BorderForeground(m.theme.Palette.Accent)
	}

	switch m.state {
	case PopupQueued:
		elapsed := m.clock().Sub(m.queuedAt).Round(time.Second)
		body := fmt.Sprintf("queued behind merge of %s — %s elapsed", m.behind, elapsed)
		return style.Render("validate: queued\n" + body)
	case PopupRunningVisible:
		return style.Render(m.renderRunning())
	case PopupFailed:
		return style.Render(m.renderFailed())
	}
	return ""
}

func (m ValidatePopupModel) renderRunning() string {
	elapsed := m.clock().Sub(m.validateAt).Round(time.Second)
	header := fmt.Sprintf("validate: running (%s)\n$ %s", elapsed, m.cmd)
	if m.logPath != "" {
		header += "\nlog: " + m.logPath
	}
	body := m.renderTail()
	footer := "(Ctrl+V to minimize)"
	return strings.Join([]string{header, "", body, footer}, "\n")
}

func (m ValidatePopupModel) renderFailed() string {
	elapsed := m.clock().Sub(m.validateAt).Round(time.Second)
	header := fmt.Sprintf("validate: FAILED (%s)\n$ %s", elapsed, m.cmd)
	body := m.renderTail()
	hint := ""
	if m.endedExit != "" {
		hint = fmt.Sprintf("exit=%s", m.endedExit)
	}
	if m.logPath != "" {
		if hint != "" {
			hint += ", "
		}
		hint += "see log: " + m.logPath
	}
	if hint == "" {
		hint = "validate failed"
	}
	hint += " · Esc to dismiss"
	return strings.Join([]string{header, "", body, hint}, "\n")
}

func (m ValidatePopupModel) renderTail() string {
	if len(m.tail) == 0 {
		return "(no output yet)"
	}
	// Compute visible window — cap by available height (header+footer ~ 5 rows).
	maxRows := m.height - 8
	if maxRows < 4 {
		maxRows = 4
	}
	start := 0
	if len(m.tail) > maxRows {
		start = len(m.tail) - maxRows
	}
	return strings.Join(m.tail[start:], "\n")
}
