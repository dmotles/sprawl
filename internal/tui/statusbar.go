package tui

import (
	"fmt"
	"strings"
	"time"
)

// StatusBarModel renders a single-line status bar.
type StatusBarModel struct {
	repoName       string
	version        string
	agentCount     int
	turnState      TurnState
	width          int
	theme          *Theme
	sessionCostUsd float64
	sessionID      string
	selectMode     bool
	// restartElapsed is non-zero while the TUI is waiting on async restart
	// work (FinalizeHandoff + Prepare). Rendered as "restart Ns" so the
	// user sees a live elapsed counter instead of a frozen UI (QUM-260).
	restartElapsed time.Duration
}

// NewStatusBarModel creates a status bar with the given info.
func NewStatusBarModel(theme *Theme, repoName, version string, agentCount int) StatusBarModel {
	return StatusBarModel{
		repoName:   repoName,
		version:    version,
		agentCount: agentCount,
		theme:      theme,
	}
}

// View renders the status bar as a single line.
func (m StatusBarModel) View() string {
	left := fmt.Sprintf(" %s", m.repoName)
	if m.selectMode {
		left = " -- SELECT -- " + m.repoName
	}

	var stateStr string
	switch m.turnState {
	case TurnThinking:
		stateStr = "Thinking..."
	case TurnStreaming:
		stateStr = "Streaming..."
	case TurnComplete:
		stateStr = "Complete"
	default:
		stateStr = ""
	}

	var parts []string
	if m.restartElapsed > 0 {
		parts = append(parts, fmt.Sprintf("restart %ds", int(m.restartElapsed.Seconds())))
	}
	if m.sessionCostUsd > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", m.sessionCostUsd))
	}
	if m.sessionID != "" {
		parts = append(parts, "sess:"+m.sessionID)
	}
	if stateStr != "" {
		parts = append(parts, stateStr)
	}
	parts = append(parts, m.version)
	parts = append(parts, fmt.Sprintf("agents: %d", m.agentCount))
	parts = append(parts, "? Help")
	right := " " + strings.Join(parts, " | ") + " "

	gap := m.width - len(left) - len(right)
	if gap < 0 {
		gap = 0
	}

	line := left + strings.Repeat(" ", gap) + right
	return m.theme.StatusBar.Width(m.width).Render(line)
}

// SetWidth updates the status bar width.
func (m *StatusBarModel) SetWidth(w int) {
	m.width = w
}

// SetTurnState updates the displayed turn state.
func (m *StatusBarModel) SetTurnState(state TurnState) {
	m.turnState = state
}

// SetTurnCost updates the session cost. The incoming cost is session-cumulative
// (total_cost_usd from Claude's result message), so we replace rather than
// accumulate to avoid double-counting across turns. (QUM-366)
func (m *StatusBarModel) SetTurnCost(cost float64) {
	m.sessionCostUsd = cost
}

// SetAgentCount updates the displayed agent count.
func (m *StatusBarModel) SetAgentCount(n int) {
	m.agentCount = n
}

// SetSessionID updates the displayed Claude session ID. The caller should
// pass the short (8-char) display form; the status bar renders it verbatim.
func (m *StatusBarModel) SetSessionID(id string) {
	m.sessionID = id
}

// SetSelectMode toggles the SELECT-mode indicator on the left of the bar.
func (m *StatusBarModel) SetSelectMode(on bool) {
	m.selectMode = on
}

// SetRestartElapsed updates the restart-in-flight indicator (QUM-260).
// Pass 0 to clear.
func (m *StatusBarModel) SetRestartElapsed(d time.Duration) {
	m.restartElapsed = d
}
