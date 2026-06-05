package tui

import (
	"fmt"
	"strings"
	"time"
)

// EventDropSegment describes one subscriber's drop telemetry surfaced in the
// status bar (QUM-681). Pushed by AppModel on the mcpOpTickMsg cadence; the
// caller filters to subscribers whose last drop is within dropClearInterval
// so the segment auto-clears after a quiet period.
type EventDropSegment struct {
	Name  string
	Count uint64
}

// OpDescriptor describes one in-flight MCP tool call surfaced in the status
// bar (QUM-497). The status bar renders elapsed time live (≥1Hz) so a hung
// tool call is visible long before the user reaches for Ctrl-C.
type OpDescriptor struct {
	CallID  string
	Tool    string
	Caller  string
	Step    string
	Started time.Time
}

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
	// selectionMode is the QUM-617 mouse-capture-off toggle (Ctrl-/). When
	// true, the status bar renders a prominent SELECT chip on the left so
	// users discover the mode and know how to leave it. Distinct from
	// selectMode above, which is the QUM-281 keyboard viewport-select mode.
	selectionMode bool
	contextTokens int // latest input_tokens from assistant message
	contextLimit  int // context window size derived from model name
	// restartLabel is the consolidation phase label (e.g. "Consolidating
	// timeline...") rendered in the right-side parts list while a background
	// consolidation pipeline is active after a handoff (QUM-391). Empty when
	// no consolidation is running.
	restartLabel string

	// pendingQuestionsDepth / pendingQuestionsAgent drive the
	// "🔔 <agent> is asking (Ctrl-Q)" segment (QUM-527 slice 2c). Depth==0
	// hides the segment entirely. pendingQuestionsHidden (QUM-611)
	// switches the hint to advertise the Esc-cancel affordance when the
	// modal is hidden but a question is still pending — the recovery hint
	// for the QUM-611 wedge state.
	pendingQuestionsDepth  int
	pendingQuestionsAgent  string
	pendingQuestionsHidden bool

	// activeOps lists in-flight MCP tool calls (QUM-497). When non-empty, a
	// "⏳ tool(caller) M:SS" segment is rendered as the first right-side part
	// so a hung tool call is visible long before the user Ctrl-Cs. The slice
	// is owned by the model — callers pass a fresh slice to SetActiveOps.
	activeOps []OpDescriptor

	// eventDrops surfaces EventBus drop telemetry as a ⚠ segment (QUM-681).
	// Owned by the model — refreshed by AppModel on each mcpOpTickMsg from
	// the runtime's DropTelemetry snapshot.
	eventDrops []EventDropSegment

	// validatePill renders a "validate: 12s, running" segment while the
	// validate popup is minimized (QUM-588). Empty hides the segment.
	validatePill string

	// resyncPill renders a "resyncing…" segment while a viewport resync is
	// in flight (QUM-669). Empty hides the segment.
	resyncPill string

	// transientLabel is the QUM-675 S5 single sink for the status/banner text
	// that used to land in the viewport via vp.AppendStatus / vp.AppendBanner.
	// Last-write-wins by construction; cleared by explicit reducer transitions
	// (TurnIdle→TurnThinking, user prompt submit, RestartCompleteMsg) rather
	// than an auto-decay timer.
	transientLabel string

	// nowFn returns the wall-clock time used for elapsed-time rendering.
	// Defaults to time.Now; tests override it for deterministic output.
	nowFn func() time.Time
}

// SetValidatePill installs the segment text shown while the validate popup
// is minimized. Empty hides the segment. QUM-588.
func (m *StatusBarModel) SetValidatePill(pill string) {
	m.validatePill = pill
}

// SetResyncPill installs the segment text shown while a viewport resync is
// in flight (QUM-669). Empty hides the segment.
func (m *StatusBarModel) SetResyncPill(text string) {
	m.resyncPill = text
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
	// QUM-617 selection mode (mouse capture off, native drag-select on)
	// supersedes the QUM-281 SELECT indicator visually because the user is
	// far more likely to land here by accident and need clear exit guidance.
	if m.selectionMode {
		left = " -- SELECT (mouse capture off) — Ctrl-/ to resume -- " + m.repoName
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
	if seg := m.pendingQuestionsSegment(); seg != "" {
		parts = append(parts, seg)
	}
	if seg := m.activeOpsSegment(); seg != "" {
		parts = append(parts, seg)
	}
	if seg := m.eventDropsSegment(); seg != "" {
		parts = append(parts, seg)
	}
	if m.validatePill != "" {
		parts = append(parts, m.validatePill)
	}
	if m.resyncPill != "" {
		parts = append(parts, m.resyncPill)
	}
	if m.restartLabel != "" {
		parts = append(parts, m.restartLabel)
	}
	if m.transientLabel != "" {
		parts = append(parts, m.transientLabel)
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

// SetSelectionMode toggles the QUM-617 mouse-capture-off indicator. Distinct
// from SetSelectMode (QUM-281 keyboard select); see the field comment.
func (m *StatusBarModel) SetSelectionMode(on bool) {
	m.selectionMode = on
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

// SetRestartLabel sets the consolidation phase label rendered in the status
// bar (QUM-391). Pass empty string to clear.
func (m *StatusBarModel) SetRestartLabel(label string) {
	m.restartLabel = label
}

// SetTransientLabel sets the QUM-675 S5 transient status text. This is the
// single sink for one-shot status/banner text formerly routed through
// vp.AppendStatus / vp.AppendBanner. Last-write-wins; pass "" to clear.
// Cleared by explicit reducer transitions, not by a timer.
func (m *StatusBarModel) SetTransientLabel(label string) {
	m.transientLabel = label
}

// TransientLabel returns the current QUM-675 transient-label payload (the
// single sink for one-shot status/banner text formerly routed through
// vp.AppendStatus / vp.AppendBanner). Empty string when nothing is installed.
// Read accessor used by tests to make clear-on-transition assertions precise.
func (m StatusBarModel) TransientLabel() string {
	return m.transientLabel
}

// SetPendingQuestions updates the pending-questions indicator (QUM-527 slice
// 2c). Depth==0 hides the indicator entirely.
func (m *StatusBarModel) SetPendingQuestions(depth int, agent string) {
	m.pendingQuestionsDepth = depth
	m.pendingQuestionsAgent = agent
}

// SetQuestionModalHidden tells the status bar whether the question modal is
// currently hidden while a request is still pending (QUM-611). When true, the
// pending-questions segment advertises the Esc-cancel affordance so users
// stuck in the hidden-but-pending wedge state have an obvious recovery path.
func (m *StatusBarModel) SetQuestionModalHidden(hidden bool) {
	m.pendingQuestionsHidden = hidden
}

// pendingQuestionsSegment renders the pending-questions indicator. The hint
// suffix is "(Ctrl-Q)" when the modal is visible, "(Ctrl-Q to reopen, Esc to
// cancel)" when the modal is hidden but a request is still pending (QUM-611).
// Empty when depth==0.
func (m StatusBarModel) pendingQuestionsSegment() string {
	if m.pendingQuestionsDepth <= 0 {
		return ""
	}
	agent := m.pendingQuestionsAgent
	if agent == "" {
		agent = "?"
	}
	hint := "Ctrl-Q"
	if m.pendingQuestionsHidden {
		hint = "Ctrl-Q to reopen, Esc to cancel"
	}
	if m.pendingQuestionsDepth == 1 {
		return fmt.Sprintf("🔔 %s is asking (%s)", agent, hint)
	}
	return fmt.Sprintf("🔔 %s +%d more (%s)", agent, m.pendingQuestionsDepth-1, hint)
}

// SetEventDrops replaces the EventBus drop-telemetry segment list rendered
// in the status bar (QUM-681). Pass nil/empty to clear. Callers should pass
// only subscribers whose drops are still recent (within dropClearInterval)
// so the segment auto-clears after a quiet period.
func (m *StatusBarModel) SetEventDrops(drops []EventDropSegment) {
	if len(drops) == 0 {
		m.eventDrops = nil
		return
	}
	m.eventDrops = append(m.eventDrops[:0], drops...)
}

// eventDropsSegment renders the "⚠ events dropped: N (name)" indicator, or
// "⚠ events dropped: N (worst) (+K more)" when multiple subscribers are
// dropping. Empty when no entries are present.
func (m StatusBarModel) eventDropsSegment() string {
	if len(m.eventDrops) == 0 {
		return ""
	}
	worst := m.eventDrops[0]
	base := fmt.Sprintf("⚠ events dropped: %d (%s)", worst.Count, worst.Name)
	if extra := len(m.eventDrops) - 1; extra > 0 {
		base += fmt.Sprintf(" (+%d more)", extra)
	}
	return base
}

// SetActiveOps replaces the in-flight MCP ops list rendered in the status
// bar (QUM-497). Pass nil/empty to clear. Callers should re-call this on
// every reduce that mutates the op set.
func (m *StatusBarModel) SetActiveOps(ops []OpDescriptor) {
	if len(ops) == 0 {
		m.activeOps = nil
		return
	}
	m.activeOps = append(m.activeOps[:0], ops...)
}

// SetNowFn overrides the wall-clock used to compute elapsed time on active
// ops. Tests use this for deterministic output. Passing nil restores the
// default time.Now.
func (m *StatusBarModel) SetNowFn(fn func() time.Time) {
	m.nowFn = fn
}

func (m *StatusBarModel) clock() time.Time {
	if m.nowFn != nil {
		return m.nowFn()
	}
	return time.Now()
}

// activeOpsSegment renders the "⏳ tool(caller) M:SS [+N more]" indicator.
// Empty string when no ops are active.
func (m StatusBarModel) activeOpsSegment() string {
	if len(m.activeOps) == 0 {
		return ""
	}
	now := m.clock()
	const showAtMost = 2
	visible := m.activeOps
	if len(visible) > showAtMost {
		visible = visible[:showAtMost]
	}
	pieces := make([]string, 0, len(visible)+1)
	for _, op := range visible {
		pieces = append(pieces, formatOpDescriptor(op, now))
	}
	if extra := len(m.activeOps) - len(visible); extra > 0 {
		pieces = append(pieces, fmt.Sprintf("+%d more", extra))
	}
	return "⏳ " + strings.Join(pieces, " · ")
}

// formatOpDescriptor renders one op as "tool(caller) M:SS" or
// "tool(caller): step M:SS" when a step is set.
func formatOpDescriptor(op OpDescriptor, now time.Time) string {
	elapsed := now.Sub(op.Started)
	if elapsed < 0 {
		elapsed = 0
	}
	caller := op.Caller
	if caller == "" {
		caller = "?"
	}
	left := fmt.Sprintf("%s(%s)", op.Tool, caller)
	if op.Step != "" {
		left += ": " + op.Step
	}
	return fmt.Sprintf("%s T+%s", left, formatElapsed(elapsed))
}

// formatElapsed renders a duration as M:SS (or H:MM:SS for ≥1h). Always
// minimum two-digit seconds so the bar doesn't shimmy.
func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	hours := total / 3600
	mins := (total % 3600) / 60
	secs := total % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, mins, secs)
	}
	return fmt.Sprintf("%d:%02ds", mins, secs)
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
