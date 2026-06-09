package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dmotles/sprawl/internal/agentloop"
	"github.com/dmotles/sprawl/internal/inboxprompt"
	"github.com/dmotles/sprawl/internal/memory"
	"github.com/dmotles/sprawl/internal/messages"
	sprawlrt "github.com/dmotles/sprawl/internal/runtime"
	"github.com/dmotles/sprawl/internal/state"
	"github.com/dmotles/sprawl/internal/supervisor"
	"github.com/dmotles/sprawl/internal/usage"
)

// AgentBuffer stores the per-agent chat-region state. Each agent owns its
// own ViewportModel (facade over ChatRegion+ChatList) so streamed assistant
// chunks, tool calls, and notification envelopes can never bleed across
// agent contexts (QUM-334).
type AgentBuffer struct {
	vp ViewportModel
	// cl is a non-owning handle on the ChatList that lives inside
	// vp.region. Retained as a field so existing tests (and AppModel
	// shorthand call sites like rootBuf().cl) keep working. All Append* /
	// MarkToolResult / SetMessages writes flow through vp; the cl handle is
	// observe-only. QUM-676.
	cl *ChatList

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

// AppendUser appends a user-typed turn. QUM-676: routes through vp, which
// now owns the single rendering pipeline (ChatList lives inside vp.region).
func (b *AgentBuffer) AppendUser(text string) {
	b.vp.ChatList().AppendUser(text)
}

// AppendAssistantChunk appends a streaming assistant chunk.
func (b *AgentBuffer) AppendAssistantChunk(text string) {
	b.vp.ChatList().AppendAssistantChunk(text)
}

// FinalizeAssistantMessage finalizes the in-flight assistant item.
func (b *AgentBuffer) FinalizeAssistantMessage() {
	b.vp.ChatList().FinalizeAssistantMessage()
}

// AppendThinking records that a thinking content block arrived. The body
// text is intentionally not stored — Claude/Opus redacts it server-side,
// and the marker carries only a count. (QUM-677 S7)
func (b *AgentBuffer) AppendThinking() {
	b.vp.ChatList().AppendThinking()
}

// AppendToolCallWithHeader appends a tool-call row.
func (b *AgentBuffer) AppendToolCallWithHeader(name, toolID string, approved bool,
	input, fullInput, headerArg string, headerParams []KVPair,
	parentToolUseID string,
) {
	b.vp.ChatList().AppendToolCallWithHeader(name, toolID, approved, input, fullInput, headerArg, headerParams, parentToolUseID)
}

// MarkToolResult resolves a pending tool call. Returns true on match.
func (b *AgentBuffer) MarkToolResult(toolID, content string, isError bool) bool {
	return b.vp.ChatList().MarkToolResult(toolID, content, isError)
}

// AppendSystemNotification appends notification envelope(s).
func (b *AgentBuffer) AppendSystemNotification(text string) {
	b.vp.ChatList().AppendSystemNotification(text)
}

// AppendAutoTrigger appends a QUM-634 auto-continue marker.
func (b *AgentBuffer) AppendAutoTrigger(summary string) {
	b.vp.ChatList().AppendAutoTrigger(summary)
}

// SetToolInputsExpanded fans the QUM-335 global expand-all toggle into the
// ChatList.
func (b *AgentBuffer) SetToolInputsExpanded(v bool) {
	b.vp.SetToolInputsExpanded(v)
}

// SetMessages replaces the buffer's transcript from a backfill snapshot
// (ChildTranscriptMsg / PreloadTranscript / resync). ChatList.Reset
// force-finalizes the trailing assistant and clears pendingTools per the
// QUM-669 wedge-exit invariant.
func (b *AgentBuffer) SetMessages(entries []MessageEntry) {
	b.vp.ChatList().Reset(entries)
}

// AppModel is the root Bubble Tea model composing all panels.
type AppModel struct {
	tree      TreeModel
	input     InputModel
	statusBar StatusBarModel
	shortHelp ShortHelpModel
	confirm   ConfirmModel

	help     HelpModel
	showHelp bool

	palette     PaletteModel
	showPalette bool

	// QUM-733 5b: agent-tree modal (TreeModalModel). Opened via `/tree`
	// palette command (ToggleTreeMsg). Lowest-priority modal — suppressed
	// while any other modal is up.
	treeModal TreeModalModel
	showTree  bool

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

	// QUM-721 — /usage modal state.
	showUsage  bool
	usageModal UsageModalModel

	// validatePopup renders the live validate-output popup (QUM-588).
	// State transitions are driven by ValidateEventMsg dispatched from
	// cmd/enter.go (which wraps the supervisor's validateEmitter).
	validatePopup ValidatePopupModel

	// toasts is the right-anchored notification overlay (QUM-649). Composited
	// above chat content but below all modals in View(). Ctrl+T clears all.
	toasts ToastModel

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

	// snapshotCmd is the injected incident-bundle producer triggered by
	// Ctrl+\ (QUM-728). When nil the key handler still fires the request
	// message, but the reducer synthesizes an error-complete instead of
	// invoking a real capture. Wired by cmd/enter.go to
	// internal/observe/incident.Snapshotter.
	snapshotCmd func() tea.Msg

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

	// toolInputsExpanded is the global flag (QUM-335) toggled by Ctrl+O.
	// When true, every per-agent viewport renders tool calls with their
	// full ToolInputFull body instead of the truncated summary. Default
	// false; survives agent cycling because new viewports inherit it on
	// creation in viewportFor.
	toolInputsExpanded bool

	// QUM-674 S4 / QUM-732: the global spinner subsystem (QUM-336) was
	// removed when streaming + tool-call lifecycle moved into ChatList.
	// QUM-732 reintroduces animation as a per-item tea.Tick driven by
	// ToolCallItem itself (toolTickMsg) — no global pulse, no shared frame
	// counter. The animator runs only for pending items in the observed
	// agent's pane; ✓ (success) and ✗ (failure) stay static.

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

	// gapState is the QUM-669 viewport-resync state machine. See gapStateNormal
	// constants below. Mutated by EventDropDetectedMsg / gapConfirmMsg /
	// ViewportResyncMsg. gapID is a monotonic counter used to discriminate
	// stale debounce-confirm deliveries (mirror mcpOpThresholdCmd's pattern).
	// pendingMissing accumulates Missing counts while the reducer is in the
	// gap-pending state so a debounced burst can still cross gapBurstThreshold
	// and short-circuit to dropped. resyncInFlight coalesces concurrent resync
	// cmds.
	gapState       gapState
	gapID          uint64
	pendingMissing uint64
	resyncInFlight bool
}

// NewAppModel constructs the root model with all sub-models.
// bridge may be nil for static placeholder mode.
// sup and sprawlRoot are optional; when provided, the tree polls agent status.
// restartFunc is called when the user requests a session restart after a crash.
func NewAppModel(accentColor, repoName, version string, bridge SessionBackend, sup supervisor.Supervisor, sprawlRoot string, restartFunc func() (SessionBackend, error)) AppModel {
	theme := NewTheme(accentColor)
	rootAgent := "weave"
	agentBuffers := make(map[string]*AgentBuffer)
	// Seed the root agent's buffer eagerly: PreloadTranscript can run before
	// Init/Update fires, so lazy-init via viewportFor would arrive too late
	// (QUM-334 §5). Child agent buffers are still lazy.
	rootVP := NewViewportModel(&theme)
	agentBuffers[rootAgent] = &AgentBuffer{vp: rootVP, cl: rootVP.ChatList()}
	// QUM-675 S5: SessionBanner is redundant with the status-bar sess:<id>
	// segment (set on SessionInitializedMsg). No banner is appended to the
	// viewport on startup.

	app := AppModel{
		tree:                NewTreeModel(&theme),
		input:               NewInputModel(&theme),
		statusBar:           NewStatusBarModel(&theme, repoName, version, 0),
		shortHelp:           NewShortHelpModel(&theme),
		help:                NewHelpModel(&theme),
		confirm:             NewConfirmModel(&theme),
		palette:             NewPaletteModel(&theme),
		treeModal:           NewTreeModalModel(&theme),
		bridge:              bridge,
		turnState:           TurnIdle,
		supervisor:          sup,
		sprawlRoot:          sprawlRoot,
		observedAgent:       rootAgent,
		rootAgent:           rootAgent,
		agentBuffers:        agentBuffers,
		faults:              make(map[string]backendFault),
		theme:               theme,
		restartFunc:         restartFunc,
		version:             version,
		history:             NewHistory(sprawlRoot),
		activeMCPOps:        make(map[string]OpDescriptor),
		mcpOpThresholdShown: make(map[string]bool),
		cache:               newViewCache(),
		questionModel:       NewQuestionModel(&theme),
		usageModal:          NewUsageModalModel(&theme),
		validatePopup:       NewValidatePopupModel(&theme, 0),
		toasts:              NewToastModel(&theme),
	}
	_ = app.history.Load()
	app.updateFocus()
	app.rebuildTree()
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

	case toolTickMsg:
		// QUM-732: route per-item animation tick to the observed agent's
		// ChatList. Background panes never receive ticks (AC #4 — only the
		// observed pane drives renders for spinner animation). Modal overlays
		// do NOT suppress this case: the underlying tool is still in flight
		// and we want the user to see the row pulsing while the modal is up.
		vp := m.observedVP()
		if vp == nil {
			return m, nil
		}
		return m, vp.ChatList().Update(msg)

	case tea.PasteMsg:
		// Bracketed-paste from the terminal. Forward to the input panel so embedded
		// newlines are inserted literally instead of being treated as Enter-submit.
		// Only when the input bar is the active panel (root-agent view, no modal).
		if m.observedAgent != m.rootAgent || m.anyModalUp() {
			return m, nil
		}
		// QUM-448: track input-box height across the Update so we can
		// re-propagate sizes to the cached tree/viewport sub-models
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

	case tea.MouseMsg:
		// QUM-731: restore mouse-wheel scroll. Mouse capture is back on via
		// MouseModeCellMotion in renderView; forward wheel / motion / click
		// events to the observed viewport so the wheel scrolls chat. Mirrors
		// the modal gating used for keyboard scroll bindings (QUM-653) — when
		// any modal is up, swallow the event so dialogs are not bypassed.
		if m.anyModalUp() {
			return m, nil
		}
		vp := m.observedVP()
		updated, cmd := vp.Update(msg)
		*vp = updated
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
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'r' && !m.anyModalUp() && m.observedAgent == m.rootAgent {
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

		// QUM-653: keyboard scroll bindings. PgUp/PgDn always forward to the
		// viewport. Home/End forward via vp.GotoTop / GotoBottom. Up/Down
		// forward only when the input is empty (otherwise they remain input
		// cursor / history navigation). Gated by modal-up so scroll keys
		// don't bypass dialogs.
		if !m.anyModalUp() {
			if msg.Code == tea.KeyPgUp || msg.Code == tea.KeyPgDown {
				vp := m.observedVP()
				updated, cmd := vp.Update(msg)
				*vp = updated
				return m, cmd
			}
			if msg.Code == tea.KeyHome || msg.Code == tea.KeyEnd {
				vp := m.observedVP()
				if msg.Code == tea.KeyHome {
					vp.region.vp.GotoTop()
				} else {
					vp.region.vp.GotoBottom()
				}
				// Mirror the auto-scroll bookkeeping ViewportModel.Update does.
				if vp.region.vp.AtBottom() {
					vp.region.autoScroll = true
					vp.region.hasNewContent = false
				} else {
					vp.region.autoScroll = false
				}
				return m, nil
			}
			if (msg.Code == tea.KeyUp || msg.Code == tea.KeyDown) && m.input.Value() == "" {
				vp := m.observedVP()
				updated, cmd := vp.Update(msg)
				*vp = updated
				return m, cmd
			}
		}

		// QUM-410: input-panel history navigation. Up/Down walk history
		// only when the textarea cursor is at the first / last line so
		// multi-line editing isn't hijacked. QUM-536: gate on the modal
		// flags too — any visible modal owns arrow keys, and prior to
		// this gate KeyUp was being asymmetrically swallowed by
		// `history.Prev` (which always succeeds when history is non-empty)
		// while KeyDown fell through to the modal because `history.Next`
		// returns ok=false on a fresh model.
		if m.history != nil && !m.anyModalUp() && m.observedAgent == m.rootAgent &&
			(msg.Code == tea.KeyUp || msg.Code == tea.KeyDown) {
			if m.handleHistoryArrow(msg) {
				return m, nil
			}
		}

		// QUM-630: Ctrl+C with a queued submit recalls it into the prompt
		// (refuse-to-clobber). Precedence above the QUM-409 clear/quit
		// ladder so the quit-confirm invariant is reached only when nothing
		// is queued. Second Ctrl+C (queue now empty) falls through to the
		// existing clear-text / quit-confirm rungs unchanged.
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'c' && m.pendingSubmit != "" && !m.showConfirm {
			queued := m.pendingSubmit
			m.pendingSubmit = ""
			m.input.SetPendingPreview("")
			if strings.TrimSpace(m.input.Value()) == "" {
				m.input.SetValue(queued)
				m.input.CursorEnd()
			}
			return m, nil
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

		// Toggle help on F1. F1 is the canonical help key — `?` is reserved
		// so users can type it literally in the input panel (QUM-695 dropped
		// `activePanel` gating, so `?` is no longer disambiguated by focus).
		if msg.Code == tea.KeyF1 {
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

		// QUM-721: when the /usage modal is visible, route ALL keys to it.
		if m.showUsage {
			var cmd tea.Cmd
			m.usageModal, cmd = m.usageModal.Update(msg)
			return m, cmd
		}

		// QUM-733 5b: while the tree modal is up, all key events route to
		// it. Placed after the higher-priority modal gates (confirm, error,
		// palette, question, help, usage) so those always take precedence.
		if m.showTree {
			var cmd tea.Cmd
			m.treeModal, cmd = m.treeModal.Update(msg)
			if !m.treeModal.Visible() {
				m.showTree = false
			}
			return m, cmd
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

		// Ctrl+T: dismiss all toasts (QUM-649). Only consumes the keystroke
		// when at least one toast is up — otherwise 't' falls through to the
		// input panel so the user can type it literally.
		if msg.Mod&tea.ModCtrl != 0 && (msg.Code == 't' || msg.Code == 'T') {
			if !m.toasts.Empty() {
				m.toasts.DismissAll()
				return m, nil
			}
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

		// QUM-669: Ctrl+L is the manual viewport-resync short-circuit. Bypasses
		// the gap-debounce state machine and immediately rebuilds the viewport
		// from the session JSONL. Gated by the modal precedence returns above
		// (showConfirm/showError/showHelp/showPalette/showQuestion) and the
		// searchActive guard at the top of the KeyPressMsg switch — Ctrl+R
		// reverse-search owns the keystroke while active.
		if msg.Mod&tea.ModCtrl != 0 && (msg.Code == 'l' || msg.Code == 'L') {
			if m.resyncInFlight {
				return m, nil
			}
			m.resyncInFlight = true
			m.gapState = gapStateResyncing
			m.statusBar.SetResyncPill("resyncing…")
			missing := m.pendingMissing
			m.pendingMissing = 0
			return m, m.resyncCmd(missing)
		}

		// QUM-728: Ctrl+\ triggers an in-process incident snapshot. The actual
		// capture runs as a tea.Cmd (background goroutine) so the TUI stays
		// responsive. Ctrl+G was the runner-up; \ chosen because it's analogous
		// to SIGQUIT and is unbound elsewhere in the TUI. Gated by the modal
		// precedence returns above (showConfirm/showError/showHelp/showPalette/
		// showQuestion/showTree).
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == '\\' {
			return m, func() tea.Msg { return IncidentSnapshotRequestedMsg{} }
		}

		// Ctrl+O: toggle the global expand-tool-inputs flag (QUM-335).
		// Affects every per-agent viewport so the user can scan the full
		// command / JSON for any tool call without leaving the TUI. Gated
		// implicitly by the modal returns above. (Rebound from Ctrl+E to
		// match Claude Code's expand convention.)
		if msg.Mod&tea.ModCtrl != 0 && msg.Code == 'o' {
			m.toolInputsExpanded = !m.toolInputsExpanded
			for _, buf := range m.agentBuffers {
				buf.SetToolInputsExpanded(m.toolInputsExpanded)
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

		// QUM-630: Esc with a queued submit is the preempt-and-send key.
		// - Mid-turn: interrupt the current turn AND deliver queued as next prompt.
		// - Idle: submit queued immediately via the standard SubmitMsg path.
		// Either way clear pendingSubmit + preview synchronously. The QUM-576
		// "reload draft on empty input" affordance has moved to Ctrl+C above.
		if msg.Code == tea.KeyEscape && m.pendingSubmit != "" {
			queued := m.pendingSubmit
			m.pendingSubmit = ""
			m.input.SetPendingPreview("")
			if m.bridge == nil {
				return m, nil
			}
			// Record in shell history so Up-arrow recall works regardless of
			// whether the preempt round-trip succeeds (text-never-lost).
			if m.history != nil {
				m.history.Append(queued)
				m.history.Reset()
			}
			if m.turnState == TurnStreaming || m.turnState == TurnThinking {
				m.statusBar.SetTransientLabel("Interrupting...")
				toastCmd := m.toasts.Spawn(Toast{
					Text:      fmt.Sprintf("interrupt sent to %s", m.rootAgent),
					Style:     ToastInfo,
					DismissOn: TimerDismiss(2 * time.Second),
				})
				return m, tea.Batch(m.bridge.InterruptAndSend(queued), toastCmd)
			}
			// Idle: route through SubmitMsg so the standard send path runs.
			return m, sendMsgCmd(SubmitMsg{Text: queued})
		}

		// QUM-380: Esc during an active turn sends an interrupt request to
		// Claude. Checked after help/select/pendingSubmit so those actions
		// take priority. The protocol confirms the interrupt asynchronously
		// via SessionResultMsg/SessionErrorMsg; we show "Interrupting..."
		// feedback immediately.
		if msg.Code == tea.KeyEscape && (m.turnState == TurnStreaming || m.turnState == TurnThinking) && m.bridge != nil {
			m.statusBar.SetTransientLabel("Interrupting...")
			// QUM-651: surface a transient toast so the user sees that the
			// interrupt request was issued. QUM-697: auto-dismiss on a 2s
			// timer — the supervisor ack (InterruptResultMsg) for local-bridge
			// agents lands in the same/next event-loop pass, so a
			// condition-dismiss would clear the toast before it ever renders.
			toastCmd := m.toasts.Spawn(Toast{
				Text:      fmt.Sprintf("interrupt sent to %s", m.rootAgent),
				Style:     ToastInfo,
				DismissOn: TimerDismiss(2 * time.Second),
			})
			return m, tea.Batch(m.bridge.Interrupt(), toastCmd)
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

	case AutoContinueMsg:
		// QUM-634: render a trigger marker before the autonomous turn's
		// assistant response so the user sees WHY weave responded.
		m.rootBuf().AppendAutoTrigger(msg.Summary)
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

	case ShowUsageMsg:
		// QUM-721: gate on other modals so usage doesn't stack atop help/etc.
		if m.input.disabled || m.anyModalUp() {
			return m, nil
		}
		totals, _ := usage.SumByAgent(m.sprawlRoot)
		m.usageModal = m.usageModal.Install(totals)
		m.usageModal = m.usageModal.SetSize(m.width, m.height)
		m.usageModal = m.usageModal.Show()
		m.showUsage = true
		return m, nil

	case DismissUsageMsg:
		m.showUsage = false
		m.usageModal = m.usageModal.Hide()
		return m, nil

	case ToggleTreeMsg:
		// QUM-733 5b: open/close the agent-tree modal. Opening is suppressed
		// while any higher-priority modal is up (mirrors the Ctrl-Q reopen
		// gate at app.go:582-586). Closing is unconditional.
		if m.showTree {
			m.showTree = false
			m.treeModal.Hide()
			return m, nil
		}
		if anyOtherModalUpExceptTree(&m) {
			return m, nil
		}
		m.treeModal.SetSize(m.width, m.height)
		m.treeModal.SetNodes(m.tree.nodes, m.observedAgent)
		m.treeModal.Show()
		m.showTree = true
		return m, nil

	case ToastSpawnMsg:
		// QUM-649: append a toast (auto-ID assigned if Toast.ID is empty).
		// Returned cmd schedules the auto-dismiss tick for TimerDismiss.
		cmd := m.toasts.Spawn(msg.Toast)
		return m, cmd

	case ToastDismissMsg:
		if msg.All {
			m.toasts.DismissAll()
		} else {
			m.toasts.Dismiss(msg.ID)
		}
		return m, nil

	case ToastConditionClearedMsg:
		m.toasts.ClearCondition(msg.ID)
		return m, nil

	case toastTimerMsg:
		m.toasts.Dismiss(msg.ID)
		return m, nil

	case PaletteQuitMsg:
		m.quitting = true
		return m, tea.Quit

	case InjectPromptMsg:
		if msg.Template == "" || m.bridge == nil || m.turnState != TurnIdle {
			return m, nil
		}
		m.setTurnState(TurnThinking)
		m.statusBar.SetTransientLabel("/handoff dispatched — see output below")
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
		// QUM-323: render the flush prompt in the viewport so the human watching
		// the TUI can see what got drained — parity with SubmitMsg which renders
		// user-typed input. Without this, the drained frame is invisible (only
		// the status line hints at it) and the only way to confirm drain worked
		// is to grep logs — which also breaks the body-in-prompt e2e assertion.
		// QUM-338 / QUM-557 / QUM-693: AppendSystemNotification strips
		// `<system-notification>` wrappers and emits SystemNotificationItem
		// envelope items. Pre-QUM-555 plain inbox banners (untagged prompts)
		// are silently dropped from ChatList; the Claude session still
		// receives the body as a user-role turn via SendMessage.
		m.rootBuf().AppendSystemNotification(msg.Prompt)
		m.pendingDrainIDs = append([]string(nil), msg.EntryIDs...)
		m.setTurnState(TurnThinking)
		// QUM-675 S5: set transient AFTER setTurnState — the Idle→Thinking
		// transition inside setTurnState clears the label.
		m.statusBar.SetTransientLabel(fmt.Sprintf("inbox: draining %d %s message(s) into next prompt", len(msg.EntryIDs), label))
		return m, m.bridge.SendMessage(msg.Prompt)

	case SubmitMsg:
		if msg.Text == "" {
			return m, nil
		}
		// QUM-675 S5: a new user prompt clears one-shot transient banners
		// (Completed/Interrupted/startup/recovered).
		m.statusBar.SetTransientLabel("")
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
		// QUM-672 dual-append: the user-typed turn lands in both the legacy
		// viewport (live-render path) and the ChatList shadow. See
		// docs/designs/tui-structural-rewrite-plan.md §3 S2.
		m.agentBuffers[m.rootAgent].AppendUser(msg.Text)
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
				m.rootBuf().AppendAssistantChunk(im.Text)
			case ThinkingMsg:
				// QUM-677 S7: thinking blocks render in-chat as a transient
				// count marker. They don't bump the turn state — TurnThinking
				// is already set by SubmitMsg/InjectPromptMsg, and the
				// assistant-text branch owns the Thinking→Streaming transition.
				_ = im
				m.rootBuf().AppendThinking()
			case SessionUsageMsg:
				// QUM-385: true context window usage = input + cache_read +
				// cache_creation. input_tokens alone is the non-cached subset
				// and understates the prefix by a large factor when prompt
				// caching is on.
				m.statusBar.SetTokenUsage(im.InputTokens + im.CacheReadInputTokens + im.CacheCreationInputTokens)
			case ToolCallMsg:
				m.rootBuf().AppendToolCallWithHeader(im.ToolName, im.ToolID, im.Approved, im.Input, im.FullInput, im.HeaderArg, im.HeaderParams, im.ParentToolUseID)
				// QUM-732: arm per-item spinner tick only for the observed pane.
				if m.observedAgent == m.rootAgent {
					if tick := m.rootBuf().cl.PendingToolTickCmds(); tick != nil {
						cmds = append(cmds, tick)
					}
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
		m.rootBuf().AppendAssistantChunk(msg.Text)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case ThinkingMsg:
		// QUM-677 S7: standalone delivery path (mirrors AssistantContentMsg).
		m.rootBuf().AppendThinking()
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case ToolCallMsg:
		m.rootBuf().AppendToolCallWithHeader(msg.ToolName, msg.ToolID, msg.Approved, msg.Input, msg.FullInput, msg.HeaderArg, msg.HeaderParams, msg.ParentToolUseID)
		var cmds []tea.Cmd
		// QUM-732: arm per-item spinner tick only for the observed pane.
		if m.observedAgent == m.rootAgent {
			if tick := m.rootBuf().cl.PendingToolTickCmds(); tick != nil {
				cmds = append(cmds, tick)
			}
		}
		if m.bridge != nil {
			cmds = append(cmds, m.bridge.WaitForEvent())
		}
		if len(cmds) == 0 {
			return m, nil
		}
		return m, tea.Batch(cmds...)

	case ToolResultMsg:
		// Route to the root buffer (bridge events are weave-only per
		// QUM-334's gating). QUM-674 S4: the global spinner counter was
		// removed; MarkToolResult's return is purely advisory now.
		_ = m.rootBuf().MarkToolResult(msg.ToolID, msg.Content, msg.IsError)
		if m.bridge != nil {
			return m, m.bridge.WaitForEvent()
		}
		return m, nil

	case SessionResultMsg:
		// Display result text only if no assistant text was already streamed.
		// When Claude returns text in the assistant message, it also appears
		// in result.Result — avoid duplicating it.
		root := m.rootVP()
		if !msg.IsError && strings.TrimSpace(msg.Result) != "" && !root.ChatList().HasPendingAssistant() {
			m.rootBuf().AppendAssistantChunk(strings.TrimSpace(msg.Result))
		}
		// Finalize the assistant chunk before appending status/error so the
		// last-entry probe in FinalizeAssistantMessage still sees an
		// assistant entry.
		finalizeCmd := m.finalizeTurn()
		if msg.IsError {
			// QUM-675 S5: session-level errors escalate to the γ overlay
			// instead of being buried as a viewport error banner.
			m.errorDialog = NewErrorDialog(&m.theme, errors.New(msg.Result))
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
		} else {
			m.statusBar.SetTransientLabel(fmt.Sprintf("Completed in %dms, cost $%.4f", msg.DurationMs, msg.TotalCostUsd))
			m.statusBar.SetTurnCost(msg.TotalCostUsd)
		}
		if finalizeCmd != nil {
			return m, finalizeCmd
		}
		return m, nil

	case InterruptCompletedMsg:
		// QUM-475: terminal interrupted-turn event from the unified runtime.
		// Mirror SessionResultMsg cleanup so the TUI returns to TurnIdle and
		// the queue-drain gate re-opens.
		root := m.rootVP()
		if strings.TrimSpace(msg.Result) != "" && !root.ChatList().HasPendingAssistant() {
			m.rootBuf().AppendAssistantChunk(strings.TrimSpace(msg.Result))
		}
		// Finalize before status append (see SessionResultMsg comment).
		finalizeCmd := m.finalizeTurn()
		m.statusBar.SetTransientLabel(fmt.Sprintf("Interrupted (%dms)", msg.DurationMs))
		if msg.TotalCostUsd > 0 {
			m.statusBar.SetTurnCost(msg.TotalCostUsd)
		}
		if finalizeCmd != nil {
			return m, finalizeCmd
		}
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
		// QUM-675 S5: non-EOF SessionErrorMsg always escalates to the γ
		// overlay (the Idle branch used to AppendError into the viewport;
		// unified with the Thinking/Streaming branch here).
		m.errorDialog = NewErrorDialog(&m.theme, msg.Err)
		m.errorDialog.SetSize(m.width, m.height)
		m.showError = true
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
		m.statusBar.SetTransientLabel(fmt.Sprintf("Session restarting (%s)...", reason))
		// QUM-340: a session restart wipes the conversational context the user
		// queued their next message against. Drop the slot and surface a
		// one-line banner so the disappearance isn't silent. The dropped
		// message label supersedes the restart label (last-write-wins).
		if m.pendingSubmit != "" {
			m.pendingSubmit = ""
			m.input.SetPendingPreview("")
			m.statusBar.SetTransientLabel("queued message dropped due to session restart")
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
		// QUM-675 S5: restartLabel is the dedicated surface for this; the
		// duplicate viewport banner is dropped.
		return m, nil

	case IncidentSnapshotRequestedMsg:
		// QUM-728: capture an incident bundle out-of-band. snapshotCmd is
		// injected by cmd/enter.go; nil in unit tests / when no supervisor
		// is wired.
		m.statusBar.SetTransientLabel("capturing incident snapshot...")
		if m.snapshotCmd != nil {
			return m, m.snapshotCmd
		}
		return m, func() tea.Msg {
			return IncidentSnapshotCompleteMsg{Err: errors.New("snapshot not configured")}
		}

	case IncidentSnapshotCompleteMsg:
		// QUM-728: surface outcome via transient label + (on error) a toast
		// so the user has the bundle path or the failure reason at a glance.
		if msg.Err != nil {
			m.statusBar.SetTransientLabel("snapshot failed")
			toastCmd := m.toasts.Spawn(Toast{
				Text:      "snapshot failed: " + msg.Err.Error(),
				Style:     ToastError,
				DismissOn: TimerDismiss(5 * time.Second),
			})
			return m, toastCmd
		}
		m.statusBar.SetTransientLabel("snapshot saved → " + msg.Path)
		return m, nil

	case ConsolidationCompleteMsg:
		m.consolidating = false
		m.statusBar.SetRestartLabel("")
		if msg.Err != nil {
			m.statusBar.SetTransientLabel(fmt.Sprintf("Consolidation failed: %v", msg.Err))
		} else {
			m.statusBar.SetTransientLabel(fmt.Sprintf("Consolidation complete (%ds)", int(msg.Duration.Seconds())))
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
		// QUM-675 S5: clear any stale "Session restarting…" transient label.
		m.statusBar.SetTransientLabel("")
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
		buf := m.rootBuf()
		// QUM-675 S5: with status/banner text rerouted out of the viewport,
		// nothing to preserve across the restart-clear.
		buf.SetMessages(nil)
		// QUM-497: drop any in-flight MCP op state from the prior session
		// so a stale call_id can't keep ticking on the new bar.
		m.activeMCPOps = make(map[string]OpDescriptor)
		m.mcpOpOrder = nil
		m.mcpOpThresholdShown = make(map[string]bool)
		m.statusBar.SetActiveOps(nil)
		// QUM-385: reset token usage; contextLimit is preserved across
		// restarts since the model usually doesn't change.
		m.statusBar.SetTokenUsage(0)
		// QUM-675 S5: SessionBanner removed; the status bar's sess:<id>
		// segment is the dedicated surface for the new session id.
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
		return m, tea.Batch(cmds...)

	case InterruptResultMsg:
		// QUM-380: the interrupt request was dispatched; show the outcome.
		// Request-ack only — does not transition turnState; terminal cleanup
		// happens in InterruptCompletedMsg (QUM-475).
		if msg.Err != nil {
			m.statusBar.SetTransientLabel(fmt.Sprintf("Interrupt failed: %v", msg.Err))
		} else {
			m.statusBar.SetTransientLabel("Interrupt sent — waiting for turn to end")
			// QUM-697: the "interrupt sent to <agent>" toast auto-dismisses on
			// a 2s timer; no condition-clear here so the user always sees it.
		}
		return m, nil

	case TurnStateMsg:
		m.setTurnState(msg.State)
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
			m.statusBar.SetTransientLabel(formatInboxBanner(msg.RootUnread-m.rootUnread, ""))
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
		hasDisk := m.sprawlRoot != "" && m.rootAgent != ""
		if hasDisk {
			if entries, err := messages.List(m.sprawlRoot, m.rootAgent, "unread"); err == nil {
				diskUnread = len(entries)
			}
		}
		switch {
		case hasDisk && diskUnread > m.rootUnread:
			m.statusBar.SetTransientLabel(formatInboxBanner(diskUnread-m.rootUnread, from))
			m.rootUnread = diskUnread
			m.rebuildTree()
		case !hasDisk:
			// QUM-675 S5: when no disk-truth is available (typical in unit
			// tests with no sprawlRoot), trust the in-process notifier and
			// surface the banner unconditionally.
			m.statusBar.SetTransientLabel(formatInboxBanner(1, from))
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
		// QUM-675 S5: backend-fault user-facing surface is the tree-row
		// FAULT badge (owned by m.faults + rebuildTree). The duplicate
		// viewport banner is dropped.
		m.rebuildTree()
		// QUM-651: spawn a transient error toast so the user sees the fault
		// even when their attention is on a non-tree surface. Cleared on
		// BackendFaultClearedMsg (in-place recovery success).
		toastCmd := m.toasts.Spawn(Toast{
			Text:      fmt.Sprintf("%s faulted: %s", msg.Agent, msg.Reason),
			Style:     ToastError,
			DismissOn: ConditionDismiss("fault-" + msg.Agent),
		})
		return m, toastCmd

	case BackendFaultClearedMsg:
		// QUM-601: in-place recovery succeeded. Drop the per-agent fault
		// sticker, surface a recovery banner in the root viewport, and
		// rebuild the tree so the FAULT badge disappears from the row.
		// Viewport history is intentionally retained — operators keep the
		// fault/recovery sequence visible for forensics.
		if m.faults != nil {
			delete(m.faults, msg.Agent)
		}
		// QUM-651: in-place recovery succeeded; clear the matching fault
		// toast spawned by the BackendFaultMsg reducer.
		m.toasts.ClearCondition("fault-" + msg.Agent)
		m.statusBar.SetTransientLabel(fmt.Sprintf("backend recovered on %s", msg.Agent))
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
			m.statusBar.SetTransientLabel(fmt.Sprintf("[startup] resumed %d agents", msg.Resumed))
		} else {
			m.statusBar.SetTransientLabel(fmt.Sprintf("[startup] resumed %d agents (%d failed)", msg.Resumed, msg.Failed))
		}
		// QUM-651: surface an Info toast for successful resumes. Auto-dismisses
		// after 5s. Failure-only events keep the status-bar transient label
		// path above without a toast (the spec spawns on Resumed>0 only).
		if msg.Resumed > 0 {
			return m, m.toasts.Spawn(Toast{
				Text:      fmt.Sprintf("recovered %d agents", msg.Resumed),
				Style:     ToastInfo,
				DismissOn: TimerDismiss(5 * time.Second),
			})
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
		// QUM-681: also pumps EventBus drop telemetry into the status bar so
		// the ⚠ segment surfaces promptly and auto-clears after a quiet period.
		if len(m.activeMCPOps) == 0 {
			m.mcpOpTickPending = false
			m.refreshDropTelemetry()
			return m, nil
		}
		// SetActiveOps re-installs the slice; the View() call this cmd
		// triggers will reformat elapsed time against the current clock.
		m.statusBar.SetActiveOps(m.orderedMCPOps())
		m.refreshDropTelemetry()
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
		m.statusBar.SetTransientLabel(fmt.Sprintf(
			"⏳ %s(%s) is taking longer than usual (T+%s). Send SIGUSR1 to capture state.",
			op.Tool, caller, elapsed,
		))
		return m, nil

	case EventDropDetectedMsg:
		// QUM-669: gap-detection state machine entry point.
		// AC #4: a gap forces TurnIdle so Ctrl+C/quit chords are unblocked
		// even before the debounce window elapses.
		if m.turnState == TurnStreaming || m.turnState == TurnThinking {
			m.setTurnState(TurnIdle)
		}
		// QUM-669 follow-up #1 (code-review): a drop arriving while a resync
		// is already in flight (gapStateResyncing) must NOT downgrade us back
		// to gap-pending or arm a fresh debounce — the JSONL re-read about to
		// land will subsume it (design §5 hotspot #1). Absorb the missing
		// count for the post-resync banner accuracy and keep state.
		if m.resyncInFlight {
			m.pendingMissing += msg.Missing
			return m, nil
		}
		m.pendingMissing += msg.Missing
		// Short-circuit to dropped when the accumulated gap is at or above
		// the burst threshold, OR when we've already entered dropped (a
		// follow-up drop during resync coalesces into a new resync only if
		// not already in flight).
		if m.pendingMissing >= gapBurstThreshold || m.gapState == gapStateDropped {
			snapshot := m.pendingMissing
			m.pendingMissing = 0
			if m.resyncInFlight {
				// Already resyncing — let the in-flight read complete; design
				// §5 hotspot #1 says any "lost" events are by definition in the
				// session JSONL we're about to read, so no second resync needed.
				m.gapState = gapStateDropped
				return m, nil
			}
			return m, m.kickResyncFromGap(snapshot)
		}
		// Below burst — enter gap-pending and arm a debounce tick. Per-call
		// timer is created inside the cmd closure (instead of tea.Tick's
		// shared-timer pattern) so test helpers that invoke the cmd multiple
		// times don't block forever on a drained channel.
		m.gapState = gapStatePending
		m.gapID++
		gid := m.gapID
		return m, gapDebounceCmd(gid, gapDebounceWindow)

	case gapConfirmMsg:
		// QUM-669 debounce confirmation. Stale deliveries (gapID mismatch or
		// state advanced) are no-ops — mirrors mcpOpThresholdMsg's pattern.
		if msg.gapID != m.gapID {
			return m, nil
		}
		if m.gapState != gapStatePending {
			return m, nil
		}
		if m.pendingMissing >= gapBurstThreshold {
			// Bursty accumulation crossed threshold during the window — kick
			// the resync after all. (Defensive: the EventDropDetectedMsg arm
			// already kicks the resync as soon as the threshold is crossed,
			// so this branch is rarely hit.)
			snapshot := m.pendingMissing
			m.pendingMissing = 0
			return m, m.kickResyncFromGap(snapshot)
		}
		// Single blip — walk back to normal silently.
		m.gapState = gapStateNormal
		m.pendingMissing = 0
		return m, nil

	case ViewportResyncMsg:
		// QUM-669 resync result. On error, keep the dropped state and surface
		// a recovery hint. On success, install the rebuilt transcript, append
		// the resync banner, and collapse back to normal.
		m.resyncInFlight = false
		m.statusBar.SetResyncPill("")
		if msg.Err != nil {
			// QUM-675 S5: route resync failures to the γ overlay so the
			// retry hint is unmistakable.
			m.errorDialog = NewErrorDialog(&m.theme, fmt.Errorf("resync failed: %w — press Ctrl+L to retry", msg.Err))
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
			m.gapState = gapStateDropped
			return m, nil
		}
		// QUM-676: LoadTranscript no longer emits a trailing "Resumed from
		// prior session" marker, so the legacy strip-tail-status defensive
		// pass is gone with it. Install the rebuilt transcript via cl.Reset
		// (force-finalizes the trailing assistant and clears pendingTools
		// per the QUM-669 wedge-exit invariant).
		entries := msg.Entries
		m.rootBuf().SetMessages(entries)
		m.statusBar.SetTransientLabel(fmt.Sprintf("✓ resynced — recovered %d events from session log", msg.MissingCount))
		m.setTurnState(TurnIdle)
		m.gapState = gapStateNormal
		m.pendingMissing = 0
		// Note: lastSeq baseline is intentionally NOT reset on the adapter
		// side. Design §5 hotspot #1: any events "lost" during the resync are
		// by definition already in the session JSONL the resync just read.
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

		var cmds []tea.Cmd

		// QUM-732: when switching the observed agent, arm spinner ticks for
		// any already-pending ToolCallItems in the newly-observed pane so
		// the animation resumes immediately on the freshly-visible buffer.
		// ResetPendingToolTicking first: a previous switch may have orphaned
		// a tick chain (delivered to a different pane, dead-ended), leaving
		// items stuck with ticking=true that StartTickCmd would refuse to
		// re-arm.
		if vp := m.viewportFor(msg.Name); vp != nil {
			vp.ChatList().ResetPendingToolTicking()
			if tick := vp.ChatList().PendingToolTickCmds(); tick != nil {
				cmds = append(cmds, tick)
			}
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
			// QUM-675 S5: escalate transcript-load failures to the γ overlay.
			_ = vp
			m.errorDialog = NewErrorDialog(&m.theme, fmt.Errorf("transcript load failed for %s: %w", msg.Agent, msg.Err))
			m.errorDialog.SetSize(m.width, m.height)
			m.showError = true
		case len(msg.Entries) > 0:
			// QUM-676: a real backfill landed — clear any "Waiting for X
			// to start" status-bar banner the empty arm installed earlier.
			if want := fmt.Sprintf("Waiting for %s to start...", msg.Agent); m.statusBar.TransientLabel() == want {
				m.statusBar.SetTransientLabel("")
			}
			m.agentBufferFor(msg.Agent).SetMessages(msg.Entries)
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
			// QUM-676 / QUM-693: empty entries — show "Waiting for X to start"
			// via the status-bar transient label. The legacy viewport status
			// path is gone with the ChatList contract-violator routing.
			banner := fmt.Sprintf("Waiting for %s to start...", msg.Agent)
			m.statusBar.SetTransientLabel(banner)
			// Also clear the agent's buffer so prior content doesn't linger
			// from a previous session under the same agent name.
			m.agentBufferFor(msg.Agent).SetMessages(nil)
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
	return ShortHelpState{
		TurnState:   m.turnState,
		InputEmpty:  strings.TrimSpace(m.input.Value()) == "",
		HasQueued:   m.pendingSubmit != "",
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
		// QUM-731: keep mouse capture on in the too-small fallback so the
		// wheel still works once the user resizes back up — bubbletea diffs
		// MouseMode across frames, so flipping it off here would emit a
		// disable sequence the user wouldn't expect.
		v.MouseMode = tea.MouseModeCellMotion
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
		layout.InputHeight = 0
	}

	// QUM-656: the agent tree moved into the header (renderView prepends it
	// below); the main row is now just the viewport. The tree panel cache
	// slot is still populated so the per-tree-mutation cache key invalidates
	// on tree changes — TODO(QUM-655): drop panelSlotTree + cachedMainRow
	// once the cache-invariance tests are reshaped.
	_ = m.cachedPanel(useCache, panelSlotTree, m.tree.View(),
		layout.HeaderTreeWidth, OrbitalHeight(layout.HeaderTreeWidth, m.tree.nodes),
		false)

	// QUM-673 S3: render finished items via ChatList; fall back to vp.View()
	// when a stream / tool call is in flight, or when ChatList has nothing
	// to render (status placeholders + banners flow through vp only).
	chatContent := m.chatRegionContent(layout.ViewportWidth - 4)
	vpView := m.cachedPanel(useCache, panelSlotViewport, chatContent,
		layout.ViewportWidth, layout.ViewportHeight,
		false)

	// QUM-656: main row is the viewport only — the tree lives in the header.
	mainRow := m.cachedMainRow(useCache, "", vpView)

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
			true)
		overlay = m.searchOverlay()
	}
	content := m.cachedComposed(useCache, layout.TermWidth, mainRow, overlay, inputView, shortHelpView, statusView, inputVisible)

	// QUM-656: prepend the header strip (SPRAWL wordmark + orbital agent
	// tree). Height is already reserved by ComputeLayout via HeaderHeight, so
	// the composed content below fits within the terminal without clipping.
	if layout.HeaderHeight > 0 {
		treeLines := m.tree.OrbitalLines(layout.HeaderTreeWidth)
		content = RenderHeader(layout.TermWidth, treeLines) + "\n\n" + content
	}

	// QUM-649: composite toasts on top of chat/header but below all modals.
	// QUM-701: anchor toasts immediately below the header (centered, stacked).
	m.toasts.SetHeaderHeight(layout.HeaderHeight)
	if !m.toasts.Empty() {
		content = m.toasts.Overlay(content)
	}

	// QUM-733 5b: tree modal sits ABOVE chat/toasts but BELOW the higher-
	// priority modals (palette, help, question, validate popup, confirm,
	// error) so those always override.
	if m.showTree {
		content = m.treeModal.View()
	}

	if m.showPalette {
		content = m.palette.View()
	}

	if m.showHelp {
		content = m.help.View()
	}

	if m.showQuestion {
		content = m.questionModel.View()
	}

	if m.showUsage {
		content = m.usageModal.View()
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
	// QUM-731: restore mouse capture so the scroll wheel reaches the TUI.
	// Native click-drag selection is blocked while this is on; users can
	// still copy via Shift+drag, tmux copy-mode (prefix [), or right-click →
	// Copy. The QUM-617 selection-mode toggle stays retired (QUM-653).
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// setTurnState mutates the turn state and propagates the change to the status
// bar and agent tree. Side-effects to be aware of:
//
//   - On the Idle→Thinking edge it clears the status bar's transient label
//     (QUM-675 S5 "next turn started" rule). This means any reducer that both
//     transitions the turn state to TurnThinking AND wants to install a
//     transient label for the new turn MUST call setTurnState(TurnThinking)
//     BEFORE m.statusBar.SetTransientLabel(...). The reverse order silently
//     wipes the just-set label. See InjectPromptMsg (the "/handoff dispatched"
//     path) and InboxDrainMsg (the "inbox: draining N…" path) for the
//     load-bearing call-site ordering. QUM-690 tracks this hazard; QUM-649
//     (toast subsystem) is the natural future resolution that will obviate it.
//   - Rebuilds the agent tree so the weave-root turn badge reflects the new
//     state.
func (m *AppModel) setTurnState(state TurnState) {
	// QUM-675 S5: clear the transient label on Idle→Thinking — the canonical
	// "next turn started" edge — so stale "Interrupt sent" / startup banners
	// don't survive into the new turn.
	//
	// SEQUENCING HAZARD (QUM-690): callers that want to set a transient label
	// for the new turn MUST call setTurnState(TurnThinking) BEFORE
	// m.statusBar.SetTransientLabel(...), or the label gets wiped here.
	// Existing load-bearing examples: InjectPromptMsg ("/handoff dispatched —
	// see output below") and InboxDrainMsg ("inbox: draining N async
	// message(s) into next prompt"). QUM-649 (toast subsystem) is the natural
	// future resolution that will obviate this hazard.
	if m.turnState == TurnIdle && state == TurnThinking {
		m.statusBar.SetTransientLabel("")
	}
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
	m.rootBuf().FinalizeAssistantMessage()
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
// SetTransientStatus installs a transient status-bar label from outside the
// Bubble Tea loop (e.g. cmd/enter.go after PreloadTranscript surfaces a
// resume hint). QUM-676.
func (m *AppModel) SetTransientStatus(text string) {
	m.statusBar.SetTransientLabel(text)
}

// SpawnToast appends a toast to the overlay stack. Returns the tea.Cmd to
// schedule auto-dismiss (non-nil only for TimerDismiss contracts). Exposed
// so callers outside the bubbletea reducer (e.g. cmd/enter.go bootstraps)
// can install toasts before Update fires. (QUM-649)
func (m *AppModel) SpawnToast(t Toast) tea.Cmd { return m.toasts.Spawn(t) }

// DismissToast removes the toast with the given ID. No-op for unknown IDs.
// (QUM-649)
func (m *AppModel) DismissToast(id string) { m.toasts.Dismiss(id) }

func (m *AppModel) PreloadTranscript(entries []MessageEntry) {
	if len(entries) == 0 {
		return
	}
	// QUM-673 post-review fix: route cold-boot transcript preload through the
	// dual-shim so cl mirrors the resumed transcript alongside vp.
	m.rootBuf().SetMessages(entries)
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
		// QUM-676: ChatList now lives inside ViewportModel.region. The
		// AgentBuffer.cl field shares the same pointer so dual-store
		// references stay consistent.
		buf = &AgentBuffer{vp: vp, cl: vp.ChatList()}
		m.agentBuffers[name] = buf
	}
	return &buf.vp
}

// rootVP returns the viewport for the root agent (weave). Bridge events,
// inbox banners, and other weave-only annotations target this viewport
// regardless of which agent the user is currently observing.
func (m *AppModel) rootVP() *ViewportModel { return m.viewportFor(m.rootAgent) }

// agentBufferFor returns the per-agent AgentBuffer for name, lazy-creating
// the underlying buffer via viewportFor's side effect when missing. QUM-673.
func (m *AppModel) agentBufferFor(name string) *AgentBuffer {
	_ = m.viewportFor(name)
	return m.agentBuffers[name]
}

// rootBuf is the dual-store entry point for weave's chat region (QUM-673 S3).
// It returns the root agent's AgentBuffer so callers fan AppendX into both
// vp (legacy live-render fallback) and cl (the new render model) via the
// shared wrappers.
func (m *AppModel) rootBuf() *AgentBuffer { return m.agentBufferFor(m.rootAgent) }

// observedVP returns the viewport for the currently-observed agent. Used
// by View() and select-mode helpers.
func (m *AppModel) observedVP() *ViewportModel { return m.viewportFor(m.observedAgent) }

// chatRegionContent is the QUM-676 chat-panel string source. ChatList +
// ChatRegion own the rendering pipeline end-to-end: ChatList caches per-Item
// renders, ChatRegion wraps a bubbles viewport that handles PgUp/PgDn,
// auto-scroll, and the "↓ New content below" indicator. The legacy
// SetContentExternal dual-shim is gone — there is no second render store.
func (m *AppModel) chatRegionContent(contentWidth int) string {
	_ = contentWidth // sizing happens in resizePanels via region.SetSize
	vp := m.observedVP()
	if vp == nil {
		return ""
	}
	return vp.View()
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
	// QUM-656: the tree lives in the header; size it to the header's tree
	// budget and the orbital row count.
	m.tree.SetSize(layout.HeaderTreeWidth, OrbitalHeight(layout.HeaderTreeWidth, m.tree.nodes))
	for name, buf := range m.agentBuffers {
		// The currently-observed viewport may need the input bar's reclaimed
		// rows (QUM-340). Other buffers stay sized for the input-visible
		// layout so they're correct on the next cycle-to-root.
		h := layout.ViewportHeight
		if name == m.observedAgent {
			h = observedHeight
		}
		// QUM-676: vp.SetSize forwards to ChatRegion.SetSize which sizes both
		// the inner bubbles viewport AND the inner ChatList. The legacy
		// "dual-store sync" cl.SetSize call has gone with the dual-append
		// shim.
		buf.vp.SetSize(layout.ViewportWidth-4, h-4)
	}
	m.input.SetWidth(layout.InputWidth - 4)
	m.statusBar.SetWidth(layout.StatusWidth)
	m.help.SetSize(m.width, m.height)
	m.confirm.SetSize(m.width, m.height)
	m.errorDialog.SetSize(m.width, m.height)
	m.palette.SetSize(m.width, m.height)
	m.treeModal.SetSize(m.width, m.height)
	m.questionModel.SetSize(m.width, m.height)
	m.usageModal = m.usageModal.SetSize(m.width, m.height)
	m.validatePopup.SetSize(m.width, m.height)
	m.toasts.SetSize(m.width, m.height)
}

// anyOtherModalUp reports whether any modal OTHER than the question modal is
// active. Used to gate auto-show / Ctrl-Q reopen.
func anyOtherModalUp(m *AppModel) bool {
	return m.showError || m.showConfirm || m.showHelp || m.showPalette || m.showUsage || m.showTree
}

// anyOtherModalUpExceptTree reports whether any modal OTHER than the tree
// modal is up. Used by the ToggleTreeMsg open gate so the tree modal cannot
// pre-empt a higher-priority overlay (QUM-733 5b).
func anyOtherModalUpExceptTree(m *AppModel) bool {
	return m.showError || m.showConfirm || m.showHelp || m.showPalette || m.showQuestion || m.showUsage
}

// anyModalUp reports whether ANY modal is currently visible. The four input-
// gating sites in Update() (mouse handler, paste handler, input-panel
// history-arrow handler, OpenPaletteMsg handler) call !m.anyModalUp() so a
// modal always owns input. Convention: when adding a new modal flag, extend
// this helper — that's it — and all gates Just Work. Distinct from
// anyOtherModalUp, which deliberately excludes showQuestion for the
// question-auto-show / Ctrl-Q-reopen gates.
func (m *AppModel) anyModalUp() bool {
	return m.showHelp || m.showConfirm || m.showError || m.showPalette || m.showQuestion || m.showUsage || m.showTree
}

// agentFromHead returns pq.Req.From if pq is non-nil, else "".
func agentFromHead(pq *supervisor.PendingQuestion) string {
	if pq == nil {
		return ""
	}
	return pq.Req.From
}

// inputBoxHeight returns the total height the input box occupies. The
// textarea's Height() reflects the current content line count when
// DynamicHeight is enabled. QUM-661 stripped the rounded border/bg fill so
// the input is now flush textarea rows — no +2 border reservation needed.
// Before the textarea has been sized (SetWidth), its Height() returns a
// stale default — fall back to the layout default in that case.
func (m *AppModel) inputBoxHeight() int {
	h := m.input.Height()
	if h < defaultInputHeight {
		h = defaultInputHeight
	}
	return h
}

// updateFocus is the post-QUM-695 stub kept for callsite stability. The
// input panel is the sole keystroke recipient, so this unconditionally
// focuses it. (Pre-QUM-695 it switched focus among Tree/Viewport/Input.)
func (m *AppModel) updateFocus() {
	m.input.Focus()
}

func (m AppModel) delegateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// QUM-695: all keystrokes go to the input panel. PgUp/PgDn were
	// intercepted earlier and forwarded to the viewport; nothing else needs
	// to reach tree/viewport via key delegation.
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

// SetHomeDir injects the user's home directory used to resolve Claude session
// log paths for child-agent transcript tailing (QUM-332). Wired by cmd/enter.go
// after model construction.
func (m *AppModel) SetHomeDir(homeDir string) {
	m.homeDir = homeDir
}

// SetSnapshotCmd installs the incident-snapshot producer used by Ctrl+\
// (QUM-728). Wired by cmd/enter.go to internal/observe/incident.Snapshotter.
func (m *AppModel) SetSnapshotCmd(fn func() tea.Msg) {
	m.snapshotCmd = fn
}

// SetValidatePopupAfter configures the auto-open threshold for the validate
// popup. Pass 0 to use the default (10s). Wired by cmd/enter.go after model
// construction from .sprawl/config.yaml's validate_popup_after_seconds.
// QUM-588.
func (m *AppModel) SetValidatePopupAfter(d time.Duration) {
	m.validatePopup = NewValidatePopupModel(&m.theme, d)
	m.validatePopup.SetSize(m.width, m.height)
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
		if buf != nil {
			buf.AppendAssistantChunk(im.Text)
		} else {
			vp.ChatList().AppendAssistantChunk(im.Text)
		}
		return nil
	case ThinkingMsg:
		// QUM-677 S7: per-child thinking blocks render into the child's
		// AgentBuffer (or fall back to the agent's viewport when the buffer
		// hasn't materialized yet — mirrors the AssistantText path).
		_ = im
		if buf != nil {
			buf.AppendThinking()
		} else {
			vp.ChatList().AppendThinking()
		}
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
		if buf != nil {
			buf.AppendToolCallWithHeader(im.ToolName, im.ToolID, im.Approved, im.Input, im.FullInput, im.HeaderArg, im.HeaderParams, im.ParentToolUseID)
		} else {
			vp.ChatList().AppendToolCallWithHeader(im.ToolName, im.ToolID, im.Approved, im.Input, im.FullInput, im.HeaderArg, im.HeaderParams, im.ParentToolUseID)
		}
		// QUM-732: arm per-item spinner tick only when this child buffer's
		// agent is the observed pane (background panes do not animate).
		if agent == m.observedAgent {
			return vp.ChatList().PendingToolTickCmds()
		}
		return nil
	case ToolResultMsg:
		// QUM-674 S4: per-item glyph; no global spinner counter to drain.
		if buf != nil {
			_ = buf.MarkToolResult(im.ToolID, im.Content, im.IsError)
		} else {
			_ = vp.ChatList().MarkToolResult(im.ToolID, im.Content, im.IsError)
		}
		return nil
	case SessionResultMsg:
		if buf != nil {
			buf.FinalizeAssistantMessage()
		} else {
			vp.ChatList().FinalizeAssistantMessage()
		}
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

// gapState models the QUM-669 viewport-resync state machine. See the design
// doc §2.3 for the full transition diagram.
type gapState int

const (
	gapStateNormal    gapState = iota // no gap detected; default
	gapStatePending                   // single-blip drop seen, debounce window armed
	gapStateDropped                   // confirmed gap; resync in flight or queued
	gapStateResyncing                 // resync cmd dispatched, awaiting ViewportResyncMsg
	gapStateRecovered                 // resync completed; transient — collapsed to Normal
)

// gapDebounceWindow is the QUM-669 single-blip suppression window. After
// receiving an EventDropDetectedMsg whose Missing count is below
// gapBurstThreshold, the reducer enters the gap-pending state and arms a
// gapConfirmMsg tick to fire after this delay. If no further drops accumulate
// the AppModel returns to normal without resyncing.
const gapDebounceWindow = 500 * time.Millisecond

// gapBurstThreshold mirrors runtime.dropWarnBurstThreshold. A gap whose
// Missing count is at or above this threshold short-circuits the debounce
// window and triggers an immediate resync — the user has already lost too
// much state for the "wait and see" path to be honest. QUM-669.
const gapBurstThreshold = uint64(10)

// mcpOpBannerThreshold is the elapsed time after which a long-running MCP
// tool call earns a viewport banner with SIGUSR1 guidance. Package-level var
// so reducer tests can compress it. (QUM-497)
var mcpOpBannerThreshold = 60 * time.Second

// mcpOpTickInterval is the refresh cadence for the live elapsed-time render
// in the status bar. Package-level var so tests can override. (QUM-497) The
// same tick also pumps EventBus drop telemetry into the status bar (QUM-681).
var mcpOpTickInterval = 1 * time.Second

// eventDropClearInterval mirrors runtime.dropClearInterval so AppModel can
// filter DropTelemetry snapshots without importing internal/runtime here.
// Subscribers whose LastDropAt is older than this are omitted from the
// status-bar segment so the ⚠ chip auto-clears after a quiet period (QUM-681).
const eventDropClearInterval = 30 * time.Second

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

// refreshDropTelemetry pulls EventBus drop telemetry from the backend (when
// supported) and pushes the recent entries to the status bar (QUM-681).
// Filters to subscribers whose LastDropAt is within eventDropClearInterval
// so the ⚠ segment auto-clears after a quiet period; surviving entries are
// sorted by Cumulative descending so the worst offender renders first.
func (m *AppModel) refreshDropTelemetry() {
	src, ok := m.bridge.(DropTelemetrySource)
	if !ok {
		return
	}
	snap := src.DropTelemetry()
	now := time.Now()
	segments := make([]EventDropSegment, 0, len(snap))
	for name, tel := range snap {
		if tel.Cumulative == 0 {
			continue
		}
		if tel.LastDropAt.IsZero() || now.Sub(tel.LastDropAt) > eventDropClearInterval {
			continue
		}
		segments = append(segments, EventDropSegment{Name: name, Count: tel.Cumulative})
	}
	sort.Slice(segments, func(i, j int) bool {
		return segments[i].Count > segments[j].Count
	})
	m.statusBar.SetEventDrops(segments)
}

// gapDebounceCmd returns a one-shot tea.Cmd that fires a gapConfirmMsg after
// delay. Unlike tea.Tick (which creates one shared timer at construction
// time), gapDebounceCmd creates a fresh timer inside each invocation so test
// helpers that call the returned cmd multiple times don't block on a drained
// channel. (QUM-669)
func gapDebounceCmd(gapID uint64, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(delay)
		<-timer.C
		return gapConfirmMsg{gapID: gapID}
	}
}

// resyncCmd builds the async session-log read that produces a
// ViewportResyncMsg. QUM-669 step 5. The caller owns transitioning the
// gapState into gapStateResyncing and setting the status-bar pill BEFORE
// returning this cmd so the user sees the in-flight indicator while the
// read is happening.
//
// missing carries the gap size through to the resync banner ("recovered N
// events"). Failure paths (empty session id, missing bridge/home, LoadTranscript
// error) emit ViewportResyncMsg{Err: ...} so the reducer can render the
// "resync failed — Ctrl+L to retry" banner uniformly.
func (m *AppModel) resyncCmd(missing uint64) tea.Cmd {
	if m.bridge == nil {
		return func() tea.Msg { return ViewportResyncMsg{MissingCount: missing, Err: errors.New("no bridge")} }
	}
	if m.homeDir == "" || m.sprawlRoot == "" {
		return func() tea.Msg {
			return ViewportResyncMsg{MissingCount: missing, Err: errors.New("home/sprawlRoot unset")}
		}
	}
	sessionID := m.bridge.SessionID()
	if sessionID == "" {
		return func() tea.Msg { return ViewportResyncMsg{MissingCount: missing, Err: errors.New("no session id")} }
	}
	path := memory.SessionLogPath(m.homeDir, m.sprawlRoot, sessionID)
	return func() tea.Msg {
		entries, err := LoadTranscript(path, ReplayMaxMessages)
		return ViewportResyncMsg{Entries: entries, MissingCount: missing, Err: err}
	}
}

// kickResyncFromGap transitions into the dropped/resyncing state and returns
// the dispatch cmd. Appends the drop banner so the user sees the gap on the
// normal→dropped boundary. QUM-669. Callers must clear pendingMissing after
// they've captured the snapshot value into the cmd. The lastSeq baseline is
// intentionally NOT reset on the adapter side — design §5 hotspot #1 notes
// the resync read already includes all "lost" events from the session log,
// so adapter-side state can keep advancing without violating invariants.
func (m *AppModel) kickResyncFromGap(missing uint64) tea.Cmd {
	m.gapState = gapStateResyncing
	m.resyncInFlight = true
	m.statusBar.SetResyncPill("resyncing…")
	// QUM-675 S5: drop banner now lives on the transient label.
	m.statusBar.SetTransientLabel(fmt.Sprintf("⚠ %d events lost — resync in flight (Ctrl+L to retry)", missing))
	return m.resyncCmd(missing)
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
