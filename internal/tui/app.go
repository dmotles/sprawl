package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/supervisor"
)

// Panel identifies which panel is active.
type Panel int

const (
	PanelTree Panel = iota
	PanelViewport
	PanelInput
	panelCount // sentinel for wrapping
)

// AgentBuffer stores the viewport state for a specific agent.
type AgentBuffer struct {
	Messages   []MessageEntry
	AutoScroll bool
}

// AppModel is the root Bubble Tea model composing all panels.
type AppModel struct {
	tree      TreeModel
	viewport  ViewportModel
	activity  ActivityPanelModel
	input     InputModel
	statusBar StatusBarModel
	confirm   ConfirmModel

	help     HelpModel
	showHelp bool

	palette     PaletteModel
	showPalette bool

	bridge    *Bridge
	turnState TurnState

	supervisor    supervisor.Supervisor
	sprawlRoot    string
	observedAgent string
	rootAgent     string
	childNodes    []TreeNode
	rootUnread    int
	agentBuffers  map[string]*AgentBuffer

	activePanel Panel
	showConfirm bool
	theme       Theme
	width       int
	height      int
	ready       bool
	tooSmall    bool

	showError   bool
	errorDialog ErrorDialogModel
	restartFunc func() (*Bridge, error)

	// quitting is set when the user confirms shutdown (Ctrl-C confirm
	// dialog). It guards against a late RestartSessionMsg triggered from an
	// EOF that arrived just before the user confirmed quit; without the
	// guard that restart would spawn a fresh Claude subprocess the user is
	// about to abandon.
	quitting bool

	// restarting tracks whether an async restartFunc invocation is in
	// flight (QUM-260). While set, ConsolidationProgressMsg ticks update
	// the status-bar elapsed counter; RestartCompleteMsg clears it.
	restarting       bool
	restartStartedAt time.Time
	// restartNow is the clock source for computing progress-tick elapsed
	// times. Tests inject a deterministic source; production defaults to
	// time.Now via restartClock().
	restartNow func() time.Time
	// restartTick is the interval between ConsolidationProgressMsg ticks
	// while a restart is in flight. Tests shorten it; zero means use
	// defaultRestartTick.
	restartTick time.Duration
}

const defaultRestartTick = time.Second

// NewAppModel constructs the root model with all sub-models.
// bridge may be nil for static placeholder mode.
// sup and sprawlRoot are optional; when provided, the tree polls agent status.
// restartFunc is called when the user requests a session restart after a crash.
func NewAppModel(accentColor, repoName, version string, bridge *Bridge, sup supervisor.Supervisor, sprawlRoot string, restartFunc func() (*Bridge, error)) AppModel {
	theme := NewTheme(accentColor)
	startPanel := PanelTree
	if bridge != nil {
		startPanel = PanelInput
	}
	rootAgent := "weave"
	app := AppModel{
		tree:          NewTreeModel(&theme),
		viewport:      NewViewportModel(&theme),
		activity:      NewActivityPanelModel(&theme),
		input:         NewInputModel(&theme),
		statusBar:     NewStatusBarModel(&theme, repoName, version, 0),
		help:          NewHelpModel(&theme),
		confirm:       NewConfirmModel(&theme),
		palette:       NewPaletteModel(&theme),
		bridge:        bridge,
		turnState:     TurnIdle,
		supervisor:    sup,
		sprawlRoot:    sprawlRoot,
		observedAgent: rootAgent,
		rootAgent:     rootAgent,
		agentBuffers:  make(map[string]*AgentBuffer),
		activePanel:   startPanel,
		theme:         theme,
		restartFunc:   restartFunc,
	}
	app.updateFocus()
	app.rebuildTree()
	app.activity.SetAgent(rootAgent)
	return app
}

// Init returns the bridge initialize command if a bridge is present,
// otherwise nil (the app waits for WindowSizeMsg).
func (m AppModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	if m.bridge != nil {
		cmds = append(cmds, m.bridge.Initialize())
	}
	if m.supervisor != nil {
		cmds = append(cmds, tickAgentsCmd(m.supervisor, m.sprawlRoot))
		cmds = append(cmds, tickActivityCmd(m.supervisor, m.observedAgent))
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// Update handles messages: window resize, global keybinds, bridge messages, and panel delegation.
func (m AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.tooSmall = IsTooSmall(m.width, m.height)
		if !m.tooSmall {
			m.resizePanels()
		}
		return m, nil

	case tea.MouseMsg:
		// Mouse capture is enabled (see View().MouseMode) so scroll wheel
		// events reach us. Suppress mouse events entirely while any modal is
		// visible — wheel scrolling behind a dialog would be disorienting —
		// and otherwise forward to the viewport (the only scrollable area).
		// Non-wheel clicks/motion are accepted but currently ignored; they
		// fall through viewport.Update harmlessly.
		if m.showHelp || m.showConfirm || m.showError || m.showPalette {
			return m, nil
		}
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		// Ctrl+C: show confirmation dialog (or ignore if already showing).
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'c' {
			if m.showConfirm {
				return m, nil
			}
			m.showConfirm = true
			m.confirm.Show()
			m.confirm.SetSize(m.width, m.height)
			return m, nil
		}

		// When confirm dialog is visible, route all keys to it.
		if m.showConfirm {
			var cmd tea.Cmd
			m.confirm, cmd = m.confirm.Update(msg)
			return m, cmd
		}

		// Toggle help on ? (but not while typing in the input panel) or F1.
		// F1 remains global since it's unambiguous — no one types F1 as text.
		if (msg.Code == '?' && m.activePanel != PanelInput) || msg.Code == tea.KeyF1 {
			m.showHelp = !m.showHelp
			return m, nil
		}

		// When help is shown, only Esc dismisses; swallow everything else.
		if m.showHelp {
			if msg.Code == tea.KeyEscape {
				m.showHelp = false
			}
			return m, nil
		}

		// When error dialog is shown, delegate all keys to it.
		if m.showError {
			cmd := m.errorDialog.Update(msg)
			return m, cmd
		}

		// When the command palette is open, route ALL keys to it — no
		// panel cycling, no input typing. Palette emits ClosePaletteMsg on
		// Esc or on command dispatch.
		if m.showPalette {
			var cmd tea.Cmd
			m.palette, cmd = m.palette.Update(msg)
			return m, cmd
		}

		// Ctrl+N / Ctrl+P: cycle observed agent globally (works from any
		// panel). Gated implicitly by the modal returns above.
		if msg.Mod&tea.ModCtrl != 0 && (msg.Code == 'n' || msg.Code == 'p') {
			delta := 1
			if msg.Code == 'p' {
				delta = -1
			}
			return m, m.cycleAgent(delta)
		}

		if msg.Code == tea.KeyTab {
			if msg.Mod&tea.ModShift != 0 {
				m.activePanel = (m.activePanel - 1 + panelCount) % panelCount
			} else {
				m.activePanel = (m.activePanel + 1) % panelCount
			}
			m.updateFocus()
			return m, nil
		}

		// QUM-281: viewport select mode. Scoped to PanelViewport so the input
		// panel still types 'v'/'y'/'j'/'k' as literals.
		if m.activePanel == PanelViewport {
			if cmd, handled := m.handleViewportSelectKey(msg); handled {
				return m, cmd
			}
		}

		// Delegate to active panel.
		return m.delegateKey(msg)

	case SessionInitializedMsg:
		// Only refresh the status bar session ID here. Do NOT touch the
		// viewport: on first launch the resume-replay transcript lives there,
		// and on restart the RestartSessionMsg handler already cleared the
		// viewport and appended the boundary banner.
		if m.bridge != nil {
			m.statusBar.SetSessionID(shortSessionID(m.bridge.SessionID()))
		}
		return m, nil

	case OpenPaletteMsg:
		if m.input.disabled || m.showConfirm || m.showError || m.showHelp || m.showPalette {
			return m, nil
		}
		m.palette.SetSize(m.width, m.height)
		m.palette.SetAgents(m.agentNames())
		m.palette.Show()
		m.showPalette = true
		return m, nil

	case ClosePaletteMsg:
		m.palette.Hide()
		m.showPalette = false
		return m, nil

	case ToggleHelpMsg:
		m.showHelp = !m.showHelp
		return m, nil

	case PaletteQuitMsg:
		m.quitting = true
		return m, tea.Quit

	case InjectPromptMsg:
		if msg.Template == "" || m.bridge == nil || m.turnState != TurnIdle {
			return m, nil
		}
		m.viewport.AppendStatus("/handoff dispatched — see output below")
		m.setTurnState(TurnThinking)
		m.input.SetDisabled(true)
		return m, m.bridge.SendMessage(msg.Template)

	case SubmitMsg:
		if msg.Text == "" || m.bridge == nil || m.turnState != TurnIdle {
			return m, nil
		}
		m.viewport.AppendUserMessage(msg.Text)
		m.setTurnState(TurnThinking)
		m.input.SetDisabled(true)
		return m, m.bridge.SendMessage(msg.Text)

	case UserMessageSentMsg:
		m.setTurnState(TurnStreaming)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case AssistantTextMsg:
		m.setTurnState(TurnStreaming)
		m.viewport.AppendAssistantChunk(msg.Text)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case ToolCallMsg:
		m.viewport.AppendToolCall(msg.ToolName, msg.Approved, msg.Input)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case SessionResultMsg:
		// Display result text only if no assistant text was already streamed.
		// When Claude returns text in the assistant message, it also appears
		// in result.Result — avoid duplicating it.
		if !msg.IsError && strings.TrimSpace(msg.Result) != "" && !m.viewport.HasPendingAssistant() {
			m.viewport.AppendAssistantChunk(strings.TrimSpace(msg.Result))
		}
		m.viewport.FinalizeAssistantMessage()
		if msg.IsError {
			m.viewport.AppendError(fmt.Sprintf("Error: %s", msg.Result))
		} else {
			m.viewport.AppendStatus(fmt.Sprintf("Completed in %dms, cost $%.4f", msg.DurationMs, msg.TotalCostUsd))
			m.statusBar.SetTurnCost(msg.TotalCostUsd)
		}
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(false)
		return m, nil

	case SessionErrorMsg:
		// Transport EOF is the normal end-of-session signal (clean exit or
		// /handoff). Auto-restart via Phase D rather than showing the crash
		// dialog — the user experience matches tmux weave's bash-loop
		// restart, but in-process.
		if errors.Is(msg.Err, io.EOF) {
			reason := "session ended"
			return m, tea.Batch(
				sendMsgCmd(SessionRestartingMsg{Reason: reason}),
				sendMsgCmd(RestartSessionMsg{}),
			)
		}
		if m.turnState == TurnThinking || m.turnState == TurnStreaming {
			m.errorDialog = NewErrorDialog(&m.theme, msg.Err)
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			m.setTurnState(TurnIdle)
			m.input.SetDisabled(true)
			return m, nil
		}
		m.viewport.AppendError(msg.Err.Error())
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(false)
		return m, nil

	case HandoffRequestedMsg:
		// Weave invoked the sprawl_handoff MCP tool. Reuse the EOF restart
		// path: status banner + restart, which closes the bridge and runs
		// FinalizeHandoff (consuming the signal file the supervisor wrote).
		return m, tea.Batch(
			sendMsgCmd(SessionRestartingMsg{Reason: "handoff"}),
			sendMsgCmd(RestartSessionMsg{}),
		)

	case SessionRestartingMsg:
		reason := msg.Reason
		if reason == "" {
			reason = "session ended"
		}
		// Force-close the palette if open — a restart swaps out the bridge,
		// so any pending palette dispatch would hit a stale channel.
		m.palette.Hide()
		m.showPalette = false
		m.viewport.AppendStatus(fmt.Sprintf("Session restarting (%s)...", reason))
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(true)
		return m, nil

	case RestartSessionMsg:
		// Ctrl-C confirm may have fired between the EOF that scheduled this
		// restart and its delivery. If the user already asked to quit, honor
		// that — do NOT spawn a new subprocess.
		if m.quitting {
			return m, tea.Quit
		}
		// Coalesce back-to-back RestartSessionMsg: if a restart is already
		// running asynchronously, drop the new one. The outcome is delivered
		// via RestartCompleteMsg regardless.
		if m.restarting {
			return m, nil
		}
		m.showError = false
		if m.bridge != nil {
			_ = m.bridge.Close()
			m.bridge = nil
		}
		if m.restartFunc == nil {
			return m, tea.Quit
		}
		// QUM-260: run restartFunc off the Bubble Tea main goroutine so the
		// UI stays responsive while FinalizeHandoff + Prepare + newSession
		// execute (back-to-back handoffs can block up to 120s waiting on
		// the prior background consolidation). Progress ticks drive the
		// status bar elapsed counter; RestartCompleteMsg delivers the
		// outcome.
		m.restarting = true
		m.restartStartedAt = m.restartClock()()
		m.input.SetDisabled(true)
		fn := m.restartFunc
		return m, tea.Batch(
			func() tea.Msg {
				b, err := fn()
				return RestartCompleteMsg{Bridge: b, Err: err}
			},
			m.restartTickCmd(),
		)

	case ConsolidationProgressMsg:
		// Ticks that arrive after the restart already completed are
		// harmless no-ops — drop them without rescheduling.
		if !m.restarting {
			m.statusBar.SetRestartElapsed(0)
			return m, nil
		}
		m.statusBar.SetRestartElapsed(msg.Elapsed)
		return m, m.restartTickCmd()

	case RestartCompleteMsg:
		m.restarting = false
		m.restartStartedAt = time.Time{}
		m.statusBar.SetRestartElapsed(0)
		// A Ctrl-C confirm landing mid-restart also shuts us down here.
		if m.quitting {
			return m, tea.Quit
		}
		if msg.Err != nil {
			m.errorDialog = NewErrorDialog(&m.theme, msg.Err)
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			m.input.SetDisabled(true)
			return m, nil
		}
		if msg.Bridge == nil {
			// No bridge and no error — shouldn't happen, but degrade
			// gracefully by showing a generic failure.
			m.errorDialog = NewErrorDialog(&m.theme, fmt.Errorf("restart produced nil bridge"))
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			m.input.SetDisabled(true)
			return m, nil
		}
		m.bridge = msg.Bridge
		shortID := shortSessionID(m.bridge.SessionID())
		m.viewport.SetMessages(nil)
		if shortID != "" {
			m.viewport.AppendStatus(fmt.Sprintf("— New session started (%s) —", shortID))
		} else {
			m.viewport.AppendStatus("— New session started —")
		}
		m.statusBar.SetSessionID(shortID)
		m.setTurnState(TurnIdle)
		m.input.SetDisabled(false)
		return m, m.bridge.Initialize()

	case TurnStateMsg:
		m.setTurnState(msg.State)
		if msg.State == TurnIdle {
			m.input.SetDisabled(false)
		}
		return m, nil

	case ActivityTickMsg:
		// Only apply the tick if it is for the currently-observed agent. A
		// selection change that happened mid-flight can race us; dropping a
		// stale tick is cheaper and simpler than cancelling the in-flight cmd.
		if msg.Agent == m.observedAgent {
			m.activity.SetEntries(msg.Entries)
		}
		if m.supervisor != nil {
			return m, scheduleActivityTick(m.supervisor, m.observedAgent)
		}
		return m, nil

	case AgentTreeMsg:
		m.childNodes = msg.Nodes
		// QUM-311: detect out-of-process inbox arrivals (child agents calling
		// `sprawl report done` / `sprawl messages send` from their own
		// processes) by comparing the disk-polled unread count to the
		// locally-tracked value. Any increase yields a banner so the user
		// gets the same UX whether the sender was in-process (InboxArrivalMsg
		// via the TUI notifier) or out-of-process (caught on the 2s tick).
		if msg.RootUnread > m.rootUnread {
			m.viewport.AppendStatus(fmt.Sprintf("inbox: %d new message(s) for weave", msg.RootUnread-m.rootUnread))
		}
		m.rootUnread = msg.RootUnread
		m.rebuildTree()
		m.statusBar.SetAgentCount(len(msg.Nodes) + 1) // +1 for weave root
		if m.supervisor != nil {
			return m, scheduleAgentTick(m.supervisor, m.sprawlRoot)
		}
		return m, nil

	case InboxArrivalMsg:
		// QUM-311: a TUI-aware notifier installed by cmd/enter.go dispatches
		// this whenever an in-process messages.Send call (MCP sprawl_send_async
		// from weave, or any Send in this process) delivers to the root
		// agent's maildir. Append a short banner with sender identity and
		// bump the unread counter; the next 2s tick reconciles the counter
		// from disk, and out-of-process arrivals are banner-surfaced there.
		from := msg.From
		if from == "" {
			from = "unknown"
		}
		m.viewport.AppendStatus(fmt.Sprintf("inbox: new message from %s", from))
		m.rootUnread++
		m.rebuildTree()
		return m, nil

	case ConfirmResultMsg:
		m.showConfirm = false
		m.confirm.Hide()
		if msg.Confirmed {
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil

	case SignalMsg:
		if !m.showConfirm {
			m.showConfirm = true
			m.confirm.Show()
			m.confirm.SetSize(m.width, m.height)
		}
		return m, nil

	case AgentSelectedMsg:
		// Save current viewport to buffer.
		m.agentBuffers[m.observedAgent] = &AgentBuffer{
			Messages:   m.viewport.GetMessages(),
			AutoScroll: m.viewport.IsAutoScroll(),
		}

		m.observedAgent = msg.Name

		// Load from buffer if exists, else show empty with status.
		if buf, ok := m.agentBuffers[msg.Name]; ok {
			m.viewport.SetMessages(buf.Messages)
			m.viewport.SetAutoScroll(buf.AutoScroll)
		} else {
			m.viewport.SetMessages(nil)
			m.viewport.AppendStatus(fmt.Sprintf("Observing %s...", msg.Name))
		}

		// Disable input for non-root agents.
		if msg.Name != m.rootAgent {
			m.input.SetDisabled(true)
		} else {
			m.input.SetDisabled(false)
		}

		// Refresh the activity panel for the newly-observed agent.
		m.activity.SetAgent(msg.Name)
		m.activity.SetEntries(nil)
		if m.supervisor != nil {
			return m, tickActivityCmd(m.supervisor, msg.Name)
		}
		return m, nil
	}

	return m, nil
}

// View renders the full TUI layout.
func (m AppModel) View() tea.View {
	if !m.ready {
		return tea.NewView("  Initializing...")
	}

	if m.tooSmall {
		msg := fmt.Sprintf("Terminal too small (minimum %dx%d)", MinTermWidth, MinTermHeight)
		v := tea.NewView(msg)
		v.AltScreen = true
		v.MouseMode = tea.MouseModeCellMotion
		return v
	}

	layout := ComputeLayout(m.width, m.height)

	// Render tree panel with border.
	treeBorder := m.borderStyle(PanelTree).
		Width(layout.TreeWidth - 2).
		Height(layout.TreeHeight - 2)
	treeView := treeBorder.Render(m.tree.View())

	// Render viewport panel with border.
	vpBorder := m.borderStyle(PanelViewport).
		Width(layout.ViewportWidth - 2).
		Height(layout.ViewportHeight - 2)
	vpView := vpBorder.Render(m.viewport.View())

	// Combine tree and viewport horizontally. On wide terminals, a third
	// column (activity panel) is added to the right. See QUM-296.
	var mainRow string
	if layout.ActivityWidth > 0 {
		actBorder := m.theme.InactiveBorder.
			Width(layout.ActivityWidth - 2).
			Height(layout.ActivityHeight - 2)
		actView := actBorder.Render(m.activity.View())
		mainRow = lipgloss.JoinHorizontal(lipgloss.Top, treeView, vpView, actView)
	} else {
		mainRow = lipgloss.JoinHorizontal(lipgloss.Top, treeView, vpView)
	}

	// Render input with border.
	inputBorder := m.borderStyle(PanelInput).
		Width(layout.InputWidth - 2).
		Height(layout.InputHeight - 2)
	inputView := inputBorder.Render(m.input.View())

	// Status bar.
	statusView := m.statusBar.View()

	// Stack vertically.
	content := lipgloss.JoinVertical(lipgloss.Left, mainRow, inputView, statusView)

	if m.showPalette {
		content = m.palette.View()
	}

	if m.showHelp {
		content = m.help.View()
	}

	if m.showConfirm {
		content = m.confirm.View()
	}

	if m.showError {
		content = m.errorDialog.View()
	}

	v := tea.NewView(content)
	v.AltScreen = true
	// QUM-280: mouse cell motion enables scroll-wheel events on the viewport.
	// Tradeoff: this breaks native terminal text-select-and-copy. Users can
	// typically hold Option/Alt (macOS) or Shift (most Linux terminals) while
	// dragging to force native select. QUM-281 owns the proper
	// selection-to-clipboard design.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m *AppModel) setTurnState(state TurnState) {
	m.turnState = state
	m.statusBar.SetTurnState(state)
	m.rebuildTree()
}

// PreloadTranscript replaces the viewport's message buffer with the given
// entries. Used on session resume to populate the viewport with the prior
// transcript before the TUI starts. No-op if entries is empty.
func (m *AppModel) PreloadTranscript(entries []MessageEntry) {
	if len(entries) == 0 {
		return
	}
	m.viewport.SetMessages(entries)
}

func (m *AppModel) rebuildTree() {
	nodes := PrependWeaveRoot(m.childNodes, m.turnState.String(), m.rootUnread)
	m.tree.SetNodes(nodes)
}

// agentNames returns the ordered list of known agent names (weave + children
// in tree order). Used by the palette's /switch agent-mode filter.
func (m *AppModel) agentNames() []string {
	names := []string{m.rootAgent}
	for _, n := range m.childNodes {
		names = append(names, n.Name)
	}
	return names
}

// cycleAgent returns a cmd that emits AgentSelectedMsg pointing at the next
// (delta=+1) or previous (delta=-1) agent in the tree, wrapping around. It's
// a no-op when there are fewer than two agents.
func (m *AppModel) cycleAgent(delta int) tea.Cmd {
	names := m.agentNames()
	if len(names) < 2 {
		return nil
	}
	idx := -1
	for i, n := range names {
		if n == m.observedAgent {
			idx = i
			break
		}
	}
	if idx < 0 {
		idx = 0
	}
	next := (idx + delta + len(names)) % len(names)
	selected := names[next]
	return sendMsgCmd(AgentSelectedMsg{Name: selected})
}

func (m *AppModel) resizePanels() {
	layout := ComputeLayout(m.width, m.height)

	// Panel borders take 2 cells total per axis (1 cell on each side). In
	// lipgloss v2, a Border()+Width(N) style sets the OUTER width to N and
	// reserves 2 of those cells for the border — leaving N-2 cells of inner
	// content. The View() below sets each border's outer width to
	// layout.<Panel>Width-2 (a 2-cell gutter between columns), so the inner
	// content budget we pass to each sub-model must be layout.<Panel>Width-4
	// (gutter + two border cells). Passing only -2 here is an off-by-two
	// that lets long tree rows bleed past the border and soft-wrap, which
	// then pushes the tree panel taller than its declared Height and clips
	// the input box off the bottom of the screen (QUM-324 residual).
	m.tree.SetSize(layout.TreeWidth-4, layout.TreeHeight-4)
	m.viewport.SetSize(layout.ViewportWidth-4, layout.ViewportHeight-4)
	if layout.ActivityWidth > 0 {
		m.activity.SetSize(layout.ActivityWidth-4, layout.ActivityHeight-4)
	} else {
		m.activity.SetSize(0, 0)
	}
	m.input.SetWidth(layout.InputWidth - 4)
	m.statusBar.SetWidth(layout.StatusWidth)
	m.help.SetSize(m.width, m.height)
	m.confirm.SetSize(m.width, m.height)
	m.errorDialog.SetSize(m.width, m.height)
	m.palette.SetSize(m.width, m.height)
}

func (m *AppModel) updateFocus() {
	if m.activePanel == PanelInput {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
}

func (m AppModel) borderStyle(panel Panel) lipgloss.Style {
	if panel == m.activePanel {
		return m.theme.ActiveBorder
	}
	return m.theme.InactiveBorder
}

// handleViewportSelectKey handles 'v'/'j'/'k'/'y'/'g'/'G'/Esc for the
// viewport select-mode UX (QUM-281). Returns handled=true to stop further
// routing; returns handled=false when the key is not a select-mode key, in
// which case the caller falls through to normal delegation.
func (m *AppModel) handleViewportSelectKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	// Entry: 'v' with no selection active.
	if !m.viewport.IsSelecting() {
		if msg.Code == 'v' && msg.Mod == 0 {
			m.viewport.EnterSelect()
			m.statusBar.SetSelectMode(m.viewport.IsSelecting())
			return nil, true
		}
		return nil, false
	}

	// Selection active — handle mode-specific keys.
	switch msg.Code {
	case tea.KeyEscape, 'v':
		m.viewport.ExitSelect()
		m.statusBar.SetSelectMode(false)
		return nil, true
	case 'j', tea.KeyDown:
		m.viewport.MoveCursor(1)
		return nil, true
	case 'k', tea.KeyUp:
		m.viewport.MoveCursor(-1)
		return nil, true
	case 'g':
		// Jump cursor to first message: move by -len.
		m.viewport.MoveCursor(-len(m.viewport.messages))
		return nil, true
	case 'G':
		m.viewport.MoveCursor(len(m.viewport.messages))
		return nil, true
	case 'y':
		raw := m.viewport.SelectedRaw()
		m.viewport.ExitSelect()
		m.statusBar.SetSelectMode(false)
		if raw == "" {
			return nil, true
		}
		m.viewport.AppendStatus("Copied selection to clipboard")
		return tea.SetClipboard(raw), true
	}
	// While selecting, swallow all other keys so they don't scroll the
	// viewport or leak to other panels.
	return nil, true
}

func (m AppModel) delegateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.activePanel {
	case PanelTree:
		var cmd tea.Cmd
		m.tree, cmd = m.tree.Update(msg)
		return m, cmd
	case PanelViewport:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case PanelInput:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func tickAgentsCmd(sup supervisor.Supervisor, sprawlRoot string) tea.Cmd {
	return func() tea.Msg {
		agents, err := sup.Status(context.Background())
		if err != nil {
			return AgentTreeMsg{}
		}
		unread := make(map[string]int)
		for _, a := range agents {
			msgs, _ := messages.List(sprawlRoot, a.Name, "unread")
			unread[a.Name] = len(msgs)
		}
		// Poll the root agent's maildir too so the tree can render an unread
		// badge on the synthesized weave row (QUM-205 / QUM-311). The root
		// name is hardcoded to "weave" here to match the AppModel default;
		// if/when that name becomes configurable, thread it through.
		rootMsgs, _ := messages.List(sprawlRoot, "weave", "unread")
		return AgentTreeMsg{Nodes: buildTreeNodes(agents, unread), RootUnread: len(rootMsgs)}
	}
}

// sendMsgCmd wraps a plain tea.Msg value as a tea.Cmd for use with tea.Batch.
func sendMsgCmd(msg tea.Msg) tea.Cmd {
	return func() tea.Msg { return msg }
}

// restartClock returns the now-source used to compute restart elapsed
// times. Tests override AppModel.restartNow; production defaults to
// time.Now.
func (m *AppModel) restartClock() func() time.Time {
	if m.restartNow != nil {
		return m.restartNow
	}
	return time.Now
}

// restartTickInterval returns the configured tick cadence for restart
// progress updates, defaulting to one second (QUM-260).
func (m *AppModel) restartTickInterval() time.Duration {
	if m.restartTick > 0 {
		return m.restartTick
	}
	return defaultRestartTick
}

// restartTickCmd schedules the next ConsolidationProgressMsg while the
// TUI is waiting on async restart work. The emitted Elapsed duration is
// measured against restartStartedAt using restartClock so tests can
// deliver deterministic values.
func (m *AppModel) restartTickCmd() tea.Cmd {
	interval := m.restartTickInterval()
	startedAt := m.restartStartedAt
	clock := m.restartClock()
	return tea.Tick(interval, func(_ time.Time) tea.Msg {
		return ConsolidationProgressMsg{Elapsed: clock().Sub(startedAt)}
	})
}

// shortSessionID returns the first 8 chars of a Claude session ID (a UUID) for
// display. Shorter IDs (e.g. from test fixtures) are returned unchanged.
func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func scheduleAgentTick(sup supervisor.Supervisor, sprawlRoot string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(2 * time.Second)
		return tickAgentsCmd(sup, sprawlRoot)()
	}
}

// tickActivityCmd fetches the activity tail for the given agent and emits
// ActivityTickMsg. Errors yield an empty tick so the panel clears rather than
// showing stale data. A nil/empty agent produces an empty tick.
func tickActivityCmd(sup supervisor.Supervisor, agent string) tea.Cmd {
	return func() tea.Msg {
		if agent == "" {
			return ActivityTickMsg{Agent: agent}
		}
		entries, err := sup.PeekActivity(context.Background(), agent, 200)
		if err != nil {
			return ActivityTickMsg{Agent: agent}
		}
		return ActivityTickMsg{Agent: agent, Entries: entries}
	}
}

func scheduleActivityTick(sup supervisor.Supervisor, agent string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(2 * time.Second)
		return tickActivityCmd(sup, agent)()
	}
}
