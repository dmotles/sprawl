package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/messages"
	"github.com/dmotles/sprawl/internal/state"
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

// AgentBuffer stores the viewport state for a specific agent. Each agent
// owns its own ViewportModel so streamed assistant chunks, tool calls, and
// status banners can never bleed across agent contexts (QUM-334).
type AgentBuffer struct {
	vp ViewportModel

	// SessionID names the Claude session_id that the cached child transcript
	// was hydrated against. Only populated for non-root agents. When the
	// underlying agent state file's session_id changes (handoff / respawn),
	// the cached buffer is invalidated and re-hydrated from offset zero.
	// QUM-332.
	SessionID string
}

// AppModel is the root Bubble Tea model composing all panels.
type AppModel struct {
	tree      TreeModel
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

	// pendingDrainIDs is populated by the InboxDrainMsg handler and consumed
	// by UserMessageSentMsg: once the drained prompt has been successfully
	// written to the bridge, these IDs are moved from weave's queue pending/
	// to delivered/. Stored on the model so the commit happens strictly AFTER
	// the send succeeds (crash-safety per QUM-323 §5).
	pendingDrainIDs []string

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

	// homeDir is the user's home directory, used to resolve Claude session
	// log paths for child-agent transcript tailing (QUM-332). Set via
	// SetHomeDir after construction. When empty, child transcripts can't be
	// loaded and the viewport falls back to the legacy "Observing X..."
	// banner.
	homeDir string

	// childTranscriptTick is the cadence between transcript re-reads while a
	// non-root agent is observed. Tests shorten it; zero means use
	// defaultChildTranscriptTick.
	childTranscriptTick time.Duration

	// toolInputsExpanded is the global flag (QUM-335) toggled by Ctrl+E.
	// When true, every per-agent viewport renders tool calls with their
	// full ToolInputFull body instead of the truncated summary. Default
	// false; survives agent cycling because new viewports inherit it on
	// creation in viewportFor.
	toolInputsExpanded bool

	// spinner drives the in-flight indicator for pending tool calls
	// (QUM-336). A single global spinner ticks while pendingToolCalls > 0,
	// pushing its current frame to every per-agent viewport. When the
	// counter drops to zero the AppModel stops re-issuing tick commands so
	// no idle CPU is consumed.
	spinner          spinner.Model
	pendingToolCalls int
}

const (
	defaultRestartTick         = time.Second
	defaultChildTranscriptTick = 2 * time.Second
)

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
	agentBuffers := make(map[string]*AgentBuffer)
	// Seed the root agent's buffer eagerly: PreloadTranscript can run before
	// Init/Update fires, so lazy-init via viewportFor would arrive too late
	// (QUM-334 §5). Child agent buffers are still lazy.
	agentBuffers[rootAgent] = &AgentBuffer{vp: NewViewportModel(&theme)}
	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = theme.AccentText
	app := AppModel{
		tree:          NewTreeModel(&theme),
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
		agentBuffers:  agentBuffers,
		activePanel:   startPanel,
		theme:         theme,
		restartFunc:   restartFunc,
		spinner:       sp,
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
		vp := m.observedVP()
		updated, cmd := vp.Update(msg)
		*vp = updated
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

		// Ctrl+E: toggle the global expand-tool-inputs flag (QUM-335).
		// Affects every per-agent viewport so the user can scan the full
		// command / JSON for any tool call without leaving the TUI. Gated
		// implicitly by the modal returns above.
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'e' {
			m.toolInputsExpanded = !m.toolInputsExpanded
			for _, buf := range m.agentBuffers {
				buf.vp.SetToolInputsExpanded(m.toolInputsExpanded)
			}
			return m, nil
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
		m.rootVP().AppendStatus("/handoff dispatched — see output below")
		m.setTurnState(TurnThinking)
		m.input.SetDisabled(true)
		return m, m.bridge.SendMessage(msg.Template)

	case InboxDrainMsg:
		// QUM-323: drain weave's harness queue into Claude's next prompt.
		// Dropped if mid-turn (the entries stay in pending/ and the next
		// AgentTreeMsg backstop will re-peek when idle) or if no bridge.
		if msg.Prompt == "" || m.bridge == nil || m.turnState != TurnIdle {
			return m, nil
		}
		label := "async"
		if msg.Class == "interrupt" {
			label = "interrupt"
		}
		m.rootVP().AppendStatus(fmt.Sprintf("inbox: draining %d %s message(s) into next prompt", len(msg.EntryIDs), label))
		// QUM-323: render the flush prompt in the viewport so the human watching
		// the TUI can see what got drained — parity with SubmitMsg which renders
		// user-typed input. Without this, the drained frame is invisible (only
		// the status line hints at it) and the only way to confirm drain worked
		// is to grep logs — which also breaks the body-in-prompt e2e assertion.
		m.rootVP().AppendUserMessage(msg.Prompt)
		m.pendingDrainIDs = append([]string(nil), msg.EntryIDs...)
		m.setTurnState(TurnThinking)
		m.input.SetDisabled(true)
		return m, m.bridge.SendMessage(msg.Prompt)

	case SubmitMsg:
		if msg.Text == "" || m.bridge == nil || m.turnState != TurnIdle {
			return m, nil
		}
		m.rootVP().AppendUserMessage(msg.Text)
		m.setTurnState(TurnThinking)
		m.input.SetDisabled(true)
		return m, m.bridge.SendMessage(msg.Text)

	case UserMessageSentMsg:
		m.setTurnState(TurnStreaming)
		// QUM-323: if the user turn we just sent was a drained inbox frame,
		// commit the drained entries to delivered/ now that the send is on
		// the wire. Doing this AFTER SendMessage (which is synchronous in the
		// current bridge impl) preserves the crash-safety invariant.
		var commitCmd tea.Cmd
		if len(m.pendingDrainIDs) > 0 {
			commitCmd = commitDrainCmd(m.sprawlRoot, m.rootAgent, m.pendingDrainIDs)
			m.pendingDrainIDs = nil
		}
		if m.bridge != nil {
			return m, tea.Batch(m.bridge.WaitForEvent(), commitCmd)
		}
		return m, commitCmd

	case AssistantTextMsg:
		m.setTurnState(TurnStreaming)
		m.rootVP().AppendAssistantChunk(msg.Text)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case ToolCallMsg:
		m.rootVP().AppendToolCall(msg.ToolName, msg.ToolID, msg.Approved, msg.Input, msg.FullInput)
		// Bump the pending counter and (re)start the spinner ticker if this
		// is the first in-flight call (QUM-336). The spinner self-perpetuates
		// via spinner.Update returning the next tick cmd, but we gate that
		// at the AppModel level — see spinner.TickMsg case below.
		wasZero := m.pendingToolCalls == 0
		m.pendingToolCalls++
		var cmds []tea.Cmd
		if wasZero {
			cmds = append(cmds, m.spinner.Tick)
		}
		if m.bridge != nil {
			cmds = append(cmds, m.bridge.WaitForEvent())
		}
		switch len(cmds) {
		case 0:
			return m, nil
		case 1:
			return m, cmds[0]
		default:
			return m, tea.Batch(cmds...)
		}

	case ToolResultMsg:
		// Route to the root viewport (bridge events are weave-only per
		// QUM-334's gating). MarkToolResult returns false for orphan IDs;
		// only decrement the pending counter on a real match so the
		// spinner keeps animating until every in-flight call resolves.
		if m.rootVP().MarkToolResult(msg.ToolID, msg.Content, msg.IsError) {
			if m.pendingToolCalls > 0 {
				m.pendingToolCalls--
			}
		}
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case spinner.TickMsg:
		// Drop ticks once no tool call is in flight — this is how the
		// ticker stops without leaking work (QUM-336 AC-7). The counter
		// can drift if an entire session ends with calls still pending
		// (e.g. an EOF mid-turn); reconcile against the viewport's actual
		// state so a stale counter cannot keep ticking forever.
		if m.pendingToolCalls == 0 || !m.rootVP().HasPendingToolCall() {
			m.pendingToolCalls = 0
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		frame := m.spinner.View()
		for _, buf := range m.agentBuffers {
			buf.vp.SetSpinnerFrame(frame)
		}
		return m, cmd

	case SessionResultMsg:
		// Display result text only if no assistant text was already streamed.
		// When Claude returns text in the assistant message, it also appears
		// in result.Result — avoid duplicating it.
		root := m.rootVP()
		if !msg.IsError && strings.TrimSpace(msg.Result) != "" && !root.HasPendingAssistant() {
			root.AppendAssistantChunk(strings.TrimSpace(msg.Result))
		}
		root.FinalizeAssistantMessage()
		if msg.IsError {
			root.AppendError(fmt.Sprintf("Error: %s", msg.Result))
		} else {
			root.AppendStatus(fmt.Sprintf("Completed in %dms, cost $%.4f", msg.DurationMs, msg.TotalCostUsd))
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
		m.rootVP().AppendError(msg.Err.Error())
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
		m.rootVP().AppendStatus(fmt.Sprintf("Session restarting (%s)...", reason))
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
		root := m.rootVP()
		root.SetMessages(nil)
		// New session starts with no in-flight tool calls; reset the
		// counter so any stale tick is dropped on next arrival (QUM-336).
		m.pendingToolCalls = 0
		if shortID != "" {
			root.AppendStatus(fmt.Sprintf("— New session started (%s) —", shortID))
		} else {
			root.AppendStatus("— New session started —")
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
			m.rootVP().AppendStatus(fmt.Sprintf("inbox: %d new message(s) for weave", msg.RootUnread-m.rootUnread))
		}
		m.rootUnread = msg.RootUnread
		m.rebuildTree()
		m.statusBar.SetAgentCount(len(msg.Nodes) + 1) // +1 for weave root
		// QUM-334: drop agentBuffers entries for retired agents to bound
		// memory. Always preserve the root and currently-observed agent.
		live := map[string]struct{}{m.rootAgent: {}, m.observedAgent: {}}
		for _, n := range msg.Nodes {
			live[n.Name] = struct{}{}
		}
		for name := range m.agentBuffers {
			if _, ok := live[name]; !ok {
				delete(m.agentBuffers, name)
			}
		}
		// QUM-323: backstop drain. Every 2s the tree polls weave's unread
		// counts; piggyback on that cadence to peek the harness queue and
		// (when idle + non-empty) schedule an InboxDrainMsg. This covers both
		// out-of-process senders (child agent `sprawl report done`) and
		// in-process senders (MCP sprawl_send_async) with a single codepath.
		var drainCmd tea.Cmd
		if m.turnState == TurnIdle && m.sprawlRoot != "" && m.bridge != nil {
			drainCmd = peekAndDrainCmd(m.sprawlRoot, m.rootAgent)
		}
		if m.supervisor != nil {
			return m, tea.Batch(scheduleAgentTick(m.supervisor, m.sprawlRoot), drainCmd)
		}
		return m, drainCmd

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
		m.rootVP().AppendStatus(fmt.Sprintf("inbox: new message from %s", from))
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
		// QUM-334: each agent owns its own ViewportModel inside agentBuffers,
		// so cycling is just a pointer swap — no snapshot/restore.
		m.observedAgent = msg.Name
		// Lazy-init the buffer so View() / select-mode helpers always have
		// something to render against.
		_ = m.viewportFor(msg.Name)

		// Disable input for non-root agents.
		if msg.Name != m.rootAgent {
			m.input.SetDisabled(true)
		} else {
			m.input.SetDisabled(false)
		}

		// Refresh the activity panel for the newly-observed agent.
		m.activity.SetAgent(msg.Name)
		m.activity.SetEntries(nil)

		var cmds []tea.Cmd
		if m.supervisor != nil {
			cmds = append(cmds, tickActivityCmd(m.supervisor, msg.Name))
		}
		// Non-root agents: kick off transcript hydration (QUM-332).
		if msg.Name != m.rootAgent {
			cmds = append(cmds, loadChildTranscriptCmd(m.sprawlRoot, m.homeDir, msg.Name))
		}
		switch len(cmds) {
		case 0:
			return m, nil
		case 1:
			return m, cmds[0]
		default:
			return m, tea.Batch(cmds...)
		}

	case ChildTranscriptMsg:
		// QUM-334: route directly to the named agent's vp. Per-agent
		// ownership means writes to non-observed buffers are correct, not
		// pollution.
		vp := m.viewportFor(msg.Agent)
		switch {
		case msg.Err != nil:
			vp.AppendError(fmt.Sprintf("transcript load failed for %s: %v", msg.Agent, msg.Err))
		case len(msg.Entries) > 0:
			vp.SetMessages(msg.Entries)
			if buf, ok := m.agentBuffers[msg.Agent]; ok {
				buf.SessionID = msg.SessionID
			}
		default:
			// Empty entries: show "Waiting for X..." idempotently. Avoid
			// re-appending on repeated empty ticks by checking whether the
			// banner is already the only entry in the viewport.
			cur := vp.GetMessages()
			banner := fmt.Sprintf("Waiting for %s to start...", msg.Agent)
			alreadyShowing := len(cur) == 1 && cur[0].Type == MessageStatus && cur[0].Content == banner
			if !alreadyShowing {
				vp.SetMessages([]MessageEntry{{
					Type: MessageStatus, Content: banner, Complete: true,
				}})
			}
		}
		// Reschedule for the next tick only while the agent is still being
		// observed; otherwise let the poll go quiet until re-selection.
		if msg.Agent == m.rootAgent || msg.Agent != m.observedAgent {
			return m, nil
		}
		return m, scheduleChildTranscriptTick(m.sprawlRoot, m.homeDir, msg.Agent, m.childTranscriptTickInterval())
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
	vpView := vpBorder.Render(m.observedVP().View())

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
	m.rootVP().SetMessages(entries)
}

func (m *AppModel) rebuildTree() {
	nodes := PrependWeaveRoot(m.childNodes, m.turnState.String(), m.rootUnread)
	m.tree.SetNodes(nodes)
}

// viewportFor returns the per-agent ViewportModel for name, lazy-creating it
// on first access (QUM-334). Newly-created viewports are sized to match the
// current resizePanels target so the first render fits the layout.
func (m *AppModel) viewportFor(name string) *ViewportModel {
	buf, ok := m.agentBuffers[name]
	if !ok {
		vp := NewViewportModel(&m.theme)
		if m.ready && !m.tooSmall {
			layout := ComputeLayout(m.width, m.height)
			vp.SetSize(layout.ViewportWidth-4, layout.ViewportHeight-4)
		}
		vp.SetToolInputsExpanded(m.toolInputsExpanded)
		buf = &AgentBuffer{vp: vp}
		m.agentBuffers[name] = buf
	}
	return &buf.vp
}

// rootVP returns the viewport for the root agent (weave). Bridge events,
// inbox banners, and other weave-only annotations target this viewport
// regardless of which agent the user is currently observing.
func (m *AppModel) rootVP() *ViewportModel { return m.viewportFor(m.rootAgent) }

// observedVP returns the viewport for the currently-observed agent. Used
// by View() and select-mode helpers.
func (m *AppModel) observedVP() *ViewportModel { return m.viewportFor(m.observedAgent) }

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
	for _, buf := range m.agentBuffers {
		buf.vp.SetSize(layout.ViewportWidth-4, layout.ViewportHeight-4)
	}
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
	vp := m.observedVP()
	// Entry: 'v' with no selection active.
	if !vp.IsSelecting() {
		if msg.Code == 'v' && msg.Mod == 0 {
			vp.EnterSelect()
			m.statusBar.SetSelectMode(vp.IsSelecting())
			return nil, true
		}
		return nil, false
	}

	// Selection active — handle mode-specific keys.
	switch msg.Code {
	case tea.KeyEscape, 'v':
		vp.ExitSelect()
		m.statusBar.SetSelectMode(false)
		return nil, true
	case 'j', tea.KeyDown:
		vp.MoveCursor(1)
		return nil, true
	case 'k', tea.KeyUp:
		vp.MoveCursor(-1)
		return nil, true
	case 'g':
		// Jump cursor to first message: move by -len.
		vp.MoveCursor(-vp.Len())
		return nil, true
	case 'G':
		vp.MoveCursor(vp.Len())
		return nil, true
	case 'y':
		raw := vp.SelectedRaw()
		vp.ExitSelect()
		m.statusBar.SetSelectMode(false)
		if raw == "" {
			return nil, true
		}
		vp.AppendStatus("Copied selection to clipboard")
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
		vp := m.observedVP()
		updated, cmd := vp.Update(msg)
		*vp = updated
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

// peekAndDrainCmd reads weave's harness queue and, if non-empty, returns an
// InboxDrainMsg with the rendered prompt and entry IDs. Disk mutation
// (MarkDelivered) happens later, in UserMessageSentMsg, strictly after the
// bridge.SendMessage returns success. Returns nil msg if queue is empty or
// unreadable. QUM-323.
func peekAndDrainCmd(sprawlRoot, rootName string) tea.Cmd {
	return func() tea.Msg {
		pending, err := agentloop.ListPending(sprawlRoot, rootName)
		if err != nil || len(pending) == 0 {
			return nil
		}
		interrupts, asyncs := agentloop.SplitByClass(pending)
		// Interrupts take priority; delivery of asyncs happens on the next
		// tick after the interrupt turn settles.
		if len(interrupts) > 0 {
			ids := make([]string, 0, len(interrupts))
			for _, e := range interrupts {
				ids = append(ids, e.ID)
			}
			return InboxDrainMsg{
				Prompt:   agentloop.BuildInterruptFlushPrompt(interrupts),
				EntryIDs: ids,
				Class:    "interrupt",
			}
		}
		ids := make([]string, 0, len(asyncs))
		for _, e := range asyncs {
			ids = append(ids, e.ID)
		}
		return InboxDrainMsg{
			Prompt:   agentloop.BuildQueueFlushPrompt(asyncs),
			EntryIDs: ids,
			Class:    "async",
		}
	}
}

// commitDrainCmd moves the given entry IDs from pending/ to delivered/ in
// weave's harness queue. Errors are swallowed (logged by agentloop); missing
// IDs are not fatal (a racing drainer may have already committed). Emits no
// message — this is a fire-and-forget cleanup. QUM-323.
func commitDrainCmd(sprawlRoot, rootName string, ids []string) tea.Cmd {
	return func() tea.Msg {
		for _, id := range ids {
			_ = agentloop.MarkDelivered(sprawlRoot, rootName, id)
		}
		return nil
	}
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

// SetHomeDir injects the user's home directory used to resolve Claude session
// log paths for child-agent transcript tailing (QUM-332). Wired by cmd/enter.go
// after model construction.
func (m *AppModel) SetHomeDir(homeDir string) {
	m.homeDir = homeDir
}

// SetChildTranscriptTick overrides the polling cadence for child-agent
// transcript re-reads. Used by tests to avoid sleeping a real interval.
func (m *AppModel) SetChildTranscriptTick(d time.Duration) {
	m.childTranscriptTick = d
}

func (m *AppModel) childTranscriptTickInterval() time.Duration {
	if m.childTranscriptTick > 0 {
		return m.childTranscriptTick
	}
	return defaultChildTranscriptTick
}

// loadChildTranscriptCmd returns a Cmd that reads .sprawl/agents/<name>.json
// for the child's session_id + worktree, resolves the Claude session log path,
// and parses entries up to ReplayMaxMessages. Returns a ChildTranscriptMsg
// regardless of outcome — empty Entries with no error signal "no session yet"
// or "log not on disk yet" so the AppModel can render the "Waiting for X..."
// placeholder. QUM-332.
func loadChildTranscriptCmd(sprawlRoot, homeDir, name string) tea.Cmd {
	return func() tea.Msg {
		return readChildTranscript(sprawlRoot, homeDir, name)
	}
}

func readChildTranscript(sprawlRoot, homeDir, name string) ChildTranscriptMsg {
	if sprawlRoot == "" || homeDir == "" {
		return ChildTranscriptMsg{Agent: name}
	}
	agent, err := state.LoadAgent(sprawlRoot, name)
	if err != nil {
		// Missing state file → treat as "not yet booted" rather than hard
		// error. The polling tick will pick up the file once it exists.
		return ChildTranscriptMsg{Agent: name}
	}
	if agent.SessionID == "" {
		return ChildTranscriptMsg{Agent: name}
	}
	var since time.Time
	if agent.CreatedAt != "" {
		if ts, perr := time.Parse(time.RFC3339, agent.CreatedAt); perr == nil {
			since = ts
		}
	}
	logPath := memory.SessionLogPath(homeDir, agent.Worktree, agent.SessionID)
	entries, err := LoadChildTranscript(logPath, since, ReplayMaxMessages)
	if err != nil {
		return ChildTranscriptMsg{Agent: name, SessionID: agent.SessionID, Err: err}
	}
	return ChildTranscriptMsg{Agent: name, SessionID: agent.SessionID, Entries: entries}
}

// scheduleChildTranscriptTick fires a follow-up ChildTranscriptMsg after the
// configured interval. Mirrors scheduleActivityTick — staleness is handled at
// receive time rather than via cancellation.
func scheduleChildTranscriptTick(sprawlRoot, homeDir, name string, interval time.Duration) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(interval)
		return readChildTranscript(sprawlRoot, homeDir, name)
	}
}
