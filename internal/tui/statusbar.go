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
	contextTokens  int // latest input_tokens from assistant message
	contextLimit   int // context window size derived from model name
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
	if m.contextTokens > 0 && m.contextLimit > 0 {
		parts = append(parts, fmt.Sprintf("%s/%s tokens",
			formatTokenCount(m.contextTokens),
			formatTokenCount(m.contextLimit)))
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

// SetTokenUsage updates the context token counter. The incoming value is the
// latest input_tokens snapshot (not cumulative), so we replace rather than
// accumulate. (QUM-385)
func (m *StatusBarModel) SetTokenUsage(inputTokens int) {
	m.contextTokens = inputTokens
}

// SetContextLimit sets the context window size derived from the model name.
// (QUM-385)
func (m *StatusBarModel) SetContextLimit(limit int) {
	m.contextLimit = limit
}

// SetRestartElapsed updates the restart-in-flight indicator (QUM-260).
// Pass 0 to clear.
func (m *StatusBarModel) SetRestartElapsed(d time.Duration) {
	m.restartElapsed = d
}

// formatTokenCount renders a token count in compact form: "500", "42.3k", "1M".
func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		if n%1_000 == 0 {
			return fmt.Sprintf("%dk", n/1_000)
		}
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

const defaultContextLimit = 1_000_000

var modelContextWindows = map[string]int{
	"claude-opus-4":   1_000_000,
	"claude-sonnet-4": 1_000_000,
	"claude-mythos":   1_000_000,
	"claude-haiku-4":  200_000,
}

// modelContextLimit returns the context window size in tokens for the given
// model name. Matches by prefix. Returns defaultContextLimit for unrecognized
// models. (QUM-385)
func modelContextLimit(model string) int {
	for prefix, limit := range modelContextWindows {
		if strings.HasPrefix(model, prefix) {
			return limit
		}
	}
	return defaultContextLimit
}
