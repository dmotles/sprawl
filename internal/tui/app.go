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

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/messages"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
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

	// seenToolIDs is populated only on the unified streaming path (QUM-439)
	// from backfill-seeded MessageEntry.ToolID values, so a live ToolCallMsg
	// re-delivered for a seeded tool call can be dropped instead of
	// double-rendered.
	seenToolIDs map[string]struct{}
}

// AppModel is the root Bubble Tea model composing all panels.
type AppModel struct {
	tree      TreeModel
	activity  ActivityPanelModel
	input     InputModel
	statusBar StatusBarModel
	shortHelp ShortHelpModel
	confirm   ConfirmModel

	help     HelpModel
	showHelp bool

	palette     PaletteModel
	showPalette bool

	bridge    SessionBackend
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

	// faults is the per-agent backend-fault sticker map keyed by agent
	// name. Populated by BackendFaultMsg and re-applied on every
	// rebuildTree so the tree row's FAULT badge survives AgentTreeMsg
	// rebuilds. QUM-602.
	faults map[string]backendFault

	activePanel Panel
	showConfirm bool
	theme       Theme
	width       int
	height      int
	ready       bool
	tooSmall    bool

	showError   bool
	errorDialog ErrorDialogModel
	restartFunc func() (SessionBackend, error)

	// showQuestion + questionModel drive the "ask the user a question" modal
	// (QUM-527 slice 2c). showQuestion gates rendering and key routing;
	// questionModel.HasPending() can be true while showQuestion is false (the
	// user dismissed the modal but the request is still queued, so Ctrl-Q
	// re-opens it).
	showQuestion  bool
	questionModel QuestionModel

	// validatePopup renders the live validate-output popup (QUM-588).
	// State transitions are driven by ValidateEventMsg dispatched from
	// cmd/enter.go (which wraps the supervisor's validateEmitter).
	validatePopup ValidatePopupModel

	// quitting is set when the user confirms shutdown (Ctrl-C confirm
	// dialog). It guards against a late RestartSessionMsg triggered from an
	// EOF that arrived just before the user confirmed quit; without the
	// guard that restart would spawn a fresh Claude subprocess the user is
	// about to abandon.
	quitting bool

	// restarting tracks whether an async restartFunc invocation is in
	// flight (QUM-260). RestartCompleteMsg clears it. Used to coalesce
	// duplicate RestartSessionMsg arrivals while a restart is running.
	restarting bool

	// consolidating tracks whether a background consolidation pipeline is
	// still running after a restart (QUM-391). Set on ConsolidationPhaseMsg,
	// cleared on ConsolidationCompleteMsg. Gates whether RestartCompleteMsg
	// should clear the status-bar phase label so the label survives across
	// the restart boundary when consolidation outlives it.
	consolidating bool

	// homeDir is the user's home directory, used to resolve Claude session
	// log paths for child-agent transcript tailing (QUM-332). Set via
	// SetHomeDir after construction. When empty, child transcripts can't be
	// loaded and the viewport falls back to the legacy "Observing X..."
	// banner.
	homeDir string

	// childAdapter, childAdapterAgent, childAdapterEpoch back the unified
	// child-viewport streaming path (QUM-439). When the observed child has a
	// UnifiedRuntime backing handle, AppModel installs an adapter pointed at
	// the child's EventBus so live events route through ChildStreamMsg
	// envelopes. The epoch increments on every install/swap/teardown so
	// stale ChildStreamMsg deliveries (after a viewport switch) are dropped.
	childAdapter      *ChildStreamAdapter
	childAdapterAgent string
	childAdapterEpoch uint64

	// childBackfillPending is true between unified-attach (AgentSelectedMsg
	// path) and the corresponding ChildTranscriptMsg arrival. While set,
	// incoming ChildStreamMsg events for the current epoch are queued in
	// childPendingEvents instead of being applied — otherwise a live event
	// that races ahead of the backfill would be appended and then clobbered
	// by ChildTranscriptMsg's vp.SetMessages call. (QUM-439, fix 2)
	childBackfillPending bool
	childPendingEvents   []ChildStreamMsg

	// activityAdapter, activityAdapterAgent, activityAdapterEpoch back the
	// unified activity-panel streaming path (QUM-440). Unlike the child
	// viewport adapter, this is installed for the root agent too — selecting
	// weave with a UnifiedRuntime registered streams its activity entries.
	activityAdapter      *ActivityStreamAdapter
	activityAdapterAgent string
	activityAdapterEpoch uint64

	// activityEntries stores the per-agent activity tail so the panel can be
	// re-rendered when the user cycles back to an agent and so stream-vs-seed
	// dedupe (QUM-440) operates over a stable canonical slice independent of
	// the panel's render state. Keyed by agent name.
	activityEntries map[string][]agentloop.ActivityEntry

	// activitySeenKeys deduplicates live ActivityStreamMsg entries against
	// already-recorded entries (seed or prior stream). Keyed by agent name,
	// then by a canonical activityEntryKey string.
	activitySeenKeys map[string]map[string]struct{}

	// toolInputsExpanded is the global flag (QUM-335) toggled by Ctrl+O.
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

	// pendingSubmit holds the next user message stashed while weave is mid-turn
	// (QUM-340). Single slot — typing a fresh message and hitting Enter again
	// replaces the previous queued content. Cleared when the turn finalizes
	// (auto-submit), the user presses Esc, or a session restart fires.
	pendingSubmit string

	// version is the build version string (e.g. "v0.2.0"), stored so the
	// session banner can include it on fresh launch and after restarts.
	version string

	// history is the persistent shell-style input history (QUM-410).
	// Up/Down at the input panel walk it; Ctrl+R triggers reverse search.
	history *History

	// searchActive, searchQuery, searchMatchIdx, searchPriorInput drive the
	// reverse-i-search overlay (QUM-410). When searchActive is true, the
	// input panel is gated so keystrokes mutate the query / cycle matches
	// rather than the textarea contents.
	searchActive     bool
	searchQuery      string
	searchMatchIdx   int
	searchPriorInput string

	// activeMCPOps tracks in-flight MCP tool calls keyed by call_id (QUM-497).
	// Mirrored into the status bar via SetActiveOps on every mutation. The map
	// owns a stable insertion order via mcpOpOrder so the first-shown op is
	// always the oldest one — useful when the bar truncates to two visible
	// segments. mcpOpThresholdShown gates the 60s viewport banner so it fires
	// at most once per call_id.
	activeMCPOps        map[string]OpDescriptor
	mcpOpOrder          []string
	mcpOpThresholdShown map[string]bool
	mcpOpTickPending    bool

	// cache memoizes bordered panel renders across View() calls so a paste
	// burst (one View() per pasted rune) doesn't re-render unchanged panels
	// (QUM-451). Held behind a pointer so the value-receiver View() can
	// mutate it; safe because Bubble Tea discards prior AppModel values
	// immediately after Update returns.
	cache *viewCache

	// selectionMode, when true, drops Bubble Tea's mouse-capture sequences
	// (?1002h / ?1006h) on every frame so the host terminal can do native
	// click-drag text selection on the viewport. Toggled by Ctrl-/. Tradeoff
	// while on: scroll wheel events are no longer captured (PgUp/PgDn still
	// scroll via keyboard). See QUM-617.
	selectionMode bool
}

// NewAppModel constructs the root model with all sub-models.
// bridge may be nil for static placeholder mode.
// sup and sprawlRoot are optional; when provided, the tree polls agent status.
// restartFunc is called when the user requests a session restart after a crash.
func NewAppModel(accentColor, repoName, version string, bridge SessionBackend, sup supervisor.Supervisor, sprawlRoot string, restartFunc func() (SessionBackend, error)) AppModel {
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
	// Set the root viewport's initial content to the session banner.
	initialSessionID := ""
	if bridge != nil {
		initialSessionID = shortSessionID(bridge.SessionID())
	}
	agentBuffers[rootAgent].vp.AppendBanner(SessionBanner(initialSessionID, version))

	app := AppModel{
		tree:                NewTreeModel(&theme),
		activity:            NewActivityPanelModel(&theme),
		input:               NewInputModel(&theme),
		statusBar:           NewStatusBarModel(&theme, repoName, version, 0),
		shortHelp:           NewShortHelpModel(&theme),
		help:                NewHelpModel(&theme),
		confirm:             NewConfirmModel(&theme),
		palette:             NewPaletteModel(&theme),
		bridge:              bridge,
		turnState:           TurnIdle,
		supervisor:          sup,
		sprawlRoot:          sprawlRoot,
		observedAgent:       rootAgent,
		rootAgent:           rootAgent,
		agentBuffers:        agentBuffers,
		faults:              make(map[string]backendFault),
		activePanel:         startPanel,
		theme:               theme,
		restartFunc:         restartFunc,
		spinner:             sp,
		version:             version,
		history:             NewHistory(sprawlRoot),
		activityEntries:     make(map[string][]agentloop.ActivityEntry),
		activitySeenKeys:    make(map[string]map[string]struct{}),
		activeMCPOps:        make(map[string]OpDescriptor),
		mcpOpThresholdShown: make(map[string]bool),
		cache:               newViewCache(),
		questionModel:       NewQuestionModel(&theme),
		validatePopup:       NewValidatePopupModel(&theme, 0),
	}
	_ = app.history.Load()
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
		if m.anyModalUp() {
			return m, nil
		}
		vp := m.observedVP()
		updated, cmd := vp.Update(msg)
		*vp = updated
		return m, cmd

	case tea.PasteMsg:
		// Bracketed-paste from the terminal. Forward to the input panel so embedded
		// newlines are inserted literally instead of being treated as Enter-submit.
		// Only when the input bar is the active panel (root-agent view, no modal).
		if m.observedAgent != m.rootAgent || m.activePanel != PanelInput || m.anyModalUp() {
			return m, nil
		}
		// QUM-448: track input-box height across the Update so we can
		// re-propagate sizes to the cached tree/viewport/activity sub-models
		// when a paste grows the textarea. Without this the cached panels
		// keep rendering at their pre-grow height and the composed View
		// overflows the terminal.
		prevInputH := m.inputBoxHeight()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.ready && !m.tooSmall && m.inputBoxHeight() != prevInputH {
			m.resizePanels()
		}
		return m, cmd

	case pasteLookaheadMsg:
		// QUM-455: post-Enter lookahead tick. The input panel scheduled this
		// via tea.Tick after a plain Enter; forward unconditionally so any
		// pending Enter resolves (real submit) or is cleanly dropped (stale
		// seq) regardless of current panel/modal state. Gating here would
		// strand `pendingEnter` if the user changed panels mid-window.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		// QUM-410: while reverse-search is active, the search overlay owns
		// every keystroke until accepted or cancelled. Handled before any
		// other key intercept so Ctrl+C cancels the search instead of
		// opening the quit-confirm dialog.
		if m.searchActive {
			return m.handleSearchKey(msg)
		}

		// QUM-410: Ctrl+R from the input panel enters reverse-search mode.
		// Stash the current input value so Esc can restore it.
		if m.activePanel == PanelInput && msg.Mod&tea.ModCtrl != 0 && msg.Code == 'r' {
			m.searchActive = true
			m.searchQuery = ""
			m.searchPriorInput = m.input.Value()
			if m.history != nil {
				m.searchMatchIdx = m.history.Len()
			} else {
				m.searchMatchIdx = 0
			}
			return m, nil
		}

		// QUM-410: input-panel history navigation. Up/Down walk history
		// only when the textarea cursor is at the first / last line so
		// multi-line editing isn't hijacked. QUM-536: gate on the modal
		// flags too — any visible modal owns arrow keys, and prior to
		// this gate KeyUp was being asymmetrically swallowed by
		// `history.Prev` (which always succeeds when history is non-empty)
		// while KeyDown fell through to the modal because `history.Next`
		// returns ok=false on a fresh model.
		if m.activePanel == PanelInput && m.history != nil && !m.anyModalUp() &&
			(msg.Code == tea.KeyUp || msg.Code == tea.KeyDown) {
			if m.handleHistoryArrow(msg) {
				return m, nil
			}
		}

		// Ctrl+C: REPL convention (QUM-409). With non-empty input, clear the
		// textarea and consume the event. With empty (or whitespace-only)
		// input, fall through to the existing quit-confirm dialog.
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'c' {
			if m.showConfirm {
				return m, nil
			}
			if strings.TrimSpace(m.input.Value()) != "" {
				m.input.SetValue("")
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

		// QUM-527 slice 2c: when the question modal is visible, route ALL
		// keys to it. The model emits QuestionAnsweredMsg / DismissQuestionMsg
		// as cmds to drive AppModel state.
		if m.showQuestion {
			var cmd tea.Cmd
			m.questionModel, cmd = m.questionModel.Update(msg)
			return m, cmd
		}

		// QUM-617: Ctrl+_ (or Ctrl+/) toggles selection mode, which drops
		// mouse-capture sequences so the terminal can do native click-drag text
		// selection on the viewport. Both keys are delivered by the terminal as
		// ASCII US (0x1F), so the Bubble Tea key parser surfaces both as
		// {Code: '_', Mod: ModCtrl}. Ctrl+_ is the reliable form on
		// browser-based terminals (Chrome/Chromium intercepts Ctrl+/ for its
		// own bindings — confirmed in the coder web terminal); Ctrl+/ works on
		// native terminals. Match either form for clarity across layouts.
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == '_' {
			m.selectionMode = !m.selectionMode
			m.statusBar.SetSelectionMode(m.selectionMode)
			return m, nil
		}

		// QUM-527 slice 2c: Ctrl-Q reopens the question modal when a request
		// is pending and no higher-priority modal is up. No-op otherwise.
		if msg.Mod&tea.ModCtrl != 0 && (msg.Code == 'q' || msg.Code == 'Q') {
			if m.questionModel.HasPending() && !anyOtherModalUp(&m) {
				m.showQuestion = true
				m.questionModel = m.questionModel.Show()
			}
			return m, nil
		}

		// Ctrl+V: toggle the validate-output popup between visible and
		// minimized states (QUM-588). No-op when no validate is running or
		// when the popup is in queued/failed sticky state.
		if msg.Mod&tea.ModCtrl != 0 && (msg.Code == 'v' || msg.Code == 'V') {
			if m.validatePopup.ToggleMinimize() {
				return m, nil
			}
			// Fall through if not consumed.
		}

		// Ctrl+O: toggle the global expand-tool-inputs flag (QUM-335).
		// Affects every per-agent viewport so the user can scan the full
		// command / JSON for any tool call without leaving the TUI. Gated
		// implicitly by the modal returns above. (Rebound from Ctrl+E to
		// match Claude Code's expand convention.)
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'o' {
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

		// QUM-340: Esc with a queued submit revokes it. Only fire when there
		// is something queued so the key is otherwise free for whatever the
		// active panel does with it. The textinput buffer is left intact so
		// the user keeps composing without losing partial typing.
		if msg.Code == tea.KeyEscape && m.pendingSubmit != "" {
			// QUM-576: option-a "refuse to clobber" rule. If the textarea is
			// empty, reload the queued draft into the input so the user can
			// edit it. If the user is mid-compose (textarea non-empty), we
			// only clear the queue and leave the buffer alone — never
			// overwrite in-flight typing.
			queued := m.pendingSubmit
			m.pendingSubmit = ""
			m.input.SetPendingPreview("")
			if strings.TrimSpace(m.input.Value()) == "" {
				m.input.SetValue(queued)
				m.input.CursorEnd()
			}
			return m, nil
		}

		// QUM-380: Esc during an active turn sends an interrupt request to
		// Claude. Checked after help/select/pendingSubmit so those actions
		// take priority. The protocol confirms the interrupt asynchronously
		// via SessionResultMsg/SessionErrorMsg; we show "Interrupting..."
		// feedback immediately.
		if msg.Code == tea.KeyEscape && (m.turnState == TurnStreaming || m.turnState == TurnThinking) && m.bridge != nil {
			m.rootVP().AppendStatus("Interrupting...")
			return m, m.bridge.Interrupt()
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
			// QUM-399: continuous bridges (UnifiedRuntime/TUIAdapter) emit
			// autonomous events outside of a user turn. Kick off WaitForEvent
			// here so the event loop starts pulling immediately after init.
			if m.bridge.IsContinuous() {
				return m, m.bridge.WaitForEvent()
			}
		}
		return m, nil

	case SessionModelMsg:
		// QUM-385: derive context window limit from the model name.
		m.statusBar.SetContextLimit(modelContextLimit(msg.Model))
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case OpenPaletteMsg:
		// Gate on modals AND observed-agent-is-root: when observing a child
		// the input bar is hidden (QUM-340), so opening the palette would
		// dispatch commands the user can't see typed into.
		if m.input.disabled || m.anyModalUp() || m.observedAgent != m.rootAgent {
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
		// QUM-338: render as a MessageSystem entry (mail glyph + distinct
		// style) so the human can tell the system spoke, not them. The Claude
		// session still receives the body as a user-role turn via SendMessage.
		// QUM-557: AppendSystemNotification strips `<system-notification>` wrappers
		// and emits MessageSystemNotification entries; falls back to AppendSystemMessage
		// (legacy MessageSystem rendering) when the prompt isn't tagged — preserving
		// behavior for pre-QUM-555 plain inbox banners.
		m.rootVP().AppendSystemNotification(msg.Prompt)
		m.pendingDrainIDs = append([]string(nil), msg.EntryIDs...)
		m.setTurnState(TurnThinking)
		return m, m.bridge.SendMessage(msg.Prompt)

	case SubmitMsg:
		if msg.Text == "" {
			return m, nil
		}
		// QUM-410: record the user input in shell-style history before
		// dispatch. Done unconditionally (even when bridge is nil in tests)
		// because history is a UX concern independent of transport.
		if m.history != nil {
			m.history.Append(msg.Text)
			m.history.Reset()
		}
		if m.bridge == nil {
			return m, nil
		}
		// QUM-340: while a turn is in flight, stash the message in the
		// single-slot pending queue rather than dropping it. Replaces any
		// previously-queued content (typing replaces, single-slot semantics).
		// The auto-submit fires from the SessionResultMsg finalize path.
		if m.turnState != TurnIdle {
			m.pendingSubmit = msg.Text
			m.input.SetPendingPreview(msg.Text)
			return m, nil
		}
		m.rootVP().AppendUserMessage(msg.Text)
		m.setTurnState(TurnThinking)
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

	case AssistantContentMsg:
		// QUM-386: batch of content blocks from a single assistant message.
		var cmds []tea.Cmd
		for _, inner := range msg.Msgs {
			switch im := inner.(type) {
			case AssistantTextMsg:
				m.setTurnState(TurnStreaming)
				m.rootVP().AppendAssistantChunk(im.Text)
			case SessionUsageMsg:
				// QUM-385: true context window usage = input + cache_read +
				// cache_creation. input_tokens alone is the non-cached subset
				// and understates the prefix by a large factor when prompt
				// caching is on.
				m.statusBar.SetTokenUsage(im.InputTokens + im.CacheReadInputTokens + im.CacheCreationInputTokens)
			case ToolCallMsg:
				m.rootVP().AppendToolCallWithHeader(im.ToolName, im.ToolID, im.Approved, im.Input, im.FullInput, im.HeaderArg, im.HeaderParams, im.ParentToolUseID)
				wasZero := m.pendingToolCalls == 0
				m.pendingToolCalls++
				if wasZero {
					cmds = append(cmds, m.spinner.Tick)
				}
			}
		}
		if m.bridge != nil {
			cmds = append(cmds, m.bridge.WaitForEvent())
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case AssistantTextMsg:
		m.setTurnState(TurnStreaming)
		m.rootVP().AppendAssistantChunk(msg.Text)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case ToolCallMsg:
		m.rootVP().AppendToolCallWithHeader(msg.ToolName, msg.ToolID, msg.Approved, msg.Input, msg.FullInput, msg.HeaderArg, msg.HeaderParams, msg.ParentToolUseID)
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
		if m.pendingToolCalls == 0 || !m.anyViewportHasPending() {
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
		// Finalize the assistant chunk before appending status/error so the
		// last-entry probe in FinalizeAssistantMessage still sees an
		// assistant entry.
		finalizeCmd := m.finalizeTurn()
		if msg.IsError {
			root.AppendError(fmt.Sprintf("Error: %s", msg.Result))
		} else {
			root.AppendStatus(fmt.Sprintf("Completed in %dms, cost $%.4f", msg.DurationMs, msg.TotalCostUsd))
			m.statusBar.SetTurnCost(msg.TotalCostUsd)
		}
		var costCmd tea.Cmd
		if msg.TotalCostUsd > 0 && m.sprawlRoot != "" && m.rootAgent != "" {
			costCmd = persistCostCmd(m.sprawlRoot, m.rootAgent, msg.TotalCostUsd)
		}
		if costCmd != nil && finalizeCmd != nil {
			return m, tea.Batch(costCmd, finalizeCmd)
		}
		if finalizeCmd != nil {
			return m, finalizeCmd
		}
		return m, costCmd

	case InterruptCompletedMsg:
		// QUM-475: terminal interrupted-turn event from the unified runtime.
		// Mirror SessionResultMsg cleanup so the TUI returns to TurnIdle and
		// the queue-drain gate re-opens.
		root := m.rootVP()
		if strings.TrimSpace(msg.Result) != "" && !root.HasPendingAssistant() {
			root.AppendAssistantChunk(strings.TrimSpace(msg.Result))
		}
		// Finalize before status append (see SessionResultMsg comment).
		finalizeCmd := m.finalizeTurn()
		root.AppendStatus(fmt.Sprintf("Interrupted (%dms)", msg.DurationMs))
		var costCmd tea.Cmd
		if msg.TotalCostUsd > 0 {
			m.statusBar.SetTurnCost(msg.TotalCostUsd)
			if m.sprawlRoot != "" && m.rootAgent != "" {
				costCmd = persistCostCmd(m.sprawlRoot, m.rootAgent, msg.TotalCostUsd)
			}
		}
		if costCmd != nil && finalizeCmd != nil {
			return m, tea.Batch(costCmd, finalizeCmd)
		}
		if finalizeCmd != nil {
			return m, finalizeCmd
		}
		return m, costCmd

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
			return m, m.finalizeTurn()
		}
		m.rootVP().AppendError(msg.Err.Error())
		return m, m.finalizeTurn()

	case HandoffRequestedMsg:
		// Weave invoked the handoff MCP tool. Reuse the EOF restart
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
		// QUM-340: a session restart wipes the conversational context the user
		// queued their next message against. Drop the slot and surface a
		// one-line banner so the disappearance isn't silent.
		if m.pendingSubmit != "" {
			m.pendingSubmit = ""
			m.input.SetPendingPreview("")
			m.rootVP().AppendStatus("queued message dropped due to session restart")
		}
		m.setTurnState(TurnIdle)
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
		// the prior background consolidation). RestartCompleteMsg delivers
		// the outcome.
		m.restarting = true
		fn := m.restartFunc
		return m, func() tea.Msg {
			b, err := fn()
			return RestartCompleteMsg{Bridge: b, Err: err}
		}

	case ConsolidationPhaseMsg:
		m.consolidating = true
		m.statusBar.SetRestartLabel(msg.Phase)
		m.rootVP().AppendStatus(msg.Phase)
		return m, nil

	case ConsolidationCompleteMsg:
		m.consolidating = false
		m.statusBar.SetRestartLabel("")
		if msg.Err != nil {
			m.rootVP().AppendStatus(fmt.Sprintf("Consolidation failed: %v", msg.Err))
		} else {
			m.rootVP().AppendStatus(fmt.Sprintf("Consolidation complete (%ds)", int(msg.Duration.Seconds())))
		}
		return m, nil

	case RestartCompleteMsg:
		m.restarting = false
		// QUM-527 slice 2c: re-poll the question queue across the restart
		// boundary so a question that became pending while the bridge was
		// down (or stayed pending across the restart) is surfaced again. Done
		// at the top so the install survives the bridge-nil error fallthrough
		// below; the status bar always reflects current depth.
		if m.supervisor != nil {
			depth, head := m.supervisor.PeekQuestions()
			m.statusBar.SetPendingQuestions(depth, agentFromHead(head))
			if !m.questionModel.HasPending() && head != nil {
				m.questionModel = m.questionModel.Install(head)
				if !anyOtherModalUp(&m) {
					m.questionModel = m.questionModel.Show()
					m.showQuestion = true
				}
			}
			m.statusBar.SetQuestionModalHidden(!m.showQuestion && m.questionModel.HasPending())
		}
		// QUM-391: only clear the status bar label if consolidation is not
		// still running in the background.
		if !m.consolidating {
			m.statusBar.SetRestartLabel("")
		}
		// A Ctrl-C confirm landing mid-restart also shuts us down here.
		if m.quitting {
			return m, tea.Quit
		}
		if msg.Err != nil {
			m.errorDialog = NewErrorDialog(&m.theme, msg.Err)
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			return m, nil
		}
		if msg.Bridge == nil {
			// No bridge and no error — shouldn't happen, but degrade
			// gracefully by showing a generic failure.
			m.errorDialog = NewErrorDialog(&m.theme, fmt.Errorf("restart produced nil bridge"))
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			return m, nil
		}
		m.bridge = msg.Bridge
		shortID := shortSessionID(m.bridge.SessionID())
		root := m.rootVP()
		// QUM-391: preserve status messages (consolidation banners) across
		// restart — only clear conversation messages.
		var preserved []MessageEntry
		for _, e := range root.GetMessages() {
			if e.Type == MessageStatus {
				preserved = append(preserved, e)
			}
		}
		root.SetMessages(preserved)
		// New session starts with no in-flight tool calls; reset the
		// counter so any stale tick is dropped on next arrival (QUM-336).
		m.pendingToolCalls = 0
		// QUM-497: drop any in-flight MCP op state from the prior session
		// so a stale call_id can't keep ticking on the new bar.
		m.activeMCPOps = make(map[string]OpDescriptor)
		m.mcpOpOrder = nil
		m.mcpOpThresholdShown = make(map[string]bool)
		m.statusBar.SetActiveOps(nil)
		// QUM-385: reset token usage; contextLimit is preserved across
		// restarts since the model usually doesn't change.
		m.statusBar.SetTokenUsage(0)
		// Show the session banner with the new session ID (QUM-390).
		root.AppendBanner(SessionBanner(shortID, m.version))
		m.statusBar.SetSessionID(shortID)
		m.setTurnState(TurnIdle)
		var cmds []tea.Cmd
		cmds = append(cmds, m.bridge.Initialize())
		// QUM-399: continuous bridges need their event pump primed after a
		// restart in addition to Initialize. SessionInitializedMsg also
		// triggers WaitForEvent, but priming here is cheap and keeps the
		// pump from idling if Init is fast enough that no events are queued
		// when the SessionInitializedMsg arrives.
		if m.bridge.IsContinuous() {
			cmds = append(cmds, m.bridge.WaitForEvent())
		}
		// QUM-479: when the activity adapter was streaming the root agent
		// before the restart, the OLD UnifiedRuntime is gone — re-point the
		// adapter at the freshly-registered runtime (if any) so the activity
		// panel keeps streaming without the user manually re-selecting weave.
		if m.activityAdapter != nil && m.activityAdapterAgent == m.rootAgent {
			if urt := m.lookupUnifiedRuntime(m.rootAgent); urt != nil {
				m.activityAdapter.Observe(urt)
				m.activityAdapterEpoch++
				cmds = append(cmds, activityStreamWaitCmd(m.activityAdapter, m.rootAgent, m.activityAdapterEpoch))
			}
		}
		return m, tea.Batch(cmds...)

	case InterruptResultMsg:
		// QUM-380: the interrupt request was dispatched; show the outcome.
		// Request-ack only — does not transition turnState; terminal cleanup
		// happens in InterruptCompletedMsg (QUM-475).
		if msg.Err != nil {
			m.rootVP().AppendStatus(fmt.Sprintf("Interrupt failed: %v", msg.Err))
		} else {
			m.rootVP().AppendStatus("Interrupt sent — waiting for turn to end")
		}
		return m, nil

	case TurnStateMsg:
		m.setTurnState(msg.State)
		return m, nil

	case ActivityTickMsg:
		// QUM-440: when the unified streaming adapter owns this agent, the
		// tick is the one-shot seed — apply, populate dedupe keys, do NOT
		// reschedule (live deltas flow through ActivityStreamMsg instead).
		if m.activityAdapter != nil && m.activityAdapterAgent == msg.Agent {
			m.activityEntries[msg.Agent] = append([]agentloop.ActivityEntry(nil), msg.Entries...)
			seen := make(map[string]struct{}, len(msg.Entries))
			for _, e := range msg.Entries {
				seen[activityEntryKey(e)] = struct{}{}
			}
			m.activitySeenKeys[msg.Agent] = seen
			if msg.Agent == m.observedAgent {
				m.activity.SetEntries(m.activityEntries[msg.Agent])
			}
			return m, nil
		}
		// Legacy poll path. Only apply the tick if it is for the
		// currently-observed agent. A selection change that happened mid-flight
		// can race us; dropping a stale tick is cheaper and simpler than
		// cancelling the in-flight cmd.
		if msg.Agent == m.observedAgent {
			m.activity.SetEntries(msg.Entries)
		}
		if m.supervisor != nil {
			return m, scheduleActivityTick(m.supervisor, m.observedAgent)
		}
		return m, nil

	case ActivityStreamMsg:
		// QUM-440: live activity entries from the per-agent unified adapter.
		// Drop stale deliveries from a previous adapter generation.
		if msg.Epoch != m.activityAdapterEpoch || msg.Agent != m.activityAdapterAgent {
			return m, nil
		}
		seen, ok := m.activitySeenKeys[msg.Agent]
		if !ok {
			seen = make(map[string]struct{})
			m.activitySeenKeys[msg.Agent] = seen
		}
		tail := m.activityEntries[msg.Agent]
		for _, e := range msg.Entries {
			k := activityEntryKey(e)
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			tail = append(tail, e)
		}
		// QUM-440: cap per-agent live tail to mirror agentloop.ActivityRing
		// (DefaultActivityCapacity = 200). Without this, long sessions grow
		// unbounded.
		if len(tail) > agentloop.DefaultActivityCapacity {
			tail = tail[len(tail)-agentloop.DefaultActivityCapacity:]
		}
		m.activityEntries[msg.Agent] = tail
		if msg.Agent == m.observedAgent {
			m.activity.SetEntries(tail)
		}
		return m, activityStreamWaitCmd(m.activityAdapter, msg.Agent, msg.Epoch)

	case ActivityStreamClosedMsg:
		// QUM-479: the activity-panel adapter's EventBus subscription closed
		// (Cancel or runtime stop). Tear down silently — do NOT trigger a
		// bridge restart. Stale-epoch deliveries from a prior generation are
		// ignored.
		if msg.Agent == m.activityAdapterAgent && msg.Epoch == m.activityAdapterEpoch {
			if m.activityAdapter != nil {
				m.activityAdapter.Cancel()
				m.activityAdapter = nil
			}
			m.activityAdapterAgent = ""
			m.activityAdapterEpoch++
		}
		return m, nil

	case ChildStreamClosedMsg:
		// QUM-479: the child viewport adapter's EventBus subscription closed
		// (Cancel or runtime stop). Tear down silently — do NOT trigger a
		// bridge restart. Stale-epoch deliveries from a prior generation are
		// ignored.
		if msg.Agent == m.childAdapterAgent && msg.Epoch == m.childAdapterEpoch {
			if m.childAdapter != nil {
				m.childAdapter.Cancel()
				m.childAdapter = nil
			}
			m.childAdapterAgent = ""
			m.childAdapterEpoch++
			m.childBackfillPending = false
			m.childPendingEvents = nil
		}
		return m, nil

	case AgentTreeMsg:
		m.childNodes = msg.Nodes
		// QUM-311: detect out-of-process inbox arrivals (e.g. an external
		// child process writing a maildir envelope directly, or any future
		// out-of-process sender) by comparing the disk-polled unread count
		// to the locally-tracked value. Any increase yields a banner so the user
		// gets the same UX whether the sender was in-process (InboxArrivalMsg
		// via the TUI notifier) or out-of-process (caught on the 2s tick).
		if msg.RootUnread > m.rootUnread {
			m.rootVP().AppendStatus(formatInboxBanner(msg.RootUnread-m.rootUnread, ""))
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
		// out-of-process senders (external processes writing maildir directly)
		// and in-process senders (MCP send_message) with a single codepath.
		//
		// QUM-399: this path is shared with the unified-runtime bridge. The
		// resulting InboxDrainMsg → bridge.SendMessage either streams the
		// drained prompt directly to claude (legacy bridge) or enqueues a
		// ClassUser item via the TUIAdapter (unified bridge). Keeping a
		// single drain pipeline preserves the user-facing AppendSystemMessage
		// rendering and the commitDrainCmd MarkDelivered timing for both
		// modes.
		var drainCmd tea.Cmd
		if m.turnState == TurnIdle && m.sprawlRoot != "" && m.bridge != nil {
			drainCmd = peekAndDrainCmd(m.sprawlRoot, m.rootAgent, m.supervisor)
		}
		if m.supervisor != nil {
			return m, tea.Batch(scheduleAgentTick(m.supervisor, m.sprawlRoot), drainCmd)
		}
		return m, drainCmd

	case InboxArrivalMsg:
		// QUM-465: under unified runtime the in-process notifier and the
		// 2s tickAgentsCmd rise-detector can both observe the same maildir
		// entry; race-ordering determined which fired the banner first
		// (sometimes both). Reconcile against disk-truth here so this case
		// and the AgentTreeMsg case converge: fire iff disk says we have
		// more unread than we've already accounted for.
		from := msg.From
		if from == "" {
			from = "unknown"
		}
		diskUnread := m.rootUnread
		if m.sprawlRoot != "" && m.rootAgent != "" {
			if entries, err := messages.List(m.sprawlRoot, m.rootAgent, "unread"); err == nil {
				diskUnread = len(entries)
			}
		}
		if diskUnread > m.rootUnread {
			m.rootVP().AppendStatus(formatInboxBanner(diskUnread-m.rootUnread, from))
			m.rootUnread = diskUnread
			m.rebuildTree()
		}
		return m, nil

	case BackendFaultMsg:
		// QUM-602: stamp the per-agent fault sticker (re-applied on every
		// rebuildTree) and surface an operator-facing banner in the root
		// viewport.
		if m.faults == nil {
			m.faults = make(map[string]backendFault)
		}
		m.faults[msg.Agent] = backendFault{
			Class:      msg.Class,
			Reason:     msg.Reason,
			NextAction: msg.NextAction,
		}
		// QUM-602: the banner is appended unconditionally on every
		// BackendFaultMsg — it is keyed off the fault transition/message
		// arrival, NOT a latched boolean. This intentionally preserves the
		// re-fire when a repeated fault occurs on the same agent.
		m.rootVP().AppendStatus(fmt.Sprintf("backend fault on %s: %s — %s", msg.Agent, msg.Class, msg.NextAction))
		m.rebuildTree()
		return m, nil

	case BackendFaultClearedMsg:
		// QUM-601: in-place recovery succeeded. Drop the per-agent fault
		// sticker, surface a recovery banner in the root viewport, and
		// rebuild the tree so the FAULT badge disappears from the row.
		// Viewport history is intentionally retained — operators keep the
		// fault/recovery sequence visible for forensics.
		if m.faults != nil {
			delete(m.faults, msg.Agent)
		}
		m.rootVP().AppendStatus(fmt.Sprintf("backend recovered on %s", msg.Agent))
		m.rebuildTree()
		// If the recovered agent is the one currently streaming into the
		// child viewport, re-point the child adapter at the new unified
		// runtime. When no new runtime is reachable (e.g. recovery is still
		// in flight), tear down the adapter and let the next AgentSelectedMsg
		// re-attach.
		if m.childAdapterAgent == msg.Agent && m.childAdapter != nil {
			if urt := m.lookupUnifiedRuntime(msg.Agent); urt != nil {
				m.childAdapter.Observe(urt)
				m.childAdapterEpoch++
			} else {
				m.childAdapter.Cancel()
				m.childAdapter = nil
				m.childAdapterAgent = ""
				m.childAdapterEpoch++
			}
		}
		return m, nil

	case AgentsResumedMsg:
		// QUM-372: render a startup banner summarizing how many suspended
		// child agents the runEnter scan resumed (and how many failed). The
		// cmd/enter.go side already gates dispatch on resumed+failed > 0,
		// but guard here too so a stray zero-count msg is a silent no-op.
		if msg.Resumed == 0 && msg.Failed == 0 {
			return m, nil
		}
		if msg.Failed == 0 {
			m.rootVP().AppendStatus(fmt.Sprintf("[startup] resumed %d agents", msg.Resumed))
		} else {
			m.rootVP().AppendStatus(fmt.Sprintf("[startup] resumed %d agents (%d failed)", msg.Resumed, msg.Failed))
		}
		return m, nil

	case MCPCallStartedMsg:
		// QUM-497: MCP server is reporting a tool call has begun. Insert into
		// the active-ops map (keyed by call_id), arm the 1Hz tick if this is
		// the zero→one edge, and schedule a 60s threshold tick that raises a
		// viewport banner with SIGUSR1 guidance if the call is still running.
		if msg.CallID == "" {
			return m, nil
		}
		if _, exists := m.activeMCPOps[msg.CallID]; !exists {
			m.mcpOpOrder = append(m.mcpOpOrder, msg.CallID)
		}
		started := msg.Started
		if started.IsZero() {
			started = time.Now()
		}
		m.activeMCPOps[msg.CallID] = OpDescriptor{
			CallID:  msg.CallID,
			Tool:    msg.Tool,
			Caller:  msg.Caller,
			Step:    msg.Step,
			Started: started,
		}
		m.statusBar.SetActiveOps(m.orderedMCPOps())
		var cmds []tea.Cmd
		if !m.mcpOpTickPending {
			m.mcpOpTickPending = true
			cmds = append(cmds, mcpOpTickCmd())
		}
		cmds = append(cmds, mcpOpThresholdCmd(msg.CallID, mcpOpBannerThreshold))
		return m, tea.Batch(cmds...)

	case MCPCallProgressMsg:
		// QUM-497: update the per-op step (and elapsed time on next tick).
		// Tail is intentionally not rendered into the status bar to keep the
		// segment narrow; the call log already records the line tail.
		if msg.CallID == "" {
			return m, nil
		}
		if op, ok := m.activeMCPOps[msg.CallID]; ok {
			if msg.Step != "" {
				op.Step = msg.Step
			}
			m.activeMCPOps[msg.CallID] = op
			m.statusBar.SetActiveOps(m.orderedMCPOps())
		}
		return m, nil

	case ValidateEventMsg:
		// QUM-588: route validate checkpoints to the popup state machine.
		// Batched cmds include the auto-open timer (one-shot tea.Tick) and
		// the 1Hz elapsed-clock tick.
		cmds := m.validatePopup.Handle(msg)
		m.statusBar.SetValidatePill(m.validatePopup.Pill())
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case validatePopupTimerMsg:
		m.validatePopup.HandleTimer(msg)
		m.statusBar.SetValidatePill(m.validatePopup.Pill())
		return m, nil

	case validatePopupTickMsg:
		next := m.validatePopup.HandleTick(msg)
		m.statusBar.SetValidatePill(m.validatePopup.Pill())
		if next == nil {
			return m, nil
		}
		return m, next

	case MCPCallEndedMsg:
		// QUM-497: tool call finished. Drop the op from the live set; the
		// status bar segment vanishes once the next render fires.
		if msg.CallID == "" {
			return m, nil
		}
		if _, ok := m.activeMCPOps[msg.CallID]; ok {
			delete(m.activeMCPOps, msg.CallID)
			delete(m.mcpOpThresholdShown, msg.CallID)
			m.mcpOpOrder = removeStr(m.mcpOpOrder, msg.CallID)
			m.statusBar.SetActiveOps(m.orderedMCPOps())
		}
		return m, nil

	case mcpOpTickMsg:
		// QUM-497: 1Hz re-render driver. Self-perpetuates only while ops are
		// active; falls silent once the map drains so idle frames cost nothing.
		if len(m.activeMCPOps) == 0 {
			m.mcpOpTickPending = false
			return m, nil
		}
		// SetActiveOps re-installs the slice; the View() call this cmd
		// triggers will reformat elapsed time against the current clock.
		m.statusBar.SetActiveOps(m.orderedMCPOps())
		return m, mcpOpTickCmd()

	case mcpOpThresholdMsg:
		// QUM-497: 60s threshold elapsed. If the op is still active and we
		// haven't already raised a banner for it, append the SIGUSR1 hint to
		// the root viewport. Idempotent across duplicate threshold deliveries
		// (defensive: only one is scheduled per Started).
		op, ok := m.activeMCPOps[msg.CallID]
		if !ok {
			return m, nil
		}
		if m.mcpOpThresholdShown[msg.CallID] {
			return m, nil
		}
		m.mcpOpThresholdShown[msg.CallID] = true
		// QUM-558: some tools are blocking-on-human by design (e.g.
		// ask_user_question waits for the user to respond). Suppress the
		// viewport banner for them — the in-flight tracker still records
		// the op so SIGUSR1 dumps remain accurate. If this map grows past
		// ~3 entries, promote to tool-side metadata (Option 3 in QUM-558).
		if mcpLongRunningBannerExempt[op.Tool] {
			return m, nil
		}
		caller := op.Caller
		if caller == "" {
			caller = "?"
		}
		elapsed := formatElapsed(time.Since(op.Started))
		m.rootVP().AppendStatus(fmt.Sprintf(
			"⏳ %s(%s) is taking longer than usual (T+%s). Send SIGUSR1 to capture state.",
			op.Tool, caller, elapsed,
		))
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
		// QUM-341: keep the tree panel's `>` cursor in sync with the observed
		// agent so Ctrl+N / Ctrl+P cycling moves the cursor too.
		m.tree.SetSelected(msg.Name)
		// Lazy-init the buffer so View() / select-mode helpers always have
		// something to render against.
		_ = m.viewportFor(msg.Name)

		// QUM-340: hide the input bar entirely while observing a non-root
		// agent. The viewport reclaims the bar's vertical space; resizePanels
		// recomputes per-agent viewport sizes against the new layout. The
		// pendingSubmit slot is preserved across cycles intentionally — when
		// the user cycles back to weave the indicator reappears alongside the
		// restored input bar.
		if m.ready && !m.tooSmall {
			m.resizePanels()
		}

		// Refresh the activity panel for the newly-observed agent.
		m.activity.SetAgent(msg.Name)
		m.activity.SetEntries(nil)

		var cmds []tea.Cmd

		// QUM-440: try the unified streaming path for the activity panel —
		// applies to the root agent too. When the agent has a UnifiedRuntime
		// backing handle, install (or re-point) the activity-stream adapter
		// and reset per-agent dedupe state. Seed synchronously via
		// PeekActivity (one-shot — no rescheduled tick); live deltas flow via
		// ActivityStreamMsg.
		activityAttached := false
		if urt := m.lookupUnifiedRuntime(msg.Name); urt != nil {
			if m.activityAdapter == nil {
				m.activityAdapter = NewActivityStreamAdapter(urt)
			} else {
				m.activityAdapter.Observe(urt)
			}
			m.activityAdapterAgent = msg.Name
			m.activityAdapterEpoch++
			// Reset per-agent dedupe + tail so the seed re-population is
			// clean.
			delete(m.activitySeenKeys, msg.Name)
			delete(m.activityEntries, msg.Name)
			// Synchronous one-shot seed via PeekActivity. Errors yield an
			// empty seed; the seen-keys map is still populated to keep the
			// invariant clean.
			if m.supervisor != nil {
				if entries, err := m.supervisor.PeekActivity(context.Background(), msg.Name, 200); err == nil {
					m.activityEntries[msg.Name] = append([]agentloop.ActivityEntry(nil), entries...)
					seen := make(map[string]struct{}, len(entries))
					for _, e := range entries {
						seen[activityEntryKey(e)] = struct{}{}
					}
					m.activitySeenKeys[msg.Name] = seen
					if msg.Name == m.observedAgent {
						m.activity.SetEntries(m.activityEntries[msg.Name])
					}
				}
			}
			cmds = append(cmds, activityStreamWaitCmd(m.activityAdapter, msg.Name, m.activityAdapterEpoch))
			activityAttached = true
		} else {
			if m.activityAdapter != nil {
				m.activityAdapter.Cancel()
				m.activityAdapter = nil
			}
			m.activityAdapterAgent = ""
			m.activityAdapterEpoch++
		}

		// Legacy poll path: kick off the periodic activity tick. Skipped on
		// the unified path because the seed is synchronous and live deltas
		// flow via ActivityStreamMsg.
		if !activityAttached && m.supervisor != nil {
			cmds = append(cmds, tickActivityCmd(m.supervisor, msg.Name))
		}

		// QUM-439: try the unified streaming path for non-root children
		// before falling back to JSONL polling. Resolve the child's
		// AgentRuntime via the supervisor's RuntimeRegistry; if its handle
		// exposes a UnifiedRuntime, install (or re-point) the per-child
		// stream adapter and skip the polling tick. Otherwise — nil
		// supervisor, nil registry, registry miss, or a legacy handle —
		// keep the existing tick-based behaviour.
		unifiedAttached := false
		if msg.Name != m.rootAgent {
			urt := m.lookupUnifiedRuntime(msg.Name)
			if urt != nil {
				if m.childAdapter == nil {
					m.childAdapter = NewChildStreamAdapter(urt)
				} else {
					m.childAdapter.Observe(urt)
				}
				m.childAdapterAgent = msg.Name
				m.childAdapterEpoch++
				// QUM-439 fix 2: gate live-event application on backfill
				// arrival. Any ChildStreamMsg that races ahead of the
				// ChildTranscriptMsg is queued and drained after the seed
				// SetMessages, preventing a clobber-then-lose race.
				m.childBackfillPending = true
				m.childPendingEvents = nil
				cmds = append(cmds,
					loadChildTranscriptCmd(m.sprawlRoot, m.homeDir, msg.Name),
					childStreamWaitCmd(m.childAdapter, msg.Name, m.childAdapterEpoch),
				)
				unifiedAttached = true
			}
		}

		// On a switch back to root or any non-unified target, tear down
		// any active child adapter and clear bookkeeping.
		if !unifiedAttached {
			if m.childAdapter != nil {
				m.childAdapter.Cancel()
				m.childAdapter = nil
			}
			m.childAdapterAgent = ""
			m.childAdapterEpoch++
			m.childBackfillPending = false
			m.childPendingEvents = nil
			// Non-root agents: kick off legacy transcript hydration + tick (QUM-332).
			if msg.Name != m.rootAgent {
				cmds = append(cmds, loadChildTranscriptCmd(m.sprawlRoot, m.homeDir, msg.Name))
			}
		}

		switch len(cmds) {
		case 0:
			return m, nil
		case 1:
			return m, cmds[0]
		default:
			return m, tea.Batch(cmds...)
		}

	case ChildStreamMsg:
		// QUM-439: live event from a per-child unified adapter. Drop stale
		// deliveries from a previous adapter generation.
		if msg.Epoch != m.childAdapterEpoch || msg.Agent != m.childAdapterAgent {
			return m, nil
		}
		// On EOF / cancellation surface: stop the loop, do not re-issue.
		if serr, ok := msg.Inner.(SessionErrorMsg); ok && errors.Is(serr.Err, io.EOF) {
			return m, nil
		}
		// QUM-439 fix 2: if the backfill ChildTranscriptMsg has not landed
		// yet, queue this event and re-issue WaitForEvent. The queue is
		// drained — in arrival order — after the seed SetMessages call so
		// live events never get clobbered by a late backfill.
		if m.childBackfillPending {
			m.childPendingEvents = append(m.childPendingEvents, msg)
			return m, childStreamWaitCmd(m.childAdapter, msg.Agent, msg.Epoch)
		}
		innerCmd := m.applyChildStreamInner(msg.Agent, msg.Inner)
		waitCmd := childStreamWaitCmd(m.childAdapter, msg.Agent, msg.Epoch)
		if innerCmd == nil {
			return m, waitCmd
		}
		return m, tea.Batch(innerCmd, waitCmd)

	case QuestionsAvailableMsg:
		// QUM-527 slice 2c: a question was enqueued or the queue depth
		// changed. If the consumer dispatched this msg (Depth=0), enrich
		// depth via PeekQuestions so the status bar reflects the true queue.
		depth := msg.Depth
		head := msg.Head
		if depth == 0 && m.supervisor != nil {
			d, h := m.supervisor.PeekQuestions()
			if d > 0 {
				depth = d
			}
			if head == nil {
				head = h
			}
		}
		// Default depth=1 if the dispatcher omitted a depth but we have a head.
		if depth == 0 && head != nil {
			depth = 1
		}
		m.statusBar.SetPendingQuestions(depth, agentFromHead(head))
		// Auto-install if nothing is currently installed AND no other modal
		// is up. If another modal is up, defer auto-show until it closes;
		// Ctrl-Q (or another QuestionsAvailableMsg later) will reopen.
		if !m.questionModel.HasPending() && head != nil {
			m.questionModel = m.questionModel.Install(head)
			if !anyOtherModalUp(&m) {
				m.questionModel = m.questionModel.Show()
				m.showQuestion = true
			}
		}
		m.statusBar.SetQuestionModalHidden(!m.showQuestion && m.questionModel.HasPending())
		return m, nil

	case ShowQuestionMsg:
		if m.questionModel.HasPending() && !anyOtherModalUp(&m) {
			m.questionModel = m.questionModel.Show()
			m.showQuestion = true
		}
		m.statusBar.SetQuestionModalHidden(!m.showQuestion && m.questionModel.HasPending())
		return m, nil

	case DismissQuestionMsg:
		// QUM-611: Hard=true (plain Esc inside modal) cancels the upstream
		// question so the blocked MCP tool returns and the caller's turn
		// finalizes. Drafts are discarded. Hard=false (Ctrl-Q) is the
		// QUM-538 soft-hide: visibility off, request stays pending, drafts
		// preserved.
		if msg.Hard {
			id := m.questionModel.activeRequestID()
			m.questionModel = m.questionModel.Reset()
			m.showQuestion = false
			m.statusBar.SetPendingQuestions(0, "")
			m.statusBar.SetQuestionModalHidden(false)
			// CancelQuestion's cancelInternal fires OnCancel on every
			// registered consumer, which for the TUI consumer calls
			// Program.Send synchronously. Calling Send from inside Update
			// can deadlock when the program's msg buffer is full (Update is
			// the goroutine that drains that buffer). Run the cancel in a
			// tea.Cmd so the main loop is free to make progress.
			if id != "" && m.supervisor != nil {
				sup := m.supervisor
				cancelID := id
				return m, func() tea.Msg {
					sup.CancelQuestion(cancelID, "user dismissed via Esc")
					return nil
				}
			}
			return m, nil
		}
		m.showQuestion = false
		m.questionModel = m.questionModel.Hide()
		m.statusBar.SetQuestionModalHidden(!m.showQuestion && m.questionModel.HasPending())
		return m, nil

	case QuestionAnsweredMsg:
		if m.supervisor != nil {
			m.supervisor.ResolveQuestion(msg.RequestID, msg.Response)
		}
		m.questionModel = m.questionModel.Reset()
		m.showQuestion = false
		// Auto-advance to next head if there is one queued.
		if m.supervisor != nil {
			depth, head := m.supervisor.PeekQuestions()
			m.statusBar.SetPendingQuestions(depth, agentFromHead(head))
			if head != nil {
				m.questionModel = m.questionModel.Install(head)
				if !anyOtherModalUp(&m) {
					m.questionModel = m.questionModel.Show()
					m.showQuestion = true
				}
			}
		} else {
			m.statusBar.SetPendingQuestions(0, "")
		}
		m.statusBar.SetQuestionModalHidden(!m.showQuestion && m.questionModel.HasPending())
		return m, nil

	case CancelQuestionMsg:
		if m.questionModel.HasPending() && m.questionModel.activeRequestID() == msg.RequestID {
			m.questionModel = m.questionModel.Reset()
			m.showQuestion = false
		}
		if m.supervisor != nil {
			depth, head := m.supervisor.PeekQuestions()
			m.statusBar.SetPendingQuestions(depth, agentFromHead(head))
		} else {
			m.statusBar.SetPendingQuestions(0, "")
		}
		m.statusBar.SetQuestionModalHidden(!m.showQuestion && m.questionModel.HasPending())
		return m, nil

	case ChildTranscriptMsg:
		// QUM-334: route directly to the named agent's vp. Per-agent
		// ownership means writes to non-observed buffers are correct, not
		// pollution.
		vp := m.viewportFor(msg.Agent)
		// QUM-439: when the unified streaming path owns this agent,
		// remember the seeded ToolIDs so a live ToolCallMsg re-delivering
		// the same tool call dedupes against the backfill.
		isUnified := m.childAdapter != nil && m.childAdapterAgent == msg.Agent
		switch {
		case msg.Err != nil:
			vp.AppendError(fmt.Sprintf("transcript load failed for %s: %v", msg.Agent, msg.Err))
		case len(msg.Entries) > 0:
			vp.SetMessages(msg.Entries)
			if buf, ok := m.agentBuffers[msg.Agent]; ok {
				// QUM-439 fix 3: when the session_id changed (handoff /
				// respawn) the previously-seeded ToolIDs no longer apply
				// to the new session's stream — clear before re-seeding so
				// dedupe is scoped to the active session.
				if buf.SessionID != msg.SessionID {
					buf.seenToolIDs = nil
				}
				buf.SessionID = msg.SessionID
				if isUnified {
					if buf.seenToolIDs == nil {
						buf.seenToolIDs = make(map[string]struct{})
					}
					for _, e := range msg.Entries {
						if e.Type == MessageToolCall && e.ToolID != "" {
							buf.seenToolIDs[e.ToolID] = struct{}{}
						}
					}
				}
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
		// QUM-439 fix 2: now that the seed (or banner) is applied, clear the
		// backfill-pending gate and drain any live ChildStreamMsg events that
		// arrived ahead of the transcript. Drain in arrival order using the
		// normal applyChildStreamInner path so dedupe / routing semantics
		// match a non-racy delivery.
		var drainCmds []tea.Cmd
		if isUnified && m.childBackfillPending {
			m.childBackfillPending = false
			pending := m.childPendingEvents
			m.childPendingEvents = nil
			for _, ev := range pending {
				if ev.Agent != m.childAdapterAgent || ev.Epoch != m.childAdapterEpoch {
					continue
				}
				if serr, ok := ev.Inner.(SessionErrorMsg); ok && errors.Is(serr.Err, io.EOF) {
					continue
				}
				if c := m.applyChildStreamInner(ev.Agent, ev.Inner); c != nil {
					drainCmds = append(drainCmds, c)
				}
			}
		}
		// Reschedule for the next tick only while the agent is still being
		// observed; otherwise let the poll go quiet until re-selection.
		if msg.Agent == m.rootAgent || msg.Agent != m.observedAgent {
			if len(drainCmds) > 0 {
				return m, tea.Batch(drainCmds...)
			}
			return m, nil
		}
		// QUM-400: unified streaming is the only path; no JSONL polling
		// reschedule. Backfill is one-shot via loadChildTranscriptCmd on
		// child observation; subsequent events arrive via TUIAdapter.
		_ = isUnified
		if len(drainCmds) > 0 {
			return m, tea.Batch(drainCmds...)
		}
		return m, nil
	}

	return m, nil
}

// View renders the full TUI layout.
func (m AppModel) View() tea.View {
	return m.renderView(true)
}

// shortHelpState collects the inputs ShortHelpModel needs from the current
// AppModel state (QUM-420). Kept tight so the call site in renderView is
// trivial and so unit tests can reason about the projection separately.
func (m *AppModel) shortHelpState() ShortHelpState {
	selectMode := false
	if vp := m.observedVP(); vp != nil {
		selectMode = vp.IsSelecting()
	}
	return ShortHelpState{
		Focus:       m.activePanel,
		TurnState:   m.turnState,
		InputEmpty:  strings.TrimSpace(m.input.Value()) == "",
		HasQueued:   m.pendingSubmit != "",
		SelectMode:  selectMode,
		PaletteOpen: m.showPalette,
	}
}

// viewUncached returns the same content View() would, bypassing the panel
// render cache. Used by tests as a byte-equivalence oracle (QUM-451).
func (m AppModel) viewUncached() tea.View {
	return m.renderView(false)
}

func (m AppModel) renderView(useCache bool) tea.View {
	if !m.ready {
		return tea.NewView("  Initializing...")
	}

	if m.tooSmall {
		msg := fmt.Sprintf("Terminal too small (minimum %dx%d)", MinTermWidth, MinTermHeight)
		v := tea.NewView(msg)
		v.AltScreen = true
		v.MouseMode = m.mouseMode()
		return v
	}

	layout := ComputeLayout(m.width, m.height, m.inputBoxHeight())
	// QUM-340: when the user is observing a non-root agent, the input bar is
	// hidden — they can only talk to weave. Reclaim its vertical space for
	// the viewport so the layout doesn't waste rows on a bar we're not
	// drawing.
	inputVisible := m.observedAgent == m.rootAgent
	if !inputVisible {
		layout.ViewportHeight += layout.InputHeight
		layout.TreeHeight += layout.InputHeight
		layout.ActivityHeight += layout.InputHeight
		layout.InputHeight = 0
	}

	// Per-panel cached bordered render. Fingerprint key is the inner
	// View() output + dimensions + active-panel flag (border style differs).
	// Inner View() is cheap relative to lipgloss border render; the cache
	// avoids the latter on the hot path (QUM-451).
	treeView := m.cachedPanel(useCache, panelSlotTree, m.tree.View(),
		layout.TreeWidth, layout.TreeHeight,
		m.activePanel == PanelTree)

	vpView := m.cachedPanel(useCache, panelSlotViewport, m.observedVP().View(),
		layout.ViewportWidth, layout.ViewportHeight,
		m.activePanel == PanelViewport)

	// Combine tree and viewport horizontally. On wide terminals, a third
	// column (activity panel) is added to the right. See QUM-296.
	hasActivity := layout.ActivityWidth > 0
	var actView string
	if hasActivity {
		actView = m.cachedPanel(useCache, panelSlotActivity, m.activity.View(),
			layout.ActivityWidth, layout.ActivityHeight,
			false) // activity is never the active panel
	}
	mainRow := m.cachedMainRow(useCache, treeView, vpView, actView, hasActivity)

	// Status bar.
	statusView := m.cachedStatus(useCache, m.statusBar.View(), layout.StatusWidth)

	// Short-help row (QUM-420): one line of context-sensitive bindings,
	// rendered between the input bar and status bar.
	m.shortHelp.SetWidth(layout.ShortHelpWidth)
	m.shortHelp.SetState(m.shortHelpState())
	shortHelpView := m.shortHelp.View()

	// Stack vertically. The input bar is omitted while observing a non-root
	// agent (QUM-340) — the viewport above already owns those rows.
	var inputView, overlay string
	if inputVisible {
		inputView = m.cachedPanel(useCache, panelSlotInput, m.input.View(),
			layout.InputWidth, layout.InputHeight,
			m.activePanel == PanelInput)
		overlay = m.searchOverlay()
	}
	content := m.cachedComposed(useCache, layout.TermWidth, mainRow, overlay, inputView, shortHelpView, statusView, inputVisible)

	if m.showPalette {
		content = m.palette.View()
	}

	if m.showHelp {
		content = m.help.View()
	}

	if m.showQuestion {
		content = m.questionModel.View()
	}

	// QUM-588: the validate popup overlays content when Visible (queued,
	// running-visible, or failed-restored). Drawn after question/help so
	// it sits above ambient content but below confirm/error which are
	// always higher priority.
	if m.validatePopup.Visible() {
		content = m.validatePopup.View()
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
	// Tradeoff: this also captures click-drag, breaking native terminal
	// text-select-and-copy. QUM-617 resolved that via the Ctrl-/ selection-mode
	// toggle — see the selectionMode field on AppModel and m.mouseMode() below.
	// Bonus: Shift+drag (or Option+drag on macOS) bypasses mouse capture in
	// most terminals and is documented as an immediate workaround.
	v.MouseMode = m.mouseMode()
	return v
}

// mouseMode returns the MouseMode value renderView should attach to the
// tea.View on the current frame. Normal mode is CellMotion so the viewport
// receives scroll-wheel events; selection mode (QUM-617) returns None so the
// Bubble Tea renderer emits ESC[?1002l on the next frame and the host
// terminal can do native click-drag text selection.
func (m AppModel) mouseMode() tea.MouseMode {
	if m.selectionMode {
		return tea.MouseModeNone
	}
	return tea.MouseModeCellMotion
}

func (m *AppModel) setTurnState(state TurnState) {
	m.turnState = state
	m.statusBar.SetTurnState(state)
	m.rebuildTree()
}

// finalizeTurn is the single chokepoint for terminal turn cleanup (QUM-475).
// Every terminal handler (SessionResultMsg, InterruptCompletedMsg, error/abort
// paths) must call this so the TUI returns to TurnIdle, the streaming
// assistant message is finalized, any queued submit auto-fires, and
// continuous-bridge event pumps stay armed. Returns a tea.Cmd (possibly nil)
// that the caller should batch with kind-specific cmds.
func (m *AppModel) finalizeTurn() tea.Cmd {
	m.rootVP().FinalizeAssistantMessage()
	m.setTurnState(TurnIdle)
	var cmds []tea.Cmd
	// QUM-340: auto-fire any queued submit by re-dispatching a SubmitMsg
	// through the same code path as a real Enter. Clear the slot + the
	// indicator before dispatching so re-entry sees a clean state.
	if m.pendingSubmit != "" {
		queued := m.pendingSubmit
		m.pendingSubmit = ""
		m.input.SetPendingPreview("")
		cmds = append(cmds, sendMsgCmd(SubmitMsg{Text: queued}))
	}
	// QUM-399: in continuous-bridge mode (UnifiedRuntime/TUIAdapter) the
	// event stream keeps emitting after a turn ends. Keep WaitForEvent
	// running so we don't park the event pump.
	if m.bridge != nil && m.bridge.IsContinuous() {
		cmds = append(cmds, m.bridge.WaitForEvent())
	}
	switch len(cmds) {
	case 0:
		return nil
	case 1:
		return cmds[0]
	default:
		return tea.Batch(cmds...)
	}
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

// backendFault stores the per-agent backend-fault sticker (QUM-602). Mirrors
// BackendFaultMsg's payload minus the agent key (which is the map key).
type backendFault struct {
	Class      string
	Reason     string
	NextAction string
}

func (m *AppModel) rebuildTree() {
	nodes := PrependWeaveRoot(m.childNodes, m.turnState.String(), m.rootUnread)
	// QUM-602: re-apply per-agent fault stickers so the FAULT badge
	// survives AgentTreeMsg-driven node rebuilds.
	if len(m.faults) > 0 {
		for i := range nodes {
			if f, ok := m.faults[nodes[i].Name]; ok {
				nodes[i].FaultClass = f.Class
			}
		}
	}
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
			layout := ComputeLayout(m.width, m.height, m.inputBoxHeight())
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

// anyViewportHasPending reports whether any agent buffer's viewport has an
// in-flight tool call. Used to gate spinner ticking across child viewports.
func (m *AppModel) anyViewportHasPending() bool {
	for _, buf := range m.agentBuffers {
		if buf.vp.HasPendingToolCall() {
			return true
		}
	}
	return false
}

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
	// Set the textarea width first so it can compute line wrapping and report
	// an accurate Height() for layout. The input spans the full terminal
	// width minus border + gutter (4 cells), same as layout.InputWidth - 4.
	m.input.SetWidth(m.width - 4)

	layout := ComputeLayout(m.width, m.height, m.inputBoxHeight())
	// QUM-340: when the user is observing a non-root agent the input bar is
	// hidden in View(); the *observed* viewport reclaims InputHeight rows.
	// Non-observed buffers stay sized to the input-visible layout so they
	// already match when the user cycles back to them.
	observedHeight := layout.ViewportHeight
	if m.observedAgent != m.rootAgent {
		observedHeight += layout.InputHeight
	}

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
	for name, buf := range m.agentBuffers {
		// The currently-observed viewport may need the input bar's reclaimed
		// rows (QUM-340). Other buffers stay sized for the input-visible
		// layout so they're correct on the next cycle-to-root.
		h := layout.ViewportHeight
		if name == m.observedAgent {
			h = observedHeight
		}
		buf.vp.SetSize(layout.ViewportWidth-4, h-4)
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
	m.questionModel.SetSize(m.width, m.height)
	m.validatePopup.SetSize(m.width, m.height)
}

// anyOtherModalUp reports whether any modal OTHER than the question modal is
// active. Used to gate auto-show / Ctrl-Q reopen.
func anyOtherModalUp(m *AppModel) bool {
	return m.showError || m.showConfirm || m.showHelp || m.showPalette
}

// anyModalUp reports whether ANY modal is currently visible. The four input-
// gating sites in Update() (mouse handler, paste handler, input-panel
// history-arrow handler, OpenPaletteMsg handler) call !m.anyModalUp() so a
// modal always owns input. Convention: when adding a new modal flag, extend
// this helper — that's it — and all gates Just Work. Distinct from
// anyOtherModalUp, which deliberately excludes showQuestion for the
// question-auto-show / Ctrl-Q-reopen gates.
func (m *AppModel) anyModalUp() bool {
	return m.showHelp || m.showConfirm || m.showError || m.showPalette || m.showQuestion
}

// agentFromHead returns pq.Req.From if pq is non-nil, else "".
func agentFromHead(pq *supervisor.PendingQuestion) string {
	if pq == nil {
		return ""
	}
	return pq.Req.From
}

// inputBoxHeight returns the total height the input box occupies including
// its border (2 cells). The textarea's Height() reflects the current content
// line count when DynamicHeight is enabled. Before the textarea has been
// sized (SetWidth), its Height() returns a stale default — fall back to the
// layout default in that case.
func (m *AppModel) inputBoxHeight() int {
	h := m.input.Height() + 2 // +2 for top/bottom border
	if h < defaultInputHeight {
		h = defaultInputHeight
	}
	return h
}

func (m *AppModel) updateFocus() {
	if m.activePanel == PanelInput {
		m.input.Focus()
	} else {
		m.input.Blur()
	}
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
		// QUM-448: re-propagate panel sizes if this keystroke grew (or
		// shrank) the textarea. See PasteMsg branch for the same pattern.
		prevInputH := m.inputBoxHeight()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.ready && !m.tooSmall && m.inputBoxHeight() != prevInputH {
			m.resizePanels()
		}
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
		// Filter out the root agent from the child list — PrependWeaveRoot
		// adds a synthetic root node, so including weave here would cause
		// it to appear twice in the tree (once as a real root from
		// buildTreeNodes and once from PrependWeaveRoot).
		filtered := make([]supervisor.AgentInfo, 0, len(agents))
		for _, a := range agents {
			if a.Name != "weave" {
				filtered = append(filtered, a)
			}
		}
		return AgentTreeMsg{Nodes: buildTreeNodes(filtered, unread), RootUnread: len(rootMsgs)}
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
//
// QUM-614: in addition to the on-disk async/interrupt queue, drain weave's
// type=status_change envelopes from the maildir (replaces the in-process
// per-recipient ring drained pre-QUM-614). Drained status lines are
// PREPENDED to the rendered prompt so report_status notifications surface
// before any queued maildir messages on the next turn. If only status
// lines exist, emit a standalone async-class InboxDrainMsg with no entry
// IDs (nothing to MarkDelivered).
func peekAndDrainCmd(sprawlRoot, rootName string, _ supervisor.Supervisor) tea.Cmd {
	return func() tea.Msg {
		pending, _ := agentloop.ListPending(sprawlRoot, rootName)
		statusLines := inboxprompt.DrainStatusChangeLines(sprawlRoot, rootName)
		if len(pending) == 0 && len(statusLines) == 0 {
			return nil
		}
		interrupts, asyncs := agentloop.SplitByClass(pending)
		// QUM-559: status lines are pre-rendered <system-notification>
		// strings independent of delivery class — prepend them to whichever
		// flush prompt we're about to emit so they never get dropped on the
		// floor when interrupts pre-empt asyncs in the same tick window.
		var statusPrefix strings.Builder
		for _, line := range statusLines {
			statusPrefix.WriteString(line)
		}
		// Interrupts take priority; delivery of asyncs happens on the next
		// tick after the interrupt turn settles.
		if len(interrupts) > 0 {
			ids := make([]string, 0, len(interrupts))
			for _, e := range interrupts {
				ids = append(ids, e.ID)
			}
			return InboxDrainMsg{
				Prompt:   statusPrefix.String() + agentloop.BuildInterruptFlushPrompt(interrupts),
				EntryIDs: ids,
				Class:    "interrupt",
			}
		}
		ids := make([]string, 0, len(asyncs))
		for _, e := range asyncs {
			ids = append(ids, e.ID)
		}
		return InboxDrainMsg{
			Prompt:   statusPrefix.String() + agentloop.BuildQueueFlushPrompt(asyncs),
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

// handleHistoryArrow drives Up/Down history navigation for the input panel
// (QUM-410). Returns handled=false when the textarea cursor is mid-buffer so
// the caller falls through to normal textarea movement.
func (m *AppModel) handleHistoryArrow(msg tea.KeyPressMsg) bool {
	if m.history == nil {
		return false
	}
	switch msg.Code {
	case tea.KeyUp:
		if !m.input.AtFirstLine() {
			return false
		}
		entry, ok := m.history.Prev(m.input.Value())
		if !ok {
			return true
		}
		m.input.SetValue(entry)
		return true
	case tea.KeyDown:
		if !m.input.AtLastLine() {
			return false
		}
		entry, _, ok := m.history.Next()
		if !ok {
			return false
		}
		m.input.SetValue(entry)
		return true
	}
	return false
}

// handleSearchKey dispatches a key while reverse-i-search is active
// (QUM-410). Ctrl+R cycles to the next-older match; Enter accepts; Esc and
// Ctrl+C cancel and restore the pre-search input; Backspace shrinks the
// query; printable runes append.
func (m AppModel) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Mod&tea.ModCtrl != 0 && msg.Code == 'r':
		// Cycle to the next-older match.
		if m.history != nil && m.searchQuery != "" {
			if entry, idx, ok := m.history.SearchOlder(m.searchQuery, m.searchMatchIdx); ok {
				m.searchMatchIdx = idx
				m.input.SetValue(entry)
			}
		}
		return m, nil
	case msg.Mod&tea.ModCtrl != 0 && msg.Code == 'c':
		m.searchActive = false
		m.searchQuery = ""
		m.input.SetValue(m.searchPriorInput)
		m.searchPriorInput = ""
		return m, nil
	case msg.Code == tea.KeyEscape:
		m.searchActive = false
		m.searchQuery = ""
		m.input.SetValue(m.searchPriorInput)
		m.searchPriorInput = ""
		return m, nil
	case msg.Code == tea.KeyEnter:
		// Accept current match — input value is already set.
		m.searchActive = false
		m.searchQuery = ""
		m.searchPriorInput = ""
		return m, nil
	case msg.Code == tea.KeyBackspace:
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
		}
		m.refreshSearchMatch()
		return m, nil
	}
	// Printable rune (no modifiers other than Shift): append to query.
	if msg.Mod&^tea.ModShift == 0 && msg.Code >= 0x20 && msg.Code < 0x7f {
		m.searchQuery += string(msg.Code)
		m.refreshSearchMatch()
		return m, nil
	}
	// Swallow other keys while in search.
	return m, nil
}

// refreshSearchMatch re-runs the reverse search from the end of history
// against the current query, updating the input value and matchIdx.
func (m *AppModel) refreshSearchMatch() {
	if m.history == nil || m.searchQuery == "" {
		m.searchMatchIdx = 0
		if m.history != nil {
			m.searchMatchIdx = m.history.Len()
		}
		return
	}
	if entry, idx, ok := m.history.SearchOlder(m.searchQuery, m.history.Len()); ok {
		m.searchMatchIdx = idx
		m.input.SetValue(entry)
	} else {
		m.searchMatchIdx = m.history.Len()
	}
}

// searchOverlay returns the bash-style "(reverse-i-search)`<q>': <match>"
// line shown above the input bar while reverse-search is active. Returns ""
// when the overlay should be hidden.
func (m AppModel) searchOverlay() string {
	if !m.searchActive {
		return ""
	}
	return "(reverse-i-search)`" + m.searchQuery + "': " + m.input.Value()
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

// SetValidatePopupAfter configures the auto-open threshold for the validate
// popup. Pass 0 to use the default (10s). Wired by cmd/enter.go after model
// construction from .sprawl/config.yaml's validate_popup_after_seconds.
// QUM-588.
func (m *AppModel) SetValidatePopupAfter(d time.Duration) {
	m.validatePopup = NewValidatePopupModel(&m.theme, d)
	m.validatePopup.SetSize(m.width, m.height)
}

// ActivityAdapter returns the activity-panel streaming adapter, or nil when
// no unified runtime is bound. (QUM-440)
func (m AppModel) ActivityAdapter() *ActivityStreamAdapter { return m.activityAdapter }

// ActivityAdapterAgent returns the agent name the activity-stream adapter is
// currently pointed at, or "" when not attached. (QUM-440)
func (m AppModel) ActivityAdapterAgent() string { return m.activityAdapterAgent }

// ActivityAdapterEpoch returns the current activity-adapter epoch counter,
// used by ActivityStreamMsg routing to drop stale deliveries. (QUM-440)
func (m AppModel) ActivityAdapterEpoch() uint64 { return m.activityAdapterEpoch }

// ActivityEntries returns the per-agent activity tail snapshot used by the
// panel. Read-only; callers must not mutate the returned slice. (QUM-440)
func (m AppModel) ActivityEntries(agent string) []agentloop.ActivityEntry {
	return m.activityEntries[agent]
}

// activityStreamWaitCmd wraps the adapter's WaitForEvent in an
// ActivityStreamMsg envelope keyed by agent + epoch so the AppModel can drop
// stale deliveries from a prior adapter generation. (QUM-440)
func activityStreamWaitCmd(a *ActivityStreamAdapter, agent string, epoch uint64) tea.Cmd {
	if a == nil {
		return nil
	}
	inner := a.WaitForEvent()
	return func() tea.Msg {
		raw := inner()
		switch v := raw.(type) {
		case ActivityStreamMsg:
			v.Agent = agent
			v.Epoch = epoch
			return v
		case ActivityStreamClosedMsg:
			// QUM-479: forward the typed close sentinel so AppModel can tear
			// down the adapter without mis-routing into the bridge-restart
			// path. Fill in the agent name (the adapter only knows the epoch).
			v.Agent = agent
			return v
		default:
			// Drop adapter-internal stragglers. The AppModel only cares about
			// ActivityStreamMsg / ActivityStreamClosedMsg from this adapter;
			// leaking other msgs (notably SessionErrorMsg) up triggers
			// spurious session restarts. (QUM-457 / QUM-479)
			return nil
		}
	}
}

// activityEntryKey returns a canonical dedupe key for an ActivityEntry,
// composed of fields that uniquely identify it. (QUM-440)
func activityEntryKey(e agentloop.ActivityEntry) string {
	return e.TS.UTC().Format(time.RFC3339Nano) + "\x00" + e.Kind + "\x00" + e.Tool + "\x00" + e.Summary
}

// ChildAdapter returns the child viewport streaming adapter, or nil when no
// unified child is currently observed. (QUM-439)
func (m AppModel) ChildAdapter() *ChildStreamAdapter { return m.childAdapter }

// ChildAdapterAgent returns the agent name the child stream adapter is
// currently pointed at, or "" when not observing. (QUM-439)
func (m AppModel) ChildAdapterAgent() string { return m.childAdapterAgent }

// ChildAdapterEpoch returns the current child-adapter epoch counter, used by
// ChildStreamMsg routing to drop stale deliveries. (QUM-439)
func (m AppModel) ChildAdapterEpoch() uint64 { return m.childAdapterEpoch }

// lookupUnifiedRuntime resolves the supervisor's RuntimeRegistry entry for
// name and returns the underlying UnifiedRuntime if the handle exposes one,
// or nil if the supervisor is nil, the registry is nil, the agent is not
// registered, or the handle is legacy. (QUM-439)
func (m *AppModel) lookupUnifiedRuntime(name string) *sprawlrt.UnifiedRuntime {
	if m.supervisor == nil {
		return nil
	}
	reg := m.supervisor.RuntimeRegistry()
	if reg == nil {
		return nil
	}
	rt, ok := reg.Get(name)
	if !ok || rt == nil {
		return nil
	}
	return rt.UnifiedRuntime()
}

// childStreamWaitCmd wraps the adapter's WaitForEvent in a ChildStreamMsg
// envelope keyed by agent + epoch so the AppModel can drop stale deliveries
// from a prior adapter generation. (QUM-439)
func childStreamWaitCmd(a *ChildStreamAdapter, agent string, epoch uint64) tea.Cmd {
	if a == nil {
		return nil
	}
	inner := a.WaitForEvent()
	return func() tea.Msg {
		raw := inner()
		// QUM-479: forward the typed close sentinel directly so AppModel can
		// tear down the adapter without mis-routing into the bridge-restart
		// path. Fill in the agent name (the adapter only knows the epoch).
		if closed, ok := raw.(ChildStreamClosedMsg); ok {
			closed.Agent = agent
			return closed
		}
		return ChildStreamMsg{Agent: agent, Epoch: epoch, Inner: raw}
	}
}

// applyChildStreamInner routes a single live tea.Msg into the named child
// agent's per-agent buffer. Mirrors the bridge-side handlers but writes to
// the child viewport instead of the root viewport. ToolCallMsg entries
// already seeded by the backfill are dropped to avoid double-render. (QUM-439)
func (m *AppModel) applyChildStreamInner(agent string, inner tea.Msg) tea.Cmd {
	vp := m.viewportFor(agent)
	buf := m.agentBuffers[agent]
	switch im := inner.(type) {
	case AssistantContentMsg:
		var cmds []tea.Cmd
		for _, sub := range im.Msgs {
			if c := m.applyChildStreamInner(agent, sub); c != nil {
				cmds = append(cmds, c)
			}
		}
		if len(cmds) > 0 {
			return tea.Batch(cmds...)
		}
		return nil
	case AssistantTextMsg:
		vp.AppendAssistantChunk(im.Text)
		return nil
	case ToolCallMsg:
		if buf != nil && im.ToolID != "" {
			if _, dup := buf.seenToolIDs[im.ToolID]; dup {
				return nil
			}
			if buf.seenToolIDs == nil {
				buf.seenToolIDs = make(map[string]struct{})
			}
			buf.seenToolIDs[im.ToolID] = struct{}{}
		}
		vp.AppendToolCallWithHeader(im.ToolName, im.ToolID, im.Approved, im.Input, im.FullInput, im.HeaderArg, im.HeaderParams, im.ParentToolUseID)
		wasZero := m.pendingToolCalls == 0
		m.pendingToolCalls++
		if wasZero {
			return m.spinner.Tick
		}
		return nil
	case ToolResultMsg:
		if vp.MarkToolResult(im.ToolID, im.Content, im.IsError) {
			if m.pendingToolCalls > 0 {
				m.pendingToolCalls--
			}
		}
		return nil
	case SessionResultMsg:
		vp.FinalizeAssistantMessage()
		return nil
	}
	return nil
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

// mcpOpBannerThreshold is the elapsed time after which a long-running MCP
// tool call earns a viewport banner with SIGUSR1 guidance. Package-level var
// so reducer tests can compress it. (QUM-497)
var mcpOpBannerThreshold = 60 * time.Second

// mcpOpTickInterval is the refresh cadence for the live elapsed-time render
// in the status bar. Package-level var so tests can override. (QUM-497)
var mcpOpTickInterval = 1 * time.Second

// mcpLongRunningBannerExempt names MCP tools whose long elapsed time is
// expected by design (blocking-on-human) and therefore should NOT raise the
// "taking longer than usual" viewport banner. The in-flight tracker still
// records these ops so SIGUSR1 state dumps remain complete. (QUM-558)
//
// TODO(QUM-558): if this grows past ~3 entries, promote to tool-side
// metadata (`BlocksOnHuman` flag on MCP tool registration) — Option 3.
var mcpLongRunningBannerExempt = map[string]bool{
	"ask_user_question": true,
}

// mcpOpTickCmd returns a tea.Cmd that fires an mcpOpTickMsg after one tick
// interval. The reducer self-perpetuates while ops remain active. (QUM-497)
func mcpOpTickCmd() tea.Cmd {
	return tea.Tick(mcpOpTickInterval, func(time.Time) tea.Msg {
		return mcpOpTickMsg{}
	})
}

// mcpOpThresholdCmd returns a one-shot tea.Cmd that fires after delay,
// scoping the resulting mcpOpThresholdMsg to a single call_id so the
// reducer can ignore stale deliveries from a finished call. (QUM-497)
func mcpOpThresholdCmd(callID string, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return mcpOpThresholdMsg{CallID: callID}
	})
}

// orderedMCPOps returns the active ops in insertion order (oldest first) so
// SetActiveOps can render the longest-running call in the leftmost slot of
// the status bar. (QUM-497)
func (m *AppModel) orderedMCPOps() []OpDescriptor {
	if len(m.activeMCPOps) == 0 {
		return nil
	}
	out := make([]OpDescriptor, 0, len(m.mcpOpOrder))
	for _, id := range m.mcpOpOrder {
		if op, ok := m.activeMCPOps[id]; ok {
			out = append(out, op)
		}
	}
	return out
}

// removeStr returns ss with the first occurrence of v removed. Returns the
// original slice when v is absent. (QUM-497)
func removeStr(ss []string, v string) []string {
	for i, s := range ss {
		if s == v {
			return append(ss[:i], ss[i+1:]...)
		}
	}
	return ss
}

// persistCostCmd writes the session cost to the agent's state file. Fire-and-
// forget: errors are swallowed because cost display is best-effort. (QUM-366)
func persistCostCmd(sprawlRoot, agentName string, totalCostUsd float64) tea.Cmd {
	return func() tea.Msg {
		agent, err := state.LoadAgent(sprawlRoot, agentName)
		if err != nil {
			return nil
		}
		agent.TotalCostUsd = totalCostUsd
		agent.LastCostUpdateAt = time.Now().UTC().Format(time.RFC3339)
		_ = state.SaveAgent(sprawlRoot, agent)
		return nil
	}
}

// formatInboxBanner renders the unified "inbox: ..." viewport banner used by
// both the AgentTreeMsg rise-detector and the InboxArrivalMsg notifier
// (QUM-473 §3). Pre-unification the two sites produced two different
// formats ("inbox: N new message(s) for weave" vs "inbox: new message from
// X") so the user saw inconsistent phrasings for the same logical event.
//
// Format:
//   - count == 1, from == "":     "inbox: 1 new message"
//   - count >  1, from == "":     "inbox: N new messages"
//   - count == 1, from != "":     "inbox: 1 new message from <from>"
//   - count >  1, from != "":     "inbox: N new messages from <from>"
//
// Caller is responsible for only invoking when count > 0.
func formatInboxBanner(count int, from string) string {
	noun := "messages"
	if count == 1 {
		noun = "message"
	}
	if from != "" {
		return fmt.Sprintf("inbox: %d new %s from %s", count, noun, from)
	}
	return fmt.Sprintf("inbox: %d new %s", count, noun)
}
