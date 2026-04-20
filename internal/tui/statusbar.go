package tui

import (
	"fmt"
	"strings"
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

// SetTurnCost updates the cumulative session cost.
func (m *StatusBarModel) SetTurnCost(cost float64) {
	m.sessionCostUsd += cost
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
